package ingest

// FR-498 P0（投影校验读回量）回归：按**事件时间**分簇 + 同批内并发。
//
// 立论（2026-10-01 实测，本地饱和夹具 60 源 ×30 行/s，见 docs/specs/log-verify-chunking）：
// 旧实现按**索引**每 500 条切一片，各片时间窗相互重叠——批内事件时间与索引顺序不一致时，
// 每一片都会把整批读回一遍，读回量放大到 k×n（k=片数），且各片串行执行。
// 单次校验查询的成本几乎全在返回体（实测 900s 窗口 / 2.7 万行 / 9.3MB → 137ms，
// 其中 VL 首字节仅 3.5ms，其余为传输与逐行 JSON 解析）。
//
// 本文件的用例：
//   1. TestVerifyProjectionChunksByTimeNotIndex   —— 读回量不放大（按时间分簇，改回按索引切即红）
//   2. TestVerifyProjectionChunkQueriesRunConcurrently —— 同批内并发（改回串行即红）
//   3. TestVerifyProjectionDetectsInjectedCorruption —— 注入缺陷仍被检出（含最晚簇，防「并发取消吞错」）
//   4. TestVerifyProjectionAcceptsWholeBatchAcrossChunks —— 多簇下 allowed（整批）语义不变

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// verifyChunkFakeVL 是「按时间窗返回窗口内全部行」的假 VL（与真实 VL 的窗口语义一致），
// 并记录查询次数、读回行数、窗口与并发峰值；支持注入三类缺陷。
type verifyChunkFakeVL struct {
	events []logtypes.Event
	delay  time.Duration
	// drop 中的 event_id 不返回（模拟「VL 少写一行」）。
	drop map[string]bool
	// mutate 把 event_id 的 _msg 改写成给定值（模拟「写错一行」）。
	mutate map[string]string
	// extra 是额外返回的行（模拟「多写/重复一行」，其 event_id 不属于本批或重复出现）。
	extra []string

	mu          sync.Mutex
	queries     int
	rowsServed  int
	extraServed int
	// mutatedServed 记录「注入的写错行」被实际写出的次数：用例必须先证明注入生效，
	// 否则断言失败会被误读成「实现漏检」。
	mutatedServed int
	windows       [][2]time.Time
	inFlight      int
	peak          int
}

func (f *verifyChunkFakeVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start, errStart := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
	end, errEnd := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
	if errStart != nil || errEnd != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.queries++
	f.windows = append(f.windows, [2]time.Time{start, end})
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if f.delay > 0 {
		// 延迟期间仍在「在飞」状态：并发实现下多个查询会同时处于该状态。
		time.Sleep(f.delay)
	}
	writeRow := func(eventID, hash, when, msg, level, stream string) {
		fmt.Fprintf(w, "{\"event_id\":%q,\"canonical_content_hash\":%q,\"_time\":%q,\"_msg\":%q,\"level\":%q,\"stream\":%q}\n",
			eventID, hash, when, msg, level, stream)
		f.mu.Lock()
		f.rowsServed++
		f.mu.Unlock()
	}
	for _, e := range f.events {
		when, err := time.Parse(time.RFC3339Nano, e.EventTimeUTC)
		if err != nil || when.Before(start) || when.After(end) {
			continue
		}
		if f.drop[e.EventID] {
			continue
		}
		msg := e.Message
		if v, ok := f.mutate[e.EventID]; ok {
			msg = v
			f.mu.Lock()
			f.mutatedServed++
			f.mu.Unlock()
		}
		writeRow(e.EventID, e.CanonicalHash, e.EventTimeUTC, msg, e.Level, e.Stream)
	}
	for _, id := range f.extra {
		// 注入生效计数：用例必须先证明「多余行确实被返回过」，否则断言失败会被误读成
		// 「实现漏检」而其实是注入没生效。
		f.mu.Lock()
		f.extraServed++
		f.mu.Unlock()
		writeRow(id, "hash-of-extra-row", start.Format(time.RFC3339Nano), "extra row", "INFO", "stdout")
	}
}

func (f *verifyChunkFakeVL) stats() (queries, rows, peak int, windows [][2]time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries, f.rowsServed, f.peak, append([][2]time.Time(nil), f.windows...)
}

func (f *verifyChunkFakeVL) extraCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.extraServed
}

func (f *verifyChunkFakeVL) mutatedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mutatedServed
}

// chunkTestEvents 生成 total 条事件，**刻意让时间顺序与索引顺序交错**：
// 第 k 条的时间 = base + (k%4)*1h + (k/4)*1ms。
// 于是「按索引每 500 条切一片」时，每片的时间跨度都覆盖 0..3h（各片窗口互相包含），
// 而「按事件时间分簇」时各簇窗口互不相接。
func chunkTestEvents(total int) []logtypes.Event {
	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	out := make([]logtypes.Event, 0, total)
	for k := 0; k < total; k++ {
		when := base.Add(time.Duration(k%4)*time.Hour + time.Duration(k/4)*time.Millisecond)
		out = append(out, logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "inst:chunk/file", SourceGeneration: "g1", ParserVersion: "v1"},
			logtypes.RecordRange{Start: uint64(k), End: uint64(k)},
			when.Format(time.RFC3339Nano), when.Format(time.RFC3339Nano),
			"INFO", "stdout", fmt.Sprintf("line %06d", k),
		))
	}
	return out
}

func chunkTestManager(t *testing.T, fixture *verifyChunkFakeVL) (*Manager, *vlsup.Client) {
	t.Helper()
	srv := httptest.NewServer(fixture)
	t.Cleanup(srv.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	m := newVerifyTestManager(10*time.Millisecond, 20*time.Millisecond, 2*time.Second)
	m.verifyChunkEvents = defaultVerifyChunkEvents
	m.verifyChunkConcurrency = defaultVerifyChunkConcurrency
	return m, client
}

// 回归 1：整批必须**只读回一次**——分簇按事件时间而非索引。
//
// 判别：4000 条（时间与索引交错）在旧实现下会按索引切成 2 片、每片时间跨度覆盖整批
// （≈8000 行读回）；新实现按时间分簇后为 2 簇，各簇窗口互不相接，读回 ≈4000 行。
// 把 planVerifyChunks 退回「按索引切」即红。
func TestVerifyProjectionChunksByTimeNotIndex(t *testing.T) {
	const total = 4000
	events := chunkTestEvents(total)
	fixture := &verifyChunkFakeVL{events: events}
	m, client := chunkTestManager(t, fixture)

	require.NoError(t, m.verifyProjection(client, SourceConfig{LogSourceID: "inst:chunk/file", SourceGeneration: "g1"},
		"projection-1", events))

	queries, rows, _, windows := fixture.stats()
	require.Equal(t, 2, queries, "4000 条按 2000/簇 应为 2 次校验查询")
	require.LessOrEqual(t, rows, total+50,
		"读回量不得随分片数放大：实测 %d 行（旧实现按索引 2 片、每片读回整批 ≈8000 行）", rows)

	// 机制证明：各查询窗口两两不得互相包含（相邻最多在 ±1s 容差处相接）。
	sort.Slice(windows, func(i, j int) bool { return windows[i][0].Before(windows[j][0]) })
	for i := 1; i < len(windows); i++ {
		overlap := windows[i-1][1].Sub(windows[i][0])
		require.LessOrEqual(t, overlap, 2*time.Second,
			"相邻簇的窗口不得互相包含（第 %d 与第 %d 个查询重叠 %s）——否则读回量随片数放大", i-1, i, overlap)
	}
}

// 回归 2：同一批内的多个簇必须**并发**在飞（改回串行即红）。
//
// 判别：把单簇上限压到 500 强制 4 簇，并让假 VL 每次查询延迟 100ms。
// 串行实现下并发峰值恒为 1、总耗时 ≥ 4×100ms；并发实现下峰值 ≥ 2、总耗时 < 3×100ms。
func TestVerifyProjectionChunkQueriesRunConcurrently(t *testing.T) {
	const total = 2000
	const delay = 200 * time.Millisecond
	events := chunkTestEvents(total)
	fixture := &verifyChunkFakeVL{events: events, delay: delay}
	m, client := chunkTestManager(t, fixture)
	m.verifyChunkEvents = 500 // 强制 4 簇

	start := time.Now()
	require.NoError(t, m.verifyProjection(client, SourceConfig{LogSourceID: "inst:chunk/file", SourceGeneration: "g1"},
		"projection-1", events))
	elapsed := time.Since(start)

	queries, _, peak, _ := fixture.stats()
	require.GreaterOrEqual(t, queries, 4, "2000 条按 500/簇 应至少 4 次查询")
	require.GreaterOrEqual(t, peak, 2,
		"同批内多个簇的校验查询必须同时在飞（峰值=%d，查询=%d）", peak, queries)
	// 不把墙钟绝对值写成断言（机器负载会影响它，容易变成对环境的测量而非对代码结构的测量）：
	// 并发峰值已足以判别「回退串行」，耗时只留痕供人工核对。
	t.Logf("并发轮耗时=%s（若串行执行，至少 4 簇 × %s = %s）", elapsed, delay, 4*delay)
}

// 回归 3：注入缺陷仍必须被检出——**注入点落在时间最晚的簇**，
// 以覆盖「并发 + 取消」不会把错误吞掉（旧实现是串行，本用例同样针对新路径）。
func TestVerifyProjectionDetectsInjectedCorruption(t *testing.T) {
	const total = 600
	events := chunkTestEvents(total)
	// 时间最晚的 50 条属于按时间排序后的最后一个簇（缺陷注入点）。
	byTime := append([]logtypes.Event(nil), events...)
	sort.SliceStable(byTime, func(i, j int) bool {
		return verifyEventTimeKey(byTime[i]).Before(verifyEventTimeKey(byTime[j]))
	})
	late := byTime[len(byTime)-50:]
	victim := late[len(late)-1].EventID

	cases := []struct {
		name    string
		fixture *verifyChunkFakeVL
		wantErr string
		// timeout 是本用例的校验窗口：判定类错误会被重试到窗口结束才作为最终错误返回
		// （与旧实现一致——不引入"确定性错误立即失败"这类行为变更），故窗口取刚好够读到缺陷的长度。
		timeout time.Duration
	}{
		{
			name:    "少写一行",
			fixture: &verifyChunkFakeVL{events: events, drop: map[string]bool{victim: true}},
			wantErr: "not fully visible before deadline",
			timeout: 2 * time.Second,
		},
		{
			name:    "写错一行",
			fixture: &verifyChunkFakeVL{events: events, mutate: map[string]string{victim: "tampered content"}},
			wantErr: "projection content mismatch for event_id " + victim,
			timeout: 10 * time.Second, // 判定类错误立即返回，窗口长度只影响「少写一行」这一支
		},
		{
			name:    "多写一行(不属于本批)",
			fixture: &verifyChunkFakeVL{events: events, extra: []string{"evt-intruder"}},
			wantErr: "unexpected or duplicate projection event_id evt-intruder",
			timeout: 10 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, client := chunkTestManager(t, tc.fixture)
			m.verifyChunkEvents = 200 // 3 簇，缺陷落在最后一簇
			m.verificationTimeout = tc.timeout
			err := m.verifyProjection(client, SourceConfig{LogSourceID: "inst:chunk/file", SourceGeneration: "g1"},
				"projection-1", events)
			queries, rows, _, _ := tc.fixture.stats()
			t.Logf("注入读数：queries=%d rows=%d mutatedServed=%d extraServed=%d err=%v",
				queries, rows, tc.fixture.mutatedCount(), tc.fixture.extraCount(), err)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr, "注入缺陷必须被检出且错误类别不变：%v", err)
		})
	}
}

// 回归 4：分簇之后，allowed（整批）语义不变——某簇的窗口返回了**其他簇**的记录时，
// 只要内容逐字段一致就必须被接受（否则误判 unexpected → 采集停摆，2026-09-28 生产事故）。
func TestVerifyProjectionAcceptsWholeBatchAcrossChunks(t *testing.T) {
	const total = 1200
	// 全部事件同一时间戳：任何一簇的窗口都会命中整批（跨簇返回的极端情形）。
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	events := make([]logtypes.Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "inst:chunk/file", SourceGeneration: "g1", ParserVersion: "v1"},
			logtypes.RecordRange{Start: uint64(i), End: uint64(i)},
			base.Format(time.RFC3339Nano), base.Format(time.RFC3339Nano),
			"INFO", "stdout", "same-instant line "+strconv.Itoa(i),
		))
	}
	fixture := &verifyChunkFakeVL{events: events}
	m, client := chunkTestManager(t, fixture)
	m.verifyChunkEvents = 500 // 3 簇，每簇窗口都覆盖整批

	require.NoError(t, m.verifyProjection(client, SourceConfig{LogSourceID: "inst:chunk/file", SourceGeneration: "g1"},
		"projection-1", events),
		"同批其他簇的记录不得被判为 unexpected")

	queries, rows, _, _ := fixture.stats()
	require.GreaterOrEqual(t, queries, 3, "1200 条按 500/簇 应为 3 次查询")
	// 同一时间戳必然跨簇命中：读回量按簇数放大属预期（窗口重叠不可避免），
	// 但每次查询的读回上限仍应是「整批」而不是「无界」。
	require.LessOrEqual(t, rows, total*queries,
		"每次查询的读回量不得超过整批规模（实测 %d 行，查询 %d 次）", rows, queries)
}
