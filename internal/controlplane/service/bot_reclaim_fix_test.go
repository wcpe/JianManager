package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeBotReclaimFleet 固定 Worker Fleet 实存快照（F1/F3 对账真源）。
type fakeBotReclaimFleet struct {
	mu        sync.Mutex
	snapshots map[string]*workerpb.GetBotFleetSnapshotResponse
	err       error
	calls     int
}

func (f *fakeBotReclaimFleet) GetBotFleetSnapshot(_ context.Context, nodeUUID, _ string) (*workerpb.GetBotFleetSnapshotResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if snapshot, ok := f.snapshots[nodeUUID]; ok {
		return snapshot, nil
	}
	return &workerpb.GetBotFleetSnapshotResponse{}, nil
}

func fleetSnapshot(capacityGeneration int64, botUUIDs ...string) *workerpb.GetBotFleetSnapshotResponse {
	out := &workerpb.GetBotFleetSnapshotResponse{CapacityGeneration: capacityGeneration}
	for _, id := range botUUIDs {
		out.Bots = append(out.Bots, &workerpb.BotRuntimeSnapshot{BotUuid: id})
	}
	return out
}

// newReclaimBotCreatedAt 创建 Bot 并显式控制 createdAt（F2 首拍时间窗测试用）。
func newReclaimBotCreatedAt(t *testing.T, db *gorm.DB, instanceID uint, sessionID *uint, executor *uint,
	name string, status model.BotStatus, epoch string, generation int64, lastSeen *time.Time, createdAt time.Time) *model.Bot {
	t.Helper()
	bot := &model.Bot{
		InstanceID: instanceID, StressSessionID: sessionID, ExecutorNodeID: executor,
		Name: name, Status: status, DesiredState: model.BotDesiredRunning,
		DesiredStateGeneration: 1, WorkerEpoch: epoch, WorkerEpochGeneration: generation,
		ConfigHash: "hash", LastSeenAt: lastSeen, CreatedAt: createdAt,
	}
	require.NoError(t, db.Create(bot).Error)
	return bot
}

// F1：分片世代分叉但 Bot 仍在 Worker 实存中 → 不得被判失败（避免健康/瞬时断线 Bot 误回收）。
func TestBotReclaim_EpochDivergenceHealthyBotKept(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	// 节点聚合世代 = 5（10 分片中某分片独立重启后 max），Bot 记录其所属分片世代 = 1。
	healthy := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-healthy", model.BotStatusError, "shard-1", 1, &old)

	svc := NewBotReclaimService(db, fakeSettings{}, fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "shard-0:10", WorkerEpochGeneration: 5},
	}}, nil, nil)
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(9, healthy.UUID),
	}})
	svc.SetNow(func() time.Time { return now })

	stale, err := svc.detectStale(context.Background(), now)
	require.NoError(t, err)
	require.Empty(t, stale, "分片世代分叉但 Bot 仍在 Worker 实存中，不得判失效")
}

// F1 对照：同世代落后但 Worker 已不持有该 Bot → 判失败并回收。
func TestBotReclaim_EpochDivergenceAbsentBotReclaimed(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	dead := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-dead", model.BotStatusError, "shard-1", 1, &old)

	svc := NewBotReclaimService(db, fakeSettings{}, fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "shard-0:10", WorkerEpochGeneration: 5},
	}}, nil, nil)
	// Worker 实存中已无该 Bot。
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(9),
	}})
	svc.SetNow(func() time.Time { return now })

	stale, err := svc.detectStale(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.Equal(t, BotReclaimStaleEpochMismatch, stale[0].reason)
	require.Equal(t, dead.ID, stale[0].bot.ID)
}

// F1 兜底：快照不可用 + 多分片（epoch 带 ":N" 后缀）→ 无法对齐分片世代，保守不判失效。
func TestBotReclaim_MultiShardWithoutSnapshotIsConservative(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-maybe", model.BotStatusError, "shard-1", 1, &old)

	svc := NewBotReclaimService(db, fakeSettings{}, fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "shard-0:10", WorkerEpochGeneration: 5},
	}}, nil, nil)
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{err: context.DeadlineExceeded})
	svc.SetNow(func() time.Time { return now })

	stale, err := svc.detectStale(context.Background(), now)
	require.NoError(t, err)
	require.Empty(t, stale, "多分片 + 快照不可用时应保守不判失效")
}

// F2：新 Bot 首拍（未超新鲜度窗口、从未上报）不判失效，消除 stop+create 振荡。
func TestBotReclaim_NewBotFirstSweepNotReclaimed(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	executor := node.ID
	// 刚创建、从未上报（last_seen 为空、epoch 为空）的 connecting Bot。
	newReclaimBotCreatedAt(t, db, instance.ID, &session.ID, &executor, "b-new", model.BotStatusConnecting,
		"", 0, nil, now.Add(-5*time.Second))

	svc := NewBotReclaimService(db, fakeSettings{}, fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
	}}, nil, nil)
	svc.SetNow(func() time.Time { return now })

	stale, err := svc.detectStale(context.Background(), now)
	require.NoError(t, err)
	require.Empty(t, stale, "新 Bot 首拍不得判失效")

	// 对照：同一判据但 Bot 已存在超窗口（createdAt 早于 cutoff）→ 判 empty_epoch。
	newReclaimBotCreatedAt(t, db, instance.ID, &session.ID, &executor, "b-old", model.BotStatusDisconnected,
		"", 0, nil, now.Add(-10*time.Minute))
	stale, err = svc.detectStale(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.Equal(t, BotReclaimStaleEmptyEpoch, stale[0].reason)
}

// F2：重新武装冷却窗内的 Bot 不立即再次判定失效（消除 stop+create 振荡）。
func TestBotReclaim_RearmCooldownSuppressesReclaim(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	bot := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-rearmed", model.BotStatusError, "e-old", 1, &old)

	svc := NewBotReclaimService(db, fakeSettings{}, fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
	}}, nil, nil)
	svc.SetNow(func() time.Time { return now })
	svc.noteRearmed([]string{bot.UUID}, now.Add(-time.Minute))

	stale, err := svc.detectStale(context.Background(), now)
	require.NoError(t, err)
	require.Empty(t, stale, "冷却窗内的 Bot 不得立即再次判失效")

	// 冷却窗过期后可再次判定。
	svc.SetNow(func() time.Time { return now.Add(botReclaimRearmCooldown + time.Minute) })
	stale, err = svc.detectStale(context.Background(), now.Add(botReclaimRearmCooldown+time.Minute))
	require.NoError(t, err)
	require.Len(t, stale, 1)
}

// F3：补足 assignment 确定性（幂等键稳定），同一 ordinal 在 refill 与 reconcile 两条路径
// 构建出完全一致的请求 identity → Worker 幂等去重，不会重复派发。
func TestBotReclaim_RefillAssignmentDeterministic(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSession(t, db, node, instance, 2)
	exec := NewBotLoadExecutionService(db, nil, nil, nil, &botLoadExecutionDispatcher{}, nil, nil)
	bot := expected[0]

	a1, err := exec.rebuildRunningAssignment(context.Background(), session, &bot)
	require.NoError(t, err)
	a2, err := exec.rebuildRunningAssignment(context.Background(), session, &bot)
	require.NoError(t, err)
	require.NotZero(t, a1.ConnectNotBeforeUnixMs)
	require.Equal(t, a1.ConnectNotBeforeUnixMs, a2.ConnectNotBeforeUnixMs, "补足 assignment 必须确定性")

	var batch model.BotLoadBatch
	require.NoError(t, db.Where("stress_session_id = ?", session.ID).First(&batch).Error)
	require.Equal(t, batch.ConnectStartAt.UnixMilli(), a1.ConnectNotBeforeUnixMs, "应与初始派发口径一致：批次 ConnectStartAt + ordinal*interval")

	const generation = int64(11)
	refillReq := buildBotLoadReconcileRequest(session.UUID, generation, []botLoadReconcileItem{{assignment: a1, bot: &bot, mode: botLoadReconcileRunning}})
	reconcileReq := buildBotLoadReconcileRequest(session.UUID, generation, []botLoadReconcileItem{{assignment: a2, bot: &bot, mode: botLoadReconcileRunning}})
	require.Equal(t, refillReq.IdempotencyKey, reconcileReq.IdempotencyKey, "同一 ordinal 的 refill/reconcile 幂等键必须一致")
	require.Equal(t, refillReq.BatchId, reconcileReq.BatchId)
}

// F3：Worker 实存中已有该 Bot → 补足不重复派发；同时 0 派发须留审计（F6）。
func TestBotReclaim_RefillSkipsBotsPresentInSnapshot(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSession(t, db, node, instance, 2)
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", session.ID).
		Updates(map[string]any{"status": model.BotStatusError}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(3, expected[0].UUID, expected[1].UUID),
	}})
	svc.SetAudit(NewAuditService(db))

	require.NoError(t, svc.refillSession(context.Background(), session))
	require.Empty(t, dispatcher.Calls(), "Worker 实存中已有的 Bot 不得重复派发")

	var refillAudit int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "bot_reclaim.refill").Count(&refillAudit).Error)
	require.EqualValues(t, 1, refillAudit, "0 派发路径也须留审计")
}

// F4：缺口低于比例阈值时不补足（抑制抖动）。
// 注：target=100 需 2 个批次（单批上限 50），否则计划本身不合法、补足会在更早处退出，测不到阈值。
func TestBotReclaim_RefillGapThresholdSuppressesTinyGap(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSessionSharded(t, db, node, instance, 2, 50)
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ? AND id <> ?", session.ID, expected[0].ID).
		Updates(map[string]any{"status": model.BotStatusConnected}).Error)
	require.NoError(t, db.Model(&model.Bot{}).Where("id = ?", expected[0].ID).
		Updates(map[string]any{"status": model.BotStatusError}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	require.NoError(t, svc.refillSession(context.Background(), session))
	require.Empty(t, dispatcher.Calls(), "缺口低于阈值（1/100 < 2%）时不得补足")
}

// F4：补足指数退避——退避窗内不得无条件重发，窗口过后可重试。
func TestBotReclaim_RefillBackoffPreventsUnconditionalResend(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, _ := buildReclaimRefillSession(t, db, node, instance, 4)
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", session.ID).
		Updates(map[string]any{"status": model.BotStatusError}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.refillSession(context.Background(), session))
	first := len(dispatcher.Calls())
	require.Greater(t, first, 0)

	require.NoError(t, svc.refillSession(context.Background(), session))
	require.Equal(t, first, len(dispatcher.Calls()), "退避窗内不得重发")

	now = now.Add(botReclaimRearmCooldown + time.Second)
	require.NoError(t, svc.refillSession(context.Background(), session))
	require.Greater(t, len(dispatcher.Calls()), first, "退避与冷却窗过期后应可重试")
}

// F8：回收须把 worker_epoch_generation 抬升到节点当前世代，丢弃迟到的旧世代事件。
func TestBotReclaim_DisposeRaisesWorkerEpochGeneration(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	bot := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-stale", model.BotStatusError, "e-old", 1, &old)

	stop := &fakeBotReclaimStopper{}
	svc := NewBotReclaimService(db,
		fakeSettings{SettingKeyBotReclaimGracePeriod: "2m", SettingKeyBotReclaimAuto: "true"},
		fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
			node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
		}}, stop, nil)
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.Sweep(context.Background()))
	now = now.Add(2*time.Minute + time.Second)
	require.NoError(t, svc.Sweep(context.Background()))

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, model.BotStatusStopped, loaded.Status)
	require.Empty(t, loaded.WorkerEpoch)
	require.EqualValues(t, 5, loaded.WorkerEpochGeneration, "回收后须抬升世代以丢弃迟到旧世代事件")
}

// F6：Bot 行已不存在（分片早已清掉）的终态路径也须写审计。
func TestBotReclaim_DisposeAuditsMissingBotRow(t *testing.T) {
	db, node, _, _ := newBotReclaimHarness(t)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, &fakeBotReclaimStopper{}, nil)
	svc.SetAudit(NewAuditService(db))

	rec := &model.FleetBotReclaim{
		BotID: 424242, BotUUID: uuid.New().String(), NodeID: node.ID, FleetOwned: true,
		StaleReason: BotReclaimStaleEmptyEpoch, Status: model.FleetBotReclaimConfirmed,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	require.NoError(t, db.Create(rec).Error)
	require.NoError(t, svc.disposeOne(context.Background(), rec, "auto", 0, ""))
	require.Equal(t, model.FleetBotReclaimDisposed, rec.Status)

	var n int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "bot_reclaim.dispose_auto").Count(&n).Error)
	require.EqualValues(t, 1, n, "Bot 行不存在的终态路径也须审计")
}
