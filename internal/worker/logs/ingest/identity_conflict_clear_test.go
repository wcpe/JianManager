package ingest

// 「人工确认并清空身份冲突」的回归（ops 入口，使日志页的质量标记不再为 conflict/partial）。
//
// 背景（2026-10-11 生产现场）：日志页显示 duplicate_quality=conflict / stats_quality=partial，
// 数据现况是只有 inst:145/file（1 条）与 inst:151/file（8 条）持有冲突记录，且这批冲突**不再
// 被重投**（近期「身份冲突」日志 0 条）⇒ 清空后不会重新产生。
//
// 立论：冲突记录是「同 event_id 异内容」的**诊断凭据**，而系统在该形态下已按确定性证据
// 「保留先到者并跳过冲突行」（appendEvents 的身份收敛），即保留的是正确版本、冲突行从未进入
// 写入面。故人工确认后清空**只移除提示**，不改任何日志内容。
//
// 本文件的用例覆盖任务书的全部红线：
//   - 正向：有冲突时清空成功、返回条数、identityConflictCount 归零；
//   - 负例：缺 operator / 缺 reason 一律拒绝；
//   - 防呆：只清 identity_conflicts，payload 其它字段逐字节不变；
//   - 断言：清空后 ConflictCount 会归零（= 质量标记消失，见 planner 的 DupConflict 派生）；
//   - 断言：有界等锁（cycleMu 被占时明确失败而非无限等待）；
//   - 断言：审计凭据确实落盘（含操作人 / 理由 / 时间 / 条数 / 被清明细）。
//
// 变异记录（实测见文件末尾各用例注释）。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// conflictClearFixture 建一个"某源持有 N 条不可裁定冲突记录"的真 Manager（真索引库、真目录）。
//
// 为什么不用桩：本功能的全部红线都落在**真实持久化链路**上（payload 搬运、快照克隆、落库
// 通道），用桩把索引换掉就测不到「凭据是否真的落盘」与「其它字段是否被误清」。
type conflictClearFixture struct {
	manager *Manager
	key     string
	source  SourceConfig
	// conflicts 是写进该源的冲突记录（与 recordIdentityConflicts 同形态）。
	conflicts []identityConflictRecord
}

// fixtureConflictRecord 造一条形态合法的冲突记录（同 ID 异哈希）。
func fixtureConflictRecord(index int) identityConflictRecord {
	return identityConflictRecord{
		EventID:     fixtureConflictEventID(index),
		Start:       uint64(index * 100),
		End:         uint64(index*100 + 99),
		KeptHash:    "kept-" + string(rune('a'+index%26)),
		DroppedHash: "dropped-" + string(rune('a'+index%26)),
		ObservedAt:  "2026-10-05T00:00:00Z",
	}
}

func fixtureConflictEventID(index int) string {
	return "event-" + string(rune('a'+index%26)) + "-" + string(rune('0'+index%10))
}

// newConflictClearFixture 建一个带 count 条冲突记录的真 Manager，并**先落库一次**，
// 使"清空前后"的对照落在同一份已持久化的状态上。
func newConflictClearFixture(t *testing.T, count int) *conflictClearFixture {
	t.Helper()
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	m, err := New(Options{Root: root, Catalog: cat, Journal: journal})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	source := SourceConfig{
		LogSourceID: "inst:145/file", SourceGeneration: "g1",
		Mode: pipeline.ModeFilePrimary, Path: t.TempDir() + "/latest.log",
		StorageNamespace: "inst:145", UTCDay: "2026-10-05",
	}
	require.NoError(t, m.Register(source))
	key := source.LogSourceID + "/" + source.SourceGeneration

	conflicts := make([]identityConflictRecord, 0, count)
	for i := 0; i < count; i++ {
		conflicts = append(conflicts, fixtureConflictRecord(i))
	}
	m.recordIdentityConflicts(key, conflicts)
	// 落库一次：让"清空前"的状态与凭据都在磁盘上（清空后的断言才能区分搬运是否真的发生）。
	require.NoError(t, m.persist())
	return &conflictClearFixture{manager: m, key: key, source: source, conflicts: conflicts}
}

// persistedPayload 读出该源当前在**索引库**里的 aux payload（原始 JSON 字节），
// 供「只清 identity_conflicts、其它字段字节不变」的防呆断言使用。
func persistedPayload(t *testing.T, m *Manager, key string) map[string]json.RawMessage {
	t.Helper()
	m.mu.Lock()
	store := m.index
	m.mu.Unlock()
	require.NotNil(t, store, "夹具前提：索引句柄必须已打开")
	rows, err := store.Load()
	require.NoError(t, err)
	for _, row := range rows.Aux {
		if row.Key != key {
			continue
		}
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(row.Payload, &payload))
		return payload
	}
	t.Fatalf("索引里找不到源 %s 的 aux payload", key)
	return nil
}

// TestClearIdentityConflictsClearsRecordsAndWritesAuditCredential 覆盖正向路径与红线 3。
//
// 断言链（对应任务书的正向 + 审计凭据落盘）：
//   - 清空返回的条数 == 原有条数；
//   - identityConflictCount 归零（这是质量标记消失的**直接判据**，见 runtime 的
//     publishSnapshot / planner 的 DupConflict 派生）；
//   - 凭据落进**索引库 payload**（不是只在内存），且字段齐备（操作人 / 理由 / 时间 / 条数 / 明细）。
func TestClearIdentityConflictsClearsRecordsAndWritesAuditCredential(t *testing.T) {
	fixture := newConflictClearFixture(t, 8)
	m, key := fixture.manager, fixture.key

	require.Len(t, savedSource(t, m, key).IdentityConflicts, 8, "夹具前提：该源应持有 8 条冲突记录")
	require.Equal(t, uint64(8), m.identityConflictCount("inst:145", key), "夹具前提：内存计数应为 8")

	result, err := m.ClearIdentityConflicts(context.Background(), key, "ops-alice", "已核对 VL 保留版本正确，同意清空")
	require.NoError(t, err)
	require.Equal(t, 8, result.ClearedCount, "必须返回真正清掉的条数")
	require.Equal(t, []string{key}, result.ClearedSources)
	require.Empty(t, result.SkippedSources, "记录集合未变 ⇒ 不应跳过任何源")

	// 红线 1 的直接判据：记录已清空、计数归零（⇒ 质量标记消失）。
	require.Empty(t, savedSource(t, m, key).IdentityConflicts, "冲突记录必须被清空")
	require.Zero(t, m.identityConflictCount("inst:145", key),
		"identityConflictCount 必须归零（它是 publishSnapshot 写 ConflictCount 的唯一输入）")

	// 红线 3：凭据必须落进**索引库**（重启/换进程后仍可核验），并字段齐备。
	cleared := savedSource(t, m, key).IdentityConflictClearances
	require.Len(t, cleared, 1, "必须留下一条清空凭据")
	credential := cleared[0]
	require.Equal(t, "ops-alice", credential.Operator, "凭据必须记录操作人")
	require.Equal(t, "已核对 VL 保留版本正确，同意清空", credential.Reason, "凭据必须记录理由")
	require.Equal(t, 8, credential.ClearedCount, "凭据必须记录清空条数")
	require.Len(t, credential.ClearedRecords, 8, "凭据必须保留被清记录明细（保留版本可回溯）")
	require.Equal(t, fixture.conflicts[0].KeptHash, credential.ClearedRecords[0].KeptHash,
		"被清明细必须与清空前逐字段一致（否则保留版本不可核验）")
	_, err = time.Parse(time.RFC3339Nano, credential.AtUTC)
	require.NoError(t, err, "凭据时间必须是可解析的 RFC3339Nano")

	payload := persistedPayload(t, m, key)
	require.NotContains(t, string(payload["identity_conflicts"]), "event-",
		"索引 payload 里的 identity_conflicts 必须已清空")
	require.Contains(t, string(payload["identity_conflict_clearances"]), "ops-alice",
		"清空凭据必须已落进索引 payload（不得只留在内存）")

	// 清空后重新装载：凭据与"已无冲突"都必须是**持久事实**（这是"落盘"与"只在内存"的分界）。
	reloaded, err := m.loadStateFromIndex()
	require.NoError(t, err)
	require.Empty(t, reloaded.Sources[key].IdentityConflicts, "重载后冲突记录应为空")
	require.Len(t, reloaded.Sources[key].IdentityConflictClearances, 1, "重载后凭据必须仍在")
	require.Equal(t, "ops-alice", reloaded.Sources[key].IdentityConflictClearances[0].Operator)
}

// TestClearIdentityConflictsClearsAcrossAllSources 覆盖"全部源"粒度（storageNamespace 为空）。
func TestClearIdentityConflictsClearsAcrossAllSources(t *testing.T) {
	fixture := newConflictClearFixture(t, 3)
	m := fixture.manager
	// 第二个源：不同实例、两条冲突。
	second := SourceConfig{
		LogSourceID: "inst:151/file", SourceGeneration: "g1",
		Mode: pipeline.ModeFilePrimary, Path: t.TempDir() + "/latest.log",
		StorageNamespace: "inst:151", UTCDay: "2026-10-05",
	}
	require.NoError(t, m.Register(second))
	secondKey := second.LogSourceID + "/" + second.SourceGeneration
	m.recordIdentityConflicts(secondKey, []identityConflictRecord{fixtureConflictRecord(10), fixtureConflictRecord(11)})
	require.NoError(t, m.persist())

	result, err := m.ClearIdentityConflicts(context.Background(), "", "ops-bob", "两个源均已核对")
	require.NoError(t, err)
	require.Equal(t, 5, result.ClearedCount, "跨源合计应为 3+2=5")
	require.Equal(t, []string{fixture.key, secondKey}, result.ClearedSources, "清空的源键必须升序返回")
	require.Zero(t, m.identityConflictCount("inst:145", fixture.key))
	require.Zero(t, m.identityConflictCount("inst:151", secondKey))
}

// TestClearIdentityConflictsRequiresOperatorAndReason 覆盖红线 1、2（负例）。
//
// 两条都必须**拒绝且不留任何痕迹**：这是"不给掩盖数据歧义开后门"的实现级判据。
func TestClearIdentityConflictsRequiresOperatorAndReason(t *testing.T) {
	fixture := newConflictClearFixture(t, 2)
	m, key := fixture.manager, fixture.key

	// 缺操作人（含纯空白）。
	for _, operator := range []string{"", "   ", "\t"} {
		result, err := m.ClearIdentityConflicts(context.Background(), key, operator, "理由充分")
		require.Error(t, err, "操作人缺失必须拒绝（operator=%q）", operator)
		require.Contains(t, err.Error(), "操作人")
		require.Zero(t, result.ClearedCount)
	}
	// 缺理由（含纯空白）。
	for _, reason := range []string{"", "  ", "\n"} {
		result, err := m.ClearIdentityConflicts(context.Background(), key, "ops-alice", reason)
		require.Error(t, err, "理由缺失必须拒绝（reason=%q）", reason)
		require.Contains(t, err.Error(), "理由")
		require.Zero(t, result.ClearedCount)
	}
	// 拒绝必须是**无痕**的：记录与凭据都不变。
	require.Len(t, savedSource(t, m, key).IdentityConflicts, 2, "被拒的请求不得清掉任何记录")
	require.Empty(t, savedSource(t, m, key).IdentityConflictClearances, "被拒的请求不得留下凭据")
	require.Equal(t, uint64(2), m.identityConflictCount("inst:145", key))
}

// TestClearIdentityConflictsOnlyTouchesIdentityConflicts 覆盖红线 4（防呆）。
//
// 判据取在**索引 payload 的 JSON 字节**上：同 payload 里的其它字段（verified_runs /
// abandonments / ledger_core / events_stored / events / wal…）在清空前后必须逐字节不变。
// 逐个字段断言会漏掉"新增字段被顺手清掉"的形态，字节级对照不会。
func TestClearIdentityConflictsOnlyTouchesIdentityConflicts(t *testing.T) {
	fixture := newConflictClearFixture(t, 4)
	m, key := fixture.manager, fixture.key

	// 造出"同 payload 里还有别的字段"，否则本用例是空断言（防呆测试最容易被做成这样）。
	// 必须有**真实缺口**才谈得上放弃：AbandonGapsThrough 在没有可放弃缺口时按设计不写凭据
	// （避免留下"什么都没放弃"的空裁定），直接调用会得到空切片。
	ledgerKey := m.pipes[key].Key()
	require.NoError(t, m.pipes[key].Ledger().RecordGap(ledgerKey, 10, 50,
		ledger.GapReasonPermanentlyLost, "fixture gap for payload byte comparison"))
	abandoned, err := m.pipes[key].Ledger().AbandonGapsThrough(ledgerKey, 50, ledger.GapAbandonment{
		ReasonCode: ledger.GapReasonPermanentlyLost, Reason: "fixture", Operator: "ops-fixture",
	})
	require.NoError(t, err)
	require.Equal(t, 1, abandoned, "夹具前提：必须真的写下一条放弃凭据")
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.VerifiedRuns = []ledger.PositionRange{{From: 1, To: 100}}
	saved.EventsStored = true
	m.state.Sources[key] = saved
	m.mu.Unlock()
	require.NoError(t, m.persist())

	before := persistedPayload(t, m, key)
	require.NotEmpty(t, before["verified_runs"], "夹具前提：payload 里应有 verified_runs")
	require.NotEmpty(t, before["abandonments"], "夹具前提：payload 里应有 abandonments")
	require.NotEmpty(t, before["ledger_core"], "夹具前提：payload 里应有 ledger_core")
	require.NotEmpty(t, before["identity_conflicts"], "夹具前提：payload 里应有待清的冲突记录")

	_, err = m.ClearIdentityConflicts(context.Background(), key, "ops-alice", "已核对保留版本")
	require.NoError(t, err)

	after := persistedPayload(t, m, key)
	for _, field := range []string{"verified_runs", "abandonments", "ledger_core", "events_stored", "events"} {
		require.Equal(t, string(before[field]), string(after[field]),
			"清空身份冲突不得改动同 payload 的其它字段 %s（防呆红线）", field)
	}
	require.NotEqual(t, string(before["identity_conflicts"]), string(after["identity_conflicts"]),
		"夹具失效：identity_conflicts 应当变了（否则本用例测不到任何东西）")
	require.Contains(t, string(after["identity_conflict_clearances"]), "ops-alice")
}

// TestClearIdentityConflictsBoundedOnCycleMu 覆盖红线 7（有界等锁）。
//
// 绿（现状）：占住 cycleMu ⇒ 调用在预算内返回"采集轮忙"，且**未做任何变更**；
// 红（变异：改回无界 m.cycleMu.Lock()）⇒ 调用阻塞远超预算 ⇒ 走 "超预算仍未返回" 分支。
func TestClearIdentityConflictsBoundedOnCycleMu(t *testing.T) {
	fixture := newConflictClearFixture(t, 2)
	m, key := fixture.manager, fixture.key

	// 占住 cycleMu（模拟"采集轮长持有"）。
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := m.ClearIdentityConflicts(context.Background(), key, "ops-alice", "已核对")
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err, "cycleMu 被占时必须明确失败而非挂死")
		require.Contains(t, err.Error(), "采集轮忙", "错误必须可判读（忙/请稍后重试）")
	case <-time.After(3 * clearIdentityConflictsCycleMuWait):
		t.Fatal("超预算仍未返回 ⇒ 仍在无限等 cycleMu（红：本仓刚修过的挂死缺陷类型）")
	}
	// 忙即失败必须是**无副作用**的（否则运维重试会得到两次变更）。
	require.Len(t, savedSource(t, m, key).IdentityConflicts, 2, "等不到锁时不得清掉任何记录")
	require.Empty(t, savedSource(t, m, key).IdentityConflictClearances, "等不到锁时不得留下凭据")
}

// TestClearIdentityConflictsSkipsRecordsAddedAfterReview 覆盖三段式复验（并发写入的窄窗口）。
//
// 语义：第一段取到的快照是**人工审阅过的那一批**。若在提交前有**新**冲突写入（那批未经审阅），
// 必须跳过该源——否则"确认后清空"就退化成"无条件清空"，人工确认这一步形同虚设。
// 用测试钩子确定性地插入"审阅后新增"的形态（不靠 sleep 猜时序）。
func TestClearIdentityConflictsSkipsRecordsAddedAfterReview(t *testing.T) {
	fixture := newConflictClearFixture(t, 2)
	m, key := fixture.manager, fixture.key

	// 在第一段（取计划快照）与第三段（提交）之间注入一条新冲突。
	var injected bool
	m.clearConflictsAfterPlanHook = func() {
		if injected {
			return
		}
		injected = true
		m.recordIdentityConflicts(key, []identityConflictRecord{fixtureConflictRecord(20)})
	}

	result, err := m.ClearIdentityConflicts(context.Background(), key, "ops-alice", "已核对审阅到的那批")
	require.NoError(t, err)
	require.True(t, injected, "夹具前提：注入钩子必须被调用（否则本用例没测到复验分支）")
	require.Zero(t, result.ClearedCount, "新增记录后不得清空该源")
	require.Empty(t, result.ClearedSources)
	require.Equal(t, []string{key}, result.SkippedSources, "被跳过的源必须如实回报")
	require.Len(t, savedSource(t, m, key).IdentityConflicts, 3,
		"未经审阅的记录必须留在库里（不得顺手清掉）")
	require.Empty(t, savedSource(t, m, key).IdentityConflictClearances, "跳过时不得留下凭据")
}

// TestClearIdentityConflictsRejectsUnknownSource 覆盖守卫：源不存在时明确失败（而不是静默成功）。
func TestClearIdentityConflictsRejectsUnknownSource(t *testing.T) {
	fixture := newConflictClearFixture(t, 1)
	m := fixture.manager

	result, err := m.ClearIdentityConflicts(context.Background(), "inst:404/file/g1", "ops-alice", "已核对")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found", "必须明确说源不存在（否则运维会以为清成功了）")
	require.Zero(t, result.ClearedCount)
}

// TestClearIdentityConflictsKeepsCredentialBounded 覆盖凭据有界化（避免 payload 无界增长）。
func TestClearIdentityConflictsKeepsCredentialBounded(t *testing.T) {
	fixture := newConflictClearFixture(t, 1)
	m, key := fixture.manager, fixture.key

	for i := 0; i < maxIdentityConflictClearances+5; i++ {
		// 每轮先补一条冲突再清，模拟反复运维。
		m.recordIdentityConflicts(key, []identityConflictRecord{fixtureConflictRecord(i)})
		_, err := m.ClearIdentityConflicts(context.Background(), key, "ops-alice", "第 "+string(rune('a'+i%26))+" 次核对")
		require.NoError(t, err)
	}
	credentialCount := len(savedSource(t, m, key).IdentityConflictClearances)
	require.Equal(t, maxIdentityConflictClearances, credentialCount,
		"凭据必须保留最近 %d 条（有界：不让 payload 随运维次数无界增长）", maxIdentityConflictClearances)
}

// TestClearIdentityConflictsClearsConflictCountSeenByPlanner 覆盖"质量标记消失"的端到端判据。
//
// 这是本功能**唯一**的产品目标：planner 在 ref.Projection.ConflictCount > 0 时把
// duplicate_quality 置为 DupConflict（前端显示「有重复日志待确认」）。故断言必须在
// **planner 的输入**上做，而不是只看计数：发布投影 → planner 报 conflict → 清空 → 重发投影
// → planner 不再报 conflict。
func TestClearIdentityConflictsClearsConflictCountSeenByPlanner(t *testing.T) {
	fixture := newConflictClearFixture(t, 2)
	m, key, source := fixture.manager, fixture.key, fixture.source
	events := ingestTestEvents(source.LogSourceID, source.SourceGeneration, 0, 2, "body")
	require.NoError(t, m.publish(source, "g-proj", events, false))

	key2 := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	rec, ok := m.cat.Get(key2)
	require.True(t, ok)
	require.NotNil(t, rec.PublishedProjection)
	require.Equal(t, uint64(2), rec.PublishedProjection.ConflictCount,
		"夹具前提：发布投影应把该源的冲突计数写进 Catalog")

	planner := query.NewPlanner(qtestCatalog{m.cat}, nil)
	before := planQuality(t, planner, key2)
	require.Equal(t, query.DupConflict, before.DuplicateQuality,
		"夹具前提：有冲突时 planner 必须报 DupConflict（= 前端「有重复日志待确认」）")
	require.Equal(t, query.StatsPartial, before.StatsQuality)

	_, err := m.ClearIdentityConflicts(context.Background(), key, "ops-alice", "已核对 VL 保留版本正确")
	require.NoError(t, err)

	// 清空后重发投影：ConflictCount 由 identityConflictCount（内存态）重算 ⇒ 必须为 0。
	require.NoError(t, m.publish(source, "g-proj-2", events, false))
	rec, ok = m.cat.Get(key2)
	require.True(t, ok)
	require.Zero(t, rec.PublishedProjection.ConflictCount,
		"清空后再发布必须把 ConflictCount 重算为 0（这是标记消失的唯一路径）")

	after := planQuality(t, planner, key2)
	require.NotEqual(t, query.DupConflict, after.DuplicateQuality,
		"清空后 planner 不得再报 DupConflict（= 前端质量标记消失）")
}

// TestClearIdentityConflictsCancelledContextWritesNothing 覆盖 ctx 取消的语义：
// 取消即不得落任何变更（凭据一旦写下就代表"有人确认过"，不该由一次失败的请求产生）。
func TestClearIdentityConflictsCancelledContextWritesNothing(t *testing.T) {
	fixture := newConflictClearFixture(t, 2)
	m, key := fixture.manager, fixture.key

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.ClearIdentityConflicts(ctx, key, "ops-alice", "已核对")
	require.Error(t, err)
	require.Len(t, savedSource(t, m, key).IdentityConflicts, 2, "取消不得清掉任何记录")
	require.Empty(t, savedSource(t, m, key).IdentityConflictClearances, "取消不得留下凭据")
}

// TestClearIdentityConflictsReadBackByOfflineEventStatus 覆盖"凭据比进程活得久"：
// 命令行 `log-event-status`（离线只读索引库）也必须能看到清空事实与凭据。
//
// 为什么要单独一条：离线路径直接读索引 payload，是"凭据真的落盘"的**独立**证据源——
// 只在 Manager 内部重载状态的话，两边共用一个实现，搬运错误可能被同一个错误掩盖。
func TestClearIdentityConflictsReadBackByOfflineEventStatus(t *testing.T) {
	fixture := newConflictClearFixture(t, 3)
	m, key := fixture.manager, fixture.key
	_, err := m.ClearIdentityConflicts(context.Background(), key, "ops-alice", "离线可核验")
	require.NoError(t, err)
	// 关闭索引句柄后从磁盘重开（模拟另一个进程/停机后的取证）。
	require.NoError(t, m.Stop())

	payload := readAuxPayloadFromDisk(t, m.indexPath(), key)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Contains(t, string(payload), "ops-alice", "离线只读索引必须能看到清空凭据")
	// 判据必须落在 identity_conflicts 这个**键**上，不能拿"整份 payload 不含 event_id"当判据——
	// 凭据里的 cleared_records 明细**本就**含 event_id（那是让"当初保留了哪个版本"可回溯的部分）。
	require.Empty(t, decoded["identity_conflicts"],
		"离线只读索引里 identity_conflicts 必须已清空（omitempty ⇒ 键消失或为空）")
	require.NotEmpty(t, decoded["identity_conflict_clearances"], "凭据必须在离线只读索引里可见")
}

// readAuxPayloadFromDisk 直接打开索引库文件读某源的 aux payload（不经 Manager 的内存态）。
func readAuxPayloadFromDisk(t *testing.T, indexPath, key string) []byte {
	t.Helper()
	store, err := stateindex.OpenReadOnly(indexPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	rows, err := store.Load()
	require.NoError(t, err)
	for _, row := range rows.Aux {
		if row.Key == key {
			return row.Payload
		}
	}
	t.Fatalf("索引里找不到源 %s", key)
	return nil
}

// planQuality 用真 planner 求某分区的质量档（与前端读到的 duplicate_quality 同源）。
func planQuality(t *testing.T, planner *query.Planner, key catalog.PartitionKey) query.Quality {
	t.Helper()
	res := planner.Plan(query.PlanRequest{TargetIDs: []string{key.String()}})
	return res.Quality
}

// qtestCatalog 把 *catalog.Catalog 适配成 planner 需要的接口（最小面）。
type qtestCatalog struct{ cat *catalog.Catalog }

func (q qtestCatalog) Keys() []catalog.PartitionKey                         { return q.cat.Keys() }
func (q qtestCatalog) Get(key catalog.PartitionKey) (*catalog.Record, bool) { return q.cat.Get(key) }
func (q qtestCatalog) OwnerForQuery(key catalog.PartitionKey) (catalog.QueryRef, bool) {
	return q.cat.OwnerForQuery(key)
}
