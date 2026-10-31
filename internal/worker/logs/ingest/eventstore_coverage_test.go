package ingest

// 2026-10-10 根治「代次可见性不一致」的**真根因**回归：段存储按身份收敛，而不是按物理行数比较。
//
// 立论（生产只读取证，2026-10-09/10）：
// 事件段是「只增不减的权威超集」，**允许包含冗余行**——同 event_id 且同 canonical_content_hash
// 的重复行，语义等价。生产实测 inst:148 段 135113 行 / 134981 个唯一 event_id（132 行冗余）；
// 全生产 158 源里 36 源含此类冗余共 59556 行（单源最多 13998 行）。而权威集合是按身份去重的。
//
// 旧实现把两者当「同一条前缀的长度」直接比较：
//
//	stored, _ := m.events.Count(key)   // 物理行数
//	if stored > len(events) { return fmt.Errorf("eventstore ahead of authoritative set") }
//	m.events.Append(key, events[stored:])   // 还按物理行数切片 ⇒ 与身份错位
//
// 于是纯冗余行被误判成「段被外部改写 / 恢复集合有漏」，**永久硬失败**：该源此后每次恢复/重建
// 都在这里撞墙 ⇒ 发布永远无法完成（PublicationPending 钉死）⇒ 对账判缺失 ⇒ 反复整窗重发。
// 生产现场该错误 952 次、涉及 45 个源。
//
// 本文件的用例全部围绕三条红线：
//  1. 不把半写/损坏判为完整可见；
//  2. 不静默丢数据（缺口、未发布、失败原因如实可见）；
//  3. 原本因该错误永久失败的源必须能恢复发布。
// 把 appendEvents 改回「物理行数比较 + 按下标切片」即红（M 系列变异见各用例注释）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// newCoverageTestManager 建一个只关心段存储的最小 Manager（不接 VL：本文件测的是段落地，
// 不测发布可见性——后者由既有 deliver_tail/rebuild 用例覆盖）。
func newCoverageTestManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	m, err := New(Options{Root: root, Catalog: cat, Journal: journal})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })
	return m
}

// storeRowCount 返回该源段的**物理行数**（含冗余行）。
func storeRowCount(t *testing.T, m *Manager, key string) int {
	t.Helper()
	n, err := m.events.Count(key)
	require.NoError(t, err)
	return n
}

// TestAppendEventsToleratesBenignDuplicateRows 覆盖红线 3：段里存在「同 id 同哈希」的冗余行
// （生产实测形态）时，appendEvents 必须成功，绝不再报 "eventstore ahead of authoritative set"。
//
// 变异即红：把 appendEvents 的判据改回 `stored > len(events) ⇒ error`，本用例立刻失败
// （stored=4 > len(events)=3）。
func TestAppendEventsToleratesBenignDuplicateRows(t *testing.T) {
	m := newCoverageTestManager(t)
	key := "inst:148/file/g1"
	events := ingestTestEvents("inst:148/file", "g1", 0, 3, "body")

	require.NoError(t, m.appendEvents(key, events))

	// 模拟生产现场的良性冗余：把同 id 同哈希的行再追加一次（绕过 appendEvents，直接写段，
	// 正是并发投递/历史重放留下的形态）。
	require.NoError(t, m.events.Append(key, []logtypes.Event{events[0]}))
	require.Equal(t, 4, storeRowCount(t, m, key), "夹具应制造出「物理 4 行 / 唯一 3 个身份」")

	// 关键断言：这一次调用在旧实现下会硬失败。
	require.NoError(t, m.appendEvents(key, events),
		"段内有同 id 同哈希的冗余行时不得硬失败（这是生产 45 源 952 次失败的真根因）")

	// 幂等：再跑一遍也不得重复追加（只应保持 4 行）。
	require.NoError(t, m.appendEvents(key, events))
	require.Equal(t, 4, storeRowCount(t, m, key), "按身份收敛必须幂等，不得重复写入")
}

// TestAppendEventsWritesMissingIdentityDespiteAheadRows 覆盖「段物理行数 > 权威条数」时，
// 仍然必须把**真的缺失身份**写进去（不因行数超了就跳过）。
//
// 这是旧实现按下标切片的错位形态：`events[stored:]` 在 stored=4 时切出空集 ⇒ 静默丢数据。
func TestAppendEventsWritesMissingIdentityDespiteAheadRows(t *testing.T) {
	m := newCoverageTestManager(t)
	key := "inst:151/file/g1"
	base := ingestTestEvents("inst:151/file", "g1", 0, 2, "body")
	require.NoError(t, m.appendEvents(key, base))
	// 制造 3 行冗余：物理 5 行 / 唯一 2 个身份。
	require.NoError(t, m.events.Append(key, []logtypes.Event{base[0]}))
	require.NoError(t, m.events.Append(key, []logtypes.Event{base[1]}))
	require.NoError(t, m.events.Append(key, []logtypes.Event{base[0], base[0]}))
	require.Equal(t, 6, storeRowCount(t, m, key))

	// 权威集合扩到 4 条：新增的 2 个身份必须落段（旧实现此处切出空集 ⇒ 静默丢 2 条）。
	extended := ingestTestEvents("inst:151/file", "g1", 0, 4, "body")
	require.NoError(t, m.appendEvents(key, extended))

	got := durableEvents(t, m, key)
	ids := map[string]bool{}
	for _, ev := range got {
		ids[ev.EventID] = true
	}
	for _, ev := range extended {
		require.True(t, ids[ev.EventID], "权威事件 %s 必须已落段（不得静默丢失）", ev.EventID)
	}
	// 只追加缺的 2 条：6 + 2 = 8（不做任何重复追加）。
	require.Equal(t, 8, storeRowCount(t, m, key), "只应追加缺失的 2 个身份")
}

// TestAppendEventsConflictIsRecordedNotFatal 覆盖红线 1 与「冲突不得静默吞掉」。
//
// 同 id 异哈希且**不可裁定**（异区间/同区间等长非同一物理行）时：不硬失败（那会让该源投递
// 永续失败、水位钉死——项目内 planDeliveryBatchOutsideLock 与 canonicalRecoveryEvents 在
// 2026-10-08 都已按同一结论修过），但必须**留痕**：计数器 + IdentityConflicts 隔离记录。
func TestAppendEventsConflictIsRecordedNotFatal(t *testing.T) {
	m := newCoverageTestManager(t)
	key := "inst:196/file/g1"
	identity := logtypes.SourceIdentity{LogSourceID: "inst:196/file", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 100, End: 199}
	storedVariant := logtypes.BuildEvent(identity, rec, "2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", "AAAA")
	// 同区间、等长（不可裁定形态 1/2 都不满足）、内容不同。
	otherVariant := logtypes.BuildEvent(identity, rec, "2026-10-05T00:00:00Z", "2026-10-05T00:00:02Z", "WARN", "stderr", "BBBB")
	require.Equal(t, storedVariant.EventID, otherVariant.EventID, "夹具必须共用同一 event_id")

	require.NoError(t, m.appendEvents(key, []logtypes.Event{storedVariant}))
	require.NoError(t, m.appendEvents(key, []logtypes.Event{otherVariant}),
		"不可裁定的身份冲突不得让整批硬失败（否则该源投递永续失败）")

	require.Equal(t, int64(1), m.eventStoreIdentityConflicts.Load(),
		"不可裁定冲突必须计入观测计数器（不得静默吞掉）")

	saved := savedSource(t, m, key)
	require.Len(t, saved.IdentityConflicts, 1, "不可裁定冲突必须落 IdentityConflicts 隔离记录")
	got := saved.IdentityConflicts[0]
	require.Equal(t, storedVariant.EventID, got.EventID)
	require.Equal(t, storedVariant.CanonicalHash, got.KeptHash, "保留段内已有内容（与查询侧同一收敛方向）")
	require.Equal(t, otherVariant.CanonicalHash, got.DroppedHash)

	// 冲突行不得入段：段里仍是 1 行。
	require.Equal(t, 1, storeRowCount(t, m, key), "被丢弃的冲突变体不得写进段")
}

// TestAppendEventsAdjudicatedLongerVariantIsAppended 覆盖可裁定形态：同区间一方严格更长
// （生产形态：轮转尾部残行 vs 重读完整行）⇒ 较长者胜，且必须真的落段。
//
// 这里同时钉住**幂等性**：段的追加语义决定它不能改写旧行，故较长变体会与残行并存；
// 若「段内已有该身份」就一律跳过，较长者永远入不了段（历史残行永远无法被修正）。
// 反之若每轮都无条件追加，段会无界增长——故必须用「与 canonicalRecoveryEvents 同一折叠规则」
// 判定段内当前胜者。变异点：把段内记账改成「只认第一条」，本用例第二轮会重复追加即红。
func TestAppendEventsAdjudicatedLongerVariantIsAppended(t *testing.T) {
	m := newCoverageTestManager(t)
	key := "inst:179/file/g1"
	identity := logtypes.SourceIdentity{LogSourceID: "inst:179/file", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 100, End: 199}
	partial := logtypes.BuildEvent(identity, rec, "2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", strings.Repeat("x", 947))
	full := logtypes.BuildEvent(identity, rec, "2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", strings.Repeat("y", 1034))
	require.Equal(t, partial.EventID, full.EventID)

	require.NoError(t, m.appendEvents(key, []logtypes.Event{partial}))
	require.NoError(t, m.appendEvents(key, []logtypes.Event{full}),
		"同区间较长变体（完整行）必须能入段以修正历史残行")

	rows := storeRowCount(t, m, key)
	require.Equal(t, 2, rows, "完整行与残行并存（段不可改写），故 2 行")
	require.Zero(t, m.eventStoreIdentityConflicts.Load(),
		"可裁定形态不属于「不可裁定冲突」，不应计入计数器")

	// 幂等：再跑两轮，段内胜者已是完整行 ⇒ 不得再追加。
	require.NoError(t, m.appendEvents(key, []logtypes.Event{full}))
	require.NoError(t, m.appendEvents(key, []logtypes.Event{full}))
	require.Equal(t, rows, storeRowCount(t, m, key), "胜者已入段后必须幂等，不得无界增长")
}

// TestAppendEventsMemoDoesNotMaskDifferentSet 覆盖红线 2：覆盖备忘**绝不能**把一份不同的
// 权威集合误判成「已覆盖」。
//
// 为什么单独立用例：备忘若以「长度」为键，就会在这里误命中——`commitBackfill`（补账路径）与
// `migrateInlineEvents`（旧格式迁移）传给 appendEvents 的是**不同集合**，它们完全可能与主路径的
// 权威集合条数相同而内容不同。误命中 = 该追加的事件被跳过 = 静默丢数据。
//
// 关键在于**先把备忘真正建立起来**（连续两轮「无待追加」才会建立），否则测不到备忘路径。
// 变异即红：把 eventSetDigest 换成「只返回 len(events)」，本用例第二轮会漏写 3 条。
func TestAppendEventsMemoDoesNotMaskDifferentSet(t *testing.T) {
	m := newCoverageTestManager(t)
	key := "inst:184/file/g1"

	first := ingestTestEvents("inst:184/file", "g1", 0, 3, "body")
	require.NoError(t, m.appendEvents(key, first)) // 写段
	require.NoError(t, m.appendEvents(key, first)) // 确认覆盖 ⇒ 建立备忘
	require.True(t, m.hasStoredCoverageMemo(key), "夹具前提：备忘已建立（否则测不到误命中）")
	require.Equal(t, 3, storeRowCount(t, m, key))

	// 同样 3 条、但身份完全不同（模拟补账/迁移传入的异集合）：必须被真实追加。
	second := ingestTestEvents("inst:184/file", "g1", 100, 3, "body")
	require.NotEqual(t, first[0].EventID, second[0].EventID)
	require.NoError(t, m.appendEvents(key, second))

	require.Equal(t, 6, storeRowCount(t, m, key),
		"内容不同的同长度集合必须被真实追加（按长度做键会在这里静默丢 3 条）")

	got := durableEvents(t, m, key)
	ids := map[string]bool{}
	for _, ev := range got {
		ids[ev.EventID] = true
	}
	for _, ev := range second {
		require.True(t, ids[ev.EventID], "异集合的每条事件都必须已落段")
	}
}

// TestAppendEventsMemoIsInvalidatedByAppend 覆盖红线 2 的另一面：备忘必须被任何一次段写入失效。
//
// 若追加后不清除/不使备忘失效，下一轮就会用「旧集合作结论」⇒ 新事件被判成「已覆盖」⇒ 静默漏写。
func TestAppendEventsMemoIsInvalidatedByAppend(t *testing.T) {
	m := newCoverageTestManager(t)
	key := "inst:188/file/g1"

	base := ingestTestEvents("inst:188/file", "g1", 0, 3, "body")
	// 第一轮把事件写进段（此时有待追加，故不建备忘）；
	// 第二轮发现「无待追加」才建立备忘——这正是备忘的语义：它记录的是「已完整覆盖」。
	require.NoError(t, m.appendEvents(key, base))
	require.False(t, m.hasStoredCoverageMemo(key), "有待追加的那一轮不得建立备忘")
	require.NoError(t, m.appendEvents(key, base))
	require.True(t, m.hasStoredCoverageMemo(key), "确认已完整覆盖后应建立备忘")

	// 任何一次段写入都必须让它失效。
	require.NoError(t, m.events.Append(key, []logtypes.Event{base[0]}))
	m.bumpEventStoreEpoch(key)
	require.False(t, m.hasStoredCoverageMemo(key), "段追加后备忘必须失效")

	// 失效后仍能正确追加新增身份。
	extended := ingestTestEvents("inst:188/file", "g1", 0, 4, "body")
	require.NoError(t, m.appendEvents(key, extended))
	got := durableEvents(t, m, key)
	ids := map[string]bool{}
	for _, ev := range got {
		ids[ev.EventID] = true
	}
	require.True(t, ids[extended[3].EventID], "新身份必须落段")
}

// TestAppendEventsMemoHitAvoidsRescan 钉住备忘**确实生效**（否则上面的正确性用例在
// 「把备忘整个删掉」的变异下仍会通过，无法防回退到「每次全量遍历」的性能退化）。
func TestAppendEventsMemoHitAvoidsRescan(t *testing.T) {
	m := newCoverageTestManager(t)
	key := "inst:189/file/g1"
	events := ingestTestEvents("inst:189/file", "g1", 0, 4, "body")
	require.NoError(t, m.appendEvents(key, events)) // 首轮真正写段
	require.Equal(t, int64(0), m.storedCoverageHits.Load(), "首轮是写段，不是命中")
	require.NoError(t, m.appendEvents(key, events)) // 次轮确认覆盖并建立备忘
	require.Equal(t, int64(0), m.storedCoverageHits.Load(), "建立备忘的那一轮也还不是命中")

	// 同一份集合再问一次：应命中备忘（零遍历）。
	require.NoError(t, m.appendEvents(key, events))
	require.GreaterOrEqual(t, m.storedCoverageHits.Load(), int64(1),
		"同一份集合重复调用应命中覆盖备忘（防回退到每次全量遍历）")
	require.Equal(t, 4, storeRowCount(t, m, key), "命中备忘时不得重复追加")
}

// TestAppendEventsLegacyStoredThroughStillAdvances 守住既有契约：落段后仍要推进
// EventsStoredThrough 并清掉内联副本（B1a 的按条引用判据的 RSS 目标）。
func TestAppendEventsLegacyStoredThroughStillAdvances(t *testing.T) {
	m := newCoverageTestManager(t)
	key := "inst:190/file/g1"
	events := ingestTestEvents("inst:190/file", "g1", 0, 3, "body")
	require.NoError(t, m.appendEvents(key, events))

	saved := savedSource(t, m, key)
	require.True(t, saved.EventsStored)
	require.Equal(t, uint64(299), saved.EventsStoredThrough)
	require.Empty(t, saved.Events, "已落段后不得保留内联副本")
}

// TestAppendEventsRejectsCorruptSegment 覆盖红线 1 的极端面：段**结构损坏**（非尾部截断）
// 必须硬失败，绝不能被「按身份收敛」悄悄放过。
//
// 与「同 id 异哈希」的区别：后者是内容层面的冲突（有确定性裁定规则，且硬失败会造成永久死锁），
// 前者是段文件本身读不出来（eventstore 的契约：中间段坏行 = 硬失败，宁可不起采集也不用残缺集合）。
func TestAppendEventsRejectsCorruptSegment(t *testing.T) {
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	build := func() *Manager {
		m, err := New(Options{Root: root, Catalog: cat, Journal: journal})
		require.NoError(t, err)
		t.Cleanup(func() { _ = m.Stop() })
		return m
	}
	m := build()
	key := "inst:191/file/g1"
	// 让每段只装很少的事件、并分两批写入，制造出**多个段**：只有末段允许尾部截断，
	// 前面的段必须严格解析。
	m.events.SetMaxSegmentBytes(1)
	events := ingestTestEvents("inst:191/file", "g1", 0, 6, "body")
	require.NoError(t, m.appendEvents(key, events[:3]))
	require.NoError(t, m.appendEvents(key, events))

	segs := m.events.Segments(key)
	require.GreaterOrEqual(t, len(segs), 2, "夹具需要至少两个段（只有末段才容忍截断）")
	require.True(t, segs[0].Sealed, "首个段应已封闭")

	dir := filepath.Join(root, "var", "log", "events")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	// 往**已封闭的非末段**追加一条坏行：Iterate 必须报 ErrCorrupt 而不是静默停止。
	target := filepath.Join(dir, entries[0].Name(), segs[0].Name)
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("{not valid json at all\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// 重新打开一个 Manager（清掉段缓存与校准状态），确保坏行真的从磁盘读出来。
	m2 := build()
	require.Error(t, m2.appendEvents(key, events), "段结构损坏必须硬失败，不得被身份收敛悄悄放过")
}
