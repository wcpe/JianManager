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
	mu      sync.Mutex
	calls   []botLoadExecutionDispatchCall
	handler func(string, *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error)
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

func (d *botLoadExecutionDispatcher) Calls() []botLoadExecutionDispatchCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]botLoadExecutionDispatchCall(nil), d.calls...)
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
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}, &model.Bot{}))

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
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "distributed-load", NamePrefix: "load", BotCount: target,
		Status: model.BotStressSessionPending, Behavior: "idle",
		Config: `{"server":"mc.example.test","port":25570,"auth":"offline","version":"1.20.4"}`,
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

func encodeBotLoadAllocationPlan(plan BotLoadAllocationPlan) (string, error) {
	raw, err := json.Marshal(plan)
	return string(raw), err
}

func TestBotLoadExecutionStart_Creates500BotsAcrossTenBatches(t *testing.T) {
	h := newBotLoadExecutionHarness(t, []int{50, 50, 50, 50, 50, 50, 50, 50, 50, 50}, 500, nil)

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
		require.Equal(t, "", bot.CohortKey)
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
		}
	}
	_, reserved := h.reservations.Lease(h.session.ID)
	require.False(t, reserved)
}

func TestBotLoadExecutionStart_InvalidPlansHaveZeroSideEffects(t *testing.T) {
	tests := []struct {
		name              string
		wantCapacityCalls int
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
			name: "节点即时不可用", wantCapacityCalls: 1,
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
			require.True(t, errors.Is(err, ErrBotLoadCapacityChanged))
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
	require.Equal(t, model.BotStressSessionPending, session.Status)
	require.Equal(t, 1, runner.Len())
	require.Empty(t, h.dispatcher.Calls())
	_, reserved := h.reservations.Lease(h.session.ID)
	require.False(t, reserved)

	runner.RunAll()
	require.Zero(t, runner.Len())
	require.Len(t, h.dispatcher.Calls(), 1)
	require.Equal(t, model.BotStressSessionRunning, loadBotLoadSession(t, h.db, h.session.ID).Status)
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

	var reachableStopped int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("executor_node_id = ? AND status = ?", h.nodes[0].ID, model.BotStatusStopped).Count(&reachableStopped).Error)
	require.Equal(t, int64(70), reachableStopped)
	var unreachableStopped int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("executor_node_id = ? AND status = ?", h.nodes[1].ID, model.BotStatusStopped).Count(&unreachableStopped).Error)
	require.Zero(t, unreachableStopped)
	var unreachableGeneration int64
	require.NoError(t, h.db.Model(&model.Bot{}).Where("executor_node_id = ?", h.nodes[1].ID).Select("desired_state_generation").Limit(1).Scan(&unreachableGeneration).Error)
	require.Equal(t, int64(2), unreachableGeneration)

	failNode = false
	beforeRetry := len(h.dispatcher.Calls())
	session, err = h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, model.BotStressSessionStopped, session.Status)
	retryCalls := h.dispatcher.Calls()[beforeRetry:]
	require.Len(t, retryCalls, 1)
	require.Equal(t, unreachable, retryCalls[0].nodeUUID)
	require.Equal(t, firstKeys[unreachable][0], retryCalls[0].request.IdempotencyKey)
	for _, assignment := range retryCalls[0].request.Assignments {
		require.Equal(t, int64(2), assignment.Generation)
	}

	beforeIdempotent := len(h.dispatcher.Calls())
	_, err = h.service.Stop(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.Equal(t, beforeIdempotent, len(h.dispatcher.Calls()))
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
	snapshot := &workerpb.GetBotFleetSnapshotResponse{
		CapacityGeneration: h.plan.CapacityGenerations[0].CapacityGeneration,
		Bots: []*workerpb.BotRuntimeSnapshot{{
			BotUuid: bots[0].UUID, SessionUuid: h.session.UUID, Generation: bots[0].DesiredStateGeneration,
			ConfigHash: bots[0].ConfigHash, Status: "connecting",
		}},
	}

	require.NoError(t, h.service.ReconcileBotFleetSnapshot(context.Background(), h.nodes[0].ID, h.nodes[0].UUID, h.session.UUID, snapshot))
	var batch model.BotLoadBatch
	require.NoError(t, h.db.First(&batch).Error)
	require.Equal(t, 2, batch.AcceptedCount)
	require.Equal(t, model.BotLoadBatchRunning, batch.State)
	require.Equal(t, 2, loadBotLoadSession(t, h.db, h.session.ID).Succeeded)
	require.Len(t, h.dispatcher.Calls(), 1)
}

func TestBotLoadExecutionReconcile_CleansStaleRuntimeWithoutAutoRecovery(t *testing.T) {
	runner := &botLoadQueuedRunner{}
	h := newBotLoadExecutionHarness(t, []int{3}, 3, runner)
	_, err := h.service.Start(context.Background(), h.session.ID, h.token)
	require.NoError(t, err)
	var bots []model.Bot
	require.NoError(t, h.db.Order("name ASC").Find(&bots).Error)
	require.Len(t, bots, 3)

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
	require.NotContains(t, assignments, bots[0].UUID)
	require.Equal(t, "stopped", assignments[bots[1].UUID].DesiredState)
	require.Equal(t, bots[1].DesiredStateGeneration, assignments[bots[1].UUID].Generation)
	require.NotContains(t, assignments, bots[2].UUID)
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
