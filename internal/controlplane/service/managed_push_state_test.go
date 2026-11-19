package service

import (
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 下发编排的状态机回归。
//
// 这组用例守的是三条「看起来能跑、真运维才炸」的缺陷：
//   - 进程重启后推进中会话永久卡死（无人工介入通道）；
//   - 熔断落成可 Resume 的 paused，一键续推会绕过「配置可能有问题」那次判断；
//   - 回滚声称重推冻结快照，实际重新合并，推出去的是一份从未生效过的配置。

// newPushTestDB 建下发编排所需的临时库。
func newPushTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{},
		&model.ManagedPushSession{}, &model.ManagedPushTarget{},
		&model.ManagedInstanceConfigState{},
	))
	return db
}

// newPushTestService 建一个不连节点池的下发服务（只跑状态机，不下发网络帧）。
func newPushTestService(t *testing.T, db *gorm.DB) *ManagedPushService {
	t.Helper()
	return NewManagedPushService(db, nil, NewManagedConfigService(db), NewManagedInstanceStateService(db))
}

// seedSession 建一个指定状态的会话。
func seedSession(t *testing.T, db *gorm.DB, status model.ManagedPushSessionStatus, kind string) model.ManagedPushSession {
	t.Helper()
	session := model.ManagedPushSession{
		Kind:                 kind,
		Scope:                model.ManagedScopeGlobal,
		ScopeName:            "全局",
		ConfigVersion:        7,
		FailureRateThreshold: 20,
		AckTimeoutSec:        30,
		Status:               status,
		PlannedCount:         2,
		PendingCount:         2,
		TotalBatches:         1,
		CurrentBatch:         1,
	}
	require.NoError(t, db.Create(&session).Error)
	return session
}

// TestRecoverInterrupted_PausesRunningSession 守「进程重启后推进中会话不再卡死」。
//
// 推进循环由内存 goroutine 驱动，重启后它消失但库里仍是 running：
// 没人推进、没人计时、`Release` 又要求 awaiting_release，人工也放行不了。
// 收口到 paused 后 Resume 才可用——这是唯一的人工脱困通道。
func TestRecoverInterrupted_PausesRunningSession(t *testing.T) {
	db := newPushTestDB(t)
	running := seedSession(t, db, model.ManagedPushRunning, model.ManagedPushKindPush)
	awaiting := seedSession(t, db, model.ManagedPushAwaitingRelease, model.ManagedPushKindPush)
	completed := seedSession(t, db, model.ManagedPushCompleted, model.ManagedPushKindPush)

	svc := newPushTestService(t, db)
	n, err := svc.RecoverInterrupted()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "只应收口 running 那一台")

	got, err := svc.Get(running.ID)
	require.NoError(t, err)
	assert.Equal(t, model.ManagedPushPaused, got.Status,
		"重启后必须置为暂停；停在 running 会让会话永久卡死（既不能推进也不能放行）")
	assert.Equal(t, model.ManagedPushPauseRestart, got.PauseKind,
		"暂停原因要能区分「重启」与「人工暂停」，否则运维会以为自己点过暂停")
	assert.NotEmpty(t, got.PauseReason, "必须留下可读原因，否则面板上只有一个没有解释的 paused")

	// awaiting_release 是静止态（等人工放行），重启不影响它，改它反而丢掉「已推完一批」的进度。
	kept, err := svc.Get(awaiting.ID)
	require.NoError(t, err)
	assert.Equal(t, model.ManagedPushAwaitingRelease, kept.Status,
		"awaiting_release 不该被重启收口改动——它本就是待人处理的静止态")

	term, err := svc.Get(completed.ID)
	require.NoError(t, err)
	assert.Equal(t, model.ManagedPushCompleted, term.Status, "终态不得被改动")

	// 幂等：再跑一次不应再改动任何行。
	again, err := svc.RecoverInterrupted()
	require.NoError(t, err)
	assert.Equal(t, int64(0), again, "收口必须幂等")
}

// TestCircuitBreak_EndsAsFailedNotPaused 守熔断是终态。
//
// 熔断若落成 paused，Resume 就能一键续推剩余批次——而触发熔断的那份配置很可能就是
// 问题本身，续推等于把已验证的坏配置扩散到更多机器。终态 failed 强制人先看清现场。
func TestCircuitBreak_EndsAsFailedNotPaused(t *testing.T) {
	db := newPushTestDB(t)
	session := seedSession(t, db, model.ManagedPushRunning, model.ManagedPushKindPush)

	svc := newPushTestService(t, db)
	require.NoError(t, svc.failByCircuitBreak(session.ID, "失败率超阈值", 20, 66.6))

	got, err := svc.Get(session.ID)
	require.NoError(t, err)
	assert.Equal(t, model.ManagedPushFailed, got.Status, "熔断必须落终态 failed")
	assert.True(t, got.Status.IsTerminal(), "failed 必须是终态")

	// 现场必须留存：运维据此判断是配置问题还是这批机器环境问题。
	assert.InDelta(t, 20.0, got.BreakThreshold, 0.01, "必须记住当时的阈值")
	assert.InDelta(t, 66.6, got.BreakObserved, 0.01, "必须记住实测失败率")
	assert.NotEmpty(t, got.BreakReason, "必须记住熔断类别")
	require.NotNil(t, got.FinishedAt, "终态要有结束时刻")

	// 关键断言：熔断后不可 Resume。这是 failed 与 paused 的真正差别。
	_, rerr := svc.Resume(session.ID)
	require.Error(t, rerr, "熔断后不得 Resume——那会绕过「配置可能有问题」这次判断")
	assert.ErrorIs(t, rerr, ErrManagedPushStateInvalid)
}

// TestRollback_ReplaysFrozenContentNotRecompute 守回滚重推的是当时发出去的内容。
//
// 真实缺陷：`Rollback` 搬了会话级 ValuesJSON，但 `pushOne` 从不读它——每台重新
// `MergeForInstance`。两次推送之间只要改过 global 层，回滚推出去的就是一份从未在
// 任何机器上生效过的配置，"回到上一次推送的状态"也就不成立。
func TestRollback_ReplaysFrozenContentNotRecompute(t *testing.T) {
	db := newPushTestDB(t)

	node := model.Node{Name: "n1", UUID: "n1-uuid"}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{Name: "hub", UUID: "hub-uuid", NodeID: node.ID}
	require.NoError(t, db.Create(&inst).Error)

	source := seedSession(t, db, model.ManagedPushCompleted, model.ManagedPushKindPush)
	// 源会话对这台机器实际发出去的内容：采集周期 100。
	frozenPayload, err := ManagedConfigPush{
		ConfigVersion: 7,
		BaseVersion:   3,
		Values:        map[string]any{"collect-period-ticks": 100},
	}.BuildConfigPushPayload()
	require.NoError(t, err)

	src := model.ManagedPushTarget{
		SessionID: source.ID, InstanceID: inst.ID, InstanceUUID: inst.UUID,
		InstanceName: inst.Name, NodeID: node.ID, BatchNo: 1,
		State:             model.ManagedPushTargetApplied,
		DesiredVersion:    7,
		PushedPayloadJSON: string(frozenPayload),
	}
	require.NoError(t, db.Create(&src).Error)

	svc := newPushTestService(t, db)

	// 回滚建会话：新目标必须带上源目标的下发载荷。
	rollback, rerr := svc.Rollback(ManagedRollbackInput{
		SourceSessionID: source.ID, Reason: "回退到上次已验证的配置", Confirmed: true,
	}, 1)
	require.NoError(t, rerr)

	var newTarget model.ManagedPushTarget
	require.NoError(t, db.Where("session_id = ?", rollback.ID).First(&newTarget).Error)
	assert.NotEmpty(t, newTarget.PushedPayloadJSON,
		"回滚目标必须继承源目标的下发载荷；为空就只能重新合并，那不再是「回到上一次推送的状态」")

	// 用同一套内容构造回滚帧：**内容取自冻结载荷，版本号取本次会话**。
	var frozen ManagedConfigPush
	require.NoError(t, json.Unmarshal([]byte(newTarget.PushedPayloadJSON), &frozen))
	// JSON 里的数字反序列化成 float64，与编码前 int 的类型不同，故按数值比较。
	assert.InDelta(t, 100.0, frozen.Values["collect-period-ticks"], 0.001,
		"回滚内容必须是当时实际发出去的值")

	// 版本号必须换成本次会话的，否则探针按旧版本回执、CP 按新版本等，回执永远对不上。
	assert.NotEqual(t, rollback.ConfigVersion, frozen.ConfigVersion,
		"回滚会话应铸造新版本号，源载荷里的是旧版本")
	assert.Greater(t, rollback.ConfigVersion, int64(7), "新版本号应大于源会话版本")
}

// TestPushedPayloadRecordedOnDispatch 守常规下发也会记录载荷原文。
//
// 没有这一步，回滚就没有可重推的来源——记录必须发生在下发路径上，而不是只在回滚时补。
func TestPushedPayloadRecordedOnDispatch(t *testing.T) {
	db := newPushTestDB(t)
	session := seedSession(t, db, model.ManagedPushRunning, model.ManagedPushKindPush)

	target := model.ManagedPushTarget{
		SessionID: session.ID, InstanceID: 1, InstanceUUID: "u", BatchNo: 1,
		State: model.ManagedPushTargetApplied, DesiredVersion: 7,
		PushedPayloadJSON: `{"configVersion":7,"baseVersion":0,"values":{"collect-period-ticks":100}}`,
	}
	require.NoError(t, db.Create(&target).Error)

	// 回滚到一个「源目标从未下发过」的会话时，载荷为空 → 按现场重新合并（合理降级）。
	var reloaded model.ManagedPushTarget
	require.NoError(t, db.First(&reloaded, target.ID).Error)
	assert.NotEmpty(t, reloaded.PushedPayloadJSON,
		"下发过的目标必须留下载荷原文，否则下一次回滚无内容可重推")
}
