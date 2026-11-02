package ingest

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件守护两处「纯可观测性/纯性能」修复，二者都不改变任何裁定结果：
//
//	① 正常身份裁定（同 ID 异哈希、恰好可裁定）的留痕节流：日志量必须与事件数**脱钩**，
//	   但"发生了多少次、涉及哪些源"仍须可查。修复前生产实测单次启动 6.6 万条 WARN、
//	   累计 13.2 万条，而真正不可裁定的冲突是 0 条 —— 噪声把信号淹没了。
//	② 一轮投递内复用权威事件集合：pending 未获确认时，取证与写入面构建问的是同一份输入，
//	   凭据成立即复用；凭据不成立一律重建（方向恒为"多算一次"）。

// fakeAdjudicationClock 是可推进的假时钟（节流器的时间窗判定要可确定性驱动）。
type fakeAdjudicationClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeAdjudicationClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeAdjudicationClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// TestIdentityAdjudicationThrottleBoundsLogVolume —— 节流器的判定本身（不依赖日志捕获）。
//
// 生产形态：单源 6.6 万次正常裁定。要求：样例有界、汇总数 = O(次数/阈值)、且**总数守恒**
// （汇总里读得到的次数加起来等于实际发生的次数 ⇒ 可观测性没丢）。
func TestIdentityAdjudicationThrottleBoundsLogVolume(t *testing.T) {
	clock := &fakeAdjudicationClock{at: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	throttle := &identityAdjudicationThrottle{now: clock.now}

	const total = 66000
	samples, summaries, reported := 0, 0, uint64(0)
	for i := 0; i < total; i++ {
		trace := throttle.observe("inst:179/g1", "同区间异哈希取较长者（弃尾部残行变体）")
		if trace.Sample {
			samples++
			require.NotEmpty(t, trace.Kind, "样例必须带裁定形态，否则运维无法判断是哪一种收敛")
		}
		if trace.Summary {
			summaries++
			reported += trace.Total
			require.NotZero(t, trace.Total)
			require.NotZero(t, trace.PerSource["inst:179/g1"], "汇总必须带按源分布（运维要能看出涉及哪些源）")
		}
	}

	require.Equal(t, 1, samples, "同一源只留一条样例（6.6 万次也只 1 条）")
	require.Equal(t, total/adjudicationFlushEvery, summaries, "汇总数只由次数阈值决定，与事件内容无关")
	require.Equal(t, uint64(total-total%adjudicationFlushEvery), reported,
		"汇总里的次数必须守恒（观测面只降噪、不丢计数）")
	require.LessOrEqual(t, samples+summaries, 64,
		"总日志行数必须与事件数脱钩（修复前这里是 66000 行）")
}

// TestIdentityAdjudicationThrottleFlushesOnWindow —— 次数不足时也必须按时间窗出汇总。
//
// 为什么单独测：安静源的长尾（每窗口只有个位数裁定）若只靠次数阈值，就永远看不到汇总，
// 这会让"发生过但很少"的情况在日志里彻底消失（等于用另一种方式丢可观测性）。
func TestIdentityAdjudicationThrottleFlushesOnWindow(t *testing.T) {
	clock := &fakeAdjudicationClock{at: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	throttle := &identityAdjudicationThrottle{now: clock.now}

	first := throttle.observe("inst:quiet/g1", "k")
	require.True(t, first.Sample, "首次裁定留一条样例")

	require.False(t, throttle.observe("inst:quiet/g1", "k").Summary, "窗内不该提前汇总")

	clock.advance(adjudicationFlushWindow + time.Second)
	sum := throttle.observe("inst:quiet/g1", "k")
	require.True(t, sum.Summary, "越过时间窗必须汇总（否则长尾永远不可见）")
	require.EqualValues(t, 3, sum.Total, "窗内发生的 3 次裁定都必须计入")
	require.Equal(t, adjudicationFlushWindow+time.Second, sum.Window)

	// 汇总后重置：下一条汇总只描述新窗口，不重复累加历史。
	clock.advance(adjudicationFlushWindow + time.Second)
	next := throttle.observe("inst:quiet/g1", "k")
	require.True(t, next.Summary)
	require.EqualValues(t, 1, next.Total)
}

// TestIdentityAdjudicationThrottleBoundsTrackedSources —— 源极多时聚合状态本身不得无界增长。
func TestIdentityAdjudicationThrottleBoundsTrackedSources(t *testing.T) {
	clock := &fakeAdjudicationClock{at: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	throttle := &identityAdjudicationThrottle{now: clock.now}

	for i := 0; i < maxAdjudicationSources+20; i++ {
		throttle.observe("inst:"+strings.Repeat("x", i+1)+"/g1", "k")
	}
	require.LessOrEqual(t, len(throttle.perSource), maxAdjudicationSources,
		"按源跟踪有上限，超出折叠进 %q", adjudicationOverflowKey)
	require.LessOrEqual(t, len(throttle.sampled), maxAdjudicationSamples,
		"样例源数有上限（源多时样例本身也会变成噪声）")
	require.Contains(t, throttle.perSource, adjudicationOverflowKey)
}

// TestIdentityAdjudicationLogsAreThrottledEndToEnd —— 走**真实调用点**验证日志行数。
//
// 为什么必须走真实函数而不是只测节流器：断言的是"planDeliveryBatchOutsideLock 不再逐条 WARN"
// 这一接线事实。变异验证据此成立：把调用点改回 slog.Warn ⇒ 本用例立刻转红（4000 行 vs 30 行）。
func TestIdentityAdjudicationLogsAreThrottledEndToEnd(t *testing.T) {
	var buf bytes.Buffer
	restore := captureSlogOutput(&buf)
	defer restore()

	m := &Manager{}
	identity := logtypes.SourceIdentity{LogSourceID: "inst:noise", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 100, End: 200}

	// 生产形态：同一身份「残行先入账、完整行后到」成对出现，每对触发一次正常裁定。
	const pairs = 4000
	batch := make([]logtypes.Event, 0, pairs*2)
	for i := 0; i < pairs; i++ {
		// event_id 按「对」唯一：同一对内同 id（才会裁定），不同对之间互不相关。
		id := "evt-" + strconv.Itoa(i)
		batch = append(batch,
			logtypes.Event{EventID: id, Source: identity, Record: rec,
				IngestTimeUTC: "2026-10-20T00:00:00.000000000Z", Level: "INFO", Stream: "stdout",
				Message: strings.Repeat("x", 8)},
			logtypes.Event{EventID: id, Source: identity, Record: rec,
				IngestTimeUTC: "2026-10-20T00:00:00.000000000Z", Level: "INFO", Stream: "stdout",
				Message: strings.Repeat("y", 12)},
		)
	}
	toWrite, fail := m.planDeliveryBatchOutsideLock(batch, map[string]eventIdentity{})
	require.Nil(t, fail)
	require.Len(t, toWrite, pairs, "每条身份只投一条（裁定结果不得因留痕改动而变）")

	out := buf.String()
	lines := strings.Count(out, "身份重复裁定")
	require.NotZero(t, lines, "正常裁定仍须留痕（不能静默）")
	require.LessOrEqual(t, lines, 30,
		"日志行数必须与裁定次数脱钩：%d 次裁定只允许常数级留痕（修复前是 %d 行）", pairs, pairs)

	// 汇总必须报出真实次数：4000 次裁定恰好 2 个满窗，合计 4000。
	require.EqualValues(t, pairs, sumReportedTotals(t, out), "汇总次数必须守恒（只降噪、不丢计数）")
	require.Contains(t, out, "身份重复裁定汇总", "必须能看出「发生了多少次」")
	require.Contains(t, out, "inst:noise/g1", "必须能看出「涉及哪些源」")
}

var totalFieldPattern = regexp.MustCompile(`total=(\d+)`)

// sumReportedTotals 把捕获到的 `total=` 字段全部相加：用来断言"汇总次数守恒"
// （节流只能降噪，不能把发生过的次数算丢）。
func sumReportedTotals(t *testing.T, out string) uint64 {
	t.Helper()
	var sum uint64
	for _, match := range totalFieldPattern.FindAllStringSubmatch(out, -1) {
		value, err := strconv.ParseUint(match[1], 10, 64)
		require.NoError(t, err)
		sum += value
	}
	return sum
}

// TestCanonicalRecoveryReusesWithinRoundWhenInputsUnchanged —— 缺陷②的正向与失效两半。
//
// 判据是「段行数 + 段物理版本号 + WAL/内联长度 + key」。本用例对每一条都做一次失效验证：
// 任何一条输入读数变化都必须导致重建（宁可多算，不可返回旧数据）。
func TestCanonicalRecoveryReusesWithinRoundWhenInputsUnchanged(t *testing.T) {
	store, err := eventstore.Open(t.TempDir())
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	m := &Manager{events: store}

	key := "inst:reuse/g1"
	identity := logtypes.SourceIdentity{LogSourceID: "inst:reuse", SourceGeneration: "g1", ParserVersion: "v1"}
	mk := func(start, end uint64, message string) logtypes.Event {
		return logtypes.BuildEvent(identity, logtypes.RecordRange{Start: start, End: end},
			"2026-10-20T00:00:00Z", "2026-10-20T00:00:01Z", "INFO", "stdout", message)
	}
	require.NoError(t, store.Append(key, []logtypes.Event{mk(10, 20, "a"), mk(20, 30, "b")}))
	saved := persistedSource{EventsStored: true}

	want, err := m.canonicalRecoveryEvents(key, saved)
	require.NoError(t, err)
	require.Len(t, want, 2)

	receipt := &recoveryReceipt{}
	m.rememberCanonicalRecovery(receipt, key, saved, want)

	got, err := m.canonicalRecoveryEventsReusing(key, saved, receipt)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.True(t, &got[0] == &want[0], "输入未变 ⇒ 必须复用同一份结果（不重复遍历段）")
	require.EqualValues(t, 1, m.CanonicalRecoveryReuses())

	// ① 段追加（行数变化）⇒ 必须重建，且新事件可见（返回旧数据即"数据陈旧"缺陷）。
	require.NoError(t, store.Append(key, []logtypes.Event{mk(30, 40, "c")}))
	afterAppend, err := m.canonicalRecoveryEventsReusing(key, saved, receipt)
	require.NoError(t, err)
	require.Len(t, afterAppend, 3, "段追加后不得复用旧集合")
	require.False(t, &afterAppend[0] == &want[0], "必须是一次新的构建")
	require.EqualValues(t, 1, m.CanonicalRecoveryReuses())

	// ② 段物理版本号变化（行数不变）⇒ 同样必须重建（版本号是同一内容的另一维凭据）。
	receipt2 := &recoveryReceipt{}
	base, err := m.canonicalRecoveryEvents(key, saved)
	require.NoError(t, err)
	m.rememberCanonicalRecovery(receipt2, key, saved, base)
	m.bumpEventStoreEpoch(key)
	afterEpoch, err := m.canonicalRecoveryEventsReusing(key, saved, receipt2)
	require.NoError(t, err)
	require.True(t, &afterEpoch[0] != &base[0], "段版本号变化后不得复用")

	// ③ WAL 长度变化 ⇒ 必须重建（同一轮内 saved 变了就不再是同一份输入）。
	receipt3 := &recoveryReceipt{}
	base3, err := m.canonicalRecoveryEvents(key, saved)
	require.NoError(t, err)
	m.rememberCanonicalRecovery(receipt3, key, saved, base3)
	withWAL := saved
	withWAL.WAL = []acquire.WALEntry{{Seq: 1, Event: mk(40, 50, "wal-only"), Appended: true, Durable: true}}
	afterWAL, err := m.canonicalRecoveryEventsReusing(key, withWAL, receipt3)
	require.NoError(t, err)
	require.Len(t, afterWAL, 4, "durable WAL 条目必须入账（复用凭据不得掩盖它）")

	// ④ 不同 key ⇒ 必须重建。
	receipt4 := &recoveryReceipt{}
	other, err := m.canonicalRecoveryEvents("inst:other/g1", persistedSource{EventsStored: true})
	require.NoError(t, err)
	m.rememberCanonicalRecovery(receipt4, "inst:other/g1", persistedSource{EventsStored: true}, other)
	require.NotPanics(t, func() {
		_, reuseErr := m.canonicalRecoveryEventsReusing(key, saved, receipt4)
		require.NoError(t, reuseErr)
	})
	require.EqualValues(t, 1, m.CanonicalRecoveryReuses(), "只有 ① 之前那一次算命中")
}

// TestDeliverReusesCanonicalSetAcrossPendingConfirm —— 缺陷②的接线验证（走真实 deliver 路径）。
//
// 形态：pending=true ∧ VL 缺行 ⇒ 本轮先取证（一次全量）再为写入面构建（又一次全量）。
// 修复后两次问的是同一份输入 ⇒ 只付一次；而写入面仍必须是权威全量（判据不放宽）。
func TestDeliverReusesCanonicalSetAcrossPendingConfirm(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守的是「pending 轮内取证与写入面构建复用同一份权威全量」。
	//
	// 那两者的存在前提是「发布确认 + 整窗重发」——pending 为真时先 `tryConfirmPendingPublication`
	// 取一份权威全量做可见性验证，再为写入面重建一次同一份集合，于是有「复不复用」可测。
	// 简化后没有发布确认、没有整窗重发：`deliverCtx` 只做「去重 → 写段 → 写 VL」，
	// `canonicalRecoveryEvents` 不再在投递路径上被调用，`CanonicalRecoveryReuses` 恒为 0。
	//
	// 代价（已向用户报告）：投递不再重发历史，也就不再需要「复用一次全量」这个优化。
	t.Skip("投递路径简化（2026-11-02）：发布确认与整窗重发已删，`canonicalRecoveryEvents` 不再在投递路径上运行；" +
		"本用例测的「pending 轮内复用」对象不存在")
	m, key, source, _, payloads, deleteDay := newPendingConfirmFixture(t)

	m.mu.Lock()
	cur := m.state.Sources[key]
	m.mu.Unlock()
	require.NotEmpty(t, cur.ProjectionGeneration)
	stored, err := m.canonicalRecoveryEvents(key, cur)
	require.NoError(t, err)
	full := len(stored)
	require.NotZero(t, full)
	m.mu.Lock()
	cur = m.state.Sources[key]
	cur.PublicationPending = true
	m.state.Sources[key] = cur
	m.mu.Unlock()

	deleteDay() // 复核必然不完整 ⇒ 回落整窗重发（这一轮里会发生两次构建）

	identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
	// 补历史窗口而非追加新尾，确保真正进入发布确认与权威集合复用路径。
	batch := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 0, End: 1},
			"2026-09-22T03:00:00Z", "2026-09-22T03:00:01Z", "INFO", "stdout", "reuse batch"),
	}
	require.False(t, m.planDeliveryTail(key, cur, batch, false).Fast)

	_, err = m.deliver(source, batch, false)
	require.NoError(t, err)

	require.Equal(t, int64(1), m.CanonicalRecoveryReuses(),
		"pending 轮内取证结果必须被写入面构建复用（否则每轮白付一次 O(段行数) 重建）")
	require.Equal(t, full+len(batch), payloadEventCount(payloads()),
		"复用不得缩小写入面：整窗包含既有权威全量与本批新增历史事件")
}
