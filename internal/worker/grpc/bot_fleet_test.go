package grpc

import (
	"context"
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
	capacity   bot.BotCapacitySnapshot
	bots       []bot.BotState
	applyCalls int
	apply      *bot.BotWorkerEvent
	signal     *bot.BotWorkerEvent
}

func (f *fakeBotFleetManager) CapacitySnapshot() bot.BotCapacitySnapshot { return f.capacity }
func (f *fakeBotFleetManager) FleetSnapshot(string) []bot.BotState       { return f.bots }
func (f *fakeBotFleetManager) ApplyBotBatch(context.Context, string, string, string, []bot.BotConfig) (*bot.BotWorkerEvent, error) {
	f.applyCalls++
	return f.apply, nil
}
func (f *fakeBotFleetManager) StopBotBatch(context.Context, string, []string, int64, string) (*bot.BotWorkerEvent, error) {
	return &bot.BotWorkerEvent{Evt: "batch-result"}, nil
}
func (f *fakeBotFleetManager) SignalActions(context.Context, string, []bot.ActionSignal) (*bot.BotWorkerEvent, error) {
	if f.signal != nil {
		return f.signal, nil
	}
	return &bot.BotWorkerEvent{Evt: "signal-result"}, nil
}
func (f *fakeBotFleetManager) RequestFleetSnapshot(context.Context, string) (*bot.BotWorkerEvent, error) {
	return &bot.BotWorkerEvent{Evt: "fleet-snapshot-result", Bots: f.bots}, nil
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
			{BotID: "bot-2", ErrorCode: "connect_rejected", Error: "连接被拒绝"},
		}},
	}
	srv := newBotFleetTestServer(fake)

	tooMany := make([]*workerpb.BotAssignment, 51)
	_, err := srv.ApplyBotBatch(context.Background(), &workerpb.ApplyBotBatchRequest{Assignments: tooMany})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.ApplyBotBatch(context.Background(), &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-generation", IdempotencyKey: "key-generation", ExpectedCapacityGeneration: 3,
		Assignments: []*workerpb.BotAssignment{{BotUuid: "bot-1"}},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	req := &workerpb.ApplyBotBatchRequest{
		BatchId: "batch-1", IdempotencyKey: "key-1", ExpectedCapacityGeneration: 4,
		Assignments: []*workerpb.BotAssignment{
			{BotUuid: "bot-1", Generation: 2, DesiredState: "running"},
			{BotUuid: "bot-2", Generation: 2, DesiredState: "running"},
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
		Assignments: []*workerpb.BotAssignment{{BotUuid: "bot-2", Generation: 2, DesiredState: "running"}},
	}
	_, err = srv.ApplyBotBatch(context.Background(), conflict)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestPlanBotBatchAllowsReplacementAtFullCapacityAndRejectsStaleGeneration(t *testing.T) {
	capacity := bot.BotCapacitySnapshot{Ready: true, MaxBots: 1, ActiveBots: 1}
	states := []bot.BotState{{ID: "bot-existing", Generation: 2, ConfigHash: "hash-2"}}
	plan := planBotBatch([]*workerpb.BotAssignment{
		{BotUuid: "bot-existing", Generation: 3, ConfigHash: "hash-3", DesiredState: "running"},
		{BotUuid: "bot-stale", Generation: 1, DesiredState: "running"},
	}, capacity, append(states, bot.BotState{ID: "bot-stale", Generation: 2}))

	require.Equal(t, []int{0}, plan.createIndexes)
	require.Len(t, plan.createConfigs, 1)
	require.Nil(t, plan.results[0])
	require.Equal(t, "stale_generation", plan.results[1].ErrorCode)
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

func newBotFleetTestServer(f botFleetManager) *Server {
	return &Server{botFleet: f, botBatchResults: make(map[string]*botBatchCacheEntry)}
}
