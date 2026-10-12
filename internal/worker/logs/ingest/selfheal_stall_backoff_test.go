package ingest

// 2026-10-10 生产活锁修复（改动 C）的回归：自愈路径的**失败退避必须按"有没有进展"分流**。
//
// 背景：自愈路径每 10s（gapAutoResolveInterval）对暂停源调一次 `DeliverPending`，投递失败时
// 旧实现只打一条日志就结束 ⇒ 同一批数据被无限期地每 10s 重试一次（现场 3.5 小时零进展的空转）。
// 但另一边：改动 A 让校验**部分通过**也登记进展（逐轮渐进收敛），如果对"投递失败"一律退避，
// 就会把这条收敛路径一起掐死 ✗。
//
// 因此判据只能有一条：**本轮有没有推进**。
//   - 有进展（新增逐字段校验覆盖 / 回收前移 / 账本被改变）⇒ 不退避；
//   - 无进展的连续失败 ⇒ 指数退避（30s → 封顶 10min），且被"有进展"立即重置。
//
// 本文件守住：
//  ① 无进展的连续失败确实推进退避（并封顶，绝不无界）；
//  ② **有进展时不退避**（本轮登记了新覆盖 ⇒ 立刻解除退避）——这是与改动 A 的接口约定；
//  ③ 退避只闸投递这一步（回收滚动/缺口消解仍照常跑，否则连"发现进展"的机会都没了）；
//  ④ 退避状态有界（有进展即清空），且不随失败次数无界增长。

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// newSelfHealStallFixture 造一个「暂停 + 投递必失败 + 无任何进展」的源（现场空转形态）。
//
// 与 newStalledPausedSourceFixture 的差别：这里**不**预置任何可补段的投递水位，使
// `selfHealPausedSource` 的三步都不产生任何改变（即"无进展"），从而能纯粹地考退避本身。
func newSelfHealStallFixture(t *testing.T, failDelivery bool) (*Manager, SourceConfig, *pipeline.Pipeline) {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	src := SourceConfig{
		LogSourceID: "inst:stall", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:stall", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src},
	})
	require.NoError(t, err)
	p := m.pipes[src.LogSourceID+"/"+src.SourceGeneration]
	require.NotNil(t, p)
	// 先灌一条**已 durable** 的条目（暂停态下 WAL.Append 会被门禁拒 ⇒ 必须先追加再暂停）。
	// 没有 durable 存量时 DeliverPending 直接空转（`(false, nil)`，连投递钩子都不会调），
	// 那就不是"投递失败"的形态了 ✗。
	identity := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1"}
	require.NoError(t, p.WAL().Append(logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 0, End: 99},
		"2026-10-10T00:00:00Z", "2026-10-10T00:00:01Z", "INFO", "stdout", "stall")))
	require.NoError(t, p.WAL().Commit())
	// 暂停（现场"顶闸"形态）——自愈面前提条件之一。
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "TEST_STALL: backlog over limit (fixture)"))
	if failDelivery {
		// 投递面必失败（确定性桩）：自愈路径的投递步必然无进展。
		p.SetDelivery(pipeline.FuncHook(func([]logtypes.Event, bool) (pipeline.DeliveryResult, error) {
			return pipeline.DeliveryResult{}, errors.New("stub: VL 不可用（本用例只考自愈退避）")
		}))
	}
	m.SetSelfHealInterval(time.Nanosecond) // 让限频闸必然放行（本用例考的是**退避**闸，不是限频闸 ✓）
	// 逐源状态条目由投递路径惰性创建；本用例直接考凭据登记（recordVerifiedRuns 的落点），
	// 故显式建好它（与 gap_autoresolve_test 的既有写法同形）。
	m.mu.Lock()
	if _, ok := m.state.Sources[p.Key().String()]; !ok {
		m.state.Sources[p.Key().String()] = persistedSource{}
	}
	m.mu.Unlock()
	return m, src, p
}

// TestSelfHealStallBacksOffWithoutProgress 是 ①：无进展的连续失败必须推进退避，且**有界**。
//
// 形态：投递必失败、账本只读不动（无缺口消解、无回收推进、无新覆盖）⇒ 每轮都"无进展"。
//
// 滞回（selfHealStallBackoffAfter = 3）：前 2 次无进展**不**退避（短促的 VL 抖动仍能
// 在下一轮自愈——这是既有回归 TestPausedSourceSelfHealsEndToEnd 的要求），第 3 次起才限频。
//
// 变异验证（实测见回复）：把 selfHealPausedSource 里的 `else if deliveryFailed { ... }` 去掉
// （即回到"失败不退避"的旧行为）⇒ 本用例转红（连续失败后仍不被挡下）。
func TestSelfHealStallBacksOffWithoutProgress(t *testing.T) {
	m, src, p := newSelfHealStallFixture(t, true)
	key := p.Key().String()

	// 滞回期内（前 selfHealStallBackoffAfter-1 次）：仍必须放行投递（不设解禁点）。
	skipsBefore := m.selfHealStallSkip.Load()
	for i := 0; i < selfHealStallBackoffAfter-1; i++ {
		require.False(t, m.selfHealPausedSource(src, p), "无进展的失败不改变账本")
		require.True(t, m.selfHealStallDue(key),
			"第 %d 次无进展仍在滞回期内 ⇒ 不得退避（VL 抖动必须能在下一轮自愈 ✓）", i+1)
	}
	require.Equal(t, int64(selfHealStallBackoffAfter-1), m.SelfHealStallCount(), "无进展必须逐次记账")
	require.Equal(t, skipsBefore, m.selfHealStallSkip.Load(), "滞回期内投递步不得被跳过")

	// 越过滞回 ⇒ 进入退避（起点 base = 30s）。
	require.False(t, m.selfHealPausedSource(src, p))
	require.Equal(t, int64(selfHealStallBackoffAfter), m.SelfHealStallCount())
	require.False(t, m.selfHealStallDue(key), "越过滞回后必须进入退避")

	// 退避期内 ⇒ **投递步被跳过**（观测计数递增），且不再重复记无进展。
	skipsBefore = m.selfHealStallSkip.Load()
	require.False(t, m.selfHealPausedSource(src, p))
	require.Equal(t, skipsBefore+1, m.selfHealStallSkip.Load(),
		"退避期内必须跳过投递步（否则等于没有退避 ✗）")
	require.Equal(t, int64(selfHealStallBackoffAfter), m.SelfHealStallCount(),
		"被退避挡下的轮次不得再加一次无进展（否则退避会自己加速到封顶 ✗）")

	st := m.selfHealBackoffStateForTest(key)
	require.NotNil(t, st)
	require.WithinDuration(t, time.Now().Add(selfHealStallBackoffBase), st.nextAllowedAt, 5*time.Second,
		"首次退避间隔必须是 base（30s），实测 %s", time.Until(st.nextAllowedAt))

	// 把解禁点拨到过去（等价于"等够了"）⇒ 必须重新允许投递并继续推进退避（指数增长 → 翻倍）。
	m.forceSelfHealStallDueForTest(key)
	require.False(t, m.selfHealPausedSource(src, p))
	require.Equal(t, int64(selfHealStallBackoffAfter+1), m.SelfHealStallCount(), "解禁后再次无进展必须继续计数")
	st = m.selfHealBackoffStateForTest(key)
	require.WithinDuration(t, time.Now().Add(2*selfHealStallBackoffBase), st.nextAllowedAt, 5*time.Second,
		"第二次退避间隔必须翻倍到 60s（指数退避），实测 %s", time.Until(st.nextAllowedAt))

	// 封顶：直接伪造很高的连续次数 ⇒ 间隔不得超过 max（绝不无界增长）。
	m.setSelfHealStallsForTest(key, 64)
	m.forceSelfHealStallDueForTest(key)
	require.False(t, m.selfHealPausedSource(src, p))
	st = m.selfHealBackoffStateForTest(key)
	require.LessOrEqual(t, time.Until(st.nextAllowedAt), selfHealStallBackoffMax+time.Second,
		"退避间隔必须封顶在 %s（有界 ⇒ 不构成无界重试循环）", selfHealStallBackoffMax)
}

// TestSelfHealProgressClearsBackoff 是 ②，也是与改动 A 的**接口约定**：
// 只要本轮有新覆盖被登记，退避必须立刻被清掉——否则渐进收敛会在退避期内停摆 ✗。
//
// 形态：先用连续无进展失败把该源推入退避，再在同一源上登记一段新的逐字段校验覆盖，
// 断言下一次自愈调用**不再被退避挡下**。
//
// 变异验证（实测见回复）：把 noteSelfHealStall 之外的两处"有进展清退避"（收尾判据与
// recordVerifiedRunsReporting 里的 delete）都去掉 ⇒ 本用例转红。
func TestSelfHealProgressClearsBackoff(t *testing.T) {
	m, src, p := newSelfHealStallFixture(t, true)
	key := p.Key().String()

	// 前置：越过滞回、进入退避。
	for i := 0; i < selfHealStallBackoffAfter; i++ {
		require.False(t, m.selfHealPausedSource(src, p))
	}
	require.False(t, m.selfHealStallDue(key), "前置：必须已进入退避")

	// 模拟"本轮校验部分通过 ⇒ 登记了新覆盖"（改动 A 在 deliver 路径上的动作）。
	require.True(t, m.recordVerifiedRunsReporting(key, []ledger.PositionRange{{From: 0, To: 99}}),
		"登记新覆盖必须被报告为进展")
	// **登记的那一刻**退避就必须被清掉（不能等下一轮收尾——那一轮的投递已经被挡下了 ✗）。
	require.Nil(t, m.selfHealBackoffStateForTest(key),
		"有进展必须**立刻**清空退避状态（有界：状态不随失败次数累积 ✓）")

	// 关键断言：下一次自愈调用**不得**被退避挡下（投递步必须真的被执行一次）。
	skipsBefore := m.selfHealStallSkip.Load()
	require.False(t, m.selfHealPausedSource(src, p), "投递仍失败（桩），但**不得**被退避挡下")
	require.Equal(t, skipsBefore, m.selfHealStallSkip.Load(),
		"有进展后不得再被退避挡下（否则改动 A 的渐进收敛会在退避期内停摆 ✗）")

	// 反向对照：此后**没有**新进展 ⇒ 滞回用尽后重新进入退避（分流判据仍在，不是"永不退避"✗）。
	for i := 0; i < selfHealStallBackoffAfter; i++ {
		m.selfHealPausedSource(src, p)
	}
	require.False(t, m.selfHealStallDue(key),
		"无进展的失败必须重新进入退避（否则本用例会退化成「永不退避」✗）")
}

// TestSelfHealReclaimAdvanceClearsBackoff 是 ② 的补充：**回收前移**同样算进展。
//
// 为什么单列一条：回收滚动是"补段 → 减积压"的路径，它推进时账本的 `changed` 未必为真
// （回收位置在账本 Positions 里，不在返回值里）。漏了这条判据就会把一个正在收敛的源
// 误判成"无进展"并套上退避 ✗。
func TestSelfHealReclaimAdvanceClearsBackoff(t *testing.T) {
	m, src, p := newSelfHealStallFixture(t, true)
	key := p.Key().String()

	// 前置：越过滞回、进入退避。
	for i := 0; i < selfHealStallBackoffAfter; i++ {
		require.False(t, m.selfHealPausedSource(src, p))
	}
	require.False(t, m.selfHealStallDue(key), "前置：必须已进入退避")

	// 造"可推进的回收"：一条已确认投递的事件 + 零覆盖段 ⇒ 自愈面步 0 会补段并推进回收。
	identity := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1"}
	require.NoError(t, p.Ledger().ResumeAcquire(p.Key())) // 暂停态下 WAL.Append 会被门禁拒（设计如此）
	require.NoError(t, p.WAL().Append(logtypes.BuildEvent(identity,
		logtypes.RecordRange{Start: 0, End: 99}, "2026-10-10T00:00:00Z", "2026-10-10T00:00:01Z", "INFO", "stdout", "stall")))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{StartPos: 0, EndPos: 99, HTTPStatus: 200}))
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "TEST_STALL: backlog over limit (fixture)"))

	// 自愈面步 0 补段并推进回收 ⇒ 这是**进展** ⇒ 退避必须被清掉。
	m.selfHealPausedSource(src, p)
	reclaim, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Greater(t, reclaim.Reclaim, uint64(0), "前置：回收确实前移了（本用例的进展来源）")
	require.Nil(t, m.selfHealBackoffStateForTest(key),
		"回收前移属于进展 ⇒ 必须清空退避（否则会把正在收敛的源误判成卡死 ✗）")
}

// ── 测试专用观察口（生产路径不暴露这些；命名以 ForTest 结尾以明示） ──────────────────────

// selfHealBackoffStateForTest 返回该源的退避状态副本（无则 nil）。
func (m *Manager) selfHealBackoffStateForTest(key string) *selfHealBackoffState {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.selfHealBackoff[key]
	if st == nil {
		return nil
	}
	copied := *st
	return &copied
}

// forceSelfHealStallDueForTest 把解禁点拨到过去（等价于"等够了"，避免用例真等 30s）。
func (m *Manager) forceSelfHealStallDueForTest(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.selfHealBackoff[key]; st != nil {
		st.nextAllowedAt = time.Now().Add(-time.Second)
	}
}

// setSelfHealStallsForTest 直接设置连续无进展次数（用于验证退避**封顶**，避免真等几十轮）。
func (m *Manager) setSelfHealStallsForTest(key string, stalls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.selfHealBackoff == nil {
		m.selfHealBackoff = map[string]*selfHealBackoffState{}
	}
	st := m.selfHealBackoff[key]
	if st == nil {
		st = &selfHealBackoffState{}
		m.selfHealBackoff[key] = st
	}
	st.stalls = stalls
}
