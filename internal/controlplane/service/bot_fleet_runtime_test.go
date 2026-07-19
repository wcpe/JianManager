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
	batch   *model.BotLoadBatch
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
	batch := &model.BotLoadBatch{
		StressSessionID: session.ID, ExecutorNodeID: node.ID, Ordinal: 1, PlannedCount: 1,
		State: model.BotLoadBatchRunning, IdempotencyKey: "fleet-runtime-test", ConnectStartAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(batch).Error)
	executorNodeID, batchID := node.ID, batch.ID
	bot := &model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, ExecutorNodeID: &executorNodeID, LoadBatchID: &batchID,
		Name: "load-001", Status: model.BotStatusPending, DesiredStateGeneration: 3,
		ConfigHash: "desired-hash", CohortKey: "combat",
	}
	require.NoError(t, db.Create(bot).Error)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	return &botFleetRuntimeHarness{
		db: db, service: NewBotFleetRuntimeService(db, botFleetTestClock{now: now}),
		node: node, other: other, session: session, batch: batch, bot: bot, now: now,
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

func (h *botFleetRuntimeHarness) reloadBatch(t *testing.T) model.BotLoadBatch {
	t.Helper()
	var batch model.BotLoadBatch
	require.NoError(t, h.db.First(&batch, h.batch.ID).Error)
	return batch
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
	require.Equal(t, 1, h.reloadBatch(t).ConnectedCount)

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
	require.Equal(t, model.BotStatusDisconnected, loaded.Status)
	require.Zero(t, h.reloadBatch(t).ConnectedCount)
}

func TestBotFleetRuntimeService_BaselineMayResetEpochButEventsMayNot(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	require.NoError(t, h.db.Model(h.bot).Updates(map[string]any{
		"worker_epoch": "old", "worker_epoch_generation": 5, "last_event_seq": 9,
	}).Error)

	lower := h.snapshot(3, 1, 1, "connected")
	lower.WorkerEpoch = "restarted"
	result, err := h.service.Ingest(context.Background(), h.node.ID, lower)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredStaleEpoch, result.Decision)
	require.Equal(t, int64(5), h.reload(t).WorkerEpochGeneration)

	result, err = h.service.IngestBaseline(context.Background(), h.node.ID, lower)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeApplied, result.Decision)
	loaded := h.reload(t)
	require.Equal(t, int64(1), loaded.WorkerEpochGeneration)
	require.Equal(t, int64(1), loaded.LastEventSeq)
	require.Equal(t, "restarted", loaded.WorkerEpoch)
}

func TestBotFleetRuntimeService_ConnectedCountTracksTransitionsExactlyOnce(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	ctx := context.Background()

	for _, snapshot := range []*workerpb.BotRuntimeSnapshot{
		h.snapshot(3, 1, 1, "connected"),
		h.snapshot(3, 1, 1, "connected"),
		h.snapshot(3, 1, 2, "connected"),
	} {
		_, err := h.service.Ingest(ctx, h.node.ID, snapshot)
		require.NoError(t, err)
	}
	require.Equal(t, 1, h.reloadBatch(t).ConnectedCount)

	for _, snapshot := range []*workerpb.BotRuntimeSnapshot{
		h.snapshot(3, 1, 3, "disconnected"),
		h.snapshot(3, 1, 3, "disconnected"),
		h.snapshot(3, 1, 4, "stopped"),
	} {
		_, err := h.service.Ingest(ctx, h.node.ID, snapshot)
		require.NoError(t, err)
	}
	require.Zero(t, h.reloadBatch(t).ConnectedCount)
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

type blockingBotFleetRuntimeRepository struct {
	bot     *model.Bot
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingBotFleetRuntimeRepository) FindBotRuntime(context.Context, string) (*model.Bot, error) {
	return r.bot, nil
}

func (r *blockingBotFleetRuntimeRepository) ApplyBotRuntime(ctx context.Context, _ *model.Bot, _ uint, _ *workerpb.BotRuntimeSnapshot, _ model.BotStatus, _ time.Time, _ bool) (bool, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-r.release:
		return true, nil
	}
}

func (r *blockingBotFleetRuntimeRepository) ConvergeMissingRuntime(ctx context.Context, _ uint, _ string, _ []string, _ time.Time) error {
	return ctx.Err()
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

func TestBotFleetRuntimeCoordinator_EmptySnapshotDisconnectsGhostRuntime(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	require.NoError(t, h.db.Model(h.bot).Updates(map[string]any{
		"status": model.BotStatusConnected, "worker_epoch_generation": 5, "last_event_seq": 9,
	}).Error)
	require.NoError(t, h.db.Model(h.batch).Updates(map[string]any{"planned_count": 2, "connected_count": 1}).Error)
	executorNodeID, batchID := h.node.ID, h.batch.ID
	ghost := &model.Bot{
		InstanceID: h.bot.InstanceID, StressSessionID: &h.session.ID, ExecutorNodeID: &executorNodeID, LoadBatchID: &batchID,
		Name: "load-002", Status: model.BotStatusConnecting, DesiredStateGeneration: 3, ConfigHash: "other-hash",
	}
	require.NoError(t, h.db.Create(ghost).Error)
	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{ObservedAtUnixMs: h.now.UnixMilli()}}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, client, nil)

	require.NoError(t, coordinator.RefreshSnapshot(context.Background(), h.node.ID, h.node.UUID, h.session.UUID))
	var bots []model.Bot
	require.NoError(t, h.db.Where("stress_session_id = ?", h.session.ID).Order("id ASC").Find(&bots).Error)
	require.Len(t, bots, 2)
	require.Equal(t, model.BotStatusDisconnected, bots[0].Status)
	require.Equal(t, model.BotStatusDisconnected, bots[1].Status)
	require.NotNil(t, bots[0].LastSeenAt)
	require.NotNil(t, bots[1].LastSeenAt)
	require.Zero(t, h.reloadBatch(t).ConnectedCount)
}

func TestBotFleetRuntimeCoordinator_BaselineRecountsConnectedLedger(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	require.NoError(t, h.db.Model(h.bot).Updates(map[string]any{
		"status": model.BotStatusConnected, "worker_epoch": "epoch-1", "worker_epoch_generation": 1, "last_event_seq": 1,
	}).Error)
	require.NoError(t, h.db.Model(h.batch).Update("connected_count", 7).Error)
	baseline := h.snapshot(3, 1, 1, "connected")
	baseline.WorkerEpoch = "epoch-1"
	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{Bots: []*workerpb.BotRuntimeSnapshot{baseline}}}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, client, nil)

	require.NoError(t, coordinator.RefreshSnapshot(context.Background(), h.node.ID, h.node.UUID, h.session.UUID))
	require.Equal(t, model.BotStatusConnected, h.reload(t).Status)
	require.Equal(t, 1, h.reloadBatch(t).ConnectedCount)
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

type botFleetRuntimeObserverFunc func(context.Context, string) error

func (f botFleetRuntimeObserverFunc) ReconcileBotFleetRuntimeState(ctx context.Context, sessionUUID string) error {
	return f(ctx, sessionUUID)
}

type botFleetContextStream struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
}

func (s *botFleetContextStream) Recv() (*workerpb.BotFleetEvent, error) {
	s.once.Do(func() { close(s.started) })
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

type botFleetSubscriptionClient struct {
	mu            sync.Mutex
	snapshot      *workerpb.GetBotFleetSnapshotResponse
	operations    []string
	snapshotCalls int
	streamCalls   int
	streams       []*botFleetContextStream
}

func (c *botFleetSubscriptionClient) GetBotFleetSnapshot(context.Context, string, string) (*workerpb.GetBotFleetSnapshotResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshotCalls++
	c.operations = append(c.operations, "snapshot")
	return c.snapshot, nil
}

func (c *botFleetSubscriptionClient) StreamBotFleetEvents(ctx context.Context, _, _ string) (BotFleetRuntimeStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stream := &botFleetContextStream{ctx: ctx, started: make(chan struct{})}
	c.streamCalls++
	c.operations = append(c.operations, "stream")
	c.streams = append(c.streams, stream)
	return stream, nil
}

func (c *botFleetSubscriptionClient) state() ([]string, int, int, []*botFleetContextStream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.operations...), c.snapshotCalls, c.streamCalls, append([]*botFleetContextStream(nil), c.streams...)
}

type botFleetBlockedSnapshot struct {
	started  chan struct{}
	release  chan struct{}
	response *workerpb.GetBotFleetSnapshotResponse
}

type botFleetSequencedSubscriptionClient struct {
	mu        sync.Mutex
	snapshots []*botFleetBlockedSnapshot
	streams   []*botFleetContextStream
}

func (c *botFleetSequencedSubscriptionClient) GetBotFleetSnapshot(context.Context, string, string) (*workerpb.GetBotFleetSnapshotResponse, error) {
	c.mu.Lock()
	if len(c.snapshots) == 0 {
		c.mu.Unlock()
		return &workerpb.GetBotFleetSnapshotResponse{}, nil
	}
	snapshot := c.snapshots[0]
	c.snapshots = c.snapshots[1:]
	c.mu.Unlock()
	close(snapshot.started)
	<-snapshot.release
	return snapshot.response, nil
}

func (c *botFleetSequencedSubscriptionClient) StreamBotFleetEvents(ctx context.Context, _, _ string) (BotFleetRuntimeStream, error) {
	stream := &botFleetContextStream{ctx: ctx, started: make(chan struct{})}
	c.mu.Lock()
	c.streams = append(c.streams, stream)
	c.mu.Unlock()
	return stream, nil
}

func TestBotFleetSubscriptionManager_SnapshotBeforeStreamDeduplicatesAndCancels(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	client := &botFleetSubscriptionClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{
		Bots: []*workerpb.BotRuntimeSnapshot{h.snapshot(3, 1, 1, "connecting")},
	}}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, client, nil)
	manager := NewBotFleetSubscriptionManager(coordinator)
	t.Cleanup(manager.Close)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}

	manager.Ensure(target)
	manager.Ensure(target)
	require.Eventually(t, func() bool {
		operations, snapshots, streams, opened := client.state()
		return snapshots == 1 && streams == 1 && len(opened) == 1 && len(operations) == 2
	}, time.Second, 10*time.Millisecond)
	operations, snapshots, streams, opened := client.state()
	require.Equal(t, []string{"snapshot", "stream"}, operations)
	require.Equal(t, 1, snapshots)
	require.Equal(t, 1, streams)
	<-opened[0].started

	manager.StopSession(h.session.UUID)
	require.Eventually(t, func() bool { return manager.activeSubscriptionCount() == 0 }, time.Second, 10*time.Millisecond)
}

func TestBotFleetSubscriptionManager_ObserverMayCancelWithoutSelfDeadlock(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	coordinator := NewBotFleetRuntimeCoordinator(h.service, nil, nil)
	manager := NewBotFleetSubscriptionManager(coordinator)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}
	slot := manager.subscriptionSlot(target)
	ctx, cancel := context.WithCancel(context.Background())
	slot.gate.Lock()
	slot.active = true
	slot.generation = 1
	slot.cancel = cancel
	slot.gate.Unlock()
	coordinator.SetRuntimeObserver(botFleetRuntimeObserverFunc(func(context.Context, string) error {
		manager.StopSession(h.session.UUID)
		return nil
	}))
	event := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_RuntimeSnapshot{RuntimeSnapshot: h.snapshot(3, 1, 1, "stopped")}}

	done := make(chan error, 1)
	go func() {
		_, err := manager.handleSubscriptionEvent(ctx, target, 1, event)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
		require.Zero(t, manager.activeSubscriptionCount())
		manager.Close()
	case <-time.After(time.Second):
		t.Fatal("observer 取消订阅发生自锁")
	}
}

type blockingFleetActionHandler struct {
	firstActionID string
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	secondSeen    chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
}

func (h *blockingFleetActionHandler) Ingest(_ context.Context, _ uint, _ string, event *workerpb.BotActionEvent) (ActionResultIngestResult, error) {
	if event.ActionRunId == h.firstActionID {
		h.firstOnce.Do(func() { close(h.firstStarted) })
		<-h.releaseFirst
	} else {
		h.secondOnce.Do(func() { close(h.secondSeen) })
	}
	return actionIngestResult(ActionResultApplied, event, "测试动作已处理"), nil
}

func TestBotFleetSubscriptionManager_ActionHandlerDoesNotBlockLaterEventsOrStop(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	handler := &blockingFleetActionHandler{
		firstActionID: "action-blocking", firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}), secondSeen: make(chan struct{}),
	}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, nil, nil)
	coordinator.SetActionEventHandler(handler)
	manager := NewBotFleetSubscriptionManager(coordinator)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}
	slot := manager.subscriptionSlot(target)
	ctx, cancel := context.WithCancel(context.Background())
	slot.gate.Lock()
	slot.active, slot.generation, slot.cancel = true, 1, cancel
	slot.gate.Unlock()
	first := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: &workerpb.BotActionEvent{ActionRunId: "action-blocking", BotUuid: h.bot.UUID}}}

	_, err := manager.handleSubscriptionEvent(ctx, target, 1, first)
	require.NoError(t, err)
	select {
	case <-handler.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("订阅实流未分流 action_event")
	}
	second := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: &workerpb.BotActionEvent{ActionRunId: "action-second", BotUuid: h.bot.UUID}}}
	_, err = manager.handleSubscriptionEvent(ctx, target, 1, second)
	require.NoError(t, err)
	select {
	case <-handler.secondSeen:
	case <-time.After(time.Second):
		t.Fatal("阻塞 action handler 阻塞了后续 action_event")
	}
	runtimeEvent := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_RuntimeSnapshot{RuntimeSnapshot: h.snapshot(3, 1, 1, "connected")}}
	_, err = manager.handleSubscriptionEvent(ctx, target, 1, runtimeEvent)
	require.NoError(t, err)
	require.Equal(t, model.BotStatusConnected, h.reload(t).Status)

	stopped := make(chan struct{})
	go func() { manager.StopSession(h.session.UUID); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("阻塞 action handler 阻塞了 StopSession")
	}
	close(handler.releaseFirst)
	manager.Close()
}

func TestBotFleetSubscriptionManager_AcceptedActionDrainsAcrossGenerationAdvance(t *testing.T) {
	h := newBotActionResultHarness(t)
	coordinator := NewBotFleetRuntimeCoordinator(NewBotFleetRuntimeService(h.db, botFleetTestClock{now: h.now}), nil, nil)
	coordinator.SetActionEventHandler(h.service)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}
	slot := &botFleetSubscriptionSlot{target: target, generation: 1, active: true}
	manager := &BotFleetSubscriptionManager{
		coordinator: coordinator, rootCtx: context.Background(), actionQueue: make(chan botFleetActionDispatch, 1),
		slots: map[string]*botFleetSubscriptionSlot{botFleetSubscriptionKey(target): slot},
	}
	event := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: h.event("running")}}
	streamCtx, cancelStream := context.WithCancel(context.Background())

	result, err := manager.handleSubscriptionEvent(streamCtx, target, 1, event)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeActionDispatched, result.Decision)
	cancelStream()
	slot.gate.Lock()
	slot.generation = 2
	slot.gate.Unlock()
	manager.handleDispatchedAction(<-manager.actionQueue)
	require.Equal(t, model.BotLoadActionRunning, h.reload(t).Status)
}

func TestBotFleetSubscriptionManager_StaleGenerationDropsActionBeforeDispatch(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	handler := &blockingFleetActionHandler{
		firstActionID: "stale-action", firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}), secondSeen: make(chan struct{}),
	}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, nil, nil)
	coordinator.SetActionEventHandler(handler)
	manager := NewBotFleetSubscriptionManager(coordinator)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}
	slot := manager.subscriptionSlot(target)
	slot.gate.Lock()
	slot.active, slot.generation = true, 2
	slot.gate.Unlock()
	event := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: &workerpb.BotActionEvent{ActionRunId: "stale-action", BotUuid: h.bot.UUID}}}

	result, err := manager.handleSubscriptionEvent(context.Background(), target, 1, event)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredStaleSubscription, result.Decision)
	select {
	case <-handler.firstStarted:
		t.Fatal("旧订阅 generation 的 action_event 被错误分流")
	case <-time.After(100 * time.Millisecond):
	}
	manager.Close()
}

func TestBotFleetSubscriptionManager_RealStreamPersistsActionStartAndTerminal(t *testing.T) {
	h := newBotActionResultHarness(t)
	start := h.event("running")
	terminal := h.event("succeeded")
	terminal.DurationMs = 250
	terminal.ObservedAtUnixMs = h.now.Add(250 * time.Millisecond).UnixMilli()
	stream := &botFleetFakeStream{events: []*workerpb.BotFleetEvent{
		{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: start}},
		{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: terminal}},
	}}
	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{}, streams: []BotFleetRuntimeStream{stream}}
	coordinator := NewBotFleetRuntimeCoordinator(NewBotFleetRuntimeService(h.db, botFleetTestClock{now: h.now}), client, nil)
	coordinator.SetActionEventHandler(h.service)
	manager := NewBotFleetSubscriptionManager(coordinator)
	t.Cleanup(manager.Close)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}

	manager.Ensure(target)
	require.Eventually(t, func() bool {
		var result model.BotLoadActionResult
		err := h.db.Where("action_run_id = ?", start.ActionRunId).First(&result).Error
		return err == nil && result.Status == model.BotLoadActionSucceeded && result.DurationMS == 250
	}, time.Second, 10*time.Millisecond)
	manager.StopSession(h.session.UUID)
}

func TestBotFleetSubscriptionManager_DisconnectReplayPersistsAcrossGenerationAdvance(t *testing.T) {
	h := newBotActionResultHarness(t)
	start := h.event("running")
	terminal := h.event("succeeded")
	terminal.DurationMs = 250
	terminal.ObservedAtUnixMs = h.now.Add(250 * time.Millisecond).UnixMilli()
	client := &botFleetFakeClient{
		snapshot: &workerpb.GetBotFleetSnapshotResponse{},
		streams: []BotFleetRuntimeStream{
			&botFleetFakeStream{disconnect: io.EOF},
			&botFleetFakeStream{events: []*workerpb.BotFleetEvent{
				{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: start}},
				{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: terminal}},
			}, disconnect: io.EOF},
		},
	}
	coordinator := NewBotFleetRuntimeCoordinator(NewBotFleetRuntimeService(h.db, botFleetTestClock{now: h.now}), client, nil)
	coordinator.SetActionEventHandler(h.service)
	manager := NewBotFleetSubscriptionManager(coordinator)
	t.Cleanup(manager.Close)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}

	manager.Ensure(target)
	require.Eventually(t, func() bool {
		var result model.BotLoadActionResult
		err := h.db.Where("action_run_id = ?", start.ActionRunId).First(&result).Error
		return err == nil && result.Status == model.BotLoadActionSucceeded && result.DurationMS == 250
	}, 2*time.Second, 10*time.Millisecond)
	require.GreaterOrEqual(t, manager.subscriptionGeneration(target), uint64(2))
	manager.StopSession(h.session.UUID)
}

func TestBotFleetSubscriptionManager_RealStreamRoutesBarrierArrival(t *testing.T) {
	h := newBotActionResultHarness(t)
	setBarrierScenario(t, h, time.Minute, ScenarioBarrierRelease{Type: "all"}, "fail")
	clock := &botLoadFakeClock{now: h.now}
	barriers := NewBarrierCoordinator(clock)
	events := NewScenarioActionEventService(
		h.service,
		barriers,
		NewActionSignalRouter(h.service, &fakeActionSignalClient{handler: acceptActionSignals}, clock),
		fixedBarrierExpectedBots{h.bot.UUID: 3},
	)
	t.Cleanup(events.Close)
	action := barrierActionEvent(h.bot.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000610", testActionCorrelationToken, h.now)
	action.ResultJson = mustBarrierPayload(t, authoritativeBarrierPayload(h.now, time.Minute, ScenarioBarrierRelease{Type: "all"}, "fail"))
	stream := &botFleetFakeStream{events: []*workerpb.BotFleetEvent{{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: action}}}}
	client := &botFleetFakeClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{}, streams: []BotFleetRuntimeStream{stream}}
	coordinator := NewBotFleetRuntimeCoordinator(NewBotFleetRuntimeService(h.db, botFleetTestClock{now: h.now}), client, nil)
	coordinator.SetActionEventHandler(events)
	manager := NewBotFleetSubscriptionManager(coordinator)
	t.Cleanup(manager.Close)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}

	manager.Ensure(target)
	scope := BarrierScope{RunID: h.session.UUID, StageIndex: 0, CohortKey: "combat", BarrierKey: "ready", Round: 1}
	require.Eventually(t, func() bool {
		var result model.BotLoadActionResult
		err := h.db.Where("action_run_id = ?", action.ActionRunId).First(&result).Error
		return err == nil && barriers.Exists(scope)
	}, time.Second, 10*time.Millisecond)
	manager.StopSession(h.session.UUID)
}

func TestBotFleetSubscriptionManager_SnapshotApplyDoesNotHoldSubscriptionGate(t *testing.T) {
	executorNodeID := uint(1)
	sessionID := uint(1)
	repository := &blockingBotFleetRuntimeRepository{
		bot: &model.Bot{
			UUID: "bot-gate", ExecutorNodeID: &executorNodeID, StressSessionID: &sessionID,
			StressSession: &model.BotStressSession{UUID: "run-gate"}, DesiredStateGeneration: 1, ConfigHash: "hash-gate",
		},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	client := &botFleetFakeClient{
		snapshot: &workerpb.GetBotFleetSnapshotResponse{Bots: []*workerpb.BotRuntimeSnapshot{{
			BotUuid: "bot-gate", SessionUuid: "run-gate", Generation: 1, ConfigHash: "hash-gate", Status: "connected",
		}}},
	}
	coordinator := NewBotFleetRuntimeCoordinator(NewBotFleetRuntimeServiceWithRepository(repository, nil), client, nil)
	manager := NewBotFleetSubscriptionManager(coordinator)
	target := BotFleetSubscriptionTarget{NodeID: executorNodeID, NodeUUID: "node-gate", SessionUUID: "run-gate"}
	manager.Ensure(target)
	select {
	case <-repository.started:
	case <-time.After(time.Second):
		close(repository.release)
		manager.Close()
		t.Fatal("baseline 未进入同步仓储写入")
	}

	stopped := make(chan struct{})
	go func() {
		manager.StopSession(target.SessionUUID)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(100 * time.Millisecond):
		close(repository.release)
		<-stopped
		manager.Close()
		t.Fatal("同步 baseline 数据库写入持有了订阅 gate")
	}
	close(repository.release)
	manager.Close()
}

func TestBotFleetSubscriptionManager_LateOldBaselineIsDiscardedBeforeApply(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	require.NoError(t, h.db.Model(h.bot).Updates(map[string]any{
		"status": model.BotStatusConnected, "worker_epoch": "initial", "worker_epoch_generation": 5, "last_event_seq": 9,
	}).Error)
	require.NoError(t, h.db.Model(h.batch).Update("connected_count", 1).Error)
	late := h.snapshot(3, 6, 1, "disconnected")
	late.WorkerEpoch = "late-old-baseline"
	restart := h.snapshot(3, 1, 1, "connected")
	restart.WorkerEpoch = "restart"
	first := &botFleetBlockedSnapshot{started: make(chan struct{}), release: make(chan struct{}), response: &workerpb.GetBotFleetSnapshotResponse{Bots: []*workerpb.BotRuntimeSnapshot{late}}}
	second := &botFleetBlockedSnapshot{started: make(chan struct{}), release: make(chan struct{}), response: &workerpb.GetBotFleetSnapshotResponse{Bots: []*workerpb.BotRuntimeSnapshot{restart}}}
	client := &botFleetSequencedSubscriptionClient{snapshots: []*botFleetBlockedSnapshot{first, second}}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, client, nil)
	manager := NewBotFleetSubscriptionManager(coordinator)
	t.Cleanup(manager.Close)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}

	manager.Ensure(target)
	<-first.started
	oldGeneration := manager.subscriptionGeneration(target)
	manager.StopSession(h.session.UUID)
	manager.Ensure(target)
	<-second.started
	require.Greater(t, manager.subscriptionGeneration(target), oldGeneration)

	close(first.release)
	require.Never(t, func() bool {
		return h.reload(t).WorkerEpoch == "late-old-baseline"
	}, 100*time.Millisecond, 10*time.Millisecond)
	loaded := h.reload(t)
	require.Equal(t, "initial", loaded.WorkerEpoch)
	require.Equal(t, model.BotStatusConnected, loaded.Status)
	require.Equal(t, 1, h.reloadBatch(t).ConnectedCount)

	close(second.release)
	require.Eventually(t, func() bool {
		loaded = h.reload(t)
		return loaded.WorkerEpochGeneration == 1 && loaded.WorkerEpoch == "restart"
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, model.BotStatusConnected, loaded.Status)
	require.Equal(t, 1, h.reloadBatch(t).ConnectedCount)
}

func TestBotFleetSubscriptionManager_OldGenerationCannotOverwriteRestartBaseline(t *testing.T) {
	h := newBotFleetRuntimeHarness(t)
	require.NoError(t, h.db.Model(h.bot).Updates(map[string]any{
		"status": model.BotStatusConnected, "worker_epoch": "old", "worker_epoch_generation": 5, "last_event_seq": 9,
	}).Error)
	require.NoError(t, h.db.Model(h.batch).Update("connected_count", 1).Error)
	baseline := h.snapshot(3, 1, 1, "connected")
	baseline.WorkerEpoch = "restart"
	client := &botFleetSubscriptionClient{snapshot: &workerpb.GetBotFleetSnapshotResponse{Bots: []*workerpb.BotRuntimeSnapshot{baseline}}}
	coordinator := NewBotFleetRuntimeCoordinator(h.service, client, nil)
	manager := NewBotFleetSubscriptionManager(coordinator)
	t.Cleanup(manager.Close)
	target := BotFleetSubscriptionTarget{NodeID: h.node.ID, NodeUUID: h.node.UUID, SessionUUID: h.session.UUID}

	manager.Ensure(target)
	require.Eventually(t, func() bool { return h.reload(t).WorkerEpochGeneration == 1 }, time.Second, 10*time.Millisecond)
	oldGeneration := manager.subscriptionGeneration(target)
	manager.StopSession(h.session.UUID)
	manager.Ensure(target)
	require.Eventually(t, func() bool {
		_, snapshots, _, _ := client.state()
		return snapshots >= 2
	}, 2*time.Second, 10*time.Millisecond)
	require.Greater(t, manager.subscriptionGeneration(target), oldGeneration)

	late := h.snapshot(3, 6, 1, "disconnected")
	late.WorkerEpoch = "late-old-stream"
	result, err := manager.handleSubscriptionEvent(context.Background(), target, oldGeneration, &workerpb.BotFleetEvent{
		Event: &workerpb.BotFleetEvent_RuntimeSnapshot{RuntimeSnapshot: late},
	})
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeIgnoredStaleSubscription, result.Decision)
	loaded := h.reload(t)
	require.Equal(t, int64(1), loaded.WorkerEpochGeneration)
	require.Equal(t, "restart", loaded.WorkerEpoch)
	require.Equal(t, model.BotStatusConnected, loaded.Status)
	require.Equal(t, 1, h.reloadBatch(t).ConnectedCount)
}
