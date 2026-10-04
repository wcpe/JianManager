package acquire

// #60-Z1 的红/绿：**同一水位下的重复排空不得反复整窗重投**。
//
// 现场（opt2 采）：门队列很浅（1–9 帧）、rowsWrittenDelta 仅 32–64 行/min，但**单次持久化
// maxMs=159,934ms** ✗✗；投递链上相邻样本 **79.7% 是新代次（+1）**⇒ 几乎每次写入都在整窗 replace；
// ≥1M 批占 4.8%、16h 峰值 126 条/h。
// 环：排空（deliverPendingBestEffort）每次只发 ≤500 条，却以 `replay=true` 调用投递 ⇒ ingest 侧
// `replace=true` ⇒ **重建权威全量 + 发新代次**（现场单次 159s）；滞留期的重复触发 = 反复重投同一段 ✗✗。
//
// 本用例的"整窗次数" = `deliver(replay=true)` 的调用次数（与 ingest 侧一次整窗 replace 一一对应）。
//
// 变异回红（见文件末尾）：把 `replayDrainDecision` 改成恒 `(false, false)`（= 撤销闸） ⇒
// 首条断言（5 次触发只允许 1 次重投）转红。

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// newReplaySuppressFixture 造一个"水位停滞"的最小夹具：真账本 + 真 WAL + 3 条 durable 存量。
// 停滞的现场形态：无人登记投递结果、回收不推进（本夹具里没有恢复链 ⇒ TryReclaim 不动）。
func newReplaySuppressFixture(t *testing.T) (*Pipeline, ledger.SourceKey, *atomic.Int64) {
	t.Helper()
	const id, gen = "src-replay-suppress", "g1"
	key := testKey(id, gen)
	led := ledger.New()
	wal := NewWAL(led, key)
	pipe := NewPipeline(led, key, wal)
	pipe.SetMaxReplayEventsPerDrain(3)
	pipe.SetReplaySuppressBase(time.Second) // 生产默认 5s；用例只需"窗口 > 0"以便确定性观测
	var replays atomic.Int64
	pipe.SetDeliver(func(_ []logtypes.Event, replay bool) (int, bool, error) {
		if replay {
			replays.Add(1)
		}
		return 200, false, nil
	})
	// 种子：走正常 Ingest（注册账本 + 一次普通投递）；此后无人再写账本 ⇒ 水位停滞。
	require.NoError(t, pipe.Ingest(buildWALEvents(id, gen, 0, 3, "seed")))
	return pipe, key, &replays
}

func TestReplayDrainSuppressedWhileWatermarkStalled(t *testing.T) {
	pipe, key, replays := newReplaySuppressFixture(t)

	// ① 同一水位下连续 5 次触发（现场三面：兜底环 / 自愈 / 追加被拒调度）⇒ 只允许 1 次整窗重投。
	base := replays.Load()
	for i := 0; i < 5; i++ {
		_, _ = pipe.DeliverPending()
	}
	require.Equal(t, base+1, replays.Load(),
		"同一水位下 5 次排空只允许 1 次整窗重投（冗余整窗是 159s/次的主项）")
	require.Equal(t, int64(4), pipe.SuppressedReplayDrains(),
		"被抑制的次数必须可读（受理 vs 跳过要可测）")

	// ② 水位前进 ⇒ 立即放行一次（合法 replay 不被饿死）。
	//    注入点必须是**真水位**（投递位置）——`RecordGap` 只加缺口、不动水位，测不到这条 ✓。
	// 投递水位 = 从 reclaim 起**连续**已确认区间的末端 ⇒ 只有**紧接**既有末端的批次才会推进
	//（重复登记既有区间 [100,399] 是 no-op —— 这正是"冗余"的判据本身 ✓）。
	require.NoError(t, pipe.wal.RecordHTTPResult(DeliveryResult{StartPos: 300, EndPos: 399, HTTPStatus: 200}))
	pos, ok := pipe.led.Positions(key)
	require.True(t, ok)
	require.Equal(t, uint64(399), pos.Delivery, "前置：投递水位必须真的前进（否则测不到放行分支）")
	_, _ = pipe.DeliverPending()
	require.Equal(t, base+2, replays.Load(), "水位前进后必须立即放行")

	// ③ **失败必须立即可重试**（不变量：合法 replay 不被饿死）。这条是证据驱动的收窄：把失败计入退避后，
	//    端到端自愈用例 TestPausedSourceSelfHealsEndToEnd 转红（首次失败 ⇒ 5s 内重试被抑制 ⇒ 缺口消解链被饿死）。
	pipe.SetDeliver(func(_ []logtypes.Event, replay bool) (int, bool, error) {
		if replay {
			replays.Add(1)
		}
		return 0, false, fmt.Errorf("stub: VL 未接受")
	})
	require.NoError(t, pipe.wal.RecordHTTPResult(DeliveryResult{StartPos: 400, EndPos: 499, HTTPStatus: 200}))
	_, err := pipe.DeliverPending()
	require.Error(t, err, "夹具：这一次投递必须失败")
	after := replays.Load()
	for i := 0; i < 3; i++ {
		_, _ = pipe.DeliverPending()
	}
	require.Equal(t, after+3, replays.Load(),
		"失败后必须**每次都允许重试**（不被退避抑制）：自愈链的时效性靠这条守 ✓")
}

// TestReplaySuppressBackoffTable 守住退避算式（父级裁定：2^k × 5s，上限 60s）。
func TestReplaySuppressBackoffTable(t *testing.T) {
	pipe := NewPipeline(ledger.New(), testKey("src-backoff", "g1"), nil)
	want := map[int]time.Duration{
		1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second,
		4: 40 * time.Second, 5: 60 * time.Second, 6: 60 * time.Second, 9: 60 * time.Second,
	}
	for streak, w := range want {
		require.Equal(t, w, pipe.replayBackoffFor(streak), "streak=%d", streak)
	}
	// 覆盖值只影响首个间隔（测试/配置用），上限不变。
	pipe.SetReplaySuppressBase(100 * time.Millisecond)
	require.Equal(t, 100*time.Millisecond, pipe.replayBackoffFor(1))
	require.Equal(t, 400*time.Millisecond, pipe.replayBackoffFor(3))
}

// 变异记录（2026-10-05，逐字还原）：把 `replayDrainDecision` 的返回改为恒 `(false, false)`
//（= 撤销闸）⇒ TestReplayDrainSuppressedWhileWatermarkStalled 的 ① 与 ②/③ 序号断言转红
//（5 次触发实测 5 次整窗重投）；还原后转绿。
