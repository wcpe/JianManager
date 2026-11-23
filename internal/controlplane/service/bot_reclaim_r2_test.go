package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// callCount 返回快照来源被调用的次数（N3 单拍复用断言用）。
func (f *fakeBotReclaimFleet) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// countingBotReclaimCapacity 统计节点世代查询次数（N7 单拍复用断言用）。
type countingBotReclaimCapacity struct {
	mu     sync.Mutex
	epochs map[uint]BotReclaimNodeEpoch
	err    error
	calls  int
}

func (c *countingBotReclaimCapacity) NodeEpochs(context.Context) (map[uint]BotReclaimNodeEpoch, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.epochs, c.err
}

func (c *countingBotReclaimCapacity) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// N1：从未上报（last_seen_at 为空）的 connecting Bot 必须受 createdAt 上限约束，
// 否则「create 已被接受但从未上报」的 Bot 永久豁免 empty_epoch，容量永久缺一格。
func TestBotReclaim_NeverReportedConnectingBotAgedOut(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	executor := node.ID
	aging := now.Add(-botReclaimConnectingFirstSeenWindow - time.Minute)
	// 僵尸：超上限且 Worker 实存中没有它。
	zombie := newReclaimBotCreatedAt(t, db, instance.ID, &session.ID, &executor, "b-zombie",
		model.BotStatusConnecting, "", 0, nil, aging)
	// 慢节点首次登录：仍在上限内，不判失效。
	newReclaimBotCreatedAt(t, db, instance.ID, &session.ID, &executor, "b-slow",
		model.BotStatusConnecting, "", 0, nil, now.Add(-time.Minute))
	// 已超上限但 Worker 实存仍持有：按 F1 口径视为健康，不判失效。
	alive := newReclaimBotCreatedAt(t, db, instance.ID, &session.ID, &executor, "b-alive",
		model.BotStatusConnecting, "", 0, nil, aging)

	svc := NewBotReclaimService(db, fakeSettings{}, fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
	}}, nil, nil)
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(3, alive.UUID),
	}})
	svc.SetNow(func() time.Time { return now })

	stale, err := svc.detectStale(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, stale, 1, "只有实存中不存在的僵尸 connecting Bot 可判失效")
	require.Equal(t, zombie.ID, stale[0].bot.ID)
	require.Equal(t, BotReclaimStaleEmptyEpoch, stale[0].reason)
}

// N2：CP 账本显示 error 但 Worker 实存仍健康持有的 Bot，不得被清掉世代与 last_event_seq。
// 旧的实现在 presence 过滤前无条件重置，导致去重基线被清空却不重派发，迟到的旧世代事件可翻状态。
func TestBotReclaim_RefillDoesNotResetBotPresentInWorker(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSession(t, db, node, instance, 2)
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", session.ID).Updates(map[string]any{
		"status": model.BotStatusError, "worker_epoch": "shard-old", "worker_epoch_generation": 7, "last_event_seq": 42,
	}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(3, expected[0].UUID, expected[1].UUID),
	}})

	require.NoError(t, svc.refillSession(context.Background(), session))
	require.Empty(t, dispatcher.Calls(), "Worker 实存中仍在的 Bot 不得重复派发")

	var bots []model.Bot
	require.NoError(t, db.Where("stress_session_id = ?", session.ID).Order("id ASC").Find(&bots).Error)
	require.Len(t, bots, 2)
	for _, bot := range bots {
		require.Equal(t, "shard-old", bot.WorkerEpoch, "不得清空健全 Bot 的世代基线")
		require.EqualValues(t, 7, bot.WorkerEpochGeneration)
		require.EqualValues(t, 42, bot.LastEventSeq)
		require.EqualValues(t, 1, bot.DesiredStateGeneration, "未派发不得推进世代")
	}
}

// N2 对照：实存中确实缺失时仍须重置基线并派发。
func TestBotReclaim_RefillResetsBotAbsentInWorker(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSession(t, db, node, instance, 2)
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", session.ID).Updates(map[string]any{
		"status": model.BotStatusError, "worker_epoch": "shard-old", "worker_epoch_generation": 7, "last_event_seq": 42,
	}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(3),
	}})

	require.NoError(t, svc.refillSession(context.Background(), session))
	require.NotEmpty(t, dispatcher.Calls())

	var bot model.Bot
	require.NoError(t, db.First(&bot, expected[0].ID).Error)
	require.Empty(t, bot.WorkerEpoch, "确实缺失的 Bot 须重建世代基线")
	require.EqualValues(t, 0, bot.WorkerEpochGeneration)
	require.EqualValues(t, 0, bot.LastEventSeq)
}

// N4：同一 Bot 在 Worker 1h 幂等缓存窗口内再次掉线时，重派发必须产生新的幂等键
// （否则 assignment 确定性 + 幂等缓存去重 → 补足被吞成 no-op）。
func TestBotReclaim_RefillRedispatchAfterSecondDropBypassesDedup(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, _ := buildReclaimRefillSession(t, db, node, instance, 2)
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", session.ID).
		Updates(map[string]any{"status": model.BotStatusError}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	// Worker 实存中已无这些 Bot（掉线后被 Worker 清掉）。
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(3),
	}})
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.refillSession(context.Background(), session))
	first := dispatcher.Calls()
	require.Len(t, first, 1)
	firstKey := first[0].request.IdempotencyKey
	firstGeneration := first[0].request.Assignments[0].Generation
	require.NotEmpty(t, firstKey)

	// 冷却窗 + 退避窗口过后（仍远在 Worker 的 1h 幂等缓存窗口内），同一 Bot 再次掉线。
	now = now.Add(botReclaimRearmCooldown + time.Minute)
	require.NoError(t, svc.refillSession(context.Background(), session))
	calls := dispatcher.Calls()
	require.Len(t, calls, 2, "再次掉线必须重新派发")
	second := calls[len(calls)-1]
	require.NotEqual(t, firstKey, second.request.IdempotencyKey, "重派发必须产生新的幂等键")
	require.Greater(t, second.request.Assignments[0].Generation, firstGeneration,
		"重派发的 assignment 须携带更高的 desired generation")
}

// buildReclaimRefillSessionSharded 构造多分片（每片 ≤ maxBotLoadBatchSize）的 running 会话与 Bot 行，
// 用于覆盖 target > 单批上限（50）时 N5 的缺口取整口径。
func buildReclaimRefillSessionSharded(t *testing.T, db *gorm.DB, node *model.Node, instance *model.Instance, shards, perShard int) (*model.BotStressSession, []model.Bot) {
	t.Helper()
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "refill-sharded", NamePrefix: "load", BotCount: shards * perShard,
		Status: model.BotStressSessionRunning, Config: `{"server":"127.0.0.1","port":25565}`,
	}
	require.NoError(t, db.Create(session).Error)
	session.Instance = *instance
	connectStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := BotLoadAllocationPlan{RunID: session.ID, RunUUID: session.UUID, TargetBots: shards * perShard}
	batches := make(map[int]model.BotLoadBatch, shards)
	for ordinal := 1; ordinal <= shards; ordinal++ {
		batchUUID := fmt.Sprintf("shard-%d", ordinal)
		idempotencyKey := fmt.Sprintf("k-%d", ordinal)
		plan.Allocations = append(plan.Allocations, BotLoadAllocation{
			BatchID: batchUUID, Ordinal: ordinal, ExecutorNodeID: node.ID, ExecutorNodeUUID: node.UUID,
			PlannedCount: perShard, ConnectStartAt: connectStart, ConnectIntervalMS: 100, IdempotencyKey: idempotencyKey,
		})
		batch := &model.BotLoadBatch{
			UUID: batchUUID, StressSessionID: session.ID, ExecutorNodeID: node.ID, Ordinal: ordinal,
			PlannedCount: perShard, State: model.BotLoadBatchPlanned, IdempotencyKey: idempotencyKey,
			ConnectStartAt: connectStart, ConnectIntervalMS: 100,
		}
		require.NoError(t, db.Create(batch).Error)
		batches[ordinal] = *batch
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, db.Model(session).Update("allocation_plan", string(raw)).Error)
	session.AllocationPlan = string(raw)

	config, err := parseBotLoadConnectionConfig(session.Config)
	require.NoError(t, err)
	prepared := &botLoadStartPreparation{session: session, plan: &plan, config: config}
	expected, err := expectedBotLoadBots(prepared, batches)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return materializeBotLoadBots(tx, session.ID, expected)
	}))
	return session, expected
}

// N5：大 target 下「单台缺失」不再被比例阈值永久拦下（1/60 < 2%）。
func TestBotReclaim_RefillSingleMissingBotAtLargeTarget(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSessionSharded(t, db, node, instance, 2, 30)
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ? AND id <> ?", session.ID, expected[0].ID).
		Updates(map[string]any{"status": model.BotStatusConnected}).Error)
	require.NoError(t, db.Model(&model.Bot{}).Where("id = ?", expected[0].ID).
		Updates(map[string]any{"status": model.BotStatusError}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)

	require.NoError(t, svc.refillSession(context.Background(), session))
	require.NotEmpty(t, dispatcher.Calls(), "target=60 下缺失 1 台也须补足")
}

// N5 对照：小缺口首拍仍被阈值抑制（保持 F4 抑制抖动的语义），持续多拍后才强制补足。
func TestBotReclaim_RefillSmallGapPersistsThenRefills(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSessionSharded(t, db, node, instance, 2, 50)
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ? AND id <> ?", session.ID, expected[0].ID).
		Updates(map[string]any{"status": model.BotStatusConnected}).Error)
	require.NoError(t, db.Model(&model.Bot{}).Where("id = ?", expected[0].ID).
		Updates(map[string]any{"status": model.BotStatusError}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.refillSession(context.Background(), session))
	require.Empty(t, dispatcher.Calls(), "小缺口首拍不得补足")

	// 缺口持续 N 拍仍未收敛 → 强制补足（1/100 小于比例阈值也不该永久缺一格）。
	now = now.Add(time.Minute)
	require.NoError(t, svc.refillSession(context.Background(), session))
	require.NotEmpty(t, dispatcher.Calls(), "缺口持续多拍后须补足")
}

// N6：0 派发（Worker 实存中已存在）不得烧掉退避预算；已结束会话的进程内状态须被裁剪。
func TestBotReclaim_ZeroDispatchKeepsBudgetAndPrunesEndedSessions(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	planSession, expected := buildReclaimRefillSession(t, db, node, instance, 1)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	svc.SetFleetSnapshotSource(&fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(3, expected[0].UUID),
	}})

	// Bot 已在 Worker 实存中 → 0 派发。
	require.NoError(t, svc.refillSession(context.Background(), planSession))
	require.Empty(t, dispatcher.Calls())

	svc.guardMu.Lock()
	require.Empty(t, svc.refillAttempts, "0 派发不得消耗退避预算")
	svc.refillAttempts = map[uint]botReclaimRefillState{session.ID + 999: {attempts: 3}}
	svc.smallGapStreak = map[uint]int{session.ID + 999: 2}
	svc.guardMu.Unlock()

	require.NoError(t, svc.refillRunningSessions(context.Background()))

	svc.guardMu.Lock()
	defer svc.guardMu.Unlock()
	require.NotContains(t, svc.refillAttempts, session.ID+999, "已结束会话的退避状态须被裁剪")
	require.NotContains(t, svc.smallGapStreak, session.ID+999, "已结束会话的小缺口状态须被裁剪")
}

// N3/N7：单拍内节点世代与 Fleet 快照各只取一次，供判定/回收/补足三阶段复用。
func TestBotReclaim_SweepReusesNodeView(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	// 失效候选放在「无服务端计划」的会话里：判定阶段会取该节点实存快照，而补足侧不会重新武装它，
	// 避免冷却窗把第二拍的确认/处置挡掉。
	staleBot := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-stale",
		model.BotStatusError, "shard-old", 1, &old)
	// 另一路：带服务端计划的 running 会话，补足阶段也需要同节点快照（应命中共拍缓存）。
	buildReclaimRefillSession(t, db, node, instance, 2)

	capacity := &countingBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, NodeUUID: node.UUID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
	}}
	fleet := &fakeBotReclaimFleet{snapshots: map[string]*workerpb.GetBotFleetSnapshotResponse{
		node.UUID: fleetSnapshot(3),
	}}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, &botLoadExecutionDispatcher{}, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, capacity, &fakeBotReclaimStopper{}, exec)
	svc.SetFleetSnapshotSource(fleet)
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.Sweep(context.Background()))
	require.Equal(t, 1, capacity.count(), "单拍内节点世代只取一次")
	require.Equal(t, 1, fleet.callCount(), "单拍内同一节点 Fleet 快照只取一次（判定与补足共用）")

	// 第二拍：确认并处置（回收路径也会读节点世代）后仍应保持单拍一次。
	now = now.Add(2*time.Minute + time.Second)
	require.NoError(t, svc.Sweep(context.Background()))
	require.Equal(t, 2, capacity.count(), "回收路径须复用本拍世代，不得每回收一台追加一次")
	require.Equal(t, 2, fleet.callCount(), "第二拍同样每拍每节点一次")

	var disposed int64
	require.NoError(t, db.Model(&model.FleetBotReclaim{}).Where("bot_id = ?", staleBot.ID).
		Where("status = ?", model.FleetBotReclaimDisposed).Count(&disposed).Error)
	require.EqualValues(t, 1, disposed)
}

// N7 辅助：nodeEpochs 在无容量来源时返回空视图而非报错（保持既有语义）。
func TestBotReclaim_NodeEpochsWithoutCapacitySource(t *testing.T) {
	db, _, _, _ := newBotReclaimHarness(t)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, nil)
	epochs, err := svc.nodeEpochs(context.Background())
	require.NoError(t, err)
	require.Empty(t, epochs)

	var bot model.Bot
	require.EqualValues(t, 0, svc.currentNodeGeneration(context.Background(), &bot))
	require.EqualValues(t, 0, svc.currentNodeGeneration(context.Background(), nil))
}

// N7：容量快照失败也占位本拍缓存——同一拍内回收多台 Bot 不得逐台重打一次失败 RPC。
func TestBotReclaim_FailedEpochLookupCachedPerSweep(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	executor := node.ID
	capacity := &countingBotReclaimCapacity{err: context.DeadlineExceeded}
	stop := &fakeBotReclaimStopper{}
	svc := NewBotReclaimService(db, fakeSettings{}, capacity, stop, nil)
	svc.SetNow(func() time.Time { return now })

	for i := 0; i < 2; i++ {
		bot := newReclaimBot(t, db, instance.ID, &session.ID, &executor,
			fmt.Sprintf("b-confirmed-%d", i), model.BotStatusError, "e-old", 1, nil)
		require.NoError(t, db.Create(&model.FleetBotReclaim{
			BotID: bot.ID, BotUUID: bot.UUID, NodeID: node.ID, SessionID: session.ID, BatchID: 0,
			FleetOwned: true, StaleReason: BotReclaimStaleEpochMismatch, Status: model.FleetBotReclaimConfirmed,
			FirstSeenAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Hour),
		}).Error)
	}

	require.NoError(t, svc.Sweep(context.Background()))
	require.Equal(t, 1, capacity.count(), "世代查询失败也只在本拍尝试一次")
	require.Equal(t, 2, stop.count(), "已确认的失效 Bot 仍须逐台下发停用")
}

// 守护：refillMinGap 的取整口径（N5）。
func TestRefillMinGapRounding(t *testing.T) {
	cases := map[int]int{0: 1, 1: 1, 50: 1, 51: 1, 60: 1, 100: 2, 150: 3, 1000: 20}
	for target, want := range cases {
		require.Equal(t, want, refillMinGap(target), "target=%d", target)
	}
}
