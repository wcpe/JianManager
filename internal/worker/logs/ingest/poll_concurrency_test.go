package ingest

// FR-498 采集侧饱和回归（可转红替身）。
//
// 立论（2026-10-01 实测）：60 源基线单轮 99.6s 里 pollSource=99.6s，其中 deliver=90.8s，
// 而 deliver 里**投影校验查询**（VL LogsQL 往返，含退避重试）=85.3s（占整轮 85.6%）；
// 同期 Worker CPU 仅 17–45%、VL CPU 仅 7–23% —— 两边都没打满，瓶颈是**等待被串行叠加**：
// 轮周期 ≈ 源数 × 单源等待（实测 ≈1.65s/源 → ≈99s/轮），采集上限因此被钉在
// ≈60×2000 行 ÷ 99s ≈ 1.2k 行/s（不足 30 行/s × 60 源生成速率的 70%）。
//
// 本用例把「校验查询」的往返延迟放大成可观测的固定值，然后断言：同一轮采集里**多个源
// 的校验查询是同时在飞的**（等待被重叠），而不是一个源接一个源地串行等待。
// 把 pollOnce 的跨源并发退回串行（等价旧实现）时，并发峰值必然退回 1，本用例即红。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// delayedVerifyVL 只在「校验查询」（带时间窗的 logsql/query）上注入固定延迟，模拟真实 VL 上
// 单次校验 100ms 量级的往返；插入与代次占用探测（无时间窗）保持即时，避免把用例变成对 VL
// 其它路径的测量。同时记录「同时在飞的校验查询数」的峰值——这就是等待是否被重叠的直接读数。
type delayedVerifyVL struct {
	inner   http.Handler
	delay   time.Duration
	queries atomic.Int64
	inFlt   atomic.Int64
	peak    atomic.Int64
}

func (d *delayedVerifyVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/select/logsql/query" && r.URL.Query().Get("start") != "" {
		cur := d.inFlt.Add(1)
		for {
			peak := d.peak.Load()
			if cur <= peak || d.peak.CompareAndSwap(peak, cur) {
				break
			}
		}
		d.queries.Add(1)
		time.Sleep(d.delay)
		d.inFlt.Add(-1)
	}
	d.inner.ServeHTTP(w, r)
}

// pollTestLines 生成 count 行可解析的 MC 风格日志（时间戳唯一，避免身份冲突）。
func pollTestLines(from, count int) string {
	var b []byte
	for i := 0; i < count; i++ {
		n := from + i
		b = append(b, fmt.Sprintf("[12:%02d:%02d] [Server thread/INFO]: line %06d\n", n/60%60, n%60, n)...)
	}
	return string(b)
}

func appendPollTestLines(t *testing.T, path string, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(text)
	require.NoError(t, err)
}

func TestPollRoundOverlapsSourceWaits(t *testing.T) {
	const sources = 12
	// 单次校验延迟取 100ms：与真实 VL 上实测的单次校验往返（141ms/次，60 源 × 2000 行基线）
	// 同量级，足以让「源数 × 单源等待」显著大于轮内**串行资源**（索引持久化 / 事件段 fsync /
	// 发布锁）的总和——后者是并发也压不下去的 Amdahl 部分，不把它算进断言里比把它混进来更诚实。
	const queryDelay = 100 * time.Millisecond

	fixture := &projectionVLFixture{}
	delayed := &delayedVerifyVL{inner: fixture, delay: queryDelay}
	server := httptest.NewServer(delayed)
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)

	root := t.TempDir()
	cfgs := make([]SourceConfig, 0, sources)
	for i := 0; i < sources; i++ {
		dir := filepath.Join(root, fmt.Sprintf("s%02d", i))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		path := filepath.Join(dir, "latest.log")
		require.NoError(t, os.WriteFile(path, []byte(pollTestLines(0, 100)), 0o600))
		cfgs = append(cfgs, SourceConfig{
			LogSourceID:      fmt.Sprintf("node:poll-%02d", i),
			SourceGeneration: "g1",
			Path:             path,
			Mode:             pipeline.ModeFilePrimary,
			StorageNamespace: fmt.Sprintf("ns:poll-%02d", i),
			UTCDay:           runtimeTestUTCDay(),
		})
	}
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: cfgs})
	require.NoError(t, err)

	// 第 1 轮：串行（旧实现的工作点）——每源的等待被逐源叠加，同时在飞的校验查询峰值为 1。
	m.pollConcurrency = 1
	serialStart := time.Now()
	m.pollOnce()
	serial := time.Since(serialStart)
	require.Positive(t, delayed.queries.Load(), "本用例必须真的走到校验查询，否则测的不是等待重叠")
	require.Equal(t, int64(1), delayed.peak.Load(),
		"串行基线（pollConcurrency=1）不应出现并发校验查询")

	// 追加新行，让第 2 轮同样有整批投递工作。
	for i, cfg := range cfgs {
		appendPollTestLines(t, cfg.Path, pollTestLines(100*(i+1), 100))
	}
	m.pollConcurrency = sources
	concurrentStart := time.Now()
	m.pollOnce()
	concurrent := time.Since(concurrentStart)

	// 主断言（判别性）：并发轮里多个源的校验查询同时在飞——等待被重叠。旧实现（逐源串行）
	// 下该峰值只能是 1，本断言即红。
	require.GreaterOrEqual(t, delayed.peak.Load(), int64(2),
		"单轮采集必须跨源重叠等待：同时在飞的校验查询峰值=%d（源数=%d，单次校验延迟=%s）",
		delayed.peak.Load(), sources, queryDelay)

	// 辅助断言（不引入退化）：并发轮的墙钟不得比串行轮慢一半以上。刻意放宽到 1.5 倍——
	// race 模式下索引持久化这类**串行资源**（12 源 × 每源一次 DurablePersist）会被放大到
	// 秒级并在两轮里同样发生，而并发轮还要额外付出调度与锁竞争，用倍数阈值会让用例变成
	// 对机器负载的测量而不是对代码结构的测量。
	require.LessOrEqual(t, concurrent, serial*3/2,
		"并发轮出现显著退化：串行=%s 并发=%s（源数=%d）", serial, concurrent, sources)

	t.Logf("串行轮=%s 并发轮=%s 源数=%d 校验查询次数=%d 并发校验峰值=%d",
		serial, concurrent, sources, delayed.queries.Load(), delayed.peak.Load())
}
