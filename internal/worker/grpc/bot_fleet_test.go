package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeBotFleetManager struct {
	capacity       bot.BotCapacitySnapshot
	bots           []bot.BotState
	applyCalls     int
	stopCalls      int
	signalCalls    int
	apply          *bot.BotWorkerEvent
	applyErr       error
	applyConfigs   []bot.BotConfig
	applyStarted   chan struct{}
	applyRelease   chan struct{}
	stop           *bot.BotWorkerEvent
	stopStarted    chan struct{}
	stopRelease    chan struct{}
	signal         *bot.BotWorkerEvent
	snapshot       *bot.BotWorkerEvent
	release        *bot.BotWorkerEvent
	releaseErr     error
	cancel         *bot.BotWorkerEvent
	cancelErr      error
}

func (f *fakeBotFleetManager) CapacitySnapshot() bot.BotCapacitySnapshot { return f.capacity }
func (f *fakeBotFleetManager) FleetSnapshot(string) []bot.BotState       { return f.bots }
func (f *fakeBotFleetManager) ApplyBotBatch(_ context.Context, _, _, _ string, configs []bot.BotConfig) (*bot.BotWorkerEvent, error) {
	f.applyCalls++
	f.applyConfigs = append([]bot.BotConfig(nil), configs...)
	if f.applyStarted != nil {
		close(f.applyStarted)
		<-f.applyRelease
	}
	return f.apply, f.applyErr
}
func (f *fakeBotFleetManager) StopBotBatch(context.Context, string, []string, int64, string) (*bot.BotWorkerEvent, error) {
	f.stopCalls++
	if f.stopStarted != nil {
		close(f.stopStarted)
		<-f.stopRelease
	}
	if f.stop != nil {
		return f.stop, nil
	}
	return &bot.BotWorkerEvent{Evt: "batch-result"}, nil
}
func (f *fakeBotFleetManager) SignalActions(context.Context, string, []bot.ActionSignal) (*bot.BotWorkerEvent, error) {
	f.signalCalls++
	if f.signal != nil {
		return f.signal, nil
	}
	return &bot.BotWorkerEvent{Evt: "signal-result"}, nil
}
func (f *fakeBotFleetManager) RequestFleetSnapshot(context.Context, string) (*bot.BotWorkerEvent, error) {
	if f.snapshot != nil {
		return f.snapshot, nil
	}
	return &bot.BotWorkerEvent{Evt: "fleet-snapshot-result", Bots: f.bots}, nil
}

func (f *fakeBotFleetManager) ApplyCommandSchedule(context.Context, string, bot.CommandScheduleCommand, time.Duration) (*bot.BotWorkerEvent, error) {
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	if f.apply != nil {
		return f.apply, nil
	}
	return &bot.BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: true}, nil
}
func (f *fakeBotFleetManager) ReleaseCommandSchedule(context.Context, string, bot.CommandScheduleReleaseCommand, time.Duration) (*bot.BotWorkerEvent, error) {
	if f.release != nil {
		return f.release, f.releaseErr
	}
	return &bot.BotWorkerEvent{Evt: "command-schedule-release-result", Accepted: true}, nil
}
func (f *fakeBotFleetManager) CancelCommandSchedule(context.Context, string, bot.CommandScheduleCancelCommand, time.Duration) (*bot.BotWorkerEvent, error) {
	if f.cancel != nil {
		return f.cancel, f.cancelErr
	}
	return &bot.BotWorkerEvent{Evt: "command-schedule-cancel-result", Accepted: true}, nil
}

func TestGetBotCapacityMapsFleetSnapshot(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{
		Ready: true, MaxBots: 50, ActiveBots: 12, ConnectingBots: 3,
		CapacityGeneration: 9, WorkerEpoch: "epoch-1", WorkerEpochGeneration: 2,
		BotWorkerVersion: "0.4.0", Features: []string{"fleet-v1"}, ObservedAt: time.Unix(100, 0),
	}}
	srv := newBotFleetTestServer(fake)

	got, err := srv.GetBotCapacity(context.Background(), &workerpb.GetBotCapacityRequest{})
	require.NoError(t, err)
	require.True(t, got.Ready)
	require.False(t, got.Legacy)
	require.EqualValues(t, 50, got.MaxBots)
	require.EqualValues(t, 9, got.CapacityGeneration)
	require.Equal(t, []string{"fleet-v1"}, got.Features)
}

func TestApplyBotBatchEnforcesLimitGenerationAndIdempotency(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, CapacityGeneration: 4},
		apply: &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{
			{BotID: "bot-1", Accepted: true, Status: "connecting"},
			{BotID: "bot-2", Skipped: true, Status: "conflict", ErrorCode: "connect_rejected", Error: "连接被拒绝"},
		}},
	}
	srv := newBotFleetTestServer(fake)

	tooMany := make([]*workerpb.BotAssignment, 51)
	_, err := srv.ApplyBotBatch(context.Background(), &workerpb.ApplyBotBatchRequest{Assignments: tooMany})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.ApplyBotBatch(context.Background(), &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-generation", IdempotencyKey: "key-generation", ExpectedCapacityGeneration: 3,
		Assignments: []*workerpb.BotAssignment{validRunningAssignment("bot-1", 1)},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	req := &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-1", IdempotencyKey: "key-1", ExpectedCapacityGeneration: 4,
		Assignments: []*workerpb.BotAssignment{
			validRunningAssignment("bot-1", 2),
			validRunningAssignment("bot-2", 2),
		},
	}
	first, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, first.Results, 2)
	require.True(t, first.Results[0].Accepted)
	require.False(t, first.Results[1].Accepted)
	require.Equal(t, "connect_rejected", first.Results[1].ErrorCode)

	second, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, fake.applyCalls)

	conflict := &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-1", IdempotencyKey: "key-1", ExpectedCapacityGeneration: 4,
		Assignments: []*workerpb.BotAssignment{validRunningAssignment("bot-2", 2)},
	}
	_, err = srv.ApplyBotBatch(context.Background(), conflict)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestApplyBotBatchPreservesScenarioEnvelopeForFleetNode(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, CapacityGeneration: 4},
		apply:    &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{BotID: "bot-envelope", Accepted: true}}},
	}
	srv := newBotFleetTestServer(fake)
	assignment := validRunningAssignment("bot-envelope", 2)
	assignment.ScenarioJson = `{"seed":42,"botOrdinal":7,"runDeadlineUnixMs":123456,"scenario":{"key":"legacy","percent":100,"steps":[{"id":"phase-01","type":"legacy_behavior","behavior":"follow","target":"Steve","durationMs":1000}]}}`
	assignment.CohortKey = "legacy"

	response, err := srv.ApplyBotBatch(context.Background(), &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-envelope", IdempotencyKey: "key-envelope", ExpectedCapacityGeneration: 4,
		Assignments: []*workerpb.BotAssignment{assignment},
	})

	require.NoError(t, err)
	require.True(t, response.Results[0].Accepted)
	require.Len(t, fake.applyConfigs, 1)
	require.JSONEq(t, assignment.ScenarioJson, string(fake.applyConfigs[0].Scenario))
}

func TestApplyBotBatchRejectsInvalidAssignmentsWithoutIPC(t *testing.T) {
	invalidAssignments := []*workerpb.BotAssignment{
		nil,
		{BotUuid: "bot-empty"},
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-session", 1)
			assignment.SessionUuid = ""
			return assignment
		}(),
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-generation", 1)
			assignment.Generation = 0
			return assignment
		}(),
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-hash-empty", 1)
			assignment.ConfigHash = ""
			return assignment
		}(),
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-hash-format", 1)
			assignment.ConfigHash = "not-a-sha256"
			return assignment
		}(),
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-state", 1)
			assignment.DesiredState = ""
			return assignment
		}(),
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-instance", 1)
			assignment.InstanceUuid = ""
			return assignment
		}(),
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-name", 1)
			assignment.Name = ""
			return assignment
		}(),
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-host", 1)
			assignment.Host = ""
			return assignment
		}(),
		func() *workerpb.BotAssignment {
			assignment := validRunningAssignment("bot-port", 1)
			assignment.Port = 0
			return assignment
		}(),
	}
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50}}
	srv := newBotFleetTestServer(fake)

	response, err := srv.ApplyBotBatch(context.Background(), &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-invalid", IdempotencyKey: "key-invalid", Assignments: invalidAssignments,
	})

	require.NoError(t, err)
	require.Len(t, response.Results, len(invalidAssignments))
	for _, result := range response.Results {
		require.False(t, result.Accepted)
		require.True(t, result.Skipped)
		require.Equal(t, botBatchStatusConflict, result.Status)
		require.Equal(t, "invalid_assignment", result.ErrorCode)
	}
	require.Zero(t, fake.applyCalls)
	require.Zero(t, fake.stopCalls)
}

func TestApplyBotBatchAllowsStoppedAssignmentWithoutConnectionFields(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50}}
	srv := newBotFleetTestServer(fake)

	response, err := srv.ApplyBotBatch(context.Background(), &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-stopped", IdempotencyKey: "key-stopped",
		Assignments: []*workerpb.BotAssignment{validStoppedAssignment("bot-stopped", 2)},
	})

	require.NoError(t, err)
	require.True(t, response.Results[0].Accepted)
	require.True(t, response.Results[0].Skipped)
	require.Equal(t, "already_stopped", response.Results[0].ErrorCode)
	require.Zero(t, fake.applyCalls)
	require.Zero(t, fake.stopCalls)
}

func TestApplyBotBatchDoesNotCacheTransientPrepareFailure(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, CapacityGeneration: 4, WorkerEpochGeneration: 1},
		apply:    &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{BotID: "bot-1", Accepted: true}}},
	}
	srv := newBotFleetTestServer(fake)
	srv.botMgr = bot.NewManager(bot.ManagerConfig{BotWorkerPath: "unused.js", NodePath: "__missing_node_for_fleet_test__"})
	req := &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-transient", IdempotencyKey: "key-transient",
		Assignments: []*workerpb.BotAssignment{validRunningAssignment("bot-1", 1)},
	}

	first, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, botBatchStatusUnavailable, first.Results[0].Status)
	require.Zero(t, fake.applyCalls)

	srv.botMgr = nil
	second, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.True(t, second.Results[0].Accepted)
	require.Equal(t, 1, fake.applyCalls, "瞬时启动失败恢复后同 key 必须重新进入 IPC")
}

func TestApplyBotBatchDoesNotCacheTransientDispatchResult(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, CapacityGeneration: 4, WorkerEpochGeneration: 1},
		applyErr: context.DeadlineExceeded,
	}
	srv := newBotFleetTestServer(fake)
	req := &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-dispatch", IdempotencyKey: "key-dispatch",
		Assignments: []*workerpb.BotAssignment{validRunningAssignment("bot-1", 1)},
	}

	first, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, botBatchStatusUnavailable, first.Results[0].Status)

	fake.applyErr = nil
	fake.apply = &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{BotID: "bot-1", Accepted: true}}}
	second, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.True(t, second.Results[0].Accepted)
	require.Equal(t, 2, fake.applyCalls, "瞬时 IPC 失败不得进入一小时结果缓存")
}

func TestApplyBotBatchCacheIsInvalidatedByWorkerExitAndEpochChange(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, CapacityGeneration: 4, WorkerEpoch: "epoch-1", WorkerEpochGeneration: 1},
		apply:    &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{BotID: "bot-1", Accepted: true}}},
	}
	srv := newBotFleetTestServer(fake)
	req := &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-epoch", IdempotencyKey: "key-epoch",
		Assignments: []*workerpb.BotAssignment{validRunningAssignment("bot-1", 1)},
	}

	_, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, fake.applyCalls)

	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "worker-exit", WorkerEpochGeneration: 1})
	fake.capacity.WorkerEpoch = "epoch-2"
	fake.capacity.WorkerEpochGeneration = 2
	_, err = srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 2, fake.applyCalls, "新 child 必须重新下发相同 idempotencyKey")

	fake.capacity.WorkerEpoch = "epoch-3"
	fake.capacity.WorkerEpochGeneration = 3
	_, err = srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 3, fake.applyCalls, "即使退出事件迟到，缓存条目的 epoch 也必须阻止跨代重放")
}

func TestPlanBotBatchMissingStopIsStableAlreadyStopped(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, WorkerEpochGeneration: 1},
	}
	srv := newBotFleetTestServer(fake)
	req := &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-stop", IdempotencyKey: "key-stop",
		Assignments: []*workerpb.BotAssignment{validStoppedAssignment("bot-missing", 2)},
	}

	first, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.True(t, first.Results[0].Accepted)
	require.True(t, first.Results[0].Skipped)
	require.Equal(t, "already_stopped", first.Results[0].ErrorCode)

	fake.bots = []bot.BotState{{ID: "bot-missing", Generation: 2}}
	second, err := srv.ApplyBotBatch(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Zero(t, fake.stopCalls, "already_stopped 是可缓存的稳定成功")
}

func TestBotItemResultMapsAlreadyStoppedAsAccepted(t *testing.T) {
	got := botItemResultToProto(bot.BotItemResult{
		BotID: "bot-1", Accepted: true, Skipped: true, Status: "accepted", ErrorCode: "already_stopped",
	})

	require.True(t, got.Accepted)
	require.True(t, got.Skipped)
	require.Equal(t, botBatchStatusAccepted, got.Status)
	require.Equal(t, "already_stopped", got.ErrorCode)
}

func TestPlanBotBatchAllowsReplacementAtFullCapacityAndRejectsStaleGeneration(t *testing.T) {
	capacity := bot.BotCapacitySnapshot{Ready: true, MaxBots: 1, ActiveBots: 1}
	states := []bot.BotState{{ID: "bot-existing", Generation: 2, ConfigHash: "hash-2"}}
	existingAssignment := validRunningAssignment("bot-existing", 3)
	existingAssignment.ConfigHash = strings.Repeat("3", 64)
	staleAssignment := validRunningAssignment("bot-stale", 1)
	plan := planBotBatch([]*workerpb.BotAssignment{
		existingAssignment,
		staleAssignment,
	}, capacity, append(states, bot.BotState{ID: "bot-stale", Generation: 2}))

	require.Equal(t, []int{0}, plan.createIndexes)
	require.Len(t, plan.createConfigs, 1)
	require.Nil(t, plan.results[0])
	require.Equal(t, "stale_generation", plan.results[1].ErrorCode)
}

func TestSetBotBehaviorRejectsFleetOwnershipButSendCommandRemainsChatOnly(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpoch: "epoch-owned", WorkerEpochGeneration: 9}}
	srv := newBotFleetTestServer(fake)
	srv.botMgr = bot.NewManager(bot.ManagerConfig{})
	srv.botOwnership = map[string]botFleetOwnership{
		"bot-owned": {workerEpoch: "epoch-owned", workerEpochGeneration: 9},
	}

	behavior, err := srv.SetBotBehavior(context.Background(), &workerpb.SetBotBehaviorRequest{BotUuid: "bot-owned", Behavior: "follow"})
	require.NoError(t, err)
	require.False(t, behavior.Success)
	require.Contains(t, behavior.Error, "Fleet")
}

func TestSignalBotActionsMapsItemResults(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true},
		signal: &bot.BotWorkerEvent{Evt: "signal-result", SignalResults: []bot.SignalItemResult{
			{SignalID: "signal-1", Accepted: true},
		}},
	}
	srv := newBotFleetTestServer(fake)

	got, err := srv.SignalBotActions(context.Background(), &workerpb.SignalBotActionsRequest{Signals: []*workerpb.BotActionSignal{{SignalId: "signal-1", BotUuid: "bot-1"}}})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	require.True(t, got.Results[0].Accepted)
}

func TestSignalBotActionsRejectsEmptyAndDuplicateSignalIDs(t *testing.T) {
	tests := []struct {
		name    string
		signals []*workerpb.BotActionSignal
	}{
		{name: "空 signalId", signals: []*workerpb.BotActionSignal{{SignalId: ""}}},
		{name: "重复 signalId", signals: []*workerpb.BotActionSignal{{SignalId: "same"}, {SignalId: "same"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true}}
			srv := newBotFleetTestServer(fake)

			_, err := srv.SignalBotActions(context.Background(), &workerpb.SignalBotActionsRequest{Signals: tt.signals})

			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Zero(t, fake.signalCalls, "无效请求不得进入 IPC，避免 map 覆盖破坏逐项对应")
		})
	}
}

func TestGetBotFleetSnapshotTreatsEmptyWorkerSnapshotAsAuthoritative(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true},
		bots:     []bot.BotState{{ID: "ghost-bot", Status: "connected"}},
		snapshot: &bot.BotWorkerEvent{Evt: "fleet-snapshot-result", Bots: []bot.BotState{}},
	}
	srv := newBotFleetTestServer(fake)

	got, err := srv.GetBotFleetSnapshot(context.Background(), &workerpb.GetBotFleetSnapshotRequest{})

	require.NoError(t, err)
	require.Empty(t, got.Bots, "bot-worker 的空快照必须覆盖 Manager 旧缓存")
}

func TestGetBotFleetSnapshotMapsGenerationAndPosition(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{CapacityGeneration: 8},
		bots: []bot.BotState{{
			ID: "bot-1", SessionID: "run-1", Generation: 3, ConfigHash: "hash",
			WorkerEpoch: "epoch-1", WorkerEpochGeneration: 2, EventSeq: 11,
			Status: "connected", Health: 20, Food: 18, Position: &bot.Vec3{X: 1, Y: 2, Z: 3},
		}},
	}
	srv := newBotFleetTestServer(fake)

	got, err := srv.GetBotFleetSnapshot(context.Background(), &workerpb.GetBotFleetSnapshotRequest{SessionUuid: "run-1"})
	require.NoError(t, err)
	require.Len(t, got.Bots, 1)
	require.EqualValues(t, 11, got.Bots[0].EventSeq)
	require.Equal(t, float64(2), got.Bots[0].Pos.Y)
	require.EqualValues(t, 8, got.CapacityGeneration)
}

type botFleetTestStream struct {
	ctx    context.Context
	events chan *workerpb.BotFleetEvent
}

func (s *botFleetTestStream) Send(event *workerpb.BotFleetEvent) error {
	s.events <- event
	return nil
}

func (s *botFleetTestStream) SetHeader(metadata.MD) error  { return nil }
func (s *botFleetTestStream) SendHeader(metadata.MD) error { return nil }
func (s *botFleetTestStream) SetTrailer(metadata.MD)       {}
func (s *botFleetTestStream) Context() context.Context     { return s.ctx }
func (s *botFleetTestStream) SendMsg(any) error            { return nil }
func (s *botFleetTestStream) RecvMsg(any) error            { return nil }

func TestStreamBotFleetEventsFiltersAndCleansSubscriberOnCancel(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true}}
	srv := newBotFleetTestServer(fake)
	ctx, cancel := context.WithCancel(context.Background())
	stream := &botFleetTestStream{ctx: ctx, events: make(chan *workerpb.BotFleetEvent, 1)}
	done := make(chan error, 1)
	go func() {
		done <- srv.StreamBotFleetEvents(&workerpb.StreamBotFleetEventsRequest{SessionUuid: "run-1"}, stream)
	}()

	waitForBotEventSubscribers(t, srv, 1)
	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "bot-state", Bots: []bot.BotState{{ID: "bot-1", SessionID: "run-1", Status: "connected"}}})
	select {
	case event := <-stream.events:
		require.Equal(t, "bot-1", event.GetRuntimeSnapshot().BotUuid)
	case <-time.After(time.Second):
		t.Fatal("未收到 Bot fleet runtime 事件")
	}

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	waitForBotEventSubscribers(t, srv, 0)
}

func TestDispatchBotEventPreservesBarrierAndTerminalActionsUnderRuntimePressure(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}}
	srv := newBotFleetTestServer(fake)
	const actionRuns = 64
	ch := make(chan *bot.BotWorkerEvent, 1)
	srv.addBotFleetEventSubscriber(ch)
	defer srv.removeBotEventSubscriber(ch)

	startedAt := time.Now()
	for i := range 256 {
		srv.dispatchBotEvent(&bot.BotWorkerEvent{
			Evt: "bot-state", WorkerEpochGeneration: 3,
			Bots: []bot.BotState{{ID: "runtime-bot", EventSeq: int64(i + 1)}},
		})
	}
	for i := range actionRuns {
		actionRunID := fmt.Sprintf("action-%03d", i)
		srv.dispatchBotEvent(grpcActionTestEvent(actionRunID, "running", json.RawMessage(`{"type":"barrier-arrived"}`)))
		srv.dispatchBotEvent(grpcActionTestEvent(actionRunID, "succeeded", nil))
	}
	if elapsed := time.Since(startedAt); elapsed >= 500*time.Millisecond {
		t.Fatalf("Bot 事件扇出不应反压 stdout: %v", elapsed)
	}

	seen := make(map[string]map[string]bool, actionRuns)
	deadline := time.After(2 * time.Second)
	for len(seen) < actionRuns || !allGRPCActionStatusesSeen(seen) {
		select {
		case event, ok := <-ch:
			if !ok {
				t.Fatal("动作事件全部交付前 Fleet 订阅被关闭")
			}
			if event.Evt != "action-event" || event.Action == nil {
				continue
			}
			statuses := seen[event.Action.ActionRunID]
			if statuses == nil {
				statuses = make(map[string]bool, 2)
				seen[event.Action.ActionRunID] = statuses
			}
			statuses[event.Action.Status] = true
		case <-deadline:
			t.Fatalf("channel 满压下 action-event 丢失: 已收到 %d/%d 组", len(seen), actionRuns)
		}
	}
	require.Len(t, seen, actionRuns)
	for _, statuses := range seen {
		require.True(t, statuses["running"], "barrier-arrived 事件不得丢失")
		require.True(t, statuses["succeeded"], "终态事件不得丢失")
	}
}

func allGRPCActionStatusesSeen(seen map[string]map[string]bool) bool {
	for _, statuses := range seen {
		if !statuses["running"] || !statuses["succeeded"] {
			return false
		}
	}
	return true
}

func TestDispatchBotEventClosesFleetSubscriberAtReliableQueueHardLimit(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}}
	srv := newBotFleetTestServer(fake)
	ch := make(chan *bot.BotWorkerEvent, 1)
	subscriber := srv.addBotFleetEventSubscriber(ch)

	for i := range botFleetReliableQueueLimit + 16 {
		srv.dispatchBotEvent(grpcActionTestEvent(fmt.Sprintf("action-%04d", i), "succeeded", nil))
	}

	waitForBotEventSubscribers(t, srv, 0)
	require.Equal(t, codes.ResourceExhausted, status.Code(subscriber.terminalError()))
	for range ch {
	}
}

func TestStreamBotFleetEventsReplaysActionCreatedBetweenSnapshotAndSubscribe(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}}
	srv := newBotFleetTestServer(fake)
	_, err := srv.GetBotFleetSnapshot(context.Background(), &workerpb.GetBotFleetSnapshotRequest{SessionUuid: "run-1"})
	require.NoError(t, err)
	srv.dispatchBotEvent(grpcActionTestEvent("action-gap", "succeeded", nil))

	ctx, cancel := context.WithCancel(context.Background())
	stream := &botFleetTestStream{ctx: ctx, events: make(chan *workerpb.BotFleetEvent, 2)}
	done := make(chan error, 1)
	go func() {
		done <- srv.StreamBotFleetEvents(&workerpb.StreamBotFleetEventsRequest{SessionUuid: "run-1"}, stream)
	}()

	select {
	case event := <-stream.events:
		require.Equal(t, "action-gap", event.GetActionEvent().ActionRunId)
	case <-time.After(time.Second):
		t.Fatal("snapshot 与 subscribe 间产生的动作事件未补发")
	}
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestBotActionJournalReplaysByLastReceiveSequence(t *testing.T) {
	srv := newBotFleetTestServer(&fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}})
	first := grpcActionTestEvent("action-a", "running", nil)
	first.Action.Message = "first"
	srv.dispatchBotEvent(first)
	srv.dispatchBotEvent(grpcActionTestEvent("action-b", "running", nil))
	latest := grpcActionTestEvent("action-a", "running", nil)
	latest.Action.Message = "latest"
	srv.dispatchBotEvent(latest)

	ch := make(chan *bot.BotWorkerEvent, 4)
	srv.addBotFleetEventSubscriber(ch, "run-1")
	defer srv.removeBotEventSubscriber(ch)

	replayed := []string{(<-ch).Action.ActionRunID, (<-ch).Action.ActionRunID}
	require.Equal(t, []string{"action-b", "action-a"}, replayed)
	require.Equal(t, "latest", srv.botActionJournalSnapshot("run-1")[1].Action.Message)
}

func TestBotActionJournalCompressesRunningWaitingAndTerminal(t *testing.T) {
	srv := newBotFleetTestServer(&fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}})
	srv.dispatchBotEvent(grpcActionTestEvent("ordinary-running", "running", nil))
	latestRunning := grpcActionTestEvent("ordinary-running", "running", nil)
	latestRunning.Action.Message = "latest-running"
	srv.dispatchBotEvent(latestRunning)

	srv.dispatchBotEvent(grpcActionTestEvent("barrier", "running", json.RawMessage(`{"type":"barrier-arrived","round":1}`)))
	srv.dispatchBotEvent(grpcActionTestEvent("barrier", "succeeded", nil))

	srv.dispatchBotEvent(grpcActionTestEvent("ordinary-terminal", "running", nil))
	srv.dispatchBotEvent(grpcActionTestEvent("ordinary-terminal", "failed", nil))

	replay := srv.botActionJournalSnapshot("run-1")
	require.Len(t, replay, 3)
	byAction := make(map[string][]*bot.ActionEvent)
	for _, event := range replay {
		byAction[event.Action.ActionRunID] = append(byAction[event.Action.ActionRunID], event.Action)
	}
	require.Len(t, byAction["ordinary-running"], 1)
	require.Equal(t, "latest-running", byAction["ordinary-running"][0].Message)
	require.Len(t, byAction["barrier"], 1, "terminal 应替代同 identity 的 waiting")
	require.Equal(t, "succeeded", byAction["barrier"][0].Status)
	require.Len(t, byAction["ordinary-terminal"], 1, "普通 terminal 应覆盖 running")
	require.Equal(t, "failed", byAction["ordinary-terminal"][0].Status)
}

func TestBotActionJournalReplaysAfterReliableQueueOverflow(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}}
	srv := newBotFleetTestServer(fake)
	slow := make(chan *bot.BotWorkerEvent, 1)
	subscriber := srv.addBotFleetEventSubscriber(slow, "run-1")
	for index := range botFleetReliableQueueLimit + 32 {
		event := grpcActionTestEvent("action-overflow", "running", nil)
		event.Action.Message = fmt.Sprintf("receive-%04d", index)
		srv.dispatchBotEvent(event)
	}
	waitForBotEventSubscribers(t, srv, 0)
	require.Equal(t, codes.ResourceExhausted, status.Code(subscriber.terminalError()))
	for range slow {
	}

	reconnected := make(chan *bot.BotWorkerEvent, 2)
	srv.addBotFleetEventSubscriber(reconnected, "run-1")
	defer srv.removeBotEventSubscriber(reconnected)
	select {
	case replayed := <-reconnected:
		require.Equal(t, "action-overflow", replayed.Action.ActionRunID)
		require.Equal(t, fmt.Sprintf("receive-%04d", botFleetReliableQueueLimit+31), replayed.Action.Message)
	case <-time.After(time.Second):
		t.Fatal("可靠队列溢出重订阅后未从 journal 补发")
	}
}

func TestBotActionJournalCoversMaximumSingleNodeScenarioWindow(t *testing.T) {
	srv := newBotFleetTestServer(&fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}})
	const actionCount = maxBotBatchSize * 100 * 10
	for index := range actionCount {
		srv.dispatchBotEvent(grpcActionTestEvent(fmt.Sprintf("action-window-%04d", index), "succeeded", nil))
	}

	replay := srv.botActionJournalSnapshot("run-1")
	require.Len(t, replay, actionCount)
	require.Equal(t, "action-window-0000", replay[0].Action.ActionRunID)
	require.Equal(t, "action-window-49999", replay[len(replay)-1].Action.ActionRunID)

	stream := make(chan *bot.BotWorkerEvent, 1)
	srv.addBotFleetEventSubscriber(stream, "run-1")
	defer srv.removeBotEventSubscriber(stream)
	for index := range actionCount {
		select {
		case event := <-stream:
			require.Equal(t, fmt.Sprintf("action-window-%04d", index), event.Action.ActionRunID)
		case <-time.After(5 * time.Second):
			t.Fatalf("第 %d 条 journal replay 超时", index)
		}
	}
}

func TestBotActionJournalReplayDoesNotConsumeLiveBacklog(t *testing.T) {
	srv := newBotFleetTestServer(&fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}})
	const actionCount = maxBotBatchSize * 100
	for index := range actionCount {
		srv.dispatchBotEvent(grpcActionTestEvent(fmt.Sprintf("action-replay-%04d", index), "succeeded", nil))
	}

	stream := make(chan *bot.BotWorkerEvent, 1)
	subscriber := srv.addBotFleetEventSubscriber(stream, "run-1")
	defer srv.removeBotEventSubscriber(stream)
	srv.dispatchBotEvent(grpcActionTestEvent("action-live", "succeeded", nil))

	for index := range actionCount {
		select {
		case event := <-stream:
			require.Equal(t, fmt.Sprintf("action-replay-%04d", index), event.Action.ActionRunID)
		case <-time.After(5 * time.Second):
			t.Fatalf("第 %d 条 journal replay 超时", index)
		}
	}
	select {
	case event := <-stream:
		require.Equal(t, "action-live", event.Action.ActionRunID)
	case <-time.After(time.Second):
		t.Fatal("journal replay 占用了 1024 条 live backlog")
	}
	require.NoError(t, subscriber.terminalError())
}

func TestBotActionJournalIgnoresStaleWorkerExitAndClearsCurrentGeneration(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 2}}
	srv := newBotFleetTestServer(fake)
	event := grpcActionTestEvent("action-current", "succeeded", nil)
	event.WorkerEpochGeneration = 2
	srv.dispatchBotEvent(event)
	require.Len(t, srv.botActionJournalSnapshot("run-1"), 1)

	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "worker-exit", WorkerEpochGeneration: 1})
	require.Len(t, srv.botActionJournalSnapshot("run-1"), 1, "旧 child 的迟到退出不得清空当前代 journal")

	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "worker-exit", WorkerEpochGeneration: 2})
	require.Zero(t, srv.botActionJournalSize())
}

func TestBotActionJournalIsProcessLocalBoundedAndDoesNotSpawnPerEventGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	srv := newBotFleetTestServer(&fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}})
	for index := range botActionJournalLimit + 256 {
		srv.dispatchBotEvent(grpcActionTestEvent(fmt.Sprintf("action-bounded-%05d", index), "succeeded", nil))
	}
	require.Equal(t, botActionJournalLimit, srv.botActionJournalSize())
	time.Sleep(20 * time.Millisecond)
	require.LessOrEqual(t, runtime.NumGoroutine(), before+2, "journal append 不得为每个事件创建 goroutine")

	fresh := newBotFleetTestServer(&fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 3}})
	require.Zero(t, fresh.botActionJournalSize(), "journal 仅存于当前 Worker 进程内")
}

func grpcActionTestEvent(actionRunID, actionStatus string, result json.RawMessage) *bot.BotWorkerEvent {
	return &bot.BotWorkerEvent{
		Evt: "action-event", WorkerEpochGeneration: 3,
		Action: &bot.ActionEvent{
			BotID: "bot-action", SessionID: "run-1", Generation: 1,
			ActionRunID: actionRunID, StepID: "barrier-1", Status: actionStatus, Result: result,
		},
	}
}

func TestAddBotFleetEventSubscriberAfterWorkerExitClosesImmediately(t *testing.T) {
	srv := &Server{botMgr: bot.NewManager(bot.ManagerConfig{})}
	ch := make(chan *bot.BotWorkerEvent, 1)

	srv.addBotFleetEventSubscriber(ch)

	_, ok := <-ch
	require.False(t, ok)
	waitForBotEventSubscribers(t, srv, 0)
}

func TestBotFleetSubscriberDoesNotMissConcurrentWorkerExit(t *testing.T) {
	for range 100 {
		srv := &Server{botMgr: bot.NewManager(bot.ManagerConfig{})}
		ch := make(chan *bot.BotWorkerEvent, 1)
		start := make(chan struct{})
		done := make(chan struct{}, 2)
		go func() {
			<-start
			srv.addBotFleetEventSubscriber(ch)
			done <- struct{}{}
		}()
		go func() {
			<-start
			srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "worker-exit"})
			done <- struct{}{}
		}()

		close(start)
		<-done
		<-done
		_, ok := <-ch
		require.False(t, ok, "并发退出后 Fleet 订阅必须关闭")
		waitForBotEventSubscribers(t, srv, 0)
	}
}

func TestStreamBotFleetEventsReturnsUnavailableWhenWorkerExitsAndAllowsResubscribe(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true}}
	srv := newBotFleetTestServer(fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &botFleetTestStream{ctx: ctx, events: make(chan *workerpb.BotFleetEvent, 1)}
	done := make(chan error, 1)
	go func() {
		done <- srv.StreamBotFleetEvents(&workerpb.StreamBotFleetEventsRequest{}, stream)
	}()

	waitForBotEventSubscribers(t, srv, 1)
	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "worker-exit", Error: "bot-worker 进程已退出"})
	select {
	case err := <-done:
		require.Equal(t, codes.Unavailable, status.Code(err))
	case <-time.After(time.Second):
		t.Fatal("bot-worker 退出后 Fleet stream 未终止")
	}
	waitForBotEventSubscribers(t, srv, 0)

	nextCtx, nextCancel := context.WithCancel(context.Background())
	nextStream := &botFleetTestStream{ctx: nextCtx, events: make(chan *workerpb.BotFleetEvent, 1)}
	nextDone := make(chan error, 1)
	go func() {
		nextDone <- srv.StreamBotFleetEvents(&workerpb.StreamBotFleetEventsRequest{}, nextStream)
	}()
	waitForBotEventSubscribers(t, srv, 1)
	nextCancel()
	require.ErrorIs(t, <-nextDone, context.Canceled)
	waitForBotEventSubscribers(t, srv, 0)
}

func TestWorkerExitDoesNotCloseLegacyBotEventStream(t *testing.T) {
	srv := &Server{}
	srv.SetBotManager(bot.NewManager(bot.ManagerConfig{}))
	ctx, cancel := context.WithCancel(context.Background())
	stream := newBotEventsTestStream(ctx)
	done := make(chan error, 1)
	go func() {
		done <- srv.StreamBotEvents(&workerpb.StreamBotEventsRequest{BotUuid: "bot-1"}, stream)
	}()

	waitForBotEventSubscribers(t, srv, 1)
	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "worker-exit", Error: "bot-worker 进程已退出"})
	select {
	case err := <-done:
		t.Fatalf("旧 StreamBotEvents 不应因 worker-exit 退出: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "bot-event", BotID: "bot-1", Type: "chat"})
	require.Equal(t, "bot-1", assertBotEventReceived(t, stream.events).BotUuid)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	waitForBotEventSubscribers(t, srv, 0)
}

func TestFleetEventGenerationDropsQueuedOldChildEventsForNewSubscriber(t *testing.T) {
	fake := &fakeBotFleetManager{capacity: bot.BotCapacitySnapshot{Ready: true, WorkerEpochGeneration: 2}}
	srv := newBotFleetTestServer(fake)
	ch := make(chan *bot.BotWorkerEvent, 2)
	srv.addBotFleetEventSubscriber(ch)
	defer srv.removeBotEventSubscriber(ch)

	srv.dispatchBotEvent(&bot.BotWorkerEvent{
		Evt: "bot-state", WorkerEpochGeneration: 1,
		Bots: []bot.BotState{{ID: "old-bot", WorkerEpochGeneration: 1}},
	})
	select {
	case event := <-ch:
		t.Fatalf("新订阅者不应收到旧 child 已排队事件: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}

	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "worker-exit", WorkerEpochGeneration: 1})
	srv.dispatchBotEvent(&bot.BotWorkerEvent{
		Evt: "bot-state", WorkerEpochGeneration: 2,
		Bots: []bot.BotState{{ID: "current-bot", WorkerEpochGeneration: 2}},
	})
	select {
	case event, ok := <-ch:
		require.True(t, ok, "旧 child 的迟到退出不得关闭新代订阅者")
		require.Equal(t, "current-bot", event.Bots[0].ID)
	case <-time.After(time.Second):
		t.Fatal("未收到当前 child 事件")
	}
}

func TestFleetOwnershipRejectsLegacyMutationWithoutRuntimeIdentity(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, WorkerEpoch: "epoch-1", WorkerEpochGeneration: 1},
		bots:     []bot.BotState{{ID: "fleet-bot", Status: "connected"}},
		apply: &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{
			BotID: "fleet-bot", Accepted: true, Status: "connecting",
		}}},
	}
	srv := newBotFleetTestServer(fake)

	response, err := srv.ApplyBotBatch(context.Background(), fleetRunningRequest("fleet-bot", "ownership"))
	require.NoError(t, err)
	require.True(t, response.Results[0].Accepted)

	create, err := srv.CreateBot(context.Background(), &workerpb.CreateBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.False(t, create.Success)
	require.Contains(t, create.Error, "Fleet RPC")
	remove, err := srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.False(t, remove.Success)
	require.Contains(t, remove.Error, "Fleet RPC")
}

func TestFleetOwnershipClearsOnlyAfterAcceptedStop(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, WorkerEpoch: "epoch-1", WorkerEpochGeneration: 1},
		apply: &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{
			BotID: "fleet-bot", Accepted: true, Status: "connecting",
		}}},
	}
	srv := newBotFleetTestServer(fake)
	_, err := srv.ApplyBotBatch(context.Background(), fleetRunningRequest("fleet-bot", "running"))
	require.NoError(t, err)

	fake.bots = []bot.BotState{{ID: "fleet-bot", Status: "connected"}}
	fake.stop = &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{
		BotID: "fleet-bot", Accepted: true, Status: "stopped",
	}}}
	fake.stopStarted = make(chan struct{})
	fake.stopRelease = make(chan struct{})
	stopDone := make(chan error, 1)
	go func() {
		_, applyErr := srv.ApplyBotBatch(context.Background(), fleetStoppedRequest("fleet-bot", "stopping"))
		stopDone <- applyErr
	}()
	<-fake.stopStarted

	deleteDone := make(chan *workerpb.DeleteBotResponse, 1)
	go func() {
		response, _ := srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
		deleteDone <- response
	}()
	select {
	case response := <-deleteDone:
		t.Fatalf("Fleet stop 回执前 legacy DeleteBot 不得越过 ownership: %+v", response)
	case <-time.After(20 * time.Millisecond):
	}

	close(fake.stopRelease)
	require.NoError(t, <-stopDone)
	select {
	case response := <-deleteDone:
		require.True(t, response.Success, "stop accepted 后 ownership 应清理")
	case <-time.After(time.Second):
		t.Fatal("stop accepted 后 legacy DeleteBot 未解除阻塞")
	}
}

func TestFleetOwnershipHandlesRejectedAndAlreadyStoppedResults(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, WorkerEpoch: "epoch-1", WorkerEpochGeneration: 1},
		apply: &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{
			BotID: "fleet-bot", Accepted: true,
		}}},
	}
	srv := newBotFleetTestServer(fake)
	_, err := srv.ApplyBotBatch(context.Background(), fleetRunningRequest("fleet-bot", "result-running"))
	require.NoError(t, err)

	fake.bots = []bot.BotState{{ID: "fleet-bot", Status: "connected"}}
	fake.stop = &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{
		BotID: "fleet-bot", Accepted: false, Status: "ephemeral_unavailable",
	}}}
	response, err := srv.ApplyBotBatch(context.Background(), fleetStoppedRequest("fleet-bot", "result-rejected"))
	require.NoError(t, err)
	require.False(t, response.Results[0].Accepted)
	legacyDelete, err := srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.False(t, legacyDelete.Success, "停止未 accepted 时必须保留 ownership")

	fake.bots = nil
	response, err = srv.ApplyBotBatch(context.Background(), fleetStoppedRequest("fleet-bot", "result-already-stopped"))
	require.NoError(t, err)
	require.True(t, response.Results[0].Accepted)
	require.Equal(t, "already_stopped", response.Results[0].ErrorCode)
	legacyDelete, err = srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.True(t, legacyDelete.Success, "already_stopped accepted 后必须清理 ownership")
}

func TestFleetOwnershipPrunesOnEpochChangeAndRequiresReapply(t *testing.T) {
	fake := &fakeBotFleetManager{
		capacity: bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, WorkerEpoch: "epoch-1", WorkerEpochGeneration: 1},
		apply: &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{
			BotID: "fleet-bot", Accepted: true,
		}}},
	}
	srv := newBotFleetTestServer(fake)
	_, err := srv.ApplyBotBatch(context.Background(), fleetRunningRequest("fleet-bot", "epoch-old"))
	require.NoError(t, err)

	fake.capacity.WorkerEpoch = "epoch-2"
	fake.capacity.WorkerEpochGeneration = 2
	legacyDelete, err := srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.True(t, legacyDelete.Success, "epoch 切换后旧 ownership 必须失效")

	_, err = srv.ApplyBotBatch(context.Background(), fleetRunningRequest("fleet-bot", "epoch-new"))
	require.NoError(t, err)
	srv.dispatchBotEvent(&bot.BotWorkerEvent{
		Evt: "worker-exit", WorkerEpoch: "epoch-1", WorkerEpochGeneration: 2,
	})
	legacyDelete, err = srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.False(t, legacyDelete.Success, "旧 epoch 的迟到退出不得清理新 child ownership")
}

func TestFleetApplySerializesConcurrentLegacyMutation(t *testing.T) {
	for _, operation := range []string{"create", "delete"} {
		t.Run(operation, func(t *testing.T) {
			for range 100 {
				fake := &fakeBotFleetManager{
					capacity:     bot.BotCapacitySnapshot{Ready: true, MaxBots: 50, WorkerEpoch: "epoch-1", WorkerEpochGeneration: 1},
					apply:        &bot.BotWorkerEvent{Evt: "batch-result", Results: []bot.BotItemResult{{BotID: "fleet-bot", Accepted: true}}},
					applyStarted: make(chan struct{}),
					applyRelease: make(chan struct{}),
				}
				srv := newBotFleetTestServer(fake)
				applyDone := make(chan error, 1)
				go func() {
					_, err := srv.ApplyBotBatch(context.Background(), fleetRunningRequest("fleet-bot", "race"))
					applyDone <- err
				}()
				<-fake.applyStarted

				legacyDone := make(chan string, 1)
				go func() {
					if operation == "create" {
						response, _ := srv.CreateBot(context.Background(), &workerpb.CreateBotRequest{BotUuid: "fleet-bot"})
						legacyDone <- response.Error
						return
					}
					response, _ := srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
					legacyDone <- response.Error
				}()
				select {
				case result := <-legacyDone:
					t.Fatalf("Apply accepted 前 legacy %s 越过同步边界: %q", operation, result)
				case <-time.After(time.Millisecond):
				}

				close(fake.applyRelease)
				require.NoError(t, <-applyDone)
				select {
				case result := <-legacyDone:
					require.Contains(t, result, "Fleet RPC")
				case <-time.After(time.Second):
					t.Fatalf("legacy %s 未在 Apply 完成后返回", operation)
				}
			}
		})
	}
}

func TestFleetOwnershipClearsOnChildExitAndRequiresNewApply(t *testing.T) {
	requireNodeForGRPCTest(t)
	script := writeGRPCTestBotWorker(t, `
const readline = require("node:readline");
const generation = Number(process.argv.find((arg) => arg.startsWith("--worker-epoch-generation="))?.split("=")[1] || 0);
console.log(JSON.stringify({evt:"worker-ready",workerEpoch:"epoch-" + generation,workerEpochGeneration:generation,maxBots:50,features:["fleet-v1"],capacityGeneration:generation}));
const rl = readline.createInterface({input: process.stdin});
rl.on("line", (line) => {
  const command = JSON.parse(line);
  if (command.cmd === "create-bots") {
    if (command.requestId) {
      console.log(JSON.stringify({evt:"batch-result",requestId:command.requestId,results:command.bots.map((item) => ({botId:item.id,accepted:true,status:"connecting"}))}));
    }
    console.log(JSON.stringify({evt:"bot-state",bots:command.bots.map((item, index) => ({id:item.id,status:"connected",workerEpochGeneration:generation,eventSeq:index + 1}))}));
  }
  if (command.cmd === "stop-bots" && command.requestId) {
    console.log(JSON.stringify({evt:"batch-result",requestId:command.requestId,results:command.botIds.map((id) => ({botId:id,accepted:true,status:"stopped"}))}));
  }
});
`)
	mgr := bot.NewManager(bot.ManagerConfig{BotWorkerPath: script})
	require.NoError(t, mgr.Start(context.Background()))
	defer mgr.Stop()
	waitBotManagerReady(t, mgr)

	srv := &Server{}
	srv.SetBotManager(mgr)
	response, err := srv.ApplyBotBatch(context.Background(), fleetRunningRequest("fleet-bot", "epoch-1"))
	require.NoError(t, err)
	require.True(t, response.Results[0].Accepted)
	require.Eventually(t, func() bool {
		state, ok := mgr.GetBot("fleet-bot")
		return ok && state.SessionID == "" && state.Generation == 0 && state.ConfigHash == ""
	}, time.Second, 10*time.Millisecond)

	fleetDelete, err := srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.False(t, fleetDelete.Success)

	mgr.Stop()
	require.NoError(t, mgr.Start(context.Background()))
	waitBotManagerReady(t, mgr)
	legacyCreate, err := srv.CreateBot(context.Background(), &workerpb.CreateBotRequest{BotUuid: "fleet-bot", Name: "普通 Bot"})
	require.NoError(t, err)
	require.True(t, legacyCreate.Success, legacyCreate.Error)
	legacyDelete, err := srv.DeleteBot(context.Background(), &workerpb.DeleteBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.True(t, legacyDelete.Success, legacyDelete.Error)

	response, err = srv.ApplyBotBatch(context.Background(), fleetRunningRequest("fleet-bot", "epoch-2"))
	require.NoError(t, err)
	require.True(t, response.Results[0].Accepted)
	fleetCreate, err := srv.CreateBot(context.Background(), &workerpb.CreateBotRequest{BotUuid: "fleet-bot"})
	require.NoError(t, err)
	require.False(t, fleetCreate.Success, "新 child 必须重新 Apply 后才恢复 Fleet ownership")
}

func waitBotManagerReady(t *testing.T, mgr *bot.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, mgr.WaitReady(ctx))
}

func fleetRunningRequest(botID, suffix string) *workerpb.ApplyBotBatchRequest {
	return &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-" + suffix, IdempotencyKey: "key-" + suffix,
		Assignments: []*workerpb.BotAssignment{validRunningAssignment(botID, 1)},
	}
}

func fleetStoppedRequest(botID, suffix string) *workerpb.ApplyBotBatchRequest {
	return &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-" + suffix, IdempotencyKey: "key-" + suffix,
		Assignments: []*workerpb.BotAssignment{validStoppedAssignment(botID, 2)},
	}
}

func requireNodeForGRPCTest(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("测试环境无 node，跳过 Bot RPC 子进程测试")
	}
}

func writeGRPCTestBotWorker(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-bot-worker.js")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func validRunningAssignment(botID string, generation int64) *workerpb.BotAssignment {
	return &workerpb.BotAssignment{
		BotUuid: botID, InstanceUuid: "instance-1", SessionUuid: "session-1",
		Generation: generation, DesiredState: "running", ConfigHash: strings.Repeat("a", 64),
		Name: botID, Host: "127.0.0.1", Port: 25565,
	}
}

func validStoppedAssignment(botID string, generation int64) *workerpb.BotAssignment {
	return &workerpb.BotAssignment{
		BotUuid: botID, SessionUuid: "session-1", Generation: generation,
		DesiredState: "stopped", ConfigHash: strings.Repeat("b", 64),
	}
}

func newBotFleetTestServer(f botFleetManager) *Server {
	return &Server{botFleet: f, botBatchResults: make(map[string]*botBatchCacheEntry)}
}
