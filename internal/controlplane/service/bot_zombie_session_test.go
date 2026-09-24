package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 本文件复现并守护「僵尸压测会话」缺陷（FR-472）。
//
// 现场事实（2026-09-24 真机）：CP 重启/Worker 死亡后，会话的生命周期终结路径（Stop 调用）丢失，
// 于是会话永久停留在 status=running。`refillRunningSessions` 无条件捞取全部 running 会话做补足，
// 对 3 天前的僵尸会话反复重建期望行并失败——每拍失败都落在事务里，产生持续写盘。
//
// 进程内退避（refillAttempts map）无法兜住：它在 CP 每次重启后清零，重启即触发一轮全量重试。
// 因此僵尸判定必须落在**数据库可见的事实**上，而不是进程内状态。

// newZombieSessionHarness 构造「僵尸会话」场景：会话 status=running，但其全部 Bot 都已 disposed
// 且 desired_state 已非 running（即平台侧已确认它们不该再跑）。
func newZombieSessionHarness(t *testing.T) (*gorm.DB, *model.Node, *model.Instance, *model.BotStressSession) {
	t.Helper()
	db, node, instance, session := newBotReclaimHarness(t)

	// 会话带一份可解析的启动分配计划，使 refillSession 不会因「无计划」而提前返回：
	// 这正是现场情形——会话有计划、有 batch、计划内 Bot 全已回收，于是每拍都走到 materialize 并失败。
	plan := map[string]any{
		"batches": []map[string]any{
			{"ordinal": 0, "plannedCount": 2},
		},
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, db.Model(session).Updates(map[string]any{
		"allocation_plan": string(raw),
		"config":          `{"server":"127.0.0.1","port":25565,"auth":"offline"}`,
		"last_error":      "",
	}).Error)

	for i := 0; i < 2; i++ {
		require.NoError(t, db.Create(&model.BotLoadBatch{
			StressSessionID: session.ID, Ordinal: 0, PlannedCount: 2,
		}).Error)
		break
	}
	return db, node, instance, session
}

// TestZombieSession_RefillDoesNotLoopForever 是缺陷复现：僵尸会话不应在每一拍都被反复补足。
//
// 断言的是**可观察的写放大**：连续多拍巡检后，僵尸会话不应仍处于 running 并被继续当作补足目标。
func TestZombieSession_RefillDoesNotLoopForever(t *testing.T) {
	db, node, _, session := newZombieSessionHarness(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	// 会话创建于 3 天前且此后无任何进展——现场僵尸会话的典型年龄。
	require.NoError(t, db.Model(session).Updates(map[string]any{
		"created_at": now.Add(-72 * time.Hour),
		"updated_at": now.Add(-72 * time.Hour),
	}).Error)

	capacity := &fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "shard-0:7", WorkerEpochGeneration: 7},
	}}
	svc := NewBotReclaimService(db, nil, capacity, &fakeBotReclaimStopper{}, NewBotLoadExecutionService(db, nil, nil, nil, nil, nil, nil))
	svc.SetNow(func() time.Time { return now })

	// 跑 3 拍：进程内退避允许前若干次尝试，足以让僵尸会话被重复选中。
	for i := 0; i < 3; i++ {
		require.NoError(t, svc.Sweep(context.Background()))
		now = now.Add(time.Minute)
	}

	var fresh model.BotStressSession
	require.NoError(t, db.First(&fresh, session.ID).Error)

	// 核心断言：僵尸会话必须已被收敛为终态，而不是仍留在 running 被无限补足。
	require.NotEqual(t, model.BotStressSessionRunning, fresh.Status,
		"长期无在线 Bot 的僵尸会话必须被收敛为终态，否则每拍都会重建期望行并失败（写放大）")
}

// TestZombieSession_ConvergenceIsAudited 守护 FR-472 的审计要求：收敛动作必须留痕。
func TestZombieSession_ConvergenceIsAudited(t *testing.T) {
	db, node, _, session := newZombieSessionHarness(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Model(session).Updates(map[string]any{
		"created_at": now.Add(-72 * time.Hour),
		"updated_at": now.Add(-72 * time.Hour),
	}).Error)

	capacity := &fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "shard-0:7", WorkerEpochGeneration: 7},
	}}
	svc := NewBotReclaimService(db, nil, capacity, &fakeBotReclaimStopper{}, NewBotLoadExecutionService(db, nil, nil, nil, nil, nil, nil))
	svc.SetNow(func() time.Time { return now })
	// 审计服务是收敛留痕的前提；不注入时 RecordResultSafe 无处落库（生产装配必然注入）。
	svc.SetAudit(NewAuditService(db))

	require.NoError(t, svc.Sweep(context.Background()))

	var count int64
	require.NoError(t, db.Model(&model.AuditLog{}).
		Where("action = ?", "bot_reclaim.session_reaped").Count(&count).Error)
	require.Greater(t, count, int64(0), "僵尸会话收敛必须写审计")
}

// TestZombieSession_HealthySessionUntouched 是反向守护：有在线 Bot 的会话绝不能被误收敛。
func TestZombieSession_HealthySessionUntouched(t *testing.T) {
	db, node, instance, session := newZombieSessionHarness(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Model(session).Updates(map[string]any{
		"created_at": now.Add(-72 * time.Hour),
		"updated_at": now.Add(-72 * time.Hour),
	}).Error)

	// 一台 online 的 Bot —— 会话因此是活的。
	seen := now.Add(-10 * time.Second)
	require.NoError(t, db.Create(&model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, ExecutorNodeID: &node.ID,
		Name: "load_aaa_000", Status: model.BotStatusConnected, DesiredState: model.BotDesiredRunning,
		DesiredStateGeneration: 1, WorkerEpoch: "7", WorkerEpochGeneration: 7,
		ConfigHash: "hash", LastSeenAt: &seen,
	}).Error)

	capacity := &fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "shard-0:7", WorkerEpochGeneration: 7},
	}}
	svc := NewBotReclaimService(db, nil, capacity, &fakeBotReclaimStopper{}, NewBotLoadExecutionService(db, nil, nil, nil, nil, nil, nil))
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.Sweep(context.Background()))

	var fresh model.BotStressSession
	require.NoError(t, db.First(&fresh, session.ID).Error)
	require.Equal(t, model.BotStressSessionRunning, fresh.Status,
		"仍有在线 Bot 的会话不得被收敛——收敛判定必须只针对确实无在线 Bot 的会话")
}
