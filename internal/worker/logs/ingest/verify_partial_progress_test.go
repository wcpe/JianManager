package ingest

// 2026-10-10 生产活锁（`inst:220/file` WAL 积压恒 424205 条 / 3.5 小时不降）的回归。
//
// ── 现场机制（逐行确证，非推测）──────────────────────────────────────────────────
//   `gap_autoresolve.go` 的暂停源自愈路径每 10s 调一次 `DeliverPending` ⇒ `reclaimProof`
//   ⇒ `releaseRecovery` ⇒ `verifyProjectionCtxYield`；事件数 = 该源积压 544205
//   ⇒ `planVerifyChunks(events, 2000)` 切成 **272 簇**；每簇走 `verifyLimiter`
//   （生产配 `max_per_second: 8` / `max_in_flight: 2`），单簇实测 17–23 秒；
//   而单源预算只有 `defaultVerifyRoundBudget = 30s` ⇒ 272 簇根本跑不完。
//   更要命的是旧实现里**已成功的约 30 簇不留任何痕迹**（任何一簇失败即停止派发剩余簇，
//   且返回值只有 error）⇒ 下一轮从第 1 簇重来 ⇒ 永远跑不完 ⇒ 不 `publishSnapshot`
//   ⇒ 代次不推进 ⇒ 10s 后再触发 ⇒ 无限循环。
//
// ── 本文件守住什么 ─────────────────────────────────────────────────────────────
// ① 部分簇成功时：**返回错误不变**（校验未全通过绝不返回 nil），但附带返回**只含成功簇**的
//    区间（失败簇/未派发簇的字节绝不出现）；
// ② 这份区间被登记进该源的持久凭据（VerifiedRuns）⇒ 下一轮不必从零开始；
// ③ 单源预算随事件数放大（改动 B）：仅显式配置估计时生效、**必被上限封顶**、默认与修复前
//    逐字一致；
// ④ 自愈路径失败退避（改动 C）：**有进展不退避**（否则扼杀 ① 的收敛），无进展才指数退避。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// partialVerifyVL 是「只让指定簇可见」的假 VL：按簇的时间窗判定该窗口内的行是否返回。
//
// 与既有夹具（verifyChunkFakeVL）的差别：这里要**逐簇**决定成功/失败，而簇是按事件时间
// 划分的（见 planVerifyChunks）⇒ 判据必须是「该行的事件时间落在哪些待返回窗口内」。
type partialVerifyVL struct {
	mu sync.Mutex
	// rows 是全部写入过的行（raw NDJSON 行原文），按事件时间升序无关，由窗口过滤决定可见性。
	rows []map[string]any
	// hiddenWindows 中任一窗口命中的行**不返回**（模拟"这段数据尚不可见"）。
	hiddenWindows [][2]time.Time
	queries       int
}

// seed 把一批事件按**真实写入面**的同一格式（projectionPayload）灌进假 VL，
// 使"窗口内的行确实可见"这一判定与生产同构。
func (f *partialVerifyVL) seed(t *testing.T, events []logtypes.Event, generation string) {
	t.Helper()
	payload, err := projectionPayload(events, generation)
	require.NoError(t, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		f.rows = append(f.rows, row)
	}
}

// hide 让给定时间窗内的行不可见（模拟"这段数据尚不可见"）。
func (f *partialVerifyVL) hide(from, to time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hiddenWindows = append(f.hiddenWindows, [2]time.Time{from, to})
}

// clearHidden 清空不可见窗口（用例在轮次之间改变"哪一段不可见"）。
func (f *partialVerifyVL) clearHidden() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hiddenWindows = nil
}

func newPartialVerifyVL(t *testing.T) (*vlsup.Client, *partialVerifyVL) {
	t.Helper()
	fixture := &partialVerifyVL{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/insert/jsonline":
			payload, _ := io.ReadAll(r.Body)
			fixture.mu.Lock()
			for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
				var row map[string]any
				if json.Unmarshal([]byte(line), &row) == nil {
					fixture.rows = append(fixture.rows, row)
				}
			}
			fixture.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case "/select/logsql/query":
			fixture.mu.Lock()
			fixture.queries++
			rows := append([]map[string]any(nil), fixture.rows...)
			hidden := append([][2]time.Time(nil), fixture.hiddenWindows...)
			fixture.mu.Unlock()
			selector := r.URL.Query().Get("query")
			for _, row := range rows {
				if gc, _ := row["projection_generation"].(string); gc == "" ||
					!strings.Contains(selector, "projection_generation:="+strconv.Quote(gc)) {
					continue
				}
				if src, _ := row["log_source_id"].(string); src != "" &&
					strings.Contains(selector, "log_source_id:=") &&
					!strings.Contains(selector, "log_source_id:="+strconv.Quote(src)) {
					continue
				}
				when, err := time.Parse(time.RFC3339Nano, fmt.Sprint(row["_time"]))
				if err != nil {
					continue
				}
				skip := false
				for _, win := range hidden {
					if !when.Before(win[0]) && !when.After(win[1]) {
						skip = true
						break
					}
				}
				if skip {
					continue
				}
				encoded, err := json.Marshal(row)
				if err != nil {
					continue
				}
				_, _ = w.Write(append(encoded, '\n'))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture
}

// partialVerifyEvents 造 n 条事件，事件时间按 index 均匀递增（便于按窗口精确切簇）。
func partialVerifyEvents(n int, base time.Time) []logtypes.Event {
	identity := logtypes.SourceIdentity{LogSourceID: "inst:partial/file", SourceGeneration: "g1", ParserVersion: "v1"}
	events := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		start := uint64(i * 10)
		when := base.Add(time.Duration(i) * time.Second)
		events = append(events, logtypes.BuildEvent(identity, logtypes.RecordRange{Start: start, End: start + 9},
			when.Format(time.RFC3339Nano), when.Format(time.RFC3339Nano), "INFO", "stdout",
			fmt.Sprintf("partial event %d", i)))
	}
	return events
}

// TestVerifyPartialChunksReportOnlyPassedRanges 是本文件的核心闸门（改动 A 的主回归）。
//
// 形态：4 簇（8 条事件 / 每簇 2 条），让**第 3 簇**的那段时间窗不可见 ⇒ 第 3 簇必失败。
//   - 断言 ①：仍然返回**错误**（校验没全通过就绝不能返回 nil —— 这是语义红线）；
//   - 断言 ②：返回的区间**只含成功簇**的源位置，第 3 簇（以及未被派发的第 4 簇）的字节
//     一个都不能出现——否则会把「从没校验过」的字节宣称成「已逐字段校验通过」。
//
// 变异验证（实测见回复）：
//   - 删掉 goroutine 里的 `verifiedRuns = append(...)`（区间累积）⇒ 断言 ② 转红（区间为空）；
//   - 把 `if err == nil` 改成无条件累积 ⇒ 断言 ② 转红（失败簇的字节混进结果）。
func TestVerifyPartialChunksReportOnlyPassedRanges(t *testing.T) {
	client, fixture := newPartialVerifyVL(t)
	base := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	events := partialVerifyEvents(8, base) // 8 条 ⇒ 每簇 2 条 ⇒ 4 簇

	source := SourceConfig{
		LogSourceID: "inst:partial/file", SourceGeneration: "g1",
		StorageNamespace: "inst:partial", UTCDay: base.Format("2006-01-02"),
	}
	journal := catalog.NewMemJournal()
	m, err := New(Options{Root: t.TempDir(), VL: client, Catalog: catalog.New(journal), Journal: journal})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })
	m.verifyChunkEvents = 2      // 每簇 2 条 ⇒ 4 簇（精确控制哪一簇失败）
	m.verifyChunkConcurrency = 1 // 串行：让"第 3 簇失败"的判定确定（并发下取消顺序不确定）
	m.verificationTimeout = 300 * time.Millisecond
	m.verifyBackoffMin, m.verifyBackoffMax = 5*time.Millisecond, 5*time.Millisecond
	m.verifyMaxQueriesPerChunk = 2 // 每簇 2 次尝试即放弃 ⇒ 用例快而不放大失败簇

	// 让第 3、4 簇（事件 index 4–7，时间 [base+4s, base+7s]）不可见 ⇒ 只剩前两簇通过。
	// 为什么连第 4 簇一起隐藏：可见性失败**不**触发 cancel（这是刻意设计，见 verifyErrorIsSemantic），
	// 故第 4 簇仍会被派发；只隐藏第 3 簇的话它会照常成功，就不是"部分成功"的形态了。
	fixture.hide(base.Add(4*time.Second), base.Add(7*time.Second))
	fixture.seed(t, events, "projection-1")

	passed, err := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, "projection-1", events, nil)
	require.Error(t, err, "有簇未通过时**必须**返回错误（校验未全通过绝不能返回 nil）")
	require.False(t, verifyErrorIsSemantic(err),
		"错误分类不受影响：'not fully visible' 仍**不**属于判定类（runtime.go 的刻意设计 ✗ 不得改）")
	require.Contains(t, err.Error(), "not fully visible before deadline",
		"结论措辞必须落在既有措辞族内（实测 %q）", err.Error())

	require.NotEmpty(t, passed, "部分簇成功时必须回报已通过区间（否则下一轮仍从零开始 ⇒ 活锁不破）")

	// 失败簇（index 4–7 ⇒ 位置 [40, 79]）的字节**一个都不能出现**在已通过区间里。
	for _, run := range passed {
		require.True(t, run.To < 40,
			"未通过的簇（起点 40 起）不得出现在已通过区间里：%+v", run)
	}
	// 成功簇（index 0–3 ⇒ 位置 [0, 39]）必须被覆盖且**连成一段**（相邻区间会合并）。
	require.Equal(t, []ledger.PositionRange{{From: 0, To: 39}}, passed,
		"已通过区间必须精确覆盖第 1–2 簇（位置 [0,39]）：失败簇与未通过簇一个字节都不能混入")
}

// TestVerifyPartialProgressIsRecordedForNextRound 断言改动 A 的**闭环**：
// deliver 路径把部分通过区间登记进该源的持久凭据 ⇒ 下一轮不必从零开始。
//
// 这里直接用 verifyProjectionCtxYieldRuns 的返回值 + recordVerifiedRuns 复刻 deliver 的
// 接线（同一个原语、同一个 key 构造），并断言：
//   - 凭据里出现**恰好**成功簇的那一段；
//   - 第二轮（同一批再次校验）后凭据不变（合并幂等，不会无界增长）；
//   - 且第二次登记**不报告新增**（进展判据必须忽略纯重复登记，否则退避会被永久重置 ✗）。
func TestVerifyPartialProgressIsRecordedForNextRound(t *testing.T) {
	client, fixture := newPartialVerifyVL(t)
	base := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	events := partialVerifyEvents(8, base)

	source := SourceConfig{
		LogSourceID: "inst:partial/file", SourceGeneration: "g1",
		StorageNamespace: "inst:partial", UTCDay: base.Format("2006-01-02"),
	}
	journal := catalog.NewMemJournal()
	root := t.TempDir()
	m, err := New(Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })
	key := source.LogSourceID + "/" + source.SourceGeneration
	// 登记源（recordVerifiedRuns 的落点就是 state.Sources）——路径只需存在（本用例不采集）。
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o600))
	source.Path, source.Mode = logPath, pipeline.ModeFilePrimary
	require.NoError(t, m.Register(source))
	// Register 只写 SourceConfigs；逐源状态条目由投递路径惰性创建。本用例直接考凭据登记，
	// 故显式建好该条目（与 gap_autoresolve_test 的既有写法同形）。
	m.mu.Lock()
	if _, ok := m.state.Sources[key]; !ok {
		m.state.Sources[key] = persistedSource{}
	}
	m.mu.Unlock()
	require.NotNil(t, m.state.Sources[key], "前置：逐源状态条目必须存在（recordVerifiedRuns 的落点）")
	m.verifyChunkEvents = 2
	m.verifyChunkConcurrency = 1
	m.verificationTimeout = 300 * time.Millisecond
	m.verifyBackoffMin, m.verifyBackoffMax = 5*time.Millisecond, 5*time.Millisecond
	m.verifyMaxQueriesPerChunk = 2
	// 隐藏第 3、4 簇（index 4–7）⇒ 只有前两簇（位置 [0,39]）通过。
	// 连第 4 簇一起隐藏的理由见 TestVerifyPartialChunksReportOnlyPassedRanges：
	// 可见性失败**不**触发 cancel，第 4 簇仍会被派发并照常成功。
	fixture.hide(base.Add(4*time.Second), base.Add(7*time.Second))
	fixture.seed(t, events, "projection-1")

	passed, err := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, "projection-1", events, nil)
	require.Error(t, err)
	require.NotEmpty(t, passed)

	reported := m.recordVerifiedRunsReporting(key, passed)
	require.True(t, reported, "首次登记成功簇区间必须被报告为「有进展」")
	require.Equal(t, []ledger.PositionRange{{From: 0, To: 39}}, m.verifiedRuns(key),
		"持久凭据必须恰好是成功簇的覆盖区间")

	// 第二轮：**同一批**再校验一次（正是现场的形态：一轮跑不完，下一轮重来），
	// 成功簇不变 ⇒ 登记必须**幂等**且**不报告进展**。
	passed2, err2 := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, "projection-1", events, nil)
	require.Error(t, err2)
	require.Equal(t, passed, passed2, "同一批的已通过区间必须可复现（同判定、同分簇）")
	require.False(t, m.recordVerifiedRunsReporting(key, passed2),
		"纯重复登记不得被报告为进展（否则退避会被永久重置 ⇒ 退避形同虚设 ✗）")
	require.Equal(t, []ledger.PositionRange{{From: 0, To: 39}}, m.verifiedRuns(key),
		"重复登记后凭据不得改变（合并幂等 ✓）")

	// 第三轮：改为只隐藏第 1 簇（index 0–1）⇒ 第 2–4 簇通过（位置 [20,79]）。
	// 这带来**新覆盖**（39 → 79 的延伸），必须被报告为进展——这正是"退避必须被解除"的信号。
	fixture.clearHidden()
	fixture.hide(base, base.Add(time.Second))
	passed3, err3 := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, "projection-1", events, nil)
	require.Error(t, err3)
	require.Equal(t, []ledger.PositionRange{{From: 20, To: 79}}, passed3,
		"只剩第 2–4 簇通过时，已通过区间必须精确是 [20,79]（实测 %+v）", passed3)
	require.True(t, m.recordVerifiedRunsReporting(key, passed3),
		"更靠后的新覆盖必须被报告为进展（否则渐进收敛会被退避掐死 ✗）")
	require.Equal(t, []ledger.PositionRange{{From: 0, To: 79}}, m.verifiedRuns(key),
		"凭据是**累积**的：新覆盖与旧覆盖合并为 [0,79]（渐进收敛语义 ✓）")
}

// TestVerifyChunkBudgetScalesWithEventsBoundedByMax 是改动 B 的闸门。
//
// 三条性质：
//  1. **保守默认**：未配 chunk_estimate ⇒ 预算与修复前逐字一致（等于常量预算）；
//  2. **随事件数放大且被上限封顶**：簇数 × 单簇估计 × 安全系数，且永远 ≤ Max；
//  3. **常量预算优先**：上限低于运维显式配置的 round_budget 时，不得回头压低常量预算。
//
// 变异验证（实测见回复）：去掉 `if scaled > max { scaled = max }` 的封顶 ⇒ 性质 2 转红
// （272 簇 × 17s × 1.5 ≈ 115 分钟 ≫ 5 分钟上限）。
func TestVerifyChunkBudgetScalesWithEventsBoundedByMax(t *testing.T) {
	journal := catalog.NewMemJournal()
	m, err := New(Options{Root: t.TempDir(), Catalog: catalog.New(journal), Journal: journal})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })
	m.verifyChunkEvents = 2000
	m.verifyRoundBudget = 30 * time.Second

	// 性质 1：默认不放大（现场 544205 事件 ⇒ 272 簇，仍只给常量预算）。
	require.Equal(t, 30*time.Second, m.verifyBudgetOfEvents(544205),
		"未配置 chunk_estimate 时预算必须与修复前逐字一致（保守默认 ⇒ 不影响现有源）")

	// 性质 2：显式给出估计后按簇数放大，并被 5 分钟上限封顶。
	m.verifyChunkEstimates = VerifyChunkEstimates{PerChunk: 17 * time.Second}
	require.Equal(t, 5*time.Minute, m.verifyBudgetOfEvents(544205),
		"272 簇 × 17s × 1.5 ≈ 115 分钟必须被上限截到 5 分钟（否则单源占住校验额度近两小时 ✗）")
	require.Equal(t, 5*time.Minute, m.verifyBudgetOfEvents(100000),
		"放大值超上限时同样封顶")
	// 小批：放大值低于常量预算 ⇒ 常量预算优先（放大只在"常量不够"时生效）。
	require.Equal(t, 30*time.Second, m.verifyBudgetOfEvents(2000),
		"单簇：折算后 25.5s < 常量 30s ⇒ 保持常量预算（放大不缩小预算 ✓）")

	// 上限可配：把上限调到高于折算值 ⇒ 生效值是折算值本身。
	// （基数是 round_budget=30s；折算 3 簇 × 20s × 1.5 = 90s > 30s ⇒ 放大生效。）
	m.verifyChunkEstimates = VerifyChunkEstimates{PerChunk: 20 * time.Second, Max: time.Hour}
	require.Equal(t, 90*time.Second, m.verifyBudgetOfEvents(6000),
		"3 簇（6000/2000）× 20s × 1.5 = 90s（未触上限 ⇒ 用折算值）")
	require.Equal(t, 60*time.Second, m.verifyBudgetOfEvents(4000),
		"2 簇 × 20s × 1.5 = 60s（簇数按**向上取整**折算 ✓）")

	// 性质 3：上限不得低头压低显式配置的常量预算。
	m.verifyRoundBudget = 10 * time.Minute
	m.verifyChunkEstimates = VerifyChunkEstimates{PerChunk: time.Second, Max: time.Minute}
	require.Equal(t, 10*time.Minute, m.verifyBudgetOfEvents(544205),
		"上限只封顶**放大值**；运维显式配的常量预算（10 分钟）不得被回头压低 ✗")
}

// TestVerifySourceDeadlineUsesBatchSize 断言预算档位真的接到了「即将校验」处：
// 交付给 setVerifySourceDeadlineFor 的事件数决定了本源的截止时刻。
//
// 变异验证（实测见回复）：把 verifyProjectionCtxYieldRuns 里的
// `m.setVerifySourceDeadlineFor(source, len(events))` 改回 `m.setVerifySourceDeadline(source)`
// ⇒ 本用例转红（大批得到的剩余预算退化为常量档位）。
func TestVerifySourceDeadlineUsesBatchSize(t *testing.T) {
	client, fixture := newPartialVerifyVL(t)
	base := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	events := partialVerifyEvents(8, base)
	source := SourceConfig{
		LogSourceID: "inst:partial/file", SourceGeneration: "g1",
		StorageNamespace: "inst:partial", UTCDay: base.Format("2006-01-02"),
	}
	journal := catalog.NewMemJournal()
	m, err := New(Options{Root: t.TempDir(), VL: client, Catalog: catalog.New(journal), Journal: journal})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })
	m.verifyChunkEvents = 2
	m.verifyChunkConcurrency = 1
	m.verifyRoundBudget = time.Second
	m.verifyChunkEstimates = VerifyChunkEstimates{PerChunk: time.Second, Max: time.Hour}
	m.verificationTimeout = 100 * time.Millisecond
	m.verifyBackoffMin, m.verifyBackoffMax = time.Millisecond, time.Millisecond
	m.verifyMaxQueriesPerChunk = 1
	fixture.seed(t, events, "projection-1") // 全可见 ⇒ 4 簇全通过

	// 4 簇 × 1s × 1.5 = 6s 预算（远大于常量 1s）⇒ 4 簇全部跑完（无不可见窗口）。
	st := m.verifySourceState(source)
	before := time.Now()
	passed, err := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, "projection-1", events, nil)
	require.NoError(t, err, "4 簇 × 1.5s 预算足够 ⇒ 必须全部通过")
	require.Empty(t, passed, "全通过时不回报部分区间（成功路径已有更强事实 ⇒ 避免无谓的整批合并）")
	// 截止时刻必须落在"按 4 簇放大后的预算"量级（常量 1s 档位不可能这么远）。
	deadline := time.Unix(0, st.deadlineNanos.Load())
	require.Greater(t, deadline.Sub(before), 3*time.Second,
		"本源截止必须按本批簇数放大（实测剩余 %s；常量档位只有 1s）", deadline.Sub(before))
}
