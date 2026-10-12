package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/sampling"
)

// buildConcurrencyFixture 造一个「模板重复、行数够多」的活文件源（采样器必然建模式表 ✓）。
func buildConcurrencyFixture(t *testing.T, lines int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	var b strings.Builder
	for i := 0; i < lines; i++ {
		// 只有 7 种模板 ⇒ 高频抑制规则会反复读写同一个签名的窗口状态。
		fmt.Fprintf(&b, "[12:00:%02d] [Server thread/INFO]: repeated message template %d\n", i%60, i%7)
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
	return path
}

// TestPipelineConcurrentPollFlushSerialized 是 **SAMPLING-1 的红证**：
// `Stop` / `PrepareCutoverReadiness` / `ImportArchivesNow` 与在飞采集 worker 会并发进到
// **同一个 Pipeline**（`cycleMu` 收窄后 worker 在分发前就放锁），而本包此前零同步原语。
//
// 本用例用 3 类并发入口压同一把锁：
//   - `Poll`（worker 侧：tailer → boundary → emit → `Sampler.Process` → WAL → 交付钩子）；
//   - `Flush`（停止/预备切换侧：tailer.FlushPartial → boundary.DrainEvents → emit）；
//   - `RefreshDegrade`（写 `lastDegradeSample` + 改采样器降级状态）。
//
// 转红方式（实测）：把 `Poll` / `Flush` / `RefreshDegrade` 顶部的 `p.mu.Lock()` 去掉（即
// 本分支修复前的行为）⇒ `-race` 报 `WARNING: DATA RACE`（最常见的一处是 `Sampler.sigs`
// 的并发写，其次是 `NormalizeBoundary`/`FileTailer` 的并发访问；非 -race 构建下也可能直接
// 以 `fatal error: concurrent map writes` 崩掉）。
func TestPipelineConcurrentPollFlushSerialized(t *testing.T) {
	path := buildConcurrencyFixture(t, 600)

	var delivered atomic.Int64
	hook := FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
		delivered.Add(int64(len(events)))
		return DeliveryResult{HTTPStatus: 200}, nil
	})
	p, err := New(Options{
		Mode:             ModeFilePrimary,
		LogSourceID:      "src-concurrent",
		SourceGeneration: "g1",
		Path:             path,
		Stream:           "stdout",
		Delivery:         hook,
		Location:         time.UTC,
		BaseTime:         time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
		// 采样**必须开启**：`Sampler` 内部无锁（自述「由采集轮对单个源串行调用」），
		// 两个 goroutine 同时 `Process` 就会并发写 `s.sigs`（map）。
		Sampling: sampling.Policy{
			Enabled: true,
			Burst:   sampling.Burst{Window: time.Second, Threshold: 1},
		},
		// `RefreshDegrade` 只有拿到容量读数才真正推进状态（否则首行即返回，压不到锁）。
		CapacityProvider: func() (acquire.CapacityBudget, error) {
			return acquire.CapacityBudget{DiskUsagePercent: 10}, nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, p.Sampler(), "采样必须真的装上，否则本用例压不到 Sampler")

	const workers = 4
	const rounds = 40
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				switch (w + i) % 3 {
				case 0:
					_, _ = p.Poll()
				case 1:
					_, _ = p.Flush()
				default:
					p.RefreshDegrade(time.Date(2026, 9, 20, 12, 0, i%60, 0, time.UTC))
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()

	// 结构性断言（任何构建下都成立）：采样读数必须自洽 —— 只有「同一批事件被两个 goroutine
	// 交错处理」才会让它分裂。
	st := p.Sampler().Stats()
	require.Greater(t, st.Seen, int64(0),
		"本用例必须真的走到采样器（否则红证为空转）：Seen=%d", st.Seen)
	require.Equal(t, st.Seen, st.Passed+st.Suppressed,
		"采样读数必须自洽：Seen=%d 但 Passed+Suppressed=%d+%d",
		st.Seen, st.Passed, st.Suppressed)
	require.Greater(t, p.DeliveredCount(), 0, "并发轮次里必须真的投出过事件")
}

// TestPipelineConcurrentPollFlushWithoutSampling 覆盖**采样关闭**的同一竞争面：
// 此时 `Sampler` 为 nil 不会崩，但 `Flush` 与 `Poll` 仍会并发操作同一个
// `NormalizeBoundary` 与 `FileTailer`（两者都无锁），故这条路径同样必须在锁内。
//
// 转红方式（实测）：同上去掉 `p.mu.Lock()` ⇒ `-race` 在 boundary/tailer 上报竞争。
func TestPipelineConcurrentPollFlushWithoutSampling(t *testing.T) {
	path := buildConcurrencyFixture(t, 400)

	var delivered atomic.Int64
	hook := FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
		delivered.Add(int64(len(events)))
		return DeliveryResult{HTTPStatus: 200}, nil
	})
	p, err := New(Options{
		Mode:             ModeFilePrimary,
		LogSourceID:      "src-concurrent-nosample",
		SourceGeneration: "g1",
		Path:             path,
		Stream:           "stdout",
		Delivery:         hook,
		Location:         time.UTC,
		BaseTime:         time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Nil(t, p.Sampler())

	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < 40; i++ {
				if (w+i)%2 == 0 {
					_, _ = p.Poll()
					continue
				}
				// Drain 一次跨 Poll + Flush 两段，必须共用同一段临界区（见 Drain 注释）。
				_, _ = p.Drain()
			}
		}(w)
	}
	close(start)
	wg.Wait()

	require.Greater(t, delivered.Load(), int64(0), "并发轮次里必须真的投出过事件")
	// 投递是**幂等**的：同一段位置不得被投两遍（位置账本按区间去重，重复投递会如实报错）。
	events, err := p.Drain()
	require.NoError(t, err)
	require.Empty(t, events, "文件已读完且已冲刷：再次 Drain 不得产出新事件")
}
