package ingest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件守「双阈值攒批 + 并发投递」的**行为**，不是常量本身。
//
// 每个用例都写明「怎么让它转红」——即哪一处实现被改坏时它会失败，否则断言只是在复述实现。

// insertBatchTestEvents 造 n 条可投事件（Message 长度可指定，用来撑字节阈值）。
func insertBatchTestEvents(n, messageLen int) []logtypes.Event {
	if messageLen <= 0 {
		messageLen = 8
	}
	pad := string(bytes.Repeat([]byte("x"), messageLen))
	events := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, logtypes.Event{EventID: fmt.Sprintf("ev-%d", i), Message: fmt.Sprintf("%s-%d", pad, i)})
	}
	return events
}

// insertBatchServer 是一个只统计「请求数 / 并发度 / 每请求行数」的假 VL。
type insertBatchServer struct {
	*httptest.Server
	mu sync.Mutex
	// requests 是收到的请求总数（含重试）；lines 是每个请求里的 NDJSON 行数（按到达顺序）。
	requests int
	lines    []int
	// maxInflight 是观测到的最大在飞请求数（并发度证据）。
	maxInflight int
	inflight    int
	// delay 是每个请求的人为耗时。
	delay time.Duration
	// respond 决定每个请求的状态码（nil ⇒ 200）。
	respond func(request int) int
}

func newInsertBatchServer(t *testing.T) *insertBatchServer {
	t.Helper()
	s := &insertBatchServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := vlInsertRequestBody(r)
		count := 0
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) > 0 {
				count++
			}
		}
		s.mu.Lock()
		s.requests++
		seq := s.requests
		s.lines = append(s.lines, count)
		s.inflight++
		if s.inflight > s.maxInflight {
			s.maxInflight = s.inflight
		}
		delay, respond := s.delay, s.respond
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			s.inflight--
			s.mu.Unlock()
		}()
		if delay > 0 {
			time.Sleep(delay)
		}
		status := http.StatusOK
		if respond != nil {
			status = respond(seq)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *insertBatchServer) snapshot() (requests, maxInflight int, lines []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, len(s.lines))
	copy(out, s.lines)
	return s.requests, s.maxInflight, out
}

// insertBatchTestManager 建一个只用于投递路径的 Manager（真实 New，不绕过任何接线）。
func insertBatchTestManager(t *testing.T, server *insertBatchServer, tuning *InsertBatchTuning) (*Manager, *vlsup.Client) {
	t.Helper()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	journal := catalog.NewMemJournal()
	m, err := New(Options{
		Root: t.TempDir(), VL: client, Catalog: catalog.New(journal), Journal: journal,
		InsertBatch: tuning,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })
	return m, client
}

// TestInsertBatchDefaultsAreCalibrated 钉住四个生产参数（改值必须同时改本用例与常量注释里的依据）。
func TestInsertBatchDefaultsAreCalibrated(t *testing.T) {
	l := defaultInsertBatchLimits()
	require.Equal(t, 500, l.MaxEvents, "硬条数上限：2026-09-28 事故兜底")
	require.EqualValues(t, 4<<20, l.MaxBytes, "字节上限 4 MiB（压缩前），官方 10MB 的保守校准值")
	require.Equal(t, time.Second, l.FlushAfter, "时间上限 1s（Vector timeout_secs 默认）")
	require.Equal(t, 4, l.Workers, "并发 4（19 源；VL maxConcurrentRequests=16）")
	require.Equal(t, 3, l.MaxAttempts, "重试有界 3 次")
}

// TestInsertBatchEventThresholdFlushes 验证**条数阈值**单独就能切批（字节/时间都给到不可达）。
//
// 转红方式：把 shouldFlush 里的条数判据删掉 ⇒ 10 条只出 1 批（本例断言 4 批）。
func TestInsertBatchEventThresholdFlushes(t *testing.T) {
	server := newInsertBatchServer(t)
	m, _ := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 3, MaxBytes: 1 << 30, FlushAfter: time.Hour, RetryBase: time.Millisecond,
	})

	status, err := m.insertInBatches(mustClient(t, m), insertBatchTestEvents(10, 8), "g1")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	_, _, lines := server.snapshot()
	require.ElementsMatch(t, []int{1, 3, 3, 3}, lines,
		"10 条 / 每批 3 条 ⇒ 三批各 3 条 + 残批 1 条（**到达次序**可因并发而交错，故比多重集）")
	stats := m.InsertBatchStats()
	require.EqualValues(t, 4, stats.Batches)
	require.EqualValues(t, 3, stats.MaxEventsPerBatch, "条数阈值必须真的卡在 3")
}

// TestInsertBatchByteThresholdFlushes 验证**字节阈值**单独就能切批。
//
// 这里的口径是「阈值小到装不下两条」⇒ 每批恒为 1 条。
// 转红方式：把 shouldFlush 的字节判据删掉（或改成只看条数）⇒ 5 条挤成 1 批（本例断言 5 批）。
func TestInsertBatchByteThresholdFlushes(t *testing.T) {
	server := newInsertBatchServer(t)
	m, _ := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 1000, MaxBytes: 64, FlushAfter: time.Hour, RetryBase: time.Millisecond,
	})

	_, err := m.insertInBatches(mustClient(t, m), insertBatchTestEvents(5, 32), "g1")
	require.NoError(t, err)

	_, _, lines := server.snapshot()
	require.Equal(t, []int{1, 1, 1, 1, 1}, lines,
		"字节阈值 64B 装不下两条投影行 ⇒ 每条单独成批（若这里变成 [5] 说明字节判据没接线）")
}

// TestInsertBatchTimeThresholdFlushes 验证**时间阈值**单独就能切批。
//
// 攒批等待是「首个事件进批 → 交批」；阈值压到 1ms 且每两条事件之间睡 5ms ⇒ 每条都超时被切走。
//
// 转红方式：把 shouldFlush 里的 `time.Since(startedAt) >= FlushAfter` 去掉 ⇒ 三条并成 1 批
// （本例断言 3 批）。这正是「小流量下延迟无限增长」的实现形态：没人按时间兜底，残批只能等下一批
// 事件到来或整轮输入结束才被送出。
func TestInsertBatchTimeThresholdFlushes(t *testing.T) {
	server := newInsertBatchServer(t)
	m, _ := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 1000, MaxBytes: 1 << 30, FlushAfter: time.Millisecond, RetryBase: time.Millisecond,
	})

	_, err := m.insertInBatches(mustClient(t, m), insertBatchTestEvents(3, 8), "g1")
	require.NoError(t, err)
	// insertInBatches 是一次性喂入（同步），时间阈值只可能在「两次 push 之间」的墙钟上体现，
	// 因此这里直接对攒批器施压：每次 push 之间睡过阈值。
	acc := newInsertBatchAccumulator(insertBatchLimits{
		MaxEvents: 1000, MaxBytes: 1 << 30, FlushAfter: time.Millisecond,
	}, "g1")
	var flushed []*insertBatch
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		b, pushErr := acc.push(logtypes.Event{EventID: fmt.Sprintf("t-%d", i), Message: "m"})
		require.NoError(t, pushErr)
		if b != nil {
			flushed = append(flushed, b)
		}
	}
	if tail := acc.take(); tail != nil {
		flushed = append(flushed, tail)
	}
	require.Len(t, flushed, 3, "每条都超过 1ms 攒批窗口 ⇒ 各自成批")
	for i, b := range flushed {
		require.Equal(t, 1, b.events, "第 %d 批应只含 1 条", i)
	}
	// 前两批是「因超时而被切走」的（中间真的睡了 5ms）；最后一批是输入结束时的残批，
	// 它本来就不需要等（拿取是即时的）——所以只对前两批断言等待时长。
	for i := 0; i < 2; i++ {
		require.GreaterOrEqual(t, flushed[i].wait, time.Millisecond,
			"第 %d 批必须是因为超过 1ms 攒批窗口而被切走的（wait=%s）", i, flushed[i].wait)
	}
}

// TestInsertBatchEmptyBatchIsNotSent 验证**空批不发送**。
//
// 转红方式：让 dispatchInsertBatches 对空输入也发一个请求（或 accumulate 里无条件 append 一个空批）
// ⇒ requests 变成 1。
func TestInsertBatchEmptyBatchIsNotSent(t *testing.T) {
	server := newInsertBatchServer(t)
	m, client := insertBatchTestManager(t, server, &InsertBatchTuning{RetryBase: time.Millisecond})

	status, err := m.insertInBatches(client, nil, "g1")
	require.NoError(t, err)
	require.Zero(t, status)

	requests, _, _ := server.snapshot()
	require.Zero(t, requests, "零事件不得发出任何请求（VL 收到空请求只是白占一次往返）")
	require.Zero(t, m.InsertBatchStats().Batches, "空输入不产生批，也不该留下批读数")
}

// TestInsertBatchDispatchesConcurrently 验证**并发投递**真的并行（不是四个名字一个串行）。
//
// 8 个单条批 + 4 worker + 每请求 40ms：串行要 ≈320ms，并发只需 ≈80ms。
// 断言「最大在飞 ≥ 2」是并发存在的直接证据（不测墙钟，避免机器噪声）。
//
// 转红方式：把 worker 数写死成 1（或 dispatch 里改成顺序 for 循环）⇒ maxInflight 恒为 1。
func TestInsertBatchDispatchesConcurrently(t *testing.T) {
	server := newInsertBatchServer(t)
	server.delay = 40 * time.Millisecond
	m, client := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 1, Workers: 4, MaxBytes: 1 << 30, FlushAfter: time.Hour, RetryBase: time.Millisecond,
	})

	started := time.Now()
	_, err := m.insertInBatches(client, insertBatchTestEvents(8, 8), "g1")
	elapsed := time.Since(started)
	require.NoError(t, err)

	requests, maxInflight, lines := server.snapshot()
	require.Equal(t, 8, requests)
	require.Len(t, lines, 8)
	require.GreaterOrEqual(t, maxInflight, 2, "4 个 worker 必须出现并行在飞（否则并发没接线）")
	require.LessOrEqual(t, maxInflight, 4, "并发度不得超过 insertBatchWorkers")
	require.Less(t, elapsed, 8*server.delay, "8 个 40ms 请求若串行需要 320ms；并发必须明显更快")
}

// TestInsertBatchRetriesTransientFailures 验证**瞬时失败被重试**且最终成功。
//
// 转红方式：去掉 sendInsertBatch 的重试循环（只发一次）⇒ status=500、err 非 nil、
// requests=1（本例断言 requests=3 且无错）。
func TestInsertBatchRetriesTransientFailures(t *testing.T) {
	server := newInsertBatchServer(t)
	server.respond = func(request int) int {
		if request <= 2 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}
	m, client := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 100, MaxAttempts: 3, RetryBase: time.Millisecond,
	})

	status, err := m.insertInBatches(client, insertBatchTestEvents(4, 8), "g1")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	requests, _, _ := server.snapshot()
	require.Equal(t, 3, requests, "前两次 500 ⇒ 第三次成功（3 次尝试 = 1 首试 + 2 重试）")
	stats := m.InsertBatchStats()
	require.EqualValues(t, 2, stats.Retries)
	require.EqualValues(t, 3, stats.Requests)
	require.Zero(t, stats.Failures)
}

// TestInsertBatchPermanentFailureIsNotRetried 验证 4xx 语义错误**不重试**（重试也不会变）。
//
// 转红方式：把 insertBatchFailureIsPermanent 的早退删掉 ⇒ stats.Requests=3（本例断言 1）。
func TestInsertBatchPermanentFailureIsNotRetried(t *testing.T) {
	server := newInsertBatchServer(t)
	server.respond = func(int) int { return http.StatusBadRequest }
	m, client := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 100, MaxAttempts: 3, RetryBase: time.Millisecond,
	})

	status, err := m.insertInBatches(client, insertBatchTestEvents(4, 8), "g1")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.True(t, insertBatchFailureIsPermanent(status, err), "4xx 必须被上层识别成「重试无意义」")
	// 制度事实（2026-10-31 实测）：vlsup.IsPermanent 认不出写入面的 4xx——insertOnce 对
	// 非 2xx 返回的是裸 fmt.Errorf，没有 *StatusError 可 errors.As。本断言把那个事实钉住：
	// 哪天 vlsup 自己修了（改回 StatusError），本用例会提醒去简化 helper。
	require.False(t, vlsup.IsPermanent(err),
		"若这里变成 true，说明 vlsup 已改成返回 *StatusError，insertBatchFailureIsPermanent 可以简化")

	// 开销说明：400 同时是 vlsup 的「压缩体被拒」候选码，所以它自己会**再发一次未压缩的**
	// （见 InsertJSONLines 的退回逻辑）。那是传输层的尝试，不是本层的重试——所以这里
	// 断言「2 个 HTTP 请求」而 stats.Requests=1（本层的尝试次数，证明有界重试没被触发）。
	requests, _, _ := server.snapshot()
	require.Equal(t, 2, requests, "唯一的第二个请求来自 vlsup 的 gzip 退回，不是重试")
	require.EqualValues(t, 1, m.InsertBatchStats().Requests, "本层对永久失败只尝试一次")
	require.Zero(t, m.InsertBatchStats().Retries, "永久失败不得重试")
}

// TestInsertBatchExhaustedRetriesAreVisible 验证**重试耗尽仍失败**是可见失败，不静默丢。
//
// 三重可见性：① 返回错误（上层据此重排、WAL 不回收）；② 计数器 Failures>0；
// ③ 日志（`VL 批量投递失败`，本用例不解析日志，由计数与返回值承担断言）。
//
// 转红方式：让 sendInsertBatch 在最后一次失败时返回 nil 错误（静默吞掉）⇒ 本用例在 require.Error 处红。
func TestInsertBatchExhaustedRetriesAreVisible(t *testing.T) {
	server := newInsertBatchServer(t)
	server.respond = func(int) int { return http.StatusInternalServerError }
	m, client := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 100, MaxAttempts: 3, RetryBase: time.Millisecond,
	})

	status, err := m.insertInBatches(client, insertBatchTestEvents(4, 8), "g1")
	require.Error(t, err, "重试耗尽必须把错误交给上层，绝不静默丢")
	require.Equal(t, http.StatusInternalServerError, status)

	requests, _, _ := server.snapshot()
	require.Equal(t, 3, requests, "有界重试：恰好 MaxAttempts 次")
	stats := m.InsertBatchStats()
	require.EqualValues(t, 1, stats.Failures, "失败必须进计数器（FR-485 读数）")
	require.EqualValues(t, 2, stats.Retries)
}

// TestInsertBatchSmallTrafficLatencyStaysLow 是本改造的**实时性**读数与防线：
// 单条事件的小流量不得被任何阈值攒住（尤其不能被 1s 时间阈值拖一秒）。
//
// 转红方式：把 collect 改成「等攒满 500 条才发」（即改造前的固定批语义 + 无跨调用 flush）
// ⇒ 小流量下残批只能等下一轮/整轮结束，本例的墙钟断言（< 300ms）会红。
func TestInsertBatchSmallTrafficLatencyStaysLow(t *testing.T) {
	server := newInsertBatchServer(t)
	m, client := insertBatchTestManager(t, server, nil) // 生产默认参数

	started := time.Now()
	_, err := m.insertInBatches(client, insertBatchTestEvents(1, 8), "g1")
	elapsed := time.Since(started)
	require.NoError(t, err)

	requests, _, lines := server.snapshot()
	require.Equal(t, 1, requests)
	require.Equal(t, []int{1}, lines, "单条事件必须单独成批发出，不得为凑满而滞留")
	require.Less(t, elapsed, 300*time.Millisecond,
		"单条事件的投递延迟必须是「一次 HTTP 往返」量级（实测 %s）", elapsed)
	t.Logf("小流量单条事件端到端延迟：%s（生产默认 4MiB/500条/1s）", elapsed)
}

// TestInsertBatchStatsRecordDistribution 验证读数：批大小分布、攒批等待、请求数都对得上。
func TestInsertBatchStatsRecordDistribution(t *testing.T) {
	server := newInsertBatchServer(t)
	m, client := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 3, MaxBytes: 1 << 30, FlushAfter: time.Hour, RetryBase: time.Millisecond,
	})

	_, err := m.insertInBatches(client, insertBatchTestEvents(10, 8), "g1")
	require.NoError(t, err)

	stats := m.InsertBatchStats()
	require.EqualValues(t, 4, stats.Batches, "10 条 / 每批 3 条 ⇒ 4 批")
	require.EqualValues(t, 10, stats.Events)
	require.EqualValues(t, 4, stats.Requests)
	require.EqualValues(t, 3, stats.MaxEventsPerBatch)
	require.InDelta(t, 2.5, stats.AvgEventsPerBatch, 0.001)
	require.Positive(t, stats.Bytes)
	require.Positive(t, stats.MaxBytesPerBatch)
	require.GreaterOrEqual(t, stats.WaitMax, time.Duration(0), "攒批等待必须被记录")
	require.GreaterOrEqual(t, stats.WaitTotal, stats.WaitMax, "累计等待不得小于单次最大值")
}

// TestInsertBatchDefaultEventCapHoldsUnderRealisticVolume 用**生产默认参数**跑一次大输入，
// 确认「500 条硬上限」仍然生效（字节阈值不会把它顶破），且请求数被压到最少。
//
// 这是「改前 vs 改后」请求数对比的落点：小事件（投影行 ~300B）下，500 条 ≈ 150KiB，
// 字节阈值 4MiB 远未触达 ⇒ 每批仍是 500 条 ⇒ **请求数与改造前相同**（改造前也是 500/批），
// 收益来自并发与时间兜底，不是靠放大批量。
//
// 转红方式：把字节判据写成 `>` 而不是 `+next >`（边界会把 4MiB 那一刻算错）不会红；
// 真正能让它红的是把 MaxEvents 判据删掉后字节阈值极大 ⇒ 批会被撑到上千条。
func TestInsertBatchDefaultEventCapHoldsUnderRealisticVolume(t *testing.T) {
	server := newInsertBatchServer(t)
	m, client := insertBatchTestManager(t, server, nil)

	const total = 1200
	_, err := m.insertInBatches(client, insertBatchTestEvents(total, 200), "g1")
	require.NoError(t, err)

	_, _, lines := server.snapshot()
	require.Len(t, lines, 3, "1200 条 / 500 条每批 ⇒ 3 个请求（与改造前一致）")
	for i, n := range lines {
		require.LessOrEqual(t, n, insertBatchMaxEvents, "第 %d 批不得超过 500 条硬上限", i)
	}
	stats := m.InsertBatchStats()
	require.EqualValues(t, 3, stats.Batches)
	require.EqualValues(t, total, stats.Events)
	require.LessOrEqual(t, stats.MaxBytesPerBatch, int64(insertBatchMaxBytes), "单批压缩前字节不得超 4 MiB")
}

// TestInsertBatchByteCapProtectsOversizedLines 验证**超大行**：单条就超过字节上限时自己成批发出
// （不许无限等待，也不许丢弃）。这也是 2026-09-28 事故的直接防线——单请求体量有上界。
//
// 转红方式：在 push 里对「单条超过阈值」的形态提前 return（不写出）⇒ 该条丢失，本例断言条数不符。
func TestInsertBatchByteCapProtectsOversizedLines(t *testing.T) {
	server := newInsertBatchServer(t)
	m, client := insertBatchTestManager(t, server, &InsertBatchTuning{
		MaxEvents: 500, MaxBytes: 200, FlushAfter: time.Hour, RetryBase: time.Millisecond,
	})

	events := []logtypes.Event{
		{EventID: "big-1", Message: string(bytes.Repeat([]byte("y"), 4096))},
		{EventID: "big-2", Message: string(bytes.Repeat([]byte("z"), 4096))},
	}
	_, err := m.insertInBatches(client, events, "g1")
	require.NoError(t, err)

	_, _, lines := server.snapshot()
	require.Equal(t, []int{1, 1}, lines, "每条超长行各自成批（合计 2 条必须都被发出）")
	stats := m.InsertBatchStats()
	require.EqualValues(t, 2, stats.Events, "超长行不得被丢")
	require.Greater(t, stats.MaxBytesPerBatch, int64(4096), "单批字节可以超过阈值（单条就超），但只能是它自己")
}

// TestInsertBatchDispatchIsOrderedBySequence 验证**派发顺序 = 事件顺序**（同源顺序契约）。
//
// 顺序对 record_start/end 的连续性判定有意义，所以即便完成可以乱序，**派发**必须按 seq 升序。
// 做法：worker 只取 1 个，服务器记录每批第一条的 event_id（按到达顺序），必须严格递增。
//
// 转红方式：把派发循环改成并发 go jobs <- b（或不按 seq 排序）⇒ 到达顺序会乱。
func TestInsertBatchDispatchIsOrderedBySequence(t *testing.T) {
	var mu sync.Mutex
	var order []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := vlInsertRequestBody(r)
		var head struct {
			EventID string `json:"event_id"`
		}
		_ = jsonUnmarshalFirstLine(body, &head)
		mu.Lock()
		order = append(order, head.EventID)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	journal := catalog.NewMemJournal()
	m, err := New(Options{
		Root: t.TempDir(), VL: client, Catalog: catalog.New(journal), Journal: journal,
		InsertBatch: &InsertBatchTuning{MaxEvents: 1, Workers: 1, FlushAfter: time.Hour},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	events := insertBatchTestEvents(12, 8)
	_, err = m.insertInBatches(client, events, "g1")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, order, len(events))
	for i := range events {
		require.Equal(t, events[i].EventID, order[i], "第 %d 个请求必须是第 %d 条事件（派发顺序即事件顺序）", i, i)
	}
}

// jsonUnmarshalFirstLine 解析 NDJSON 的第一行（本文件私有小工具，避开与既有 helper 重名）。
func jsonUnmarshalFirstLine(body []byte, out any) error {
	if idx := bytes.IndexByte(body, '\n'); idx >= 0 {
		body = body[:idx]
	}
	return json.Unmarshal(body, out)
}

// mustClient 取 Manager 的默认 VL 客户端（用例里 clientForSource 的等价物）。
func mustClient(t *testing.T, m *Manager) *vlsup.Client {
	t.Helper()
	require.NotNil(t, m.vl)
	return m.vl
}

// 确保 context 在用例里被使用（dispatch 的取消语义走它）。
var _ = context.Background
var _ = atomic.Int64{}
