package service

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
	"gorm.io/gorm"
)

type botFleetTestClock struct{ now time.Time }

func (c botFleetTestClock) Now() time.Time { return c.now }

type botFleetRuntimeHarness struct {
	db      *gorm.DB
	service *BotFleetRuntimeService
	node    *model.Node
	other   *model.Node
	session *model.BotStressSession
	bot     *model.Bot
	now     time.Time
}

func newBotFleetRuntimeHarness(t *testing.T) *botFleetRuntimeHarness {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "fleet.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}, &model.Bot{},
	))

	node := &model.Node{Name: "executor", Host: "127.0.0.1", Secret: "secret"}
	other := &model.Node{Name: "other", Host: "127.0.0.2", Secret: "secret"}
	require.NoError(t, db.Create(node).Error)
	require.NoError(t, db.Create(other).Error)
	instance := &model.Instance{NodeID: node.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{InstanceID: instance.ID, Name: "load", NamePrefix: "load", BotCount: 1}
	require.NoError(t, db.Create(session).Error)
	executorNodeID := node.ID
	bot := &model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, ExecutorNodeID: &executorNodeID,
		Name: "load-001", Status: model.BotStatusPending, DesiredStateGeneration: 3,
		ConfigHash: "desired-hash", CohortKey: "combat",
	}
	require.NoError(t, db.Create(bot).Error)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	return &botFleetRuntimeHarness{
		db: db, service: NewBotFleetRuntimeService(db, botFleetTestClock{now: now}),
		node: node, other: other, session: session, bot: bot, now: now,
	}
}

func (h *botFleetRuntimeHarness) snapshot(generation, epochGeneration, eventSeq int64, status string) *workerpb.BotRuntimeSnapshot {
	return &workerpb.BotRuntimeSnapshot{
		BotUuid: h.bot.UUID, SessionUuid: h.session.UUID, Generation: generation,
		ConfigHash: "desired-hash", WorkerEpoch: "epoch-current",
		WorkerEpochGeneration: epochGeneration, EventSeq: eventSeq, Status: status,
		ObservedAtUnixMs: h.now.UnixMilli(),
	}
}

func (h *botFleetRuntimeHarness) reload(t *testing.T) model.Bot {
	t.Helper()
	var bot model.Bot
	require.NoError(t, h.db.First(&bot, h.bot.ID).Error)
	return bot
}

func TestBotFleetRuntimeService_GenerationEpochAndSequenceGuards(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	ctx := context.Background()

	staleGeneration := h.snapshot(2, 1, 1, "connected")
	result, err := h.service.Ingest(ctx, h.node.ID, staleGeneration)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredStaleGeneration, result.Decision)

	higherGeneration := h.snapshot(4, 1, 1, "connected")
	result, err = h.service.Ingest(ctx, h.node.ID, higherGeneration)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeSnapshotRequired, result.Decision)
	require.True(t, result.SnapshotRequired)

	accepted := h.snapshot(3, 2, 8, "connected")
	result, err = h.service.Ingest(ctx, h.node.ID, accepted)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeApplied, result.Decision)

	duplicate := h.snapshot(3, 2, 8, "disconnected")
	result, err = h.service.Ingest(ctx, h.node.ID, duplicate)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredDuplicateEvent, result.Decision)

	outOfOrder := h.snapshot(3, 2, 7, "disconnected")
	result, err = h.service.Ingest(ctx, h.node.ID, outOfOrder)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredDuplicateEvent, result.Decision)

	staleEpoch := h.snapshot(3, 1, 99, "disconnected")
	result, err = h.service.Ingest(ctx, h.node.ID, staleEpoch)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredStaleEpoch, result.Decision)

	newEpoch := h.snapshot(3, 3, 1, "disconnected")
	newEpoch.WorkerEpoch = "epoch-new"
	newEpoch.LastError = "连接中断"
	result, err = h.service.Ingest(ctx, h.node.ID, newEpoch)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeApplied, result.Decision)

	loaded := h.reload(t)
	require.Equal(t, model.BotStatusDisconnected, loaded.Status)
	require.Equal(t, "连接中断", loaded.LastError)
	require.Equal(t, "epoch-new", loaded.WorkerEpoch)
	require.Equal(t, int64(3), loaded.WorkerEpochGeneration)
	require.Equal(t, int64(1), loaded.LastEventSeq)
	require.Equal(t, int64(3), loaded.DesiredStateGeneration)
}

func TestBotFleetRuntimeService_ConnectedAtSetOnceAndDesiredFieldsStayImmutable(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	ctx := context.Background()

	first := h.snapshot(3, 1, 1, "connected")
	first.LastError = ""
	result, err := h.service.Ingest(ctx, h.node.ID, first)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeApplied, result.Decision)

	later := h.now.Add(5 * time.Minute)
	second := h.snapshot(3, 1, 2, "disconnected")
	second.ObservedAtUnixMs = later.UnixMilli()
	second.LastError = "网络断开"
	result, err = h.service.Ingest(ctx, h.node.ID, second)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeApplied, result.Decision)

	loaded := h.reload(t)
	require.NotNil(t, loaded.ConnectedAt)
	require.Equal(t, h.now, loaded.ConnectedAt.UTC())
	require.NotNil(t, loaded.LastSeenAt)
	require.Equal(t, later, loaded.LastSeenAt.UTC())
	require.Equal(t, "desired-hash", loaded.ConfigHash)
	require.Equal(t, "combat", loaded.CohortKey)
	require.Equal(t, h.session.ID, *loaded.StressSessionID)

	mismatch := h.snapshot(3, 1, 3, "connected")
	mismatch.ConfigHash = "runtime-tampered"
	result, err = h.service.Ingest(ctx, h.node.ID, mismatch)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredConfigMismatch, result.Decision)
	require.True(t, result.SnapshotRequired)

	afterMismatch := h.reload(t)
	require.Equal(t, "desired-hash", afterMismatch.ConfigHash)
	require.Equal(t, int64(2), afterMismatch.LastEventSeq)
	require.Equal(t, model.BotStatusDisconnected, afterMismatch.Status)
}

func TestBotFleetRuntimeService_IgnoresUnknownBotExecutorAndSessionMismatch(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	ctx := context.Background()

	unknown := h.snapshot(3, 1, 1, "connected")
	unknown.BotUuid = "missing"
	result, err := h.service.Ingest(ctx, h.node.ID, unknown)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredBotMissing, result.Decision)

	result, err = h.service.Ingest(ctx, h.other.ID, h.snapshot(3, 1, 1, "connected"))
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredExecutorMismatch, result.Decision)

	wrongSession := h.snapshot(3, 1, 1, "connected")
	wrongSession.SessionUuid = "other-session"
	result, err = h.service.Ingest(ctx, h.node.ID, wrongSession)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredSessionMismatch, result.Decision)

	loaded := h.reload(t)
	require.Equal(t, model.BotStatusPending, loaded.Status)
	require.Zero(t, loaded.LastEventSeq)
}

func TestBotFleetRuntimeService_LegacyBotFallsBackToInstanceNode(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	legacy := &model.Bot{InstanceID: h.bot.InstanceID, Name: "legacy", Status: model.BotStatusPending}
	require.NoError(t, h.db.Create(legacy).Error)
	snapshot := &workerpb.BotRuntimeSnapshot{
		BotUuid: legacy.UUID, Generation: 1, WorkerEpoch: "legacy-epoch",
		WorkerEpochGeneration: 1, EventSeq: 1, Status: "connected",
		ObservedAtUnixMs: h.now.UnixMilli(),
	}

	result, err := h.service.Ingest(context.Background(), h.node.ID, snapshot)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeApplied, result.Decision)

	var loaded model.Bot
	require.NoError(t, h.db.First(&loaded, legacy.ID).Error)
	require.Equal(t, model.BotStatusConnected, loaded.Status)
	require.Equal(t, int64(1), loaded.WorkerEpochGeneration)
}

func TestBotFleetRuntimeService_ConcurrentUpdatesNeverRegress(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)

	lowEpochHighSeq := h.snapshot(3, 5, 100, "disconnected")
	lowEpochHighSeq.WorkerEpoch = "epoch-5"
	highEpochLowSeq := h.snapshot(3, 6, 1, "connected")
	highEpochLowSeq.WorkerEpoch = "epoch-6"

	for _, snapshot := range []*workerpb.BotRuntimeSnapshot{lowEpochHighSeq, highEpochLowSeq} {
		snapshot := snapshot
		go func() {
			<-start
			_, err := h.service.Ingest(ctx, h.node.ID, snapshot)
			results <- err
		}()
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)

	loaded := h.reload(t)
	require.Equal(t, int64(6), loaded.WorkerEpochGeneration)
	require.Equal(t, int64(1), loaded.LastEventSeq)
	require.Equal(t, "epoch-6", loaded.WorkerEpoch)
	require.Equal(t, model.BotStatusConnected, loaded.Status)

	var wg sync.WaitGroup
	sequenceErrors := make(chan error, 2)
	for _, seq := range []int64{9, 7} {
		seq := seq
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot := h.snapshot(3, 6, seq, "disconnected")
			snapshot.WorkerEpoch = "epoch-6"
			_, err := h.service.Ingest(ctx, h.node.ID, snapshot)
			sequenceErrors <- err
		}()
	}
	wg.Wait()
	close(sequenceErrors)
	for err := range sequenceErrors {
		require.NoError(t, err)
	}
	loaded = h.reload(t)
	require.Equal(t, int64(6), loaded.WorkerEpochGeneration)
	require.Equal(t, int64(9), loaded.LastEventSeq)
}

type botFleetFakeStream struct {
	mu         sync.Mutex
	events     []*workerpb.BotFleetEvent
	disconnect error
	operations *[]string
}

func (s *botFleetFakeStream) Recv() (*workerpb.BotFleetEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	if s.operations != nil {
		*s.operations = append(*s.operations, "disconnect")
	}
	if s.disconnect == nil {
		return nil, io.EOF
	}
	return nil, s.disconnect
}

type botFleetFakeClient struct {
	mu            sync.Mutex
	snapshot      *workerpb.GetBotFleetSnapshotResponse
	snapshotErr   error
	streams       []BotFleetRuntimeStream
	streamErr     error
	snapshotCalls int
	streamCalls   int
	operations    []string
}

func (c *botFleetFakeClient) GetBotFleetSnapshot(context.Context, string, string) (*workerpb.GetBotFleetSnapshotResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshotCalls++
	c.operations = append(c.operations, "snapshot")
	return c.snapshot, c.snapshotErr
}

func (c *botFleetFakeClient) StreamBotFleetEvents(context.Context, string, string) (BotFleetRuntimeStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamCalls++
	c.operations = append(c.operations, "stream")
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	if len(c.streams) == 0 {
		return &botFleetFakeStream{}, nil
	}
	stream := c.streams[0]
	c.streams = c.streams[1:]
	return stream, nil
}

type botFleetCapacitySink struct {
	nodeID     uint
	generation int64
}

func (s *botFleetCapacitySink) ObserveBotFleetCapacityGeneration(nodeID uint, generation int64) {
	s.nodeID = nodeID
	s.generation = generation
}

type botFleetSnapshotReconciler struct {
	mu    sync.Mutex
	calls int
}

func (r *botFleetSnapshotReconciler) ReconcileBotFleetSnapshot(context.Context, uint, string, string, *workerpb.GetBotFleetSnapshotResponse) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return nil
}

func (r *botFleetSnapshotReconciler) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestBotFleetRuntimeCoordinator_HigherGenerationRequestsSnapshotOnce(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	higher := h.snapshot(4, 1, 1, "connected")
	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{
		Bots: []*workerpb.BotRuntimeSnapshot{higher}, CapacityGeneration: 12,
	}}
	sink := &botFleetCapacitySink{}
	reconciler := &botFleetSnapshotReconciler{}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, client, sink)
	coordinator.SetSnapshotReconciler(reconciler)
	event := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_RuntimeSnapshot{RuntimeSnapshot: higher}}

	type handleResult struct {
		result BotFleetRuntimeResult
		err    error
	}
	results := make(chan handleResult, 2)
	for range 2 {
		go func() {
			result, err := coordinator.HandleEvent(context.Background(), h.node.ID, h.node.UUID, h.session.UUID, event)
			results <- handleResult{result: result, err: err}
		}()
	}
	for range 2 {
		handled := <-results
		require.NoError(t, handled.err)
		require.Equal(t, BotFleetRuntimeSnapshotRequired, handled.result.Decision)
	}
	require.Equal(t, 1, client.snapshotCalls)
	require.Equal(t, 1, reconciler.Calls())
	require.Equal(t, h.node.ID, sink.nodeID)
	require.Equal(t, int64(12), sink.generation)
	require.Equal(t, int64(3), h.reload(t).DesiredStateGeneration)
}

func TestBotFleetRuntimeCoordinator_DisconnectSnapshotsBeforeReconnect(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{Bots: []*workerpb.BotRuntimeSnapshot{h.snapshot(3, 1, 1, "connected")}}}
	disconnected := errors.New("流已断开")
	stream := &botFleetFakeStream{disconnect: disconnected, operations: &client.operations}
	reconnected := &botFleetFakeStream{}
	client.streams = []BotFleetRuntimeStream{reconnected}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, client, nil)

	got, err := coordinator.ConsumeUntilDisconnectAndReconnect(
		context.Background(), h.node.ID, h.node.UUID, h.session.UUID, stream,
	)
	require.NoError(t, err)
	require.Same(t, reconnected, got)
	require.Equal(t, []string{"disconnect", "snapshot", "stream"}, client.operations)
	require.Equal(t, model.BotStatusConnected, h.reload(t).Status)
}

func TestBotFleetRuntimeCoordinator_ActionEventIsSafelyIgnored(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	client := &botFleetFakeClient{}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, client, nil)
	action := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: &workerpb.BotActionEvent{
		BotUuid: h.bot.UUID, SessionUuid: h.session.UUID, Generation: 3, Status: "succeeded",
	}}}

	result, err := coordinator.HandleEvent(context.Background(), h.node.ID, h.node.UUID, h.session.UUID, action)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredActionEvent, result.Decision)
	require.Zero(t, client.snapshotCalls)
	require.Equal(t, model.BotStatusPending, h.reload(t).Status)
}
