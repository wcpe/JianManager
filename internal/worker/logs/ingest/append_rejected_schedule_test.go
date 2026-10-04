package ingest

// #56 的红/绿：`WAL.Append` 被拒（暂停/上限）**不得**在 emit→ingest 链上同步执行整窗 replay。
//
// 现场（opt2 三栈定名）：`acquire/pipeline.go` 的 Append 失败分支原先当场调用
// `deliverPendingBestEffort()`（replay=true、整窗、百万级批）⇒ 摄取路径被按住、采集轮读不再前进、
// goroutine 堆积 183→242，而全程 `m.mu` 无慢持锁记录 ⇒ 典型的**在途阻塞**（in-path blocking）。
// 修法：摄取路径只**登记待办 + 非阻塞唤醒**（O(1)），排空由既有兜底环在路径外执行。
//
// 本用例是**确定性**的（不依赖负载/时序运气）：
//  1. WALLimits{MaxEntries:1} ⇒ 首批追加后该源即处于暂停态（与 acquire 的 drain 用例同夹具手法）；
//  2. 暂停后尾随读被拒（tailer 暂停门），但 `Flush()` 强制冲刷**未闭合事件**仍会走到 emit→ingest
//     ⇒ Append 被拒 ⇒ 命中本修复的分支；
//  3. 投递钩子把 **replay** 投递阻塞住（模拟整窗很慢）⇒
//     绿：`Flush()` 立即返回（不被 replay 拖住），且 replay 仍在数秒内被调度送达；
//     红（把 Register 的 AppendRejected 接线去掉 ⇒ acquire 退回原有的同步调用 = 改前逐字路径）：
//     `Flush()` 要等满整窗耗时才返回 ⇒ 首断言转红（见本文件末尾的变异记录）。

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// replayBlockFor 是"整窗 replay"的模拟阻塞时长：远大于"摄取路径应当立即返回"的上限。
const replayBlockFor = 3 * time.Second

// ingestNotBlockedWithin 是"摄取路径不得被 replay 拖住"的上限（与 replayBlockFor 相差 3× 余量）。
const ingestNotBlockedWithin = time.Second

// TestAppendRejectedSchedulesReplayOutsideIngestPath 守 #56 的核心不变量：
// ① 追加被拒后 ingest 调用**不被整窗 replay 拖住**（消除在途阻塞）；
// ② replay **仍会被及时投递**（滞回修复意图保留：存量必须外发，不能因为改调度而丢）。
func TestAppendRejectedSchedulesReplayOutsideIngestPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	// 三行 Paper 风格日志：前两行会被 Poll 读成**两个**完整事件（条目数 2 > 上限 1 ⇒ 暂停），
	// 最后一行在夹具里永远"未闭合"（见 runtime_test.go 的说明）⇒ 可被 Flush 强制冲刷，
	// 从而在**暂停之后**再产出一批（这正是"Append 被拒"分支的确定性入口）。
	require.NoError(t, os.WriteFile(path, []byte(pollTestLines(0, 3)), 0o600))
	src := SourceConfig{
		LogSourceID:      "node:append-rejected",
		SourceGeneration: "g1",
		Path:             path,
		StorageNamespace: "ns:append-rejected",
		UTCDay:           runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, Catalog: catalog.New(nil), Sources: []SourceConfig{src},
		// 条目上限 1 ⇒ 首批追加即越界 ⇒ 该源进入暂停态（其后的追加一律被拒）。
		WALLimits: &acquire.WALLimits{MaxEntries: 1},
	})
	require.NoError(t, err)
	key := src.LogSourceID + "/" + src.SourceGeneration
	p := m.pipes[key]
	require.NotNil(t, p)

	// 兜底环是"排空"的宿主（#56 复用它 ⇒ 不新增环、生命周期沿用已审路径）。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		m.RunCapacityResumeLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-loopDone
	})

	replayEntered := make(chan struct{})
	releaseReplay := make(chan struct{})
	var replayOnce sync.Once
	// 放行用 Once：红（/变异）时会提前放行一次，用例末尾再放行一次不得重复 close。
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReplay) }) }
	var normalDeliveries atomic.Int64
	p.SetDelivery(pipeline.FuncHook(func(events []logtypes.Event, replay bool) (pipeline.DeliveryResult, error) {
		if !replay {
			normalDeliveries.Add(1)
			// 模拟"VL 未接受"（现场形态：源正是因为投递跟不上才积压被暂停）⇒ 条目保持未确认
			// ⇒ 积压不回落 ⇒ 暂停态稳定（否则成功的投递会立刻 reclaim 并把源自动恢复）。
			return pipeline.DeliveryResult{HTTPStatus: 500}, nil
		}
		// 模拟"整窗 replay 很慢"（现场：百万级批。原实现在此处把摄取路径按住 ⇒ 在途阻塞）：
		// 阻塞到测试显式放行，或以 replayBlockFor 为上限（避免任何残留 goroutine 拖长用例）。
		replayOnce.Do(func() { close(replayEntered) })
		select {
		case <-releaseReplay:
		case <-time.After(replayBlockFor):
		}
		return pipeline.DeliveryResult{HTTPStatus: 200}, nil
	}))

	// 第 1 步：正常读一批 ⇒ 追加成功但越界 ⇒ 进入暂停态（夹具前置）。
	_, err = p.Poll()
	require.NoError(t, err)
	require.True(t, p.Ledger().Get(p.Key()).AcquirePaused,
		"前置：WALLimits{MaxEntries:1} 下首批追加后该源必须处于暂停态")

	// 第 2 步：强制冲刷未闭合事件 ⇒ emit→ingest 走到 Append 被拒分支（本修复的现场点）。
	//
	// 有界观测（而不是同步直接调用）：改前（或把接线去掉的变异）时本调用会被整窗 replay 按住，
	// 同步等待只会把用例一起挂死（得到"超时"而不是"被拖住多久"这个读数）⇒ 这里**限时等**，
	// 超时即提前放行并让断言失败（红）。
	flushErrCh := make(chan error, 1)
	start := time.Now()
	go func() {
		_, ferr := p.Flush()
		flushErrCh <- ferr
	}()
	var flushErr error
	select {
	case flushErr = <-flushErrCh:
	case <-time.After(ingestNotBlockedWithin):
		release() // 放行被按住的整窗 replay（避免用例挂死）
		flushErr = <-flushErrCh
	}
	blocked := time.Since(start)
	require.Error(t, flushErr, "暂停期间不得再追加")
	require.Contains(t, flushErr.Error(), "paused")

	t.Logf("#56 观测：Flush（含 Append 被拒路径）用时 %s · 调度计数 %d · 正常投递 %d 次",
		blocked, m.ScheduledReplayCount(), normalDeliveries.Load())

	// ① 摄取路径不得被整窗 replay 拖住（绿：立即返回；红：等满 replayBlockFor）。
	require.Less(t, blocked, ingestNotBlockedWithin,
		"#56：Append 被拒后的 ingest 调用必须立即返回（整窗 replay 只能在路径外执行）；实测被拖住 %s", blocked)

	// ② 但 replay 仍必须被**及时**送达（滞回修复意图保留：存量必须外发）。
	select {
	case <-replayEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("#56：追加被拒后必须在数秒内调度一次 replay 投递（否则滞回自愈链断裂）")
	}
	require.GreaterOrEqual(t, m.ScheduledReplayCount(), int64(1),
		"观测面：追加被拒必须登记过一次待办排空")
	require.GreaterOrEqual(t, normalDeliveries.Load(), int64(1), "正常批次投递语义不变")

	release() // 放行后台那次 replay，避免用例结束后它仍挂着
	_ = replayBlockFor
}
