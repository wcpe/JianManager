package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeBotReclaimCapacity 固定执行节点世代视图。
type fakeBotReclaimCapacity struct {
	epochs map[uint]BotReclaimNodeEpoch
	err    error
}

func (f fakeBotReclaimCapacity) NodeEpochs(context.Context) (map[uint]BotReclaimNodeEpoch, error) {
	return f.epochs, f.err
}

// fakeBotReclaimStopper 记录停用下发，可注入 skipped/错误。
type fakeBotReclaimStopper struct {
	mu      sync.Mutex
	calls   []string
	assigns []*workerpb.BotAssignment
	skipped bool
	err     error
	callN   int
}

func (f *fakeBotReclaimStopper) StopFleetBot(_ context.Context, nodeUUID string, assignment *workerpb.BotAssignment) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callN++
	f.calls = append(f.calls, nodeUUID)
	f.assigns = append(f.assigns, assignment)
	return f.skipped, f.err
}

func (f *fakeBotReclaimStopper) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callN
}

func newBotReclaimHarness(t *testing.T) (*gorm.DB, *model.Node, *model.Instance, *model.BotStressSession) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "reclaim.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{},
		&model.Bot{}, &model.FleetBotReclaim{}, &model.AuditLog{}, &model.User{},
	))

	node := &model.Node{Name: "executor", Host: "127.0.0.1", Secret: "secret", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{
		NodeID: node.ID, Name: "target", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java",
	}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "load", NamePrefix: "load", BotCount: 3,
		Status: model.BotStressSessionRunning,
	}
	require.NoError(t, db.Create(session).Error)
	return db, node, instance, session
}

func newReclaimBot(t *testing.T, db *gorm.DB, instanceID uint, sessionID *uint, executor *uint, name string,
	status model.BotStatus, epoch string, generation int64, lastSeen *time.Time) *model.Bot {
	t.Helper()
	bot := &model.Bot{
		InstanceID: instanceID, StressSessionID: sessionID, ExecutorNodeID: executor,
		Name: name, Status: status, DesiredState: model.BotDesiredRunning,
		DesiredStateGeneration: 1, WorkerEpoch: epoch, WorkerEpochGeneration: generation,
		ConfigHash: "hash", LastSeenAt: lastSeen,
	}
	require.NoError(t, db.Create(bot).Error)
	return bot
}

func TestBotReclaim_DetectStaleReasonsAndBoundaries(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	fresh := now.Add(-5 * time.Second)
	executor := node.ID

	// 1) epoch_mismatch：status=error，世代落后节点当前世代。
	mismatch := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-mismatch", model.BotStatusError, "e-old", 1, &old)
	// 2) empty_epoch：分片重启前残留（worker_epoch 为空）。
	empty := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-empty", model.BotStatusDisconnected, "", 0, &old)
	// 3) 边界：世代匹配的 error Bot 不回收。
	newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-matched", model.BotStatusError, "e-cur", 5, &old)
	// 4) 边界：connected 不回收。
	newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-connected", model.BotStatusConnected, "e-old", 1, &old)
	// 5) 边界：stopped 不回收。
	newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-stopped", model.BotStatusStopped, "e-old", 1, &old)
	// 6) 边界：last_seen 新鲜（未超新鲜度窗口）不回收。
	newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-fresh", model.BotStatusError, "e-old", 1, &fresh)
	// 7) node_missing：executor_node_id 为空。
	missing := newReclaimBot(t, db, instance.ID, &session.ID, nil, "b-nonode", model.BotStatusError, "e-old", 1, &old)

	svc := NewBotReclaimService(db, fakeSettings{}, fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
		node.ID: {NodeID: node.ID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
	}}, nil, nil)
	svc.SetNow(func() time.Time { return now })

	stale, err := svc.detectStale(context.Background(), now)
	require.NoError(t, err)
	byID := make(map[uint]string, len(stale))
	for _, item := range stale {
		byID[item.bot.ID] = item.reason
	}
	require.Equal(t, map[uint]string{
		mismatch.ID: BotReclaimStaleEpochMismatch,
		empty.ID:    BotReclaimStaleEmptyEpoch,
		missing.ID:  BotReclaimStaleNodeMissing,
	}, byID)
}

func TestBotReclaim_GraceThenAutoDisposeFleet(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	bot := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-mismatch", model.BotStatusError, "e-old", 1, &old)

	stop := &fakeBotReclaimStopper{}
	svc := NewBotReclaimService(db,
		fakeSettings{SettingKeyBotReclaimGracePeriod: "2m", SettingKeyBotReclaimAuto: "true"},
		fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
			node.ID: {NodeID: node.ID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
		}}, stop, nil)
	svc.SetAudit(NewAuditService(db))
	svc.SetNow(func() time.Time { return now })

	// 第一拍：判定失效 → pending，未处置。
	require.NoError(t, svc.Sweep(context.Background()))
	recs, err := svc.List("", false, 0)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, model.FleetBotReclaimPending, recs[0].Status)
	require.Equal(t, BotReclaimStaleEpochMismatch, recs[0].StaleReason)
	require.Equal(t, 0, stop.count())

	// 第二拍（超宽限）：confirmed → 自动处置。
	now = now.Add(2*time.Minute + time.Second)
	require.NoError(t, svc.Sweep(context.Background()))
	recs, err = svc.List("", false, 0)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, model.FleetBotReclaimDisposed, recs[0].Status)
	require.Equal(t, "auto", recs[0].DisposeMode)
	require.NotNil(t, recs[0].DisposedAt)
	require.Equal(t, 1, stop.count())
	require.Equal(t, "stopped", stop.assigns[0].DesiredState)

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, model.BotDesiredStopped, loaded.DesiredState)
	require.Equal(t, model.BotStatusStopped, loaded.Status)
	require.Empty(t, loaded.WorkerEpoch)

	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "bot_reclaim.dispose_auto").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)
}

func TestBotReclaim_V1BotNotAutoDisposed(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	// V1 手动 Bot：无 batch/session 归属。
	newReclaimBot(t, db, instance.ID, nil, &executor, "v1", model.BotStatusError, "e-old", 1, &old)

	stop := &fakeBotReclaimStopper{}
	svc := NewBotReclaimService(db,
		fakeSettings{SettingKeyBotReclaimGracePeriod: "1m", SettingKeyBotReclaimAuto: "true"},
		fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
			node.ID: {NodeID: node.ID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
		}}, stop, nil)
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.Sweep(context.Background()))
	now = now.Add(2 * time.Minute)
	require.NoError(t, svc.Sweep(context.Background()))

	recs, err := svc.List("", false, 0)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.False(t, recs[0].FleetOwned)
	require.Equal(t, model.FleetBotReclaimConfirmed, recs[0].Status, "V1 手动 Bot 不得自动回收")
	require.Equal(t, 0, stop.count())
}

func TestBotReclaim_ManualConfirmAndIdempotentStop(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	bot := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-manual", model.BotStatusError, "e-old", 1, &old)

	// Worker 已无该 Bot（skipped）→ 回收仍视为成功（幂等）。
	stop := &fakeBotReclaimStopper{skipped: true}
	svc := NewBotReclaimService(db,
		fakeSettings{SettingKeyBotReclaimGracePeriod: "1m", SettingKeyBotReclaimAuto: "false"},
		fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
			node.ID: {NodeID: node.ID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
		}}, stop, nil)
	svc.SetAudit(NewAuditService(db))
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.Sweep(context.Background()))
	now = now.Add(2 * time.Minute)
	require.NoError(t, svc.Sweep(context.Background())) // auto=false → 停在 confirmed。

	recs, _ := svc.List("", false, 0)
	require.Len(t, recs, 1)
	require.Equal(t, model.FleetBotReclaimConfirmed, recs[0].Status)
	require.Equal(t, 0, stop.count())

	out, err := svc.ConfirmReclaim(recs[0].UUID, 7, "10.0.0.1")
	require.NoError(t, err)
	require.Equal(t, model.FleetBotReclaimDisposed, out.Status)
	require.Equal(t, "manual", out.DisposeMode)
	require.Equal(t, 1, stop.count())

	// 再处置：已终态 → 幂等 no-op，不再下发。
	require.NoError(t, svc.disposeOne(context.Background(), out, "manual", 7, "10.0.0.1"))
	require.Equal(t, 1, stop.count())

	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "bot_reclaim.dispose_manual").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, model.BotDesiredStopped, loaded.DesiredState)
}

func TestBotReclaim_CancelWhenEpochRecovers(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * time.Minute)
	executor := node.ID
	bot := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-recover", model.BotStatusError, "e-old", 1, &old)

	svc := NewBotReclaimService(db,
		fakeSettings{SettingKeyBotReclaimGracePeriod: "10m", SettingKeyBotReclaimAuto: "false"},
		fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{
			node.ID: {NodeID: node.ID, Exists: true, Online: true, WorkerEpoch: "e-cur", WorkerEpochGeneration: 5},
		}}, nil, nil)
	svc.SetNow(func() time.Time { return now })

	require.NoError(t, svc.Sweep(context.Background()))
	recs, _ := svc.List("", false, 0)
	require.Len(t, recs, 1)
	require.Equal(t, model.FleetBotReclaimPending, recs[0].Status)

	// Bot 被新世代认领（世代追平）→ 下一拍跟踪取消。
	require.NoError(t, db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]any{
		"worker_epoch": "e-cur", "worker_epoch_generation": 5, "status": model.BotStatusConnected,
	}).Error)
	now = now.Add(time.Minute)
	require.NoError(t, svc.Sweep(context.Background()))

	recs, _ = svc.List("", false, 0)
	require.Len(t, recs, 1)
	require.Equal(t, model.FleetBotReclaimCancelled, recs[0].Status)
}

// buildReclaimRefillSession 构造带服务端分片计划的 running 会话并按计划落 Bot 行。
func buildReclaimRefillSession(t *testing.T, db *gorm.DB, node *model.Node, instance *model.Instance, target int) (*model.BotStressSession, []model.Bot) {
	t.Helper()
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "refill", NamePrefix: "load", BotCount: target,
		Status: model.BotStressSessionRunning, Config: `{"server":"127.0.0.1","port":25565}`,
	}
	require.NoError(t, db.Create(session).Error)
	session.Instance = *instance
	connectStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := BotLoadAllocationPlan{
		RunID: session.ID, RunUUID: session.UUID, TargetBots: target,
		Allocations: []BotLoadAllocation{{
			BatchID: "b1", Ordinal: 1, ExecutorNodeID: node.ID, ExecutorNodeUUID: node.UUID,
			PlannedCount: target, ConnectStartAt: connectStart, ConnectIntervalMS: 100, IdempotencyKey: "k1",
		}},
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, db.Model(session).Update("allocation_plan", string(raw)).Error)
	session.AllocationPlan = string(raw)

	batch := &model.BotLoadBatch{
		UUID: "b1", StressSessionID: session.ID, ExecutorNodeID: node.ID, Ordinal: 1,
		PlannedCount: target, State: model.BotLoadBatchPlanned, IdempotencyKey: "k1",
		ConnectStartAt: connectStart, ConnectIntervalMS: 100,
	}
	require.NoError(t, db.Create(batch).Error)

	config, err := parseBotLoadConnectionConfig(session.Config)
	require.NoError(t, err)
	prepared := &botLoadStartPreparation{session: session, plan: &plan, config: config}
	expected, err := expectedBotLoadBots(prepared, map[int]model.BotLoadBatch{1: *batch})
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return materializeBotLoadBots(tx, session.ID, expected)
	}))
	return session, expected
}

func TestBotReclaim_RefillMissingToPlannedCount(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSession(t, db, node, instance, 3)

	// 模拟缺失 ordinal：物理删除计划内 2 行。
	var created []model.Bot
	require.NoError(t, db.Where("stress_session_id = ?", session.ID).Order("id ASC").Find(&created).Error)
	require.Len(t, created, 3)
	require.NoError(t, db.Unscoped().Delete(&model.Bot{}, created[0].ID).Error)
	require.NoError(t, db.Unscoped().Delete(&model.Bot{}, created[1].ID).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)
	svc.SetAudit(NewAuditService(db))

	require.NoError(t, svc.refillSession(context.Background(), session))

	var count int64
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ? AND deleted_at IS NULL", session.ID).Count(&count).Error)
	require.EqualValues(t, len(expected), count, "补足应把 Bot 数恢复回 planned_count")

	var runningBots []model.Bot
	require.NoError(t, db.Where("stress_session_id = ? AND desired_state = ?", session.ID, model.BotDesiredRunning).Find(&runningBots).Error)
	require.Len(t, runningBots, len(expected))

	calls := dispatcher.Calls()
	require.NotEmpty(t, calls, "补足应下发 running assignment")
	runningAssignments := 0
	for _, call := range calls {
		for _, assignment := range call.request.Assignments {
			if assignment.DesiredState == "running" {
				runningAssignments++
			}
		}
	}
	require.GreaterOrEqual(t, runningAssignments, 1)

	var refillAudit int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "bot_reclaim.refill").Count(&refillAudit).Error)
	require.EqualValues(t, 1, refillAudit)
}

func TestBotReclaim_RefillRearmsReclaimedBot(t *testing.T) {
	db, node, instance, _ := newBotReclaimHarness(t)
	session, expected := buildReclaimRefillSession(t, db, node, instance, 2)

	// 模拟回收：其中 1 个 Bot 已被回收为 stopped 并留 disposed 记录。
	var created []model.Bot
	require.NoError(t, db.Where("stress_session_id = ?", session.ID).Order("id ASC").Find(&created).Error)
	require.Len(t, created, 2)
	require.NoError(t, db.Model(&model.Bot{}).Where("id = ?", created[0].ID).Updates(map[string]any{
		"desired_state": model.BotDesiredStopped, "status": model.BotStatusStopped, "worker_epoch": "",
	}).Error)
	require.NoError(t, db.Create(&model.FleetBotReclaim{
		BotID: created[0].ID, BotUUID: created[0].UUID, NodeID: node.ID, SessionID: session.ID,
		FleetOwned: true, StaleReason: BotReclaimStaleEpochMismatch, Status: model.FleetBotReclaimDisposed,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}).Error)

	dispatcher := &botLoadExecutionDispatcher{}
	exec := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, nil, nil)
	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, exec)

	require.NoError(t, svc.refillSession(context.Background(), session))

	var rearmed model.Bot
	require.NoError(t, db.First(&rearmed, created[0].ID).Error)
	require.Equal(t, model.BotDesiredRunning, rearmed.DesiredState, "被回收 Bot 应恢复 desired=running")

	var count int64
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ? AND deleted_at IS NULL", session.ID).Count(&count).Error)
	require.EqualValues(t, len(expected), count)
	require.NotEmpty(t, dispatcher.Calls())
}

func TestBotReclaim_ListActiveOnly(t *testing.T) {
	db, node, instance, session := newBotReclaimHarness(t)
	executor := node.ID
	active := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-active", model.BotStatusError, "e-old", 1, nil)
	done := newReclaimBot(t, db, instance.ID, &session.ID, &executor, "b-done", model.BotStatusError, "e-old", 1, nil)
	require.NoError(t, db.Create(&model.FleetBotReclaim{BotID: active.ID, BotUUID: active.UUID, SessionID: session.ID, Status: model.FleetBotReclaimPending, StaleReason: BotReclaimStaleEmptyEpoch, FirstSeenAt: time.Now(), LastSeenAt: time.Now()}).Error)
	require.NoError(t, db.Create(&model.FleetBotReclaim{BotID: done.ID, BotUUID: done.UUID, SessionID: session.ID, Status: model.FleetBotReclaimDisposed, StaleReason: BotReclaimStaleEmptyEpoch, FirstSeenAt: time.Now(), LastSeenAt: time.Now()}).Error)

	svc := NewBotReclaimService(db, fakeSettings{}, nil, nil, nil)
	activeOnly, err := svc.List("", true, 0)
	require.NoError(t, err)
	require.Len(t, activeOnly, 1)
	require.Equal(t, model.FleetBotReclaimPending, activeOnly[0].Status)

	all, err := svc.List("", false, 0)
	require.NoError(t, err)
	statuses := []string{string(all[0].Status), string(all[1].Status)}
	sort.Strings(statuses)
	require.Equal(t, []string{"disposed", "pending"}, statuses)
}

func TestBotReclaimSweeper_StopReleasesGoroutine(t *testing.T) {
	db, _, _, _ := newBotReclaimHarness(t)
	svc := NewBotReclaimService(db, fakeSettings{}, fakeBotReclaimCapacity{epochs: map[uint]BotReclaimNodeEpoch{}}, nil, nil)
	sweeper := &BotReclaimSweeper{service: svc, interval: 20 * time.Millisecond}
	sweeper.Start()
	sweeper.Start() // 幂等
	time.Sleep(50 * time.Millisecond)
	sweeper.Stop()
	sweeper.Stop() // 幂等
}
