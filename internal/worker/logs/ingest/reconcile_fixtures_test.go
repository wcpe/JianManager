package ingest

// 本文件是**共享测试夹具**：内存 VL（reconcileVLFixture）、行解析、按天删除等。
//
// 2026-11-03：事件段 storage 与重建/对账链整体删除后，原 reconcile_test.go 里的回归用例
// 全部失去对象一并移除，但这些夹具仍被投递、事件状态、写前幂等等用例使用，
// 故按既有先例（见 testfixtures_test.go 的文件头说明）单独落在这里。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// reconcileStatsPath 是 VL 统计端点在夹具里的路径键。
const reconcileStatsPath = "/select/logsql/stats_query"

// reconcileVLRow 是内存 VL 中的一条记录（字段与 projectionPayload 对齐）。
type reconcileVLRow struct {
	ProjectionGeneration string `json:"projection_generation"`
	EventID              string `json:"event_id"`
	EventTimeUTC         string `json:"_time"`
	LogSourceID          string `json:"log_source_id"`
	SourceGeneration     string `json:"source_generation"`
	RecordEnd            uint64 `json:"record_end"`
	Message              string `json:"_msg"`
	Level                string `json:"level"`
	Stream               string `json:"stream"`
	CanonicalHash        string `json:"canonical_content_hash"`
}

// reconcileVLFixture 是「可对账的内存 VL」：除写入与内容查询外，还实现只读的 stats count
// 查询，并允许测试直接删/改某天的记录（模拟人为删除与内容缺失两种现场）。
type reconcileVLFixture struct {
	mu             sync.Mutex
	rows           []reconcileVLRow
	inserts        [][]byte
	statsQueries   int
	contentQueries int
	// insertAttempts 统计插入请求次数（含被 failInsert / silentDropInsert 拦下的那些）：
	// 「循环重写了多少次」靠它可数，而不是靠落库行数（后者在 drop 形态下恒为 0）。
	insertAttempts int
	// stallStats 为 true 时 stats 查询挂起直到请求被取消（模拟「对账不可信」）。
	stallStats bool
	// failInsert 为 true 时插入接口返回 500（模拟「写入直接失败」：外部条件类失败）。
	failInsert bool
	// silentDropInsert 为 true 时插入接口返回 200 但**不落任何行**
	//（模拟「写入自称成功、数据却永远不可见」= 2026-10-09 生产循环的形态：
	// `投影写入结束 status=200` 之后 `投影校验完成` 恒报 not fully visible）。
	silentDropInsert bool
	// delayedVisibilityQueries 让「刚写入的行」在**最初的 N 次内容查询里不可见**，
	// 之后才出现——精确复刻真实 VL 的「写后约 1 秒才可见」（2026-11-02 实测 1.028~1.057s）。
	//
	// 为什么要这个旋钮：缺口核对（landedVisibleInVL）此前**只查一次**，于是每次都在
	// 可见延迟窗口内查空 ⇒ 缺口永不消解 ⇒ WAL 不回收 ⇒ 积压丢最旧 ⇒ 更多缺口。
	// 有了它，「重试是否真的生效」就能被断言咬住（去掉重试 ⇒ 必红）。
	delayedVisibilityQueries int
	// visibleAfterQuery 记录每个 event_id 第一次变得可见时所处的查询序号（从 1 起），
	// 仅用于诊断输出。
	visibleAfterQuery map[string]int
	// querySeq 是内容查询的单调序号（delayedVisibilityQueries 的判据基准）。
	querySeq int
	// contentQueryDelay 给每次内容查询加固定延时（毫秒级），用于断言「多片查询是否并发」，
	// 零值 = 不延时（保持全部既有用例的行为）。
	contentQueryDelay time.Duration
	// queryGate 非 nil 时每次内容查询都挂住，用于把「确认取证进行中」钉在确定位置。
	queryGate *queryGate
}

func (f *reconcileVLFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		// VL 真实服务提供 /health；夹具此前没有它——于是「VL 未就绪」这条路径从没被任何用例走过。
		w.WriteHeader(http.StatusOK)
	case "/insert/jsonline":
		// gzip（vlsup 默认）：不解压则 parseReconcileRows 恒得 0 行，整套对账用例失去判别力。
		payload, _ := vlInsertRequestBody(r)
		f.mu.Lock()
		fail := f.failInsert
		drop := f.silentDropInsert
		f.insertAttempts++ // 无论落不落行都计：这是「循环重写了多少次」的直接读数
		if !fail && !drop {
			f.inserts = append(f.inserts, payload)
			f.rows = append(f.rows, parseReconcileRows(payload)...)
		}
		f.mu.Unlock()
		if fail {
			// 失败必须**可观测且非 2xx**：调用方据状态与错误判定"未落库"，
			// 绝不能让失败看起来像成功（这是「不把失败伪装成成功」的第一道事实来源）。
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// drop：**声称成功**但数据不可见 —— 这正是生产循环的形态（校验随后必然失败）。
		w.WriteHeader(http.StatusOK)
	case "/select/logsql/query":
		f.mu.Lock()
		f.contentQueries++
		gate := f.queryGate
		f.mu.Unlock()
		gate.block()
		f.serveContentQuery(w, r)
	case reconcileStatsPath:
		f.serveStats(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *reconcileVLFixture) serveContentQuery(w http.ResponseWriter, r *http.Request) {
	// 固定延时旋钮（零值不延时）：给「多片是否并发」这条断言一个可测量的时间基线。
	f.mu.Lock()
	queryDelay := f.contentQueryDelay
	f.mu.Unlock()
	if queryDelay > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(queryDelay):
		}
	}
	filter := parseReconcileSelector(r.URL.Query().Get("query"))
	from, to := reconcileWindow(r)
	f.mu.Lock()
	rows := append([]reconcileVLRow(nil), f.rows...)
	// 可见延迟：只让「本次查询之前就已经存在 N 次查询还看不见」——按写入序号做，
	// 见 insertVisibilityGate。这里取当前查询序号，供逐行判断是否已过延迟窗口。
	f.querySeq++
	seq := f.querySeq
	if f.visibleAfterQuery == nil {
		f.visibleAfterQuery = make(map[string]int)
	}
	delayed := f.delayedVisibilityQueries
	for _, row := range rows {
		if _, ok := f.visibleAfterQuery[row.EventID]; !ok && seq > delayed {
			f.visibleAfterQuery[row.EventID] = seq
		}
	}
	f.mu.Unlock()
	for _, row := range rows {
		// 延迟窗口内：这些行对查询不可见（但要先登记「它此刻变得可见了」）。
		if delayed > 0 {
			f.mu.Lock()
			shown := seq > delayed
			f.mu.Unlock()
			if !shown {
				continue
			}
		}
		if !filter.match(row) || !reconcileWithin(row.EventTimeUTC, from, to) {
			continue
		}
		line, err := json.Marshal(map[string]any{
			"_time": row.EventTimeUTC, "_msg": row.Message, "event_id": row.EventID,
			"level": row.Level, "stream": row.Stream, "canonical_content_hash": row.CanonicalHash,
		})
		if err != nil {
			continue
		}
		_, _ = w.Write(append(line, '\n'))
	}
}

func (f *reconcileVLFixture) serveStats(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	stall := f.stallStats
	f.statsQueries++
	f.mu.Unlock()
	if stall {
		// 挂起到请求被取消：验证对账侧的超时是有界的（客户端超时 → ctx 取消 → 回退整窗重发）。
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return
	}
	query := strings.TrimSuffix(r.URL.Query().Get("query"), " | stats count()")
	filter := parseReconcileSelector(query)
	from, to := reconcileWindow(r)
	f.mu.Lock()
	rows := append([]reconcileVLRow(nil), f.rows...)
	f.mu.Unlock()
	count := 0
	for _, row := range rows {
		if filter.match(row) && reconcileWithin(row.EventTimeUTC, from, to) {
			count++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,%q]}]}}`,
		strconv.Itoa(count))))
}

func (f *reconcileVLFixture) insertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inserts)
}

// insertAttemptCount 返回**插入请求次数**（含被 failInsert / silentDropInsert 拦下的）。
//
// 为什么需要它（与 insertCount 的区别）：在 `silentDropInsert` 形态（写入自称成功、数据不可见）
// 下没有任何行落库，`insertCount` 恒不增长 —— 而「它到底重写了几遍」正是循环重建的核心读数。
func (f *reconcileVLFixture) insertAttemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.insertAttempts
}

func (f *reconcileVLFixture) statsQueryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statsQueries
}

func (f *reconcileVLFixture) contentQueryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.contentQueries
}

// payloadsSince 返回第 index 次之后的全部插入载荷（用于断言「重启后到底写了什么」）。
func (f *reconcileVLFixture) payloadsSince(index int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index > len(f.inserts) {
		index = len(f.inserts)
	}
	out := make([]string, 0, len(f.inserts)-index)
	for _, payload := range f.inserts[index:] {
		out = append(out, string(payload))
	}
	return out
}

func (f *reconcileVLFixture) setStallStats(stall bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stallStats = stall
}

// queryGate 让测试在「确认取证的真实查询」途中挂住，从而与真实新尾发布精确重叠。
// 生产恒为 nil（零开销）。**只挂住第一次查询**：后续查询（包括新尾自己的校验查询）必须照常
// 返回，否则挂住的就不是「确认与尾巴重叠」而是把整个源都堵死了。
//   - entered：第一次查询已进入（通知测试可以推进新尾）；
//   - release：测试放行该查询。
type queryGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	used    atomic.Bool
}

func (g *queryGate) block() {
	if g == nil || g.used.Swap(true) {
		return // 只拦第一枪
	}
	g.once.Do(func() { close(g.entered) })
	<-g.release
}

// rowCount 返回当前 VL 中的行数（只读；供幂等用例断言"前提确实成立"）。
func (f *reconcileVLFixture) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

// deleteDay 删除某 UTC 天的全部记录（模拟「人为删掉 VL 某天数据」）。
func (f *reconcileVLFixture) deleteDay(day string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := make([]reconcileVLRow, 0, len(f.rows))
	for _, row := range f.rows {
		if reconcileRowDay(row) == day {
			continue
		}
		kept = append(kept, row)
	}
	f.rows = kept
}

// replaceMessage 改写某条记录的内容但保持条数不变（模拟「条数相同、内容缺失」的边界现场）。
func (f *reconcileVLFixture) replaceMessage(eventID, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.rows {
		if f.rows[index].EventID != eventID {
			continue
		}
		f.rows[index].Message = message
		f.rows[index].CanonicalHash = logtypes.CanonicalContentHash(
			f.rows[index].EventTimeUTC, f.rows[index].Level, f.rows[index].Stream, message)
	}
}

// reconcileSelectorFilter 是测试侧的选择器匹配器：只解析对账/校验用到的四个条件。
type reconcileSelectorFilter struct {
	logSourceID      string
	sourceGeneration string
	generations      []string
	recordEndMax     uint64
	eventIDs         map[string]struct{}
}

var (
	reconcileLogSourcePattern  = regexp.MustCompile(`log_source_id:="([^"]*)"`)
	reconcileSourceGenPattern  = regexp.MustCompile(`source_generation:="([^"]*)"`)
	reconcileGenerationPattern = regexp.MustCompile(`projection_generation:="([^"]*)"`)
	reconcileRecordEndPattern  = regexp.MustCompile(`record_end:<=([0-9]+)`)
)

func parseReconcileSelector(selector string) reconcileSelectorFilter {
	filter := reconcileSelectorFilter{}
	if match := reconcileLogSourcePattern.FindStringSubmatch(selector); match != nil {
		filter.logSourceID = match[1]
	}
	if match := reconcileSourceGenPattern.FindStringSubmatch(selector); match != nil {
		filter.sourceGeneration = match[1]
	}
	for _, match := range reconcileGenerationPattern.FindAllStringSubmatch(selector, -1) {
		filter.generations = append(filter.generations, match[1])
	}
	if match := reconcileRecordEndPattern.FindStringSubmatch(selector); match != nil {
		filter.recordEndMax, _ = strconv.ParseUint(match[1], 10, 64)
	}
	if _, raw, exists := strings.Cut(selector, "event_id:in("); exists {
		raw, _, _ = strings.Cut(raw, ")")
		filter.eventIDs = make(map[string]struct{})
		for _, quoted := range strings.Split(raw, ",") {
			if id, err := strconv.Unquote(strings.TrimSpace(quoted)); err == nil {
				filter.eventIDs[id] = struct{}{}
			}
		}
	}
	return filter
}

func (f reconcileSelectorFilter) match(row reconcileVLRow) bool {
	if f.logSourceID != "" && row.LogSourceID != f.logSourceID {
		return false
	}
	if f.sourceGeneration != "" && row.SourceGeneration != f.sourceGeneration {
		return false
	}
	if len(f.generations) > 0 {
		matched := false
		for _, generation := range f.generations {
			if generation == row.ProjectionGeneration {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if f.recordEndMax > 0 && row.RecordEnd > f.recordEndMax {
		return false
	}
	if f.eventIDs != nil {
		if _, ok := f.eventIDs[row.EventID]; !ok {
			return false
		}
	}
	return true
}

func parseReconcileRows(payload []byte) []reconcileVLRow {
	rows := make([]reconcileVLRow, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row reconcileVLRow
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func reconcileWindow(r *http.Request) (time.Time, time.Time) {
	from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
	to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
	return from, to
}

func reconcileWithin(eventTime string, from, to time.Time) bool {
	when, err := time.Parse(time.RFC3339Nano, eventTime)
	if err != nil {
		// Raw 事件（无语义时间）在**真实 VL** 里的 `_time` 是摄取时间，不会为空。
		// 夹具存的是空串时，这里不能一律当「窗口外」——那会把每条 Raw 行都滤掉，
		// 使「Raw 正文在不在 VL」这类断言永远拿不到证据（实测 TestIngestSixFixRawEvidenceUsesStableCanonicalTime：
		// `fixture.rowCount()` 是 1，但 `landedVisibleInVL` 恒返回空）。
		//
		// 故：无法解析的时间视为「无法用于窗口判定」⇒ 只要查询带了窗口就放行。
		// 这是**夹具**的近似（真实 VL 不会出现空 `_time`），不影响生产语义。
		return true
	}
	if !from.IsZero() && when.Before(from) {
		return false
	}
	if !to.IsZero() && when.After(to) {
		return false
	}
	return true
}

func reconcileRowDay(row reconcileVLRow) string {
	when, err := time.Parse(time.RFC3339Nano, row.EventTimeUTC)
	if err != nil {
		return ""
	}
	return when.UTC().Format("2006-01-02")
}

func newReconcileVL(t *testing.T) (*vlsup.Client, *reconcileVLFixture, *httptest.Server) {
	t.Helper()
	fixture := &reconcileVLFixture{}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture, server
}

// reconcileTestSource 是回归用的源配置（两天的日志都落在过去，避免与「今天」耦合）。
//
// Path 必须真实存在：Register 会为 FILE_PRIMARY 源建立 FileTailer，pipeline 会校验路径。
// 回归不驱动 poll（只驱动启动恢复），故文件内容保持为空。
func reconcileTestSource(t *testing.T, root string) SourceConfig {
	t.Helper()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o600))
	return SourceConfig{
		LogSourceID: "node:reconcile", SourceGeneration: "g1",
		Path: logPath, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:reconcile", UTCDay: runtimeTestUTCDay(),
	}
}

// reconcileTestEvents 返回跨两个 UTC 日的三条权威事件（09-22 两条、09-23 一条）。
func reconcileTestEvents() []logtypes.Event {
	identity := logtypes.SourceIdentity{LogSourceID: "node:reconcile", SourceGeneration: "g1", ParserVersion: "v1"}
	return []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 1, End: 10}, "2026-09-22T01:00:00Z", "2026-09-22T01:00:01Z", "INFO", "stdout", "day one alpha"),
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 10, End: 20}, "2026-09-22T02:00:00Z", "2026-09-22T02:00:02Z", "INFO", "stdout", "day one beta"),
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 20, End: 30}, "2026-09-23T01:00:00Z", "2026-09-23T01:00:03Z", "INFO", "stdout", "day two alpha"),
	}
}

// seedProjection 复现「重启前现场」：把权威事件按天写进 VL、发布 catalog、落段存储并持久化。
//
// 这里手动 New + Stop（不走 newTestManager 的 t.Cleanup）：seed 出来的进程必须在重启前

// seedProjection 复现「重启前现场」：把事件写进 VL 并发布 catalog。
//
// 2026-11-03：原实现走 `writeProjection`（重建路径，已随事件段一并删除）。
// 现在改走投递主路径（`deliverCtx` → 写 VL → 建 Catalog 分区），这正是生产实际发生的事。
// 手动 New + Stop（不走 newTestManager 的 Cleanup）：seed 出来的进程必须在重启前完全关闭，
// 否则 Cleanup 会用旧内存状态再持久化一次，把重启后的索引覆盖回旧代次，污染「重启」语义。
func seedProjection(t *testing.T, client *vlsup.Client, root string, journal catalog.Journal, cat *catalog.Catalog, source SourceConfig, events []logtypes.Event) {
	t.Helper()
	manager, err := New(Options{
		Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source},
		VerificationTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	stopped := false
	defer func() {
		if !stopped {
			_ = manager.Stop()
		}
	}()
	_, err = manager.deliver(source, events, false)
	require.NoError(t, err)
	require.NoError(t, manager.Stop())
	stopped = true
}

// newProbeManager 构造带**真实 pipeline**（真账本 + 真 WAL）的最小 Manager：
// persist 路径需要管道才有账本修订号（无管道的源按"每轮都变更"处理，测不到守卫与复验）。
//
// 2026-11-03：原在 rebuild_source_outside_lock_test.go（该文件随重建链一并删除），
// 但本夹具与重建无关，属通用探测夹具，故迁到这里供 resolve_gaps_* 用例使用。
func newProbeManager(t *testing.T) (*Manager, SourceConfig, string, *pipeline.Pipeline) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	src := SourceConfig{
		LogSourceID:      "node:probe",
		SourceGeneration: "g1",
		Path:             path,
		StorageNamespace: "ns:probe",
		UTCDay:           runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(nil), Sources: []SourceConfig{src}})
	require.NoError(t, err)
	key := src.LogSourceID + "/" + src.SourceGeneration
	p := m.pipes[key]
	require.NotNil(t, p, "夹具必须登记出真实管道")
	return m, src, key, p
}
