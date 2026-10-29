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

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
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
