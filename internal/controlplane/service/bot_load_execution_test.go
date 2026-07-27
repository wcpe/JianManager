package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type botLoadExecutionCapacity struct {
	mu       sync.Mutex
	snapshot *BotLoadCapacitySnapshot
	calls    int
}

func (c *botLoadExecutionCapacity) Refresh(context.Context, uint) (*BotLoadCapacitySnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	copySnapshot := *c.snapshot
	copySnapshot.NodeCapacities = append([]BotLoadNodeCapacity(nil), c.snapshot.NodeCapacities...)
	copySnapshot.ReservationLimits = cloneBotLoadCounts(c.snapshot.ReservationLimits)
	return &copySnapshot, nil
}

func (c *botLoadExecutionCapacity) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type botLoadExecutionDispatchCall struct {
	nodeUUID string
	request  *workerpb.ApplyBotBatchRequest
}

type botLoadExecutionDispatcher struct {
	mu    sync.Mutex
	calls []botLoadExecutionDispatchCall
	// scheduleCalls 记录 FR-369 ApplyBotCommandSchedules 调用。
	scheduleCalls []botLoadScheduleDispatchCall
	// cancelCalls 记录 FR-369 CancelBotCommandSchedules 调用。
	cancelCalls     []botLoadCancelDispatchCall
	handler         func(string, *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error)
	scheduleHandler func(string, *workerpb.ApplyBotCommandSchedulesRequest) (*workerpb.ApplyBotCommandSchedulesResponse, error)
}

type botLoadScheduleDispatchCall struct {
	nodeUUID string
	request  *workerpb.ApplyBotCommandSchedulesRequest
}

type botLoadCancelDispatchCall struct {
	nodeUUID string
	request  *workerpb.CancelBotCommandSchedulesRequest
}

func (d *botLoadExecutionDispatcher) ApplyBotBatch(_ context.Context, nodeUUID string, request *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
	d.mu.Lock()
	d.calls = append(d.calls, botLoadExecutionDispatchCall{nodeUUID: nodeUUID, request: proto.Clone(request).(*workerpb.ApplyBotBatchRequest)})
	handler := d.handler
	d.mu.Unlock()
	if handler != nil {
		return handler(nodeUUID, request)
	}
	return acceptedBotLoadBatchResponse(request), nil
}

func (d *botLoadExecutionDispatcher) ApplyBotCommandSchedules(_ context.Context, nodeUUID string, request *workerpb.ApplyBotCommandSchedulesRequest) (*workerpb.ApplyBotCommandSchedulesResponse, error) {
	d.mu.Lock()
	d.scheduleCalls = append(d.scheduleCalls, botLoadScheduleDispatchCall{
		nodeUUID: nodeUUID,
		request:  proto.Clone(request).(*workerpb.ApplyBotCommandSchedulesRequest),
	})
	handler := d.scheduleHandler
	d.mu.Unlock()
	if handler != nil {
		return handler(nodeUUID, request)
	}
	results := make([]*workerpb.ApplyBotCommandScheduleItemResult, 0, len(request.Items))
	for _, item := range request.Items {
		results = append(results, &workerpb.ApplyBotCommandScheduleItemResult{
			BotUuid: item.BotUuid, ScheduleRunId: item.ScheduleRunId, Disposition: "accepted",
		})
	}
	return &workerpb.ApplyBotCommandSchedulesResponse{RequestId: request.RequestId, Results: results}, nil
}

func (d *botLoadExecutionDispatcher) CancelBotCommandSchedules(_ context.Context, nodeUUID string, request *workerpb.CancelBotCommandSchedulesRequest) (*workerpb.CancelBotCommandSchedulesResponse, error) {
	d.mu.Lock()
	d.cancelCalls = append(d.cancelCalls, botLoadCancelDispatchCall{
		nodeUUID: nodeUUID,
		request:  proto.Clone(request).(*workerpb.CancelBotCommandSchedulesRequest),
	})
	d.mu.Unlock()
	results := make([]*workerpb.CancelBotCommandScheduleItemResult, 0, len(request.Items))
	for _, item := range request.Items {
		results = append(results, &workerpb.CancelBotCommandScheduleItemResult{
			BotUuid: item.BotUuid, ScheduleRunId: item.ScheduleRunId, Disposition: "accepted",
		})
	}
	return &workerpb.CancelBotCommandSchedulesResponse{RequestId: request.RequestId, Results: results}, nil
}

func (d *botLoadExecutionDispatcher) Calls() []botLoadExecutionDispatchCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]botLoadExecutionDispatchCall(nil), d.calls...)
}

func (d *botLoadExecutionDispatcher) ScheduleCalls() []botLoadScheduleDispatchCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]botLoadScheduleDispatchCall(nil), d.scheduleCalls...)
}

func (d *botLoadExecutionDispatcher) CancelCalls() []botLoadCancelDispatchCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]botLoadCancelDispatchCall(nil), d.cancelCalls...)
}

func acceptedBotLoadBatchResponse(request *workerpb.ApplyBotBatchRequest) *workerpb.ApplyBotBatchResponse {
	results := make([]*workerpb.ApplyBotBatchItemResult, 0, len(request.Assignments))
	for _, assignment := range request.Assignments {
		results = append(results, &workerpb.ApplyBotBatchItemResult{BotUuid: assignment.BotUuid, Accepted: true, Status: "accepted"})
	}
	return &workerpb.ApplyBotBatchResponse{BatchId: request.BatchId, IdempotencyKey: request.IdempotencyKey, Results: results}
}

type botLoadImmediateRunner struct{}

func (botLoadImmediateRunner) Submit(task func()) error {
	task()
	return nil
}

type botLoadExecutionSubscriptions struct {
	mu      sync.Mutex
	ensured []BotFleetSubscriptionTarget
	stopped []string
}

func (s *botLoadExecutionSubscriptions) Ensure(target BotFleetSubscriptionTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensured = append(s.ensured, target)
}

func (s *botLoadExecutionSubscriptions) Restore(targets []BotFleetSubscriptionTarget) {
	for _, target := range targets {
		s.Ensure(target)
	}
}

func (s *botLoadExecutionSubscriptions) StopSession(sessionUUID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, sessionUUID)
}

func (s *botLoadExecutionSubscriptions) Ensured() []BotFleetSubscriptionTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]BotFleetSubscriptionTarget(nil), s.ensured...)
}

func (s *botLoadExecutionSubscriptions) Stopped() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.stopped...)
}

type botLoadExecutionScenarioLifecycle struct {
	mu      sync.Mutex
	stopped []string
}

func (s *botLoadExecutionScenarioLifecycle) StopRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, runID)
}

func (s *botLoadExecutionScenarioLifecycle) Stopped() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.stopped...)
}

type botLoadQueuedRunner struct {
	mu    sync.Mutex
	tasks []func()
}

func (r *botLoadQueuedRunner) Submit(task func()) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, task)
	return nil
}

func (r *botLoadQueuedRunner) RunAll() {
	for {
		r.mu.Lock()
		if len(r.tasks) == 0 {
			r.mu.Unlock()
			return
		}
		task := r.tasks[0]
		r.tasks = r.tasks[1:]
		r.mu.Unlock()
		task()
	}
}

func (r *botLoadQueuedRunner) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tasks)
}

func (r *botLoadQueuedRunner) RunAt(index int) {
	r.mu.Lock()
	task := r.tasks[index]
	r.tasks = append(r.tasks[:index], r.tasks[index+1:]...)
	r.mu.Unlock()
	task()
}

type botLoadExecutionHarness struct {
	db           *gorm.DB
	clock        *botLoadFakeClock
	signer       *BotLoadPlanTokenSigner
	reservations *BotLoadReservationStore
	capacity     *botLoadExecutionCapacity
	dispatcher   *botLoadExecutionDispatcher
	service      *BotLoadExecutionService
	session      *model.BotStressSession
	instance     *model.Instance
	nodes        []model.Node
	plan         BotLoadAllocationPlan
	token        string
}

func newBotLoadExecutionHarness(t *testing.T, capacities []int, target int, runner BotLoadBackgroundRunner) *botLoadExecutionHarness {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "bot-load-execution.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}, &model.Bot{}, &model.BotLoadCommandCheckpoint{}, &model.BotLoadRunEvent{}))

	nodes := make([]model.Node, 0, len(capacities))
	nodeCapacities := make([]BotLoadNodeCapacity, 0, len(capacities))
	limits := make(map[uint]int, len(capacities))
	for index, available := range capacities {
		node := model.Node{Name: fmt.Sprintf("executor-%02d", index+1), Host: fmt.Sprintf("10.0.0.%d", index+1), Secret: "test-secret", Status: model.NodeStatusOnline}
		require.NoError(t, db.Create(&node).Error)
		nodes = append(nodes, node)
		generation := int64(100 + index)
		nodeCapacities = append(nodeCapacities, BotLoadNodeCapacity{
			NodeID: node.ID, NodeUUID: node.UUID, NodeName: node.Name, Online: true,
			BotWorkerReady: true, MaxBots: available, AvailableBots: available, CapacityGeneration: generation,
		})
		limits[node.ID] = available
	}
	instance := &model.Instance{NodeID: nodes[0].ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java", ServerPort: 25570}
	require.NoError(t, db.Create(instance).Error)
	// 默认附带 FR-369 命令计划快照，验证 start 派发后会 ApplyBotCommandSchedules。
	scheduleSnap := `{"commands":[{"id":"say-ready","atMs":0,"command":"/say ready {{botName}}"}],"durationMs":3000,"jitterMs":0}`
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "distributed-load", NamePrefix: "load", BotCount: target,
		Status: model.BotStressSessionPending, Behavior: "idle",
		Config:              `{"server":"mc.example.test","port":25570,"auth":"offline","version":"1.20.4"}`,
		CommandScheduleSnap: scheduleSnap,
	}
	require.NoError(t, db.Create(session).Error)

	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	planning, err := (BotLoadPlanner{}).Plan(BotLoadPlanRequest{
		RunID: session.ID, RunUUID: session.UUID, TargetBots: target,
		NodeCapacities: nodeCapacities, ConnectRatePerSecondPerNode: 5, ConnectStartAt: clock.Now(),
	})
	require.NoError(t, err)
	require.True(t, planning.Ready)
	plan := BotLoadAllocationPlan{
		RunID: session.ID, RunUUID: session.UUID, TargetBots: target,
		Allocations: planning.Allocations, CapacityGenerations: planning.CapacityGenerations,
	}
	rawPlan, err := encodeBotLoadAllocationPlan(plan)
	require.NoError(t, err)
	require.NoError(t, db.Model(session).Update("allocation_plan", rawPlan).Error)
	session.AllocationPlan = rawPlan

	signer, err := NewBotLoadPlanTokenSigner([]byte("bot-load-execution-test-secret"), clock)
	require.NoError(t, err)
	hash, err := BotLoadAllocationHash(plan.RunID, plan.TargetBots, plan.Allocations)
	require.NoError(t, err)
	token, expiresAt, err := signer.Issue(plan.RunID, hash, plan.CapacityGenerations)
	require.NoError(t, err)
	reservations := NewBotLoadReservationStore(clock, time.Minute)
	_, err = reservations.ReplaceUntil(session.ID, allocationBotLoadCounts(plan.Allocations), limits, expiresAt)
	require.NoError(t, err)
	capacity := &botLoadExecutionCapacity{snapshot: &BotLoadCapacitySnapshot{NodeCapacities: nodeCapacities, ReservationLimits: limits, UpdatedAt: clock.Now()}}
	dispatcher := &botLoadExecutionDispatcher{}
	if runner == nil {
		runner = botLoadImmediateRunner{}
	}
	service := NewBotLoadExecutionService(db, capacity, reservations, signer, dispatcher, runner, clock)
	return &botLoadExecutionHarness{
		db: db, clock: clock, signer: signer, reservations: reservations, capacity: capacity,
		dispatcher: dispatcher, service: service, session: session, instance: instance,
		nodes: nodes, plan: plan, token: token,
	}
}

func addBotLoadExecutionSession(t *testing.T, h *botLoadExecutionHarness, target int) (*model.BotStressSession, BotLoadAllocationPlan, string) {
	t.Helper()
	session := &model.BotStressSession{
		InstanceID: h.instance.ID, Name: fmt.Sprintf("distributed-load-%d", target), NamePrefix: "load", BotCount: target,
		Status: model.BotStressSessionPending, Behavior: "idle", Config: h.session.Config,
	}
	require.NoError(t, h.db.Create(session).Error)
	planning, err := (BotLoadPlanner{}).Plan(BotLoadPlanRequest{
		RunID: session.ID, RunUUID: session.UUID, TargetBots: target,
		NodeCapacities: h.capacity.snapshot.NodeCapacities, ConnectRatePerSecondPerNode: 5, ConnectStartAt: h.clock.Now(),
	})
	require.NoError(t, err)
	require.True(t, planning.Ready)
	plan := BotLoadAllocationPlan{
		RunID: session.ID, RunUUID: session.UUID, TargetBots: target,
		Allocations: planning.Allocations, CapacityGenerations: planning.CapacityGenerations,
	}
	rawPlan, err := encodeBotLoadAllocationPlan(plan)
	require.NoError(t, err)
	require.NoError(t, h.db.Model(session).Update("allocation_plan", rawPlan).Error)
	hash, err := BotLoadAllocationHash(plan.RunID, plan.TargetBots, plan.Allocations)
	require.NoError(t, err)
	token, expiresAt, err := h.signer.Issue(plan.RunID, hash, plan.CapacityGenerations)
	require.NoError(t, err)
	_, err = h.reservations.ReplaceUntil(session.ID, allocationBotLoadCounts(plan.Allocations), h.capacity.snapshot.ReservationLimits, expiresAt)
	require.NoError(t, err)
	return session, plan, token
}

func encodeBotLoadAllocationPlan(plan BotLoadAllocationPlan) (string, error) {
	raw, err := json.Marshal(plan)
	return string(raw), err
}

func setBotLoadExecutionScenario(t *testing.T, h *botLoadExecutionHarness, raw string) *ScenarioV2 {
	t.Helper()
	scenario, err := ParseScenarioV2([]byte(raw))
	require.NoError(t, err)
	snapshot, err := CanonicalScenarioSnapshot(scenario, false)
	require.NoError(t, err)
	require.NoError(t, h.db.Model(h.session).Update("scenario_snapshot", snapshot).Error)
	h.session.ScenarioSnapshot = snapshot
	return scenario
}

func assignmentRunDeadlineUnixMS(t *testing.T, assignment *workerpb.BotAssignment) int64 {
	t.Helper()
	var envelope struct {
		RunDeadlineUnixMS int64 `json:"runDeadlineUnixMs"`
	}
	require.NoError(t, json.Unmarshal([]byte(assignment.ScenarioJson), &envelope))
	return envelope.RunDeadlineUnixMS
}

func TestBotLoadExecutionStart_Creates500BotsAcrossTenBatches(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{50, 50, 50, 50, 50, 50, 50, 50, 50, 50}, 500, nil)
	setBotLoadExecutionScenario(t, h, `{"version":2,"seed":20260719,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]}]}`)

	snapshot, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.Equal(t, model.BotStressSessionRunning, snapshot.Status)
	require.Equal(t, 500, snapshot.Succeeded)
	require.Zero(t, snapshot.Failed)
	require.NotNil(t, snapshot.StartedAt)

	var batches []model.BotLoadBatch
	require.NoError(t, h.db.Order("ordinal ASC").Find(&batches).Error)
	require.Len(t, batches, 10)
	for index, batch := range batches {
		require.Equal(t, index+1, batch.Ordinal)
		require.Equal(t, 50, batch.PlannedCount)
		require.Equal(t, 50, batch.AcceptedCount)
		require.Zero(t, batch.FailedCount)
		require.Equal(t, model.BotLoadBatchRunning, batch.State)
		require.Equal(t, h.plan.Allocations[index].BatchID, batch.UUID)
		require.Equal(t, h.plan.Allocations[index].IdempotencyKey, batch.IdempotencyKey)
	}

	var bots []model.Bot
	require.NoError(t, h.db.Order("name ASC").Find(&bots).Error)
	require.Len(t, bots, 500)
	require.Equal(t, stableBotLoadBotName(h.session.NamePrefix, h.session.UUID, 1), bots[0].Name)
	require.Equal(t, stableBotLoadBotName(h.session.NamePrefix, h.session.UUID, 500), bots[499].Name)
	for _, bot := range bots {
		require.Equal(t, h.instance.ID, bot.InstanceID)
		require.NotNil(t, bot.StressSessionID)
		require.Equal(t, h.session.ID, *bot.StressSessionID)
		require.NotNil(t, bot.ExecutorNodeID)
		require.NotNil(t, bot.LoadBatchID)
		require.Equal(t, int64(1), bot.DesiredStateGeneration)
		require.Len(t, bot.ConfigHash, 64)
		require.Equal(t, h.session.Config, bot.Config)
		require.Equal(t, "all", bot.CohortKey)
		require.Equal(t, model.BotStatusConnecting, bot.Status)
	}

	calls := h.dispatcher.Calls()
	require.Len(t, calls, 10)
	generationByNode := make(map[string]int64)
	for index, node := range h.nodes {
		generationByNode[node.UUID] = int64(100 + index)
	}
	for _, call := range calls {
		require.Len(t, call.request.Assignments, 50)
		require.Equal(t, generationByNode[call.nodeUUID], call.request.ExpectedCapacityGeneration)
		for _, assignment := range call.request.Assignments {
			require.Equal(t, "running", assignment.DesiredState)
			require.Equal(t, "mc.example.test", assignment.Host)
			require.EqualValues(t, 25570, assignment.Port)
			require.Equal(t, "offline", assignment.Auth)
			require.Equal(t, h.session.UUID, assignment.SessionUuid)
			require.NotEqual(t, "connected", assignment.DesiredState)
			require.Equal(t, int64(1000), assignmentRunDeadlineUnixMS(t, assignment)-assignment.ConnectNotBeforeUnixMs)
		}
	}
	_, reserved := h.reservations.Lease(h.session.ID)
	require.False(t, reserved)

	// FR-369：每个 accepted Bot 应触发一次命令计划 Apply（可按节点批量）。
	schedCalls := h.dispatcher.ScheduleCalls()
	require.NotEmpty(t, schedCalls)
	totalSchedItems := 0
	for _, sc := range schedCalls {
		require.NotEmpty(t, sc.request.Items)
		for _, item := range sc.request.Items {
			require.Equal(t, "absolute", item.StartMode)
			require.NotEmpty(t, item.ScheduleRunId)
			require.NotEmpty(t, item.CorrelationToken)
			require.NotNil(t, item.Plan)
			require.NotEmpty(t, item.Plan.Occurrences)
			require.Contains(t, item.Plan.Occurrences[0].Command, "/say ready")
			require.Greater(t, item.ScheduleStartAtUnixMs, int64(0))
			require.Greater(t, item.RunDeadlineUnixMs, item.ScheduleStartAtUnixMs)
		}
		totalSchedItems += len(sc.request.Items)
	}
	require.Equal(t, 500, totalSchedItems)
	var ckCount int64
	require.NoError(t, h.db.Model(&model.BotLoadCommandCheckpoint{}).Count(&ckCount).Error)
	require.Equal(t, int64(500), ckCount) // 每 bot 1 occurrence
}

func TestBotLoadExecutionStart_V2ReadyTransitionsToRunning(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{10}, 10, nil)
	ready := model.BotLoadRunReady
	verdict := model.BotLoadVerdictPending
	require.NoError(t, h.db.Model(h.session).Updates(map[string]any{
		"schema_version": 2, "run_state": ready, "verdict": verdict,
	}).Error)
	h.service.SetRunIntentService(NewBotLoadRunIntentService(h.db))

	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	var saved model.BotStressSession
	require.NoError(t, h.db.First(&saved, h.session.ID).Error)
	require.NotNil(t, saved.RunState)
	require.Equal(t, model.BotLoadRunRunning, *saved.RunState)
	var eventCount int64
	require.NoError(t, h.db.Model(&model.BotLoadRunEvent{}).
		Where("stress_session_id = ?", h.session.ID).Count(&eventCount).Error)
	require.Equal(t, int64(2), eventCount)
}

// TestBotLoadExecutionStop_CancelsOpenCommandSchedules FR-369：stop 对未终态 checkpoint 下发 Cancel。
func TestBotLoadExecutionStop_CancelsOpenCommandSchedules(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{2}, 2, nil)
	// 注入 V2 intent，使 stop 写 bot_load_run_events。
	h.service.SetRunIntentService(NewBotLoadRunIntentService(h.db))
	runState := model.BotLoadRunRunning
	require.NoError(t, h.db.Model(h.session).Updates(map[string]any{"schema_version": 2, "run_state": runState}).Error)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.NotEmpty(t, h.dispatcher.ScheduleCalls())

	var open int64
	require.NoError(t, h.db.Model(&model.BotLoadCommandCheckpoint{}).
		Where("status = ?", model.BotLoadCommandCheckpointPrepared).Count(&open).Error)
	require.Equal(t, int64(2), open)

	_, err = h.service.Stop(context.Background(), h.session.ID, "测试取消编排")
	require.NoError(t, err)

	// 同步 runner 默认立即执行 stop dispatch
	cancels := h.dispatcher.CancelCalls()
	require.NotEmpty(t, cancels)
	totalItems := 0
	for _, c := range cancels {
		for _, item := range c.request.Items {
			require.Equal(t, "session_stop", item.Reason)
			require.NotEmpty(t, item.ScheduleRunId)
			require.NotEmpty(t, item.UnresolvedOccurrences)
			totalItems++
		}
	}
	require.Equal(t, 2, totalItems)

	var cancelled int64
	require.NoError(t, h.db.Model(&model.BotLoadCommandCheckpoint{}).
		Where("status = ?", model.BotLoadCommandCheckpointCancelled).Count(&cancelled).Error)
	require.Equal(t, int64(2), cancelled)

	// FR-370：stop 应通过 V2 intent 写入 run-state 事件。
	var eventCount int64
	require.NoError(t, h.db.Model(&model.BotLoadRunEvent{}).
		Where("stress_session_id = ?", h.session.ID).Count(&eventCount).Error)
	require.GreaterOrEqual(t, eventCount, int64(1), "stop 应写 bot_load_run_events")
}

func TestBotLoadExecutionStart_MaterializesStableCohortAndScenarioAssignments(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{10}, 10, nil)
	rawScenario := `{"version":2,"seed":20260719,"cohorts":[{"key":"lobby","percent":20,"steps":[{"id":"lobby-observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]},{"key":"combat","percent":80,"steps":[{"id":"combat-observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]}]}`
	scenario, err := ParseScenarioV2([]byte(rawScenario))
	require.NoError(t, err)
	scenarioSnapshot, err := CanonicalScenarioSnapshot(scenario, false)
	require.NoError(t, err)
	existingStart := h.clock.Now().Add(-time.Minute).UTC()
	require.NoError(t, h.db.Model(h.session).Updates(map[string]any{
		"scenario_snapshot": scenarioSnapshot,
		"started_at":        existingStart,
	}).Error)
	h.session.ScenarioSnapshot = scenarioSnapshot
	h.session.StartedAt = &existingStart

	started, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.NotNil(t, started.StartedAt)
	require.Equal(t, existingStart, started.StartedAt.UTC())
	var bots []model.Bot
	require.NoError(t, h.db.Where("stress_session_id = ?", h.session.ID).Order("id ASC").Find(&bots).Error)
	require.Len(t, bots, 10)
	expected, err := AssignScenarioCohorts(scenario.Seed, 10, scenario.Cohorts)
	require.NoError(t, err)

	byUUID := make(map[string]model.Bot, len(bots))
	for index, bot := range bots {
		require.Equal(t, expected[index], bot.CohortKey)
		byUUID[bot.UUID] = bot
	}
	calls := h.dispatcher.Calls()
	require.Len(t, calls, 1)
	for _, assignment := range calls[0].request.Assignments {
		bot := byUUID[assignment.BotUuid]
		require.Equal(t, bot.CohortKey, assignment.CohortKey)
		require.NotEmpty(t, assignment.ScenarioJson)
		var envelope struct {
			Seed              int64 `json:"seed"`
			BotOrdinal        int   `json:"botOrdinal"`
			RunDeadlineUnixMS int64 `json:"runDeadlineUnixMs"`
			Scenario          struct {
				Key   string            `json:"key"`
				Steps []json.RawMessage `json:"steps"`
			} `json:"scenario"`
		}
		require.NoError(t, json.Unmarshal([]byte(assignment.ScenarioJson), &envelope))
		require.Equal(t, scenario.Seed, envelope.Seed)
		require.Equal(t, botLoadOrdinalFromUUID(h.session.UUID, h.session.BotCount, assignment.BotUuid), envelope.BotOrdinal)
		require.Greater(t, envelope.BotOrdinal, 0)
		require.Greater(t, envelope.RunDeadlineUnixMS, existingStart.UnixMilli())
		require.Equal(t, int64(1000), envelope.RunDeadlineUnixMS-assignment.ConnectNotBeforeUnixMs)
		require.Equal(t, bot.CohortKey, envelope.Scenario.Key)
		require.NotEmpty(t, envelope.Scenario.Steps)
		require.Equal(t, assignment.ConfigHash, botLoadAssignmentConfigHash(assignment))
		require.Equal(t, bot.ConfigHash, assignment.ConfigHash)
	}

	firstCohorts := make(map[string]string, len(bots))
	for _, bot := range bots {
		firstCohorts[bot.UUID] = bot.CohortKey
	}
	_, err = h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.Len(t, h.dispatcher.Calls(), 1)
	var replayed []model.Bot
	require.NoError(t, h.db.Where("stress_session_id = ?", h.session.ID).Find(&replayed).Error)
	for _, bot := range replayed {
		require.Equal(t, firstCohorts[bot.UUID], bot.CohortKey)
	}

	h.clock.Advance(30 * time.Minute)
	loaded, err := h.service.loadSession(context.Background(), h.session.ID)
	require.NoError(t, err)
	var ordered []model.Bot
	require.NoError(t, h.db.Where("stress_session_id = ?", h.session.ID).Order("id ASC").Find(&ordered).Error)
	_, cohortJSON, cohortBudgets, err := prepareBotLoadScenarioAssignments(loaded)
	require.NoError(t, err)
	rebuilt, err := buildStartBotLoadBatchRequest(loaded, &h.plan, botLoadConnectionConfig{Server: "mc.example.test", Port: 25570, Auth: "offline", Version: "1.20.4"}, h.plan.Allocations[0], ordered, cohortJSON, cohortBudgets)
	require.NoError(t, err)
	firstByBot := make(map[string]*workerpb.BotAssignment, len(calls[0].request.Assignments))
	for _, assignment := range calls[0].request.Assignments {
		firstByBot[assignment.BotUuid] = assignment
	}
	for _, assignment := range rebuilt.Assignments {
		require.Equal(t, firstByBot[assignment.BotUuid].ScenarioJson, assignment.ScenarioJson)
		require.Equal(t, firstByBot[assignment.BotUuid].ConfigHash, assignment.ConfigHash)
	}
}

func TestBotLoadExecutionStart_RunDeadlineCoversScheduleAndFullCohortBudget(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{50}, 50, nil)
	setBotLoadExecutionScenario(t, h, `{"version":2,"seed":20260719,"cohorts":[{"key":"combat","percent":100,"steps":[{"id":"probe","type":"wait_probe_event","event":"room_ready","timeoutMs":1000,"maxAttempts":2,"retryBackoffMs":100},{"id":"barrier","type":"barrier","key":"ready","release":{"type":"all"},"timeoutMs":2000},{"id":"move","type":"move_to_and_wait","pos":{"x":10,"y":64,"z":10},"radius":2,"timeoutMs":3000,"maxAttempts":2,"retryBackoffMs":200},{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":4000,"timeoutMs":6000,"maxAttempts":2,"retryBackoffMs":300,"area":{"type":"radius","center":{"x":10,"y":64,"z":10},"radius":2}}]}]}`)
	startedAt := h.clock.Now().Add(5 * time.Second).UTC()
	require.NoError(t, h.db.Model(h.session).Update("started_at", startedAt).Error)
	h.session.StartedAt = &startedAt

	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	calls := h.dispatcher.Calls()
	require.Len(t, calls, 1)
	require.Len(t, calls[0].request.Assignments, 50)
	const cohortBudgetMS = int64(18_600)
	for _, assignment := range calls[0].request.Assignments {
		baseline := max(startedAt.UnixMilli(), assignment.ConnectNotBeforeUnixMs)
		require.Equal(t, baseline+cohortBudgetMS, assignmentRunDeadlineUnixMS(t, assignment))
	}
	last := calls[0].request.Assignments[49]
	require.Equal(t, h.plan.Allocations[0].ConnectStartAt.Add(49*200*time.Millisecond).UnixMilli(), last.ConnectNotBeforeUnixMs)
	require.Equal(t, cohortBudgetMS, assignmentRunDeadlineUnixMS(t, last)-last.ConnectNotBeforeUnixMs)
}

func TestBotLoadScenarioBudget_TowerDefenseCoversLobbyAndCombat(t *testing.T) {
	seed := int64(20260719)
	lobbyRadius, combatRadius := 30.0, 2.0
	scenario, err := BuildScenarioPreset(TowerDefenseCorePresetKey, TowerDefenseCorePresetParams{
		Seed: &seed, RoomKey: "room-352", JoinCommand: "/tower join {{roomKey}} {{correlationToken}}",
		LobbyCenter: &ScenarioPosition{X: 10, Y: 64, Z: -5}, LobbyRadius: &lobbyRadius,
		CombatPosition: &ScenarioPosition{X: 100, Y: 65, Z: 100}, CombatRadius: &combatRadius,
		CombatAreaID: "combat-zone-a", MonsterTypes: []string{"zombie"}, AttackRadius: &lobbyRadius,
	})
	require.NoError(t, err)

	budgets, err := scenarioCohortBudgetMSMap(scenario)
	require.NoError(t, err)
	require.Equal(t, int64(3_630_000), budgets["lobby"])
	require.Equal(t, int64(3_805_000), budgets["combat"])
}

func TestBotLoadScenarioBudget_UsesTimeoutOnceAndExpandsBoundedRespawn(t *testing.T) {
	scenario, err := ParseScenarioV2([]byte(`{"version":2,"seed":1,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"setup","type":"wait","durationMs":5000,"timeoutMs":1000,"maxAttempts":2,"retryBackoffMs":100},{"id":"rejoin","type":"respawn_and_rejoin","entryStepId":"setup","timeoutMs":200,"maxAttempts":2},{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":300,"timeoutMs":300,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]}]}`))
	require.NoError(t, err)

	budgets, err := scenarioCohortBudgetMSMap(scenario)
	require.NoError(t, err)
	// setup 每次最多 2×1000+100，respawn 每次最多 2×200；回跳段再有界展开 2 次。
	require.Equal(t, int64(7_800), budgets["all"])
}

func TestBotLoadScenarioRunDeadline_RejectsTimestampOverflow(t *testing.T) {
	startedAt := time.UnixMilli(maxScenarioBudgetMS - 50).UTC()
	deadline, err := botLoadScenarioRunDeadlineUnixMS(&model.BotStressSession{StartedAt: &startedAt}, 0, 100)
	require.ErrorContains(t, err, "溢出")
	require.Zero(t, deadline)
}

func TestBotLoadExecutionStart_RejectsInvalidScenarioSnapshotBeforeMaterialization(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{1}, 1, nil)
	require.NoError(t, h.db.Model(h.session).Update("scenario_snapshot", "{").Error)
	h.session.ScenarioSnapshot = "{"

	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.Error(t, err)
	require.Empty(t, h.dispatcher.Calls())
	var count int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("stress_session_id = ?", h.session.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestBotLoadAllocationLocalIndex_UsesStableAllocationOrdinal(t *testing.T) {
	plan := &BotLoadAllocationPlan{Allocations: []BotLoadAllocation{
		{Ordinal: 2, PlannedCount: 2},
		{Ordinal: 1, PlannedCount: 3},
	}}

	require.Equal(t, 4, botLoadAllocationFirstOrdinal(plan, 2))
	require.Equal(t, 0, botLoadAllocationLocalIndex(plan, 2, 4))
	require.Equal(t, 1, botLoadAllocationLocalIndex(plan, 2, 5))
}

func TestBotLoadExecutionStart_EnsuresOneFleetSubscriptionPerExecutorNode(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{50, 50}, 100, nil)
	subscriptions := &botLoadExecutionSubscriptions{}
	h.service.SetFleetSubscriptionManager(subscriptions)

	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)

	targets := subscriptions.Ensured()
	require.Len(t, targets, 2)
	require.Equal(t, h.session.UUID, targets[0].SessionUUID)
	require.Equal(t, h.session.UUID, targets[1].SessionUUID)
	require.ElementsMatch(t, []uint{h.nodes[0].ID, h.nodes[1].ID}, []uint{targets[0].NodeID, targets[1].NodeID})
}

func TestBotLoadExecutionRecoverFleetSubscriptions_SelectsActiveTargetsForConnectedNode(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{50, 50}, 2, nil)
	require.NoError(t, h.db.Model(h.session).Updates(map[string]any{
		"status": model.BotStressSessionRunning,
	}).Error)
	activeBatches := []model.BotLoadBatch{
		{StressSessionID: h.session.ID, ExecutorNodeID: h.nodes[0].ID, Ordinal: 1, PlannedCount: 1, State: model.BotLoadBatchRunning, IdempotencyKey: "recover-active-a", ConnectStartAt: h.clock.Now()},
		{StressSessionID: h.session.ID, ExecutorNodeID: h.nodes[0].ID, Ordinal: 2, PlannedCount: 1, State: model.BotLoadBatchDispatching, IdempotencyKey: "recover-active-a-duplicate", ConnectStartAt: h.clock.Now()},
		{StressSessionID: h.session.ID, ExecutorNodeID: h.nodes[1].ID, Ordinal: 3, PlannedCount: 1, State: model.BotLoadBatchRunning, IdempotencyKey: "recover-active-b", ConnectStartAt: h.clock.Now()},
	}
	require.NoError(t, h.db.Create(&activeBatches).Error)

	waiting := &model.BotStressSession{
		InstanceID: h.instance.ID, Name: "waiting-runtime", NamePrefix: "waiting", BotCount: 2,
		Status: model.BotStressSessionRunning, LastError: botLoadStopSessionError("waiting_runtime", ""),
	}
	require.NoError(t, h.db.Create(waiting).Error)
	require.NoError(t, h.db.Create(&[]model.BotLoadBatch{
		{
			StressSessionID: waiting.ID, ExecutorNodeID: h.nodes[0].ID, Ordinal: 1, PlannedCount: 1,
			State: model.BotLoadBatchPlanned, IdempotencyKey: "recover-waiting-runtime-planned", ConnectStartAt: h.clock.Now(),
		},
		{
			StressSessionID: waiting.ID, ExecutorNodeID: h.nodes[0].ID, Ordinal: 2, PlannedCount: 1,
			State: model.BotLoadBatchFailed, IdempotencyKey: "recover-waiting-runtime-failed", ConnectStartAt: h.clock.Now(),
		},
	}).Error)

	inactive := &model.BotStressSession{
		InstanceID: h.instance.ID, Name: "inactive", NamePrefix: "inactive", BotCount: 1,
		Status: model.BotStressSessionStopped, LastError: botLoadStopSessionError("waiting_runtime", ""),
	}
	require.NoError(t, h.db.Create(inactive).Error)
	require.NoError(t, h.db.Create(&model.BotLoadBatch{
		StressSessionID: inactive.ID, ExecutorNodeID: h.nodes[0].ID, Ordinal: 1, PlannedCount: 1,
		State: model.BotLoadBatchRunning, IdempotencyKey: "recover-inactive", ConnectStartAt: h.clock.Now(),
	}).Error)

	failed := &model.BotStressSession{
		InstanceID: h.instance.ID, Name: "failed", NamePrefix: "failed", BotCount: 1,
		Status: model.BotStressSessionError, LastError: botLoadStopSessionError("waiting_runtime", ""),
	}
	require.NoError(t, h.db.Create(failed).Error)
	require.NoError(t, h.db.Create(&model.BotLoadBatch{
		StressSessionID: failed.ID, ExecutorNodeID: h.nodes[0].ID, Ordinal: 1, PlannedCount: 1,
		State: model.BotLoadBatchFailed, IdempotencyKey: "recover-failed", ConnectStartAt: h.clock.Now(),
	}).Error)

	subscriptions := &botLoadExecutionSubscriptions{}
	h.service.SetFleetSubscriptionManager(subscriptions)
	require.NoError(t, h.service.RecoverFleetSubscriptions(context.Background(), []string{h.nodes[0].UUID}))

	targets := subscriptions.Ensured()
	require.Len(t, targets, 2)
	require.ElementsMatch(t, []string{h.session.UUID, waiting.UUID}, []string{targets[0].SessionUUID, targets[1].SessionUUID})
	for _, target := range targets {
		require.Equal(t, h.nodes[0].ID, target.NodeID)
		require.Equal(t, h.nodes[0].UUID, target.NodeUUID)
	}

	require.NoError(t, h.service.RecoverFleetSubscriptions(context.Background(), []string{h.nodes[1].UUID}))
	targets = subscriptions.Ensured()
	require.Len(t, targets, 3)
	require.Equal(t, h.session.UUID, targets[2].SessionUUID)
	require.Equal(t, h.nodes[1].UUID, targets[2].NodeUUID)

	require.NoError(t, h.service.RecoverFleetSubscriptions(context.Background(), nil))
	require.Len(t, subscriptions.Ensured(), 3)
}

func TestBotLoadExecutionRecoverFleetSubscriptions_RestartsManagerAndDeduplicatesReconnects(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	require.NoError(t, h.db.Model(h.session).Updates(map[string]any{
		"status": model.BotStressSessionRunning,
	}).Error)
	require.NoError(t, h.db.Create(&model.BotLoadBatch{
		StressSessionID: h.session.ID, ExecutorNodeID: h.other.ID, Ordinal: 2, PlannedCount: 1,
		State: model.BotLoadBatchDispatching, IdempotencyKey: "fleet-recover-other", ConnectStartAt: h.now,
	}).Error)

	firstClient := &botFleetSubscriptionClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{}}
	firstManager := NewBotFleetSubscriptionManager(NewBotFleetRuntimeCoordinator(h.service, firstClient, nil))
	firstExecution := NewBotLoadExecutionService(h.db, nil, nil, nil, nil, nil, nil)
	firstExecution.SetFleetSubscriptionManager(firstManager)
	require.NoError(t, firstExecution.RecoverFleetSubscriptions(context.Background(), []string{h.node.UUID}))
	require.Eventually(t, func() bool {
		_, snapshots, streams, _ := firstClient.state()
		return snapshots == 1 && streams == 1
	}, time.Second, 10*time.Millisecond)
	firstManager.Close()

	secondClient := &botFleetSubscriptionClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{}}
	secondManager := NewBotFleetSubscriptionManager(NewBotFleetRuntimeCoordinator(h.service, secondClient, nil))
	t.Cleanup(secondManager.Close)
	secondExecution := NewBotLoadExecutionService(h.db, nil, nil, nil, nil, nil, nil)
	secondExecution.SetFleetSubscriptionManager(secondManager)

	for range 3 {
		require.NoError(t, secondExecution.RecoverFleetSubscriptions(context.Background(), []string{h.node.UUID}))
	}
	require.Eventually(t, func() bool {
		_, snapshots, streams, _ := secondClient.state()
		return snapshots == 1 && streams == 1
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, secondExecution.RecoverFleetSubscriptions(context.Background(), []string{h.other.UUID}))
	require.Eventually(t, func() bool {
		_, snapshots, streams, _ := secondClient.state()
		return snapshots == 2 && streams == 2
	}, time.Second, 10*time.Millisecond)
}

func TestBotLoadExecutionStart_InvalidPlansHaveZeroSideEffects(t *testing.T) {
	tests := []struct {
		name              string
		wantCapacityCalls int
		wantError         error
		mutate            func(*botLoadExecutionHarness) string
	}{
		{
			name: "令牌过期",
			mutate: func(h *botLoadExecutionHarness) string {
				h.clock.Advance(time.Minute + time.Nanosecond)
				return h.token
			},
		},
		{
			name: "令牌篡改",
			mutate: func(h *botLoadExecutionHarness) string {
				parts := strings.Split(h.token, ".")
				signature, _ := base64.RawURLEncoding.DecodeString(parts[1])
				signature[0] ^= 0x01
				parts[1] = base64.RawURLEncoding.EncodeToString(signature)
				return strings.Join(parts, ".")
			},
		},
		{
			name: "容量世代变化", wantCapacityCalls: 1,
			mutate: func(h *botLoadExecutionHarness) string {
				h.capacity.snapshot.NodeCapacities[0].CapacityGeneration++
				return h.token
			},
		},
		{
			name: "节点即时不可用", wantCapacityCalls: 1, wantError: ErrBotLoadNodeUnavailable,
			mutate: func(h *botLoadExecutionHarness) string {
				h.capacity.snapshot.NodeCapacities[0].BotWorkerReady = false
				h.capacity.snapshot.NodeCapacities[0].UnavailableReason = BotLoadUnavailableAdmission
				return h.token
			},
		},
		{
			name: "计划缺失",
			mutate: func(h *botLoadExecutionHarness) string {
				require.NoError(t, h.db.Model(h.session).Update("allocation_plan", "").Error)
				return h.token
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newBotLoadExecutionHarness(t, []int{50}, 50, nil)
			token := test.mutate(h)
			_, err := h.service.Start(context.Background(), h.session.ID, token)
			require.Error(t, err)
			wantError := test.wantError
			if wantError == nil {
				wantError = ErrBotLoadCapacityChanged
			}
			require.ErrorIs(t, err, wantError)
			require.Equal(t, test.wantCapacityCalls, h.capacity.Calls())
			require.Zero(t, countBotLoadRows(t, h.db, &model.BotLoadBatch{}))
			require.Zero(t, countBotLoadRows(t, h.db, &model.Bot{}))
			require.Empty(t, h.dispatcher.Calls())
		})
	}
}

func TestBotLoadExecutionStart_ConfigAndDatabaseFailureDoNotDispatch(t *testing.T) {
	t.Run("连接配置缺字段", func(t *testing.T) {
		h := newBotLoadExecutionHarness(t, []int{50}, 50, nil)
		require.NoError(t, h.db.Model(h.session).Update("config", `{"auth":"offline"}`).Error)
		_, err := h.service.Start(context.Background(), h.session.ID, h.token)
		require.ErrorIs(t, err, ErrBotLoadConfigInvalid)
		require.Zero(t, h.capacity.Calls())
		require.Zero(t, countBotLoadRows(t, h.db, &model.BotLoadBatch{}))
		require.Empty(t, h.dispatcher.Calls())
	})

	t.Run("事务创建失败", func(t *testing.T) {
		h := newBotLoadExecutionHarness(t, []int{50}, 50, nil)
		callbackName := "test:fail_bot_load_batch_create"
		require.NoError(t, h.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "BotLoadBatch" {
				tx.AddError(errors.New("模拟批次创建失败"))
			}
		}))
		t.Cleanup(func() { _ = h.db.Callback().Create().Remove(callbackName) })
		_, err := h.service.Start(context.Background(), h.session.ID, h.token)
		require.Error(t, err)
		require.Zero(t, countBotLoadRows(t, h.db, &model.BotLoadBatch{}))
		require.Zero(t, countBotLoadRows(t, h.db, &model.Bot{}))
		require.Empty(t, h.dispatcher.Calls())
	})
}

func TestBotLoadExecutionStart_StableIdentityDoesNotReuseLegacyBotName(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{1}, 1, nil)
	legacy := &model.Bot{
		InstanceID: h.instance.ID, Name: stressSessionBotName(h.session.NamePrefix, 1),
		Status: model.BotStatusStopped, Config: h.session.Config, Behavior: h.session.Behavior,
	}
	require.NoError(t, h.db.Create(legacy).Error)

	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	var planned model.Bot
	require.NoError(t, h.db.Where("stress_session_id = ?", h.session.ID).First(&planned).Error)
	require.NotEqual(t, legacy.Name, planned.Name)
	require.Equal(t, stableBotLoadUUID(fmt.Sprintf("%s|bot|1", h.session.UUID)), planned.UUID)
	require.LessOrEqual(t, len(planned.Name), 16)
	require.Equal(t, planned.Name, h.dispatcher.Calls()[0].request.Assignments[0].Username)
}

func TestBotLoadExecutionStart_ReplayAndConcurrentCallsStayIdempotent(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{50, 50}, 100, nil)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	firstCalls := len(h.dispatcher.Calls())
	firstBots := countBotLoadRows(t, h.db, &model.Bot{})
	firstBatches := countBotLoadRows(t, h.db, &model.BotLoadBatch{})
	for index := range h.capacity.snapshot.NodeCapacities {
		h.capacity.snapshot.NodeCapacities[index].AvailableBots = 0
	}

	_, err = h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.Equal(t, firstCalls, len(h.dispatcher.Calls()))
	require.Equal(t, firstBots, countBotLoadRows(t, h.db, &model.Bot{}))
	require.Equal(t, firstBatches, countBotLoadRows(t, h.db, &model.BotLoadBatch{}))

	h2 := newBotLoadExecutionHarness(t, []int{50, 50}, 100, nil)
	peer := NewBotLoadExecutionService(h2.db, h2.capacity, h2.reservations, h2.signer, h2.dispatcher, botLoadImmediateRunner{}, h2.clock)
	services := []*BotLoadExecutionService{h2.service, peer}
	start := make(chan struct{})
	errs := make(chan error, 8)
	for index := range 8 {
		go func(service *BotLoadExecutionService) {
			<-start
			_, err := service.Start(context.Background(), h2.session.ID, h2.token)
			errs <- err
		}(services[index%len(services)])
	}
	close(start)
	for range 8 {
		require.NoError(t, <-errs)
	}
	require.Equal(t, int64(100), countBotLoadRows(t, h2.db, &model.Bot{}))
	require.Equal(t, int64(2), countBotLoadRows(t, h2.db, &model.BotLoadBatch{}))
	require.Len(t, h2.dispatcher.Calls(), 2)
}

func TestBotLoadExecutionDispatch_PartialReceiptsContinueOtherBatches(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{50, 50, 50}, 150, nil)
	secondNode := h.nodes[1].UUID
	h.dispatcher.handler = func(nodeUUID string, request *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
		if nodeUUID == secondNode {
			return nil, errors.New("节点暂不可达")
		}
		response := acceptedBotLoadBatchResponse(request)
		if nodeUUID == h.nodes[0].UUID {
			for index := 25; index < len(response.Results); index++ {
				response.Results[index] = &workerpb.ApplyBotBatchItemResult{
					BotUuid: request.Assignments[index].BotUuid, Status: "capacity_insufficient",
					ErrorCode: "capacity_insufficient", Error: "容量不足",
				}
			}
		}
		return response, nil
	}

	session, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.Equal(t, model.BotStressSessionRunning, session.Status)
	require.Equal(t, 75, session.Succeeded)
	require.Equal(t, 75, session.Failed)
	require.Len(t, h.dispatcher.Calls(), 3)

	var batches []model.BotLoadBatch
	require.NoError(t, h.db.Order("ordinal ASC").Find(&batches).Error)
	require.Equal(t, []model.BotLoadBatchState{model.BotLoadBatchRunning, model.BotLoadBatchFailed, model.BotLoadBatchRunning}, []model.BotLoadBatchState{batches[0].State, batches[1].State, batches[2].State})
	require.Equal(t, 25, batches[0].AcceptedCount)
	require.Equal(t, 25, batches[0].FailedCount)
	require.Equal(t, 50, batches[1].FailedCount)
	require.NotEmpty(t, batches[0].LastError)
	require.NotEmpty(t, batches[1].LastError)

	var connecting int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("status = ?", model.BotStatusConnecting).Count(&connecting).Error)
	require.Equal(t, int64(75), connecting)
	var failedBot model.Bot
	require.NoError(t, h.db.Where("status = ?", model.BotStatusError).First(&failedBot).Error)
	require.Contains(t, failedBot.LastError, `"code"`)

	beforeReplay := len(h.dispatcher.Calls())
	_, err = h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.Equal(t, beforeReplay, len(h.dispatcher.Calls()))
}

func TestBotLoadExecutionStart_BackgroundRunnerIsBounded(t *testing.T) {
	runner := &botLoadQueuedRunner{}
	h := newBotLoadExecutionHarness(t, []int{50}, 50, runner)

	session, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.Equal(t, model.BotStressSessionRunning, session.Status)
	require.Equal(t, 1, runner.Len())
	require.Empty(t, h.dispatcher.Calls())
	_, reserved := h.reservations.Lease(h.session.ID)
	require.False(t, reserved)

	runner.RunAll()
	require.Zero(t, runner.Len())
	require.Len(t, h.dispatcher.Calls(), 1)
	require.Equal(t, model.BotStressSessionRunning, loadBotLoadSession(t, h.db, h.session.ID).Status)
}

func TestBotLoadExecutionStartStop_SerializesPerSessionWithoutBlockingOthers(t *testing.T) {
	runner := &botLoadQueuedRunner{}
	h := newBotLoadExecutionHarness(t, []int{4}, 2, runner)

	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	_, err = h.service.Stop(context.Background(), h.session.ID, "立即停止")
	require.NoError(t, err)
	other, _, otherToken := addBotLoadExecutionSession(t, h, 2)
	_, err = h.service.Start(context.Background(), other.ID, otherToken)
	require.NoError(t, err)
	require.Equal(t, 2, runner.Len(), "同一会话启停应复用一个串行任务，其他会话保留独立任务")

	runner.RunAt(1)
	require.Equal(t, model.BotStressSessionRunning, loadBotLoadSession(t, h.db, other.ID).Status)
	require.True(t, botLoadStopIntentRecorded(loadBotLoadSession(t, h.db, h.session.ID).LastError))

	runner.RunAt(0)
	stopping := loadBotLoadSession(t, h.db, h.session.ID)
	require.NotEqual(t, model.BotStressSessionStopped, stopping.Status)
	require.True(t, botLoadStopIntentRecorded(stopping.LastError))
	seenStopped := false
	for _, call := range h.dispatcher.Calls() {
		for _, assignment := range call.request.Assignments {
			if assignment.SessionUuid != h.session.UUID {
				continue
			}
			require.NotEqual(t, "running", assignment.DesiredState, "stop intent 后不得发送晚到 running assignment")
			seenStopped = seenStopped || assignment.DesiredState == "stopped"
		}
	}
	require.True(t, seenStopped)
}

func TestBotLoadExecutionDispatch_ReloadKeepsThousandBotOrderAndAssignmentHash(t *testing.T) {
	runner := &botLoadQueuedRunner{}
	h := newBotLoadExecutionHarness(t, []int{1000}, 1000, runner)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)

	mutated := h.plan
	mutated.Allocations = append([]BotLoadAllocation(nil), h.plan.Allocations...)
	for index := range mutated.Allocations {
		mutated.Allocations[index].ConnectStartAt = mutated.Allocations[index].ConnectStartAt.Add(time.Hour)
	}
	rawPlan, err := encodeBotLoadAllocationPlan(mutated)
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.BotStressSession{}).Where("id = ?", h.session.ID).Update("allocation_plan", rawPlan).Error)

	runner.RunAll()
	calls := h.dispatcher.Calls()
	require.Len(t, calls, len(h.plan.Allocations))
	var bots []model.Bot
	require.NoError(t, h.db.Where("stress_session_id = ?", h.session.ID).Find(&bots).Error)
	byUUID := make(map[string]model.Bot, len(bots))
	for _, bot := range bots {
		byUUID[bot.UUID] = bot
	}

	globalIndex := 1
	for allocationIndex, call := range calls {
		allocation := h.plan.Allocations[allocationIndex]
		require.Len(t, call.request.Assignments, allocation.PlannedCount)
		for localIndex, assignment := range call.request.Assignments {
			require.Equal(t, stableBotLoadBotName(h.session.NamePrefix, h.session.UUID, globalIndex), assignment.Name)
			expectedConnectAt := allocation.ConnectStartAt.Add(time.Duration(localIndex*allocation.ConnectIntervalMS) * time.Millisecond).UnixMilli()
			require.Equal(t, expectedConnectAt, assignment.ConnectNotBeforeUnixMs)
			require.Equal(t, assignment.ConfigHash, botLoadAssignmentConfigHash(assignment))
			require.Equal(t, byUUID[assignment.BotUuid].ConfigHash, assignment.ConfigHash)
			globalIndex++
		}
	}
	require.Equal(t, 1001, globalIndex)
	lastBatch := calls[len(calls)-1].request.Assignments
	require.Equal(t, stableBotLoadBotName(h.session.NamePrefix, h.session.UUID, 999), lastBatch[len(lastBatch)-2].Name)
	require.Equal(t, stableBotLoadBotName(h.session.NamePrefix, h.session.UUID, 1000), lastBatch[len(lastBatch)-1].Name)
}

func TestBotLoadExecutionStop_GroupsChunksAndDoesNotFakeUnreachableBots(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{70, 50}, 120, nil)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	startCalls := len(h.dispatcher.Calls())
	unreachable := h.nodes[1].UUID
	var failNode = true
	h.dispatcher.handler = func(nodeUUID string, request *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
		if nodeUUID == unreachable && failNode {
			return nil, errors.New("Worker 不可达")
		}
		return acceptedBotLoadBatchResponse(request), nil
	}

	session, err := h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, model.BotStressSessionError, session.Status)
	stopCalls := h.dispatcher.Calls()[startCalls:]
	require.Len(t, stopCalls, 3)
	chunks := make(map[string][]int)
	firstKeys := make(map[string][]string)
	for _, call := range stopCalls {
		chunks[call.nodeUUID] = append(chunks[call.nodeUUID], len(call.request.Assignments))
		firstKeys[call.nodeUUID] = append(firstKeys[call.nodeUUID], call.request.IdempotencyKey)
		for _, assignment := range call.request.Assignments {
			require.Equal(t, "stopped", assignment.DesiredState)
			require.Equal(t, int64(2), assignment.Generation)
		}
	}
	sort.Ints(chunks[h.nodes[0].UUID])
	require.Equal(t, []int{20, 50}, chunks[h.nodes[0].UUID])
	require.Equal(t, []int{50}, chunks[h.nodes[1].UUID])

	var stopped int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("status = ?", model.BotStatusStopped).Count(&stopped).Error)
	require.Zero(t, stopped, "Worker accepted 只确认命令接收，不得伪造 runtime stopped")
	var connecting int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("status = ?", model.BotStatusConnecting).Count(&connecting).Error)
	require.Equal(t, int64(120), connecting)
	var unreachableGeneration int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("executor_node_id = ?", h.nodes[1].ID).Select("desired_state_generation").Limit(1).Scan(&unreachableGeneration).Error)
	require.Equal(t, int64(2), unreachableGeneration)
	require.NotEqual(t, model.BotStressSessionStopped, session.Status)
	require.True(t, botLoadStopIntentRecorded(session.LastError))
	var failedBotCount int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("executor_node_id = ? AND last_error <> ''", h.nodes[1].ID).Count(&failedBotCount).Error)
	require.Equal(t, int64(50), failedBotCount)
	require.NoError(t, h.db.Model(&model.Bot{}).Where("executor_node_id = ? AND last_error <> ''", h.nodes[0].ID).Count(&failedBotCount).Error)
	require.Zero(t, failedBotCount, "重试只能选择仍有错误且未收束的 Bot")

	failNode = false
	beforeRetry := len(h.dispatcher.Calls())
	session, err = h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.NotEqual(t, model.BotStressSessionStopped, session.Status)
	retryCalls := h.dispatcher.Calls()[beforeRetry:]
	require.Len(t, retryCalls, 1)
	require.Equal(t, unreachable, retryCalls[0].nodeUUID)
	require.Equal(t, firstKeys[unreachable][0], retryCalls[0].request.IdempotencyKey)
	for _, assignment := range retryCalls[0].request.Assignments {
		require.Equal(t, int64(2), assignment.Generation)
	}
	require.NoError(t, h.db.Model(&model.Bot{}).Where("status = ?", model.BotStatusStopped).Count(&stopped).Error)
	require.Zero(t, stopped)
	require.Equal(t, "waiting_runtime", botLoadStopIntentState(session.LastError))
	require.NoError(t, h.db.Model(&model.Bot{}).Where("last_error <> ''").Count(&failedBotCount).Error)
	require.Zero(t, failedBotCount)
	var failedBatchCount int64
	require.NoError(t, h.db.Model(&model.BotLoadBatch{}).Where("state = ?", model.BotLoadBatchFailed).Count(&failedBatchCount).Error)
	require.Zero(t, failedBatchCount)

	beforeWaitingRetry := len(h.dispatcher.Calls())
	_, err = h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, beforeWaitingRetry, len(h.dispatcher.Calls()), "waiting_runtime 重复 Stop 不得重复 RPC")
}

func TestBotLoadExecutionStop_IncrementsExistingGenerationOnlyOnce(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{1}, 1, nil)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	var bot model.Bot
	require.NoError(t, h.db.First(&bot).Error)
	require.NoError(t, h.db.Model(&bot).Update("desired_state_generation", 7).Error)
	h.dispatcher.handler = func(string, *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
		return nil, errors.New("Worker 不可达")
	}

	_, err = h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, int64(8), loadBotLoadBot(t, h.db, bot.ID).DesiredStateGeneration)
	firstStop := h.dispatcher.Calls()[1]
	require.Equal(t, int64(8), firstStop.request.Assignments[0].Generation)

	_, err = h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, int64(8), loadBotLoadBot(t, h.db, bot.ID).DesiredStateGeneration)
	secondStop := h.dispatcher.Calls()[2]
	require.Equal(t, firstStop.request.IdempotencyKey, secondStop.request.IdempotencyKey)
}

func TestBotLoadExecutionPrepareStopIntent_ClaimsScenarioLifecycleOnce(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{1}, 1, nil)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)

	count, claimed, err := h.service.prepareStopIntent(context.Background(), h.session.ID, "测试停止")
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.True(t, claimed)
	count, claimed, err = h.service.prepareStopIntent(context.Background(), h.session.ID, "重复停止")
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.False(t, claimed)
}

func TestBotLoadExecutionStop_FleetStoppedFinalizesBatchAndSession(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{1}, 1, nil)
	subscriptions := &botLoadExecutionSubscriptions{}
	lifecycle := &botLoadExecutionScenarioLifecycle{}
	h.service.SetFleetSubscriptionManager(subscriptions)
	h.service.SetScenarioRunLifecycle(lifecycle)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)

	stopping, err := h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.NotEqual(t, model.BotStressSessionStopped, stopping.Status)
	require.Equal(t, []string{h.session.UUID}, lifecycle.Stopped(), "停止意图持久化后必须立即取消屏障调度")
	_, err = h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, []string{h.session.UUID}, lifecycle.Stopped(), "重复停止不得重复收束场景生命周期")
	bot := loadBotLoadBot(t, h.db, 1)
	require.Equal(t, model.BotStatusConnecting, bot.Status)

	runtime := NewBotFleetRuntimeService(h.db, h.clock)
	coordinator := NewBotFleetRuntimeCoordinator(runtime, nil, nil)
	coordinator.SetRuntimeObserver(h.service)
	event := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_RuntimeSnapshot{RuntimeSnapshot: &workerpb.BotRuntimeSnapshot{
		BotUuid: bot.UUID, SessionUuid: h.session.UUID, Generation: bot.DesiredStateGeneration,
		ConfigHash: bot.ConfigHash, WorkerEpoch: "epoch-stop", WorkerEpochGeneration: 1,
		EventSeq: 1, Status: "stopped", ObservedAtUnixMs: h.clock.Now().UnixMilli(),
	}}}
	result, err := coordinator.HandleEvent(context.Background(), h.nodes[0].ID, h.nodes[0].UUID, h.session.UUID, event)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeApplied, result.Decision)
	require.Equal(t, model.BotStatusStopped, loadBotLoadBot(t, h.db, bot.ID).Status)

	var batch model.BotLoadBatch
	require.NoError(t, h.db.First(&batch).Error)
	require.Equal(t, model.BotLoadBatchStopped, batch.State)
	finished := loadBotLoadSession(t, h.db, h.session.ID)
	require.Equal(t, model.BotStressSessionStopped, finished.Status)
	require.NotNil(t, finished.EndedAt)
	require.Equal(t, []string{h.session.UUID}, subscriptions.Stopped())
}

func TestBotLoadExecutionStop_EmptyBaselineFinalizesMissingRuntime(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{1}, 1, nil)
	subscriptions := &botLoadExecutionSubscriptions{}
	h.service.SetFleetSubscriptionManager(subscriptions)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	_, err = h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)

	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{ObservedAtUnixMs: h.clock.Now().UnixMilli()}}
	runtime := NewBotFleetRuntimeService(h.db, h.clock)
	coordinator := NewBotFleetRuntimeCoordinator(runtime, client, nil)
	coordinator.SetRuntimeObserver(h.service)
	require.NoError(t, coordinator.RefreshSnapshot(context.Background(), h.nodes[0].ID, h.nodes[0].UUID, h.session.UUID))

	bot := loadBotLoadBot(t, h.db, 1)
	require.Equal(t, model.BotStatusStopped, bot.Status)
	var batch model.BotLoadBatch
	require.NoError(t, h.db.First(&batch).Error)
	require.Equal(t, model.BotLoadBatchStopped, batch.State)
	finished := loadBotLoadSession(t, h.db, h.session.ID)
	require.Equal(t, model.BotStressSessionStopped, finished.Status)
	require.NotNil(t, finished.EndedAt)
	require.Equal(t, []string{h.session.UUID}, subscriptions.Stopped())
}

// TestBotLoadExecutionStop_DispatchAutoRefreshesEmptyBaseline 覆盖真机缺陷：
// stop RPC accepted 后 Worker 不再推 runtime 事件时，必须主动空 baseline 才能让 counts 归零。
func TestBotLoadExecutionStop_DispatchAutoRefreshesEmptyBaseline(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{2}, 2, nil)
	subscriptions := &botLoadExecutionSubscriptions{}
	h.service.SetFleetSubscriptionManager(subscriptions)

	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{ObservedAtUnixMs: h.clock.Now().UnixMilli()}}
	runtime := NewBotFleetRuntimeService(h.db, h.clock)
	coordinator := NewBotFleetRuntimeCoordinator(runtime, client, nil)
	coordinator.SetRuntimeObserver(h.service)
	h.service.SetFleetSnapshotRefresher(coordinator)

	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.Bot{}).Where("stress_session_id = ?", h.session.ID).
		Updates(map[string]any{"status": model.BotStatusConnected, "load_batch_id": 1}).Error)
	require.NoError(t, h.db.Model(&model.BotLoadBatch{}).Where("stress_session_id = ?", h.session.ID).
		Update("connected_count", 2).Error)

	// 模拟 stop 已 accepted 但无任何 runtime 事件：仅靠 DispatchStop 内嵌的 baseline 刷新收敛。
	finished, err := h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, model.BotStressSessionStopped, finished.Status)
	require.NotNil(t, finished.EndedAt)

	var stopped int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("stress_session_id = ? AND status = ?", h.session.ID, model.BotStatusStopped).Count(&stopped).Error)
	require.Equal(t, int64(2), stopped)
	var connected int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("stress_session_id = ? AND status = ?", h.session.ID, model.BotStatusConnected).Count(&connected).Error)
	require.Zero(t, connected)
	var batch model.BotLoadBatch
	require.NoError(t, h.db.Where("stress_session_id = ?", h.session.ID).First(&batch).Error)
	require.Equal(t, model.BotLoadBatchStopped, batch.State)
	require.Zero(t, batch.ConnectedCount)
	require.Equal(t, []string{h.session.UUID}, subscriptions.Stopped())
}

// TestBotLoadExecutionStop_WaitingRuntimeRetryRefreshesWithoutStopRPC 保证 waiting_runtime 重入只刷新不重复 stop RPC。
func TestBotLoadExecutionStop_WaitingRuntimeRetryRefreshesWithoutStopRPC(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{1}, 1, nil)
	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{ObservedAtUnixMs: h.clock.Now().UnixMilli()}}
	runtime := NewBotFleetRuntimeService(h.db, h.clock)
	coordinator := NewBotFleetRuntimeCoordinator(runtime, client, nil)
	coordinator.SetRuntimeObserver(h.service)
	h.service.SetFleetSnapshotRefresher(coordinator)

	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	// 第一次 stop：不注入 refresher 路径前半段用无事件留下 waiting_runtime。
	h.service.SetFleetSnapshotRefresher(nil)
	stopping, err := h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, "waiting_runtime", botLoadStopIntentState(stopping.LastError))
	stopRPCCalls := 0
	for _, call := range h.dispatcher.Calls() {
		for _, assignment := range call.request.Assignments {
			if assignment.DesiredState == "stopped" {
				stopRPCCalls++
			}
		}
	}
	require.Equal(t, 1, stopRPCCalls)

	// 重装 refresher 后再次 Stop：不得再发 stop RPC，但空 baseline 必须收束。
	h.service.SetFleetSnapshotRefresher(coordinator)
	before := len(h.dispatcher.Calls())
	finished, err := h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, before, len(h.dispatcher.Calls()), "waiting_runtime 重入不得重复 stop RPC")
	require.Equal(t, model.BotStressSessionStopped, finished.Status)
	require.Equal(t, model.BotStatusStopped, loadBotLoadBot(t, h.db, 1).Status)
}

func TestBotLoadExecutionReconcile_SnapshotConvergesPostRPCWriteFailure(t *testing.T) {
	runner := &botLoadQueuedRunner{}
	h := newBotLoadExecutionHarness(t, []int{2}, 2, runner)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	callbackName := "test:fail_first_bot_dispatch_update"
	failed := false
	require.NoError(t, h.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if !failed && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Bot" {
			failed = true
			tx.AddError(errors.New("模拟 RPC 后回写失败"))
		}
	}))
	runner.RunAll()
	require.True(t, failed)
	require.NoError(t, h.db.Callback().Update().Remove(callbackName))

	var batch model.BotLoadBatch
	require.NoError(t, h.db.First(&batch).Error)
	require.Equal(t, model.BotLoadBatchDispatching, batch.State)
	var bots []model.Bot
	require.NoError(t, h.db.Order("name ASC").Find(&bots).Error)
	snapshot := &workerpb.GetBotFleetSnapshotResponse{CapacityGeneration: h.plan.CapacityGenerations[0].CapacityGeneration}
	for _, bot := range bots {
		snapshot.Bots = append(snapshot.Bots, &workerpb.BotRuntimeSnapshot{
			BotUuid: bot.UUID, SessionUuid: h.session.UUID, Generation: bot.DesiredStateGeneration,
			ConfigHash: bot.ConfigHash, Status: "connecting",
		})
	}

	require.NoError(t, h.service.ReconcileBotFleetSnapshot(context.Background(), h.nodes[0].ID, h.nodes[0].UUID, h.session.UUID, snapshot))
	require.NoError(t, h.db.First(&batch, batch.ID).Error)
	require.Equal(t, model.BotLoadBatchRunning, batch.State)
	require.Equal(t, 2, batch.AcceptedCount)
	session := loadBotLoadSession(t, h.db, h.session.ID)
	require.Equal(t, 2, session.Succeeded)
	require.Zero(t, session.Failed)
	require.Len(t, h.dispatcher.Calls(), 1)
}

func TestBotLoadExecutionReconcile_DoesNotReplaceAcceptedLedgerFromRuntime(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{2}, 2, nil)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	var bots []model.Bot
	require.NoError(t, h.db.Order("name ASC").Find(&bots).Error)
	// 完整 snapshot：两 Bot 均在，generation/config 一致 → 不额外 Apply；账本 accepted 不被 runtime 覆盖。
	snapshot := &workerpb.GetBotFleetSnapshotResponse{
		CapacityGeneration: h.plan.CapacityGenerations[0].CapacityGeneration,
		Bots: []*workerpb.BotRuntimeSnapshot{
			{
				BotUuid: bots[0].UUID, SessionUuid: h.session.UUID, Generation: bots[0].DesiredStateGeneration,
				ConfigHash: bots[0].ConfigHash, Status: "connecting",
			},
			{
				BotUuid: bots[1].UUID, SessionUuid: h.session.UUID, Generation: bots[1].DesiredStateGeneration,
				ConfigHash: bots[1].ConfigHash, Status: "connecting",
			},
		},
	}

	require.NoError(t, h.service.ReconcileBotFleetSnapshot(context.Background(), h.nodes[0].ID, h.nodes[0].UUID, h.session.UUID, snapshot))
	var batch model.BotLoadBatch
	require.NoError(t, h.db.First(&batch).Error)
	require.Equal(t, 2, batch.AcceptedCount)
	require.Equal(t, model.BotLoadBatchRunning, batch.State)
	require.Equal(t, 2, loadBotLoadSession(t, h.db, h.session.ID).Succeeded)
	require.Len(t, h.dispatcher.Calls(), 1)
}

func TestBotLoadExecutionReconcile_GenerationAboveOneDoesNotImplyStopped(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{1}, 1, nil)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	bot := loadBotLoadBot(t, h.db, 1)
	require.NoError(t, h.db.Model(&bot).Updates(map[string]any{
		"desired_state_generation": 7, "status": model.BotStatusConnected,
	}).Error)
	before := len(h.dispatcher.Calls())
	snapshot := &workerpb.GetBotFleetSnapshotResponse{Bots: []*workerpb.BotRuntimeSnapshot{{
		BotUuid: bot.UUID, SessionUuid: h.session.UUID, Generation: 7,
		ConfigHash: bot.ConfigHash, Status: "connected",
	}}}

	require.NoError(t, h.service.ReconcileBotFleetSnapshot(context.Background(), h.nodes[0].ID, h.nodes[0].UUID, h.session.UUID, snapshot))
	require.Equal(t, before, len(h.dispatcher.Calls()))
	require.Equal(t, model.BotStatusConnected, loadBotLoadBot(t, h.db, bot.ID).Status)
}

func TestBotLoadExecutionReconcile_OnlyStopsRuntimeMissingFromDesired(t *testing.T) {
	runner := &botLoadQueuedRunner{}
	h := newBotLoadExecutionHarness(t, []int{3}, 3, runner)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	var bots []model.Bot
	require.NoError(t, h.db.Order("name ASC").Find(&bots).Error)
	require.Len(t, bots, 3)

	// FR-365：一致 running 不重派；generation 漂移/缺失则下发 running；orphan 下发 stopped。
	snapshot := &workerpb.GetBotFleetSnapshotResponse{
		CapacityGeneration: h.plan.CapacityGenerations[0].CapacityGeneration,
		Bots: []*workerpb.BotRuntimeSnapshot{
			{BotUuid: bots[0].UUID, SessionUuid: h.session.UUID, Generation: bots[0].DesiredStateGeneration, ConfigHash: bots[0].ConfigHash, Status: "connecting"},
			{BotUuid: bots[1].UUID, SessionUuid: h.session.UUID, Generation: 0, ConfigHash: bots[1].ConfigHash, Status: "connecting"},
			{BotUuid: "extra-bot", SessionUuid: h.session.UUID, Generation: 5, ConfigHash: "extra", Status: "connected"},
		},
	}
	require.NoError(t, h.service.ReconcileBotFleetSnapshot(context.Background(), h.nodes[0].ID, h.nodes[0].UUID, h.session.UUID, snapshot))

	calls := h.dispatcher.Calls()
	require.NotEmpty(t, calls)
	assignments := make(map[string]*workerpb.BotAssignment)
	for _, call := range calls {
		require.LessOrEqual(t, len(call.request.Assignments), 50)
		for _, assignment := range call.request.Assignments {
			assignments[assignment.BotUuid] = assignment
		}
	}
	require.NotContains(t, assignments, bots[0].UUID, "generation/config 一致不得重派")
	require.Contains(t, assignments, bots[1].UUID, "generation 漂移应重放 desired running")
	require.Equal(t, "running", assignments[bots[1].UUID].DesiredState)
	require.Contains(t, assignments, bots[2].UUID, "snapshot 缺失的 desired running 应创建")
	require.Equal(t, "running", assignments[bots[2].UUID].DesiredState)
	require.Equal(t, "stopped", assignments["extra-bot"].DesiredState)
	require.Equal(t, int64(6), assignments["extra-bot"].Generation)
}

func countBotLoadRows(t *testing.T, db *gorm.DB, value any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(value).Count(&count).Error)
	return count
}

func loadBotLoadSession(t *testing.T, db *gorm.DB, id uint) model.BotStressSession {
	t.Helper()
	var session model.BotStressSession
	require.NoError(t, db.First(&session, id).Error)
	return session
}

func loadBotLoadBot(t *testing.T, db *gorm.DB, id uint) model.Bot {
	t.Helper()
	var bot model.Bot
	require.NoError(t, db.First(&bot, id).Error)
	return bot
}
