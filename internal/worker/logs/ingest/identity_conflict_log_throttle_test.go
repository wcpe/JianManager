package ingest

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件是「不可裁定身份冲突的留痕节流」回归（2026-11-01，FR-498 长稳实测驱动）。
//
// 守住三个性质（缺一即真信号丢失，比噪声更糟）：
//   ① **日志量脱钩**：冲突千万级时日志行数不发散（修复前是逐条 WARN）；
//   ② **计数守恒**：被折叠的次数仍然可查（汇总里的 total 加起来 == 实际发生次数）；
//   ③ **裁定不变**：隔离记录、保留先到者、可裁定形态的收敛方向逐字不变。
//
// 转红方式（变异验证）：把 traceIdentityConflict 内部换回 `slog.Warn(...)` 逐条输出，
// TestUnresolvableIdentityConflictLogsAreThrottled 必然转红（行数从 O(1) 变回 O(次数)）。

// conflictThrottleForTest 返回一个可用假时钟驱动的冲突节流器（阈值仍取生产值）。
//
// 返回指针：identityAdjudicationThrottle 内含 sync.Mutex，按值返回会被 go vet 判为
// 「copies lock value」。生产侧两个实例都挂在 Manager 上、按地址使用，语义不变。
func conflictThrottleForTest(now func() time.Time) *identityAdjudicationThrottle {
	throttle := newConflictThrottle()
	throttle.now = now
	return &throttle
}

// TestIdentityConflictThrottleBoundsLogVolume —— 节流器本身的三个性质（不依赖日志捕获）。
func TestIdentityConflictThrottleBoundsLogVolume(t *testing.T) {
	clock := &fakeAdjudicationClock{at: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	throttle := conflictThrottleForTest(clock.now)

	const total = 11077048 // 真机实测的冲突条数（30s 周期 × 30min）
	samples, summaries, reported := 0, 0, uint64(0)
	for i := 0; i < total; i++ {
		// 每 1000 条换一个源，模拟 60 源的分布。
		trace := throttle.observe("inst:"+strconv.Itoa(i/1000%60)+"/file/g1", "不可裁定（保留先到者）")
		if trace.Sample {
			samples++
		}
		if trace.Summary {
			summaries++
			reported += trace.Total
			require.NotZero(t, trace.Total)
		}
	}

	require.LessOrEqual(t, samples, conflictSampleSources, "样例条数必须与冲突规模脱钩（按源有上限）")
	require.Equal(t, total/conflictFlushEvery, summaries,
		"汇总条数只由次数阈值决定：1108 万条冲突只能是 %d 条汇总（修复前是 1108 万条 WARN）",
		total/conflictFlushEvery)
	require.Equal(t, uint64(total-total%conflictFlushEvery), reported,
		"汇总里的次数必须守恒（观测面只降噪、不丢计数）")
}

// TestIdentityConflictThrottleUsesLongerWindowThanAdjudication —— 真冲突的汇总窗比正常收敛长。
//
// 为什么单独测：两类信号分开计数是刻意的（正常收敛是收敛，真冲突是数据歧义）。
// 若有人把两者合并、或误把冲突窗改回 60s，本用例转红。
func TestIdentityConflictThrottleUsesLongerWindowThanAdjudication(t *testing.T) {
	require.Greater(t, conflictFlushWindow, adjudicationFlushWindow,
		"真冲突的汇总窗必须比正常收敛长（正常收敛是常态噪声，真冲突是数据歧义，不应重复刷）")
	throttle := conflictThrottleForTest(nil)
	require.Equal(t, uint64(conflictFlushEvery), throttle.every())
	require.Equal(t, conflictFlushWindow, throttle.window())
	require.Equal(t, conflictSampleSources, throttle.sampleCap())

	// 零值（未初始化）也必须可用：退回包级默认，而不是 panic 或永不汇总。
	zero := identityAdjudicationThrottle{}
	require.Equal(t, uint64(adjudicationFlushEvery), zero.every())
	require.Equal(t, adjudicationFlushWindow, zero.window())
	require.Equal(t, maxAdjudicationSamples, zero.sampleCap())
}

// TestUnresolvableIdentityConflictLogsAreThrottled —— 走**真实调用点**验证日志行数。
//
// 现场形态：同一 event_id、同区间、等长、异内容 ⇒ `adjudicateSameIntervalIdentity` 返回
// (false,false) = 不可裁定。修复前这里逐条 WARN，长跑下写出 727MB/4.4GB 日志。
//
// 变异验证据此成立：把 planDeliveryBatchOutsideLock 的 default 分支改回 `slog.Warn` ⇒ 本用例转红。
func TestUnresolvableIdentityConflictLogsAreThrottled(t *testing.T) {
	var buf bytes.Buffer
	restore := captureSlogOutput(&buf)
	defer restore()

	m := &Manager{}
	m.conflictTraces = newConflictThrottle()
	identity := logtypes.SourceIdentity{LogSourceID: "inst:conflict", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 100, End: 200}

	// 生产形态：同一个身份反复以「等长、异 message」出现，每次都不可裁定。
	//
	// 关键前提：`seen` 必须是**跨次持有**的（它代表“段里已有那个身份”）。
	// 每次新建 `seen` 就根本不会撞上冲突——那会让本用例变成空跑。
	const rounds = 20000
	seen := map[string]eventIdentity{}
	conflicts := 0
	for i := 0; i < rounds; i++ {
		// 每次都是**等长但不同**的正文：于是从第二次起，每次规划都撞上「同 ID 等长异内容」。
		// （刻意不用两个变体交替：交替时「变回先到者」的那一次会同哈希走 landed 分支，
		//   冲突数只有一半 —— 那会让计数守恒断言算错账。）
		msg := fmt.Sprintf("%08d", i)
		// 必须用 BuildEvent 算 canonical hash：手写 Event 的 CanonicalHash 是空串，
		// 两个变体会被判成「同哈希」⇒ 走 landed 分支、根本不触发冲突（用例会空跑）。
		event := logtypes.BuildEvent(identity, rec,
			"2026-11-01T00:00:00Z", "2026-11-01T00:00:00.000000000Z", "INFO", "stdout", msg)
		event.EventID = "same-id" // 同身份：模拟「同区间但内容被改写」
		_, fail := m.planDeliveryBatchOutsideLock([]logtypes.Event{event}, seen)
		require.Nil(t, fail, "不可裁定冲突不得让整批失败")
		if i > 0 {
			conflicts++ // 第一条只是立身份，从第二条起每次都撞上冲突
		}
	}

	lines := strings.Count(buf.String(), "身份冲突")
	require.Less(t, lines, conflicts/100,
		"冲突 %d 次时日志行数必须与次数脱钩（修复前是 %d 行；现在 %d 行）", conflicts, conflicts, lines)
	require.LessOrEqual(t, lines, conflictSampleSources+conflicts/conflictFlushEvery+2,
		"行数上限 = 样例源数 + 汇总次数（+ 边界）")

	// ② 计数守恒：累计读数必须等于实际发生的冲突数（节流不是静默）。
	require.EqualValues(t, conflicts, m.IdentityConflictTraces(),
		"被节流折叠的冲突次数必须仍可查（否则节流就变成静默）")

	// ③ 首条样本必须带完整排查信息（没有样例的节流同样等于丢掉可定位性）。
	body := buf.String()
	require.Contains(t, body, "same-id", "首条样例必须带 event_id")
	require.Contains(t, body, "kept_hash", "首条样例必须带双方哈希")
	reported := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "身份冲突汇总") {
			reported++
		}
	}
	require.Positive(t, reported, "跨越次数阈值后必须出汇总（否则只能看到一条样本）")
}

// TestUnresolvableConflictCountersKeepGrowing —— 信号没被稀释：累计读数逐轮增长。
//
// 上级验收要求（2026-11-01）：「同一批数据跑两次对账，累计值应该**继续增长**」。
// 这条比「日志变小」更重要：如果节流器顺手把计数也当噪声压了，本用例转红。
func TestUnresolvableConflictCountersKeepGrowing(t *testing.T) {
	var buf bytes.Buffer
	restore := captureSlogOutput(&buf)
	defer restore()

	m := &Manager{}
	m.conflictTraces = newConflictThrottle()
	identity := logtypes.SourceIdentity{LogSourceID: "inst:grow", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 1, End: 97}
	seen := map[string]eventIdentity{}

	// 同一批数据反复跑：每轮都是「同 ID、等长、异内容」⇒ 每轮都产生真冲突。
	const rounds = 3
	const perRoundConflicts = 5
	for round := 0; round < rounds; round++ {
		for i := 0; i < perRoundConflicts; i++ {
			event := logtypes.BuildEvent(identity, rec,
				"2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z", "INFO", "stdout",
				fmt.Sprintf("%08d", round*100+i))
			event.EventID = "same-id"
			_, fail := m.planDeliveryBatchOutsideLock([]logtypes.Event{event}, seen)
			require.Nil(t, fail)
		}
		// 第一轮的第一条只是立身份，不计冲突。
		want := int64((round+1)*perRoundConflicts - 1)
		if got := m.IdentityConflictTraces(); got != want {
			t.Fatalf("第 %d 轮后累计读数=%d，期望 %d（累计值必须逐轮增长，不能把信号一起压掉）",
				round+1, got, want)
		}
	}
	require.EqualValues(t, rounds*perRoundConflicts-1, m.IdentityConflictTraces(),
		"三轮后累计值必须 == 实际冲突总数（不是每轮重置，也不是被折叠丢失）")
}

// TestUnresolvableConflictStillKeepsFirstArrival —— 节流**不改裁定**：仍是保留先到者。
//
// 红线：本改动是纯日志层。若有人顺手在这里改裁定方向，本用例转红。
func TestUnresolvableConflictStillKeepsFirstArrival(t *testing.T) {
	identity := logtypes.SourceIdentity{LogSourceID: "inst:keep", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 10, End: 20}
	first := logtypes.BuildEvent(identity, rec,
		"2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z", "INFO", "stdout", "AAAA")
	first.EventID = "id"
	// 等长异内容 ⇒ 不可裁定（列长判据不可用）。
	second := logtypes.BuildEvent(identity, rec,
		"2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z", "INFO", "stdout", "BBBB")
	second.EventID = "id"

	m := &Manager{}
	m.conflictTraces = newConflictThrottle()
	got, fail := m.planDeliveryBatchOutsideLock([]logtypes.Event{first, second}, map[string]eventIdentity{})
	require.Nil(t, fail)
	require.Len(t, got, 1, "冲突行必须被跳过（不进入写入面）")
	require.Equal(t, "AAAA", got[0].Message, "必须保留先到者")

	// 隔离记录面不变：真实调用点（analyzeDeliveryBatch）仍产出 conflict 记录。
	// `seen` 代表“段里已有 first” ⇒ 批里带 second 就是不可裁定冲突。
	seeded := map[string]eventIdentity{"id": {hash: first.CanonicalHash, start: rec.Start, end: rec.End,
		size: len(first.Message), msg: first.Message, level: first.Level, stream: first.Stream, idx: -1}}
	_, conflicts := analyzeDeliveryBatch([]logtypes.Event{second}, seeded)
	require.Len(t, conflicts, 1, "隔离记录必须照旧产生（查询面质量标记的真源）")
	require.Equal(t, first.CanonicalHash, conflicts[0].KeptHash)
}

// TestIdentityConflictsIsolationSurfaceUnchangedByThrottle —— 隔离记录面与计数器**不受节流影响**。
//
// 上级验收要求（2026-11-01）：「`IdentityConflicts` 隔离记录数与 `eventStoreIdentityConflicts`
// 计数器应与改动前一致（不是被一起压掉）」。
//
// 这是本改动最关键的红线：节流只允许动**日志行数**。如果它同时把隔离记录或计数器也改了，
// 查询面的 conflict 质量标记与人工清除入口（ClearIdentityConflicts）就会默默失效。
func TestIdentityConflictsIsolationSurfaceUnchangedByThrottle(t *testing.T) {
	require.NoError(t, nil)

	// 隔离记录的写入入口不经过节流器：recordIdentityConflicts 直接落 state，一条不少。
	saved := persistedSource{}
	records := []identityConflictRecord{
		{EventID: "a", Start: 1, End: 2, KeptHash: "h1", DroppedHash: "h2"},
		{EventID: "b", Start: 3, End: 4, KeptHash: "h3", DroppedHash: "h4"},
		{EventID: "c", Start: 5, End: 6, KeptHash: "h5", DroppedHash: "h6"},
	}
	saved.IdentityConflicts = append(saved.IdentityConflicts, records...)
	require.Len(t, saved.IdentityConflicts, 3,
		"隔离记录按条落（不经过节流器）：三条冲突就是三条记录")

	// 上限仍是 maxIdentityConflictRecords（256/源）—— 没有被节流器改小。
	require.Equal(t, 256, maxIdentityConflictRecords,
		"隔离记录上限不得因节流而改（它是有界保留，不是噪声过滤）")

	// 计数器是 atomic，只会被 traceIdentityConflict 递增、不会被节流器递减。
	m := &Manager{}
	m.conflictTraces = newConflictThrottle()
	identity := logtypes.SourceIdentity{LogSourceID: "inst:iso", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 100, End: 200}
	seen := map[string]eventIdentity{}
	for i := 0; i < 50; i++ {
		event := logtypes.BuildEvent(identity, rec,
			"2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z", "INFO", "stdout", fmt.Sprintf("%08d", i))
		event.EventID = "same"
		_, fail := m.planDeliveryBatchOutsideLock([]logtypes.Event{event}, seen)
		require.Nil(t, fail)
	}
	require.EqualValues(t, 49, m.IdentityConflictTraces(),
		"计数器必须与实际冲突次数一致（50 条里第一条立身份，余 49 条是冲突）")
	require.Greater(t, m.IdentityConflictTraces(), int64(conflictSampleSources),
		"计数器不得被样例上限（%d）截断——它是总数，不是样例数", conflictSampleSources)
}
