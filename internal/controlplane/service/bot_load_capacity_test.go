package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type fakeBotLoadCapacityRepository struct {
	nodes      []model.Node
	persistent func(excludeRunID uint) map[uint]int
}

func (r *fakeBotLoadCapacityRepository) ListBotLoadNodes(context.Context) ([]model.Node, error) {
	out := make([]model.Node, len(r.nodes))
	copy(out, r.nodes)
	return out, nil
}

func (r *fakeBotLoadCapacityRepository) PersistentBotLoadOccupancy(_ context.Context, excludeRunID uint) (map[uint]int, error) {
	if r.persistent == nil {
		return map[uint]int{}, nil
	}
	return r.persistent(excludeRunID), nil
}

type fakeBotLoadTunnelStatus map[string]bool

func (s fakeBotLoadTunnelStatus) Connected(nodeUUID string) bool {
	return s[nodeUUID]
}

type fakeBotLoadCapacityClient struct {
	mu        sync.Mutex
	responses map[string]BotLoadWorkerCapacity
	errors    map[string]error
	hook      func(context.Context, string) error
	calls     atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
}

func (c *fakeBotLoadCapacityClient) GetBotCapacity(ctx context.Context, nodeUUID string) (BotLoadWorkerCapacity, error) {
	c.calls.Add(1)
	active := c.active.Add(1)
	for {
		old := c.maxActive.Load()
		if active <= old || c.maxActive.CompareAndSwap(old, active) {
			break
		}
	}
	defer c.active.Add(-1)
	if c.hook != nil {
		if err := c.hook(ctx, nodeUUID); err != nil {
			return BotLoadWorkerCapacity{}, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.errors[nodeUUID]; err != nil {
		return BotLoadWorkerCapacity{}, err
	}
	return c.responses[nodeUUID], nil
}

func onlineBotLoadNode(id uint) model.Node {
	return model.Node{ID: id, UUID: fmt.Sprintf("node-%d", id), Name: fmt.Sprintf("节点-%d", id), Status: model.NodeStatusOnline}
}

func workerCapacity(now time.Time, maxBots, active int, generation int64) BotLoadWorkerCapacity {
	return BotLoadWorkerCapacity{
		Ready: true, MaxBots: maxBots, ActiveBots: active, CapacityGeneration: generation,
		BotWorkerVersion: "1.0.0", Features: []string{"fleet-v1"}, ObservedAt: now,
	}
}

func TestBotLoadCapacityDirectory_CacheRefreshAndSingleflight(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	repo := &fakeBotLoadCapacityRepository{nodes: []model.Node{onlineBotLoadNode(1)}}
	client := &fakeBotLoadCapacityClient{responses: map[string]BotLoadWorkerCapacity{"node-1": workerCapacity(clock.Now(), 50, 3, 7)}, errors: map[string]error{}}
	directory := NewBotLoadCapacityDirectory(repo, client, NewBotLoadReservationStore(clock, time.Minute), clock)

	first, err := directory.Snapshot(context.Background(), 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, client.calls.Load())
	second, err := directory.Snapshot(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, first.NodeCapacities, second.NodeCapacities)
	require.EqualValues(t, 1, client.calls.Load())

	clock.Advance(15*time.Second - time.Nanosecond)
	_, err = directory.Snapshot(context.Background(), 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, client.calls.Load())
	clock.Advance(2 * time.Nanosecond)
	client.responses["node-1"] = workerCapacity(clock.Now(), 50, 4, 7)
	_, err = directory.Snapshot(context.Background(), 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, client.calls.Load())

	shared := &botLoadCapacityRefresh{
		done: make(chan struct{}),
		result: botLoadCachedCapacity{
			capacity:  workerCapacity(clock.Now(), 50, 4, 7),
			fetchedAt: clock.Now(),
		},
	}
	directory.mu.Lock()
	directory.refreshing["node-1"] = shared
	directory.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := directory.loadWorkerCapacity(context.Background(), "node-1", true)
			require.NoError(t, got.err)
			require.Equal(t, shared.result.capacity, got.capacity)
		}()
	}
	close(shared.done)
	wg.Wait()
	require.EqualValues(t, 2, client.calls.Load())
	directory.mu.Lock()
	delete(directory.refreshing, "node-1")
	directory.mu.Unlock()

	repo.nodes = nil
	_, err = directory.Snapshot(context.Background(), 0)
	require.NoError(t, err)
	directory.mu.Lock()
	require.Empty(t, directory.cache)
	directory.mu.Unlock()
}

func TestBotLoadCapacityDirectory_ConcurrencyLimitAndTimeoutContract(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	nodes := make([]model.Node, 32)
	responses := make(map[string]BotLoadWorkerCapacity, len(nodes))
	for i := range nodes {
		nodes[i] = onlineBotLoadNode(uint(i + 1))
		responses[nodes[i].UUID] = workerCapacity(clock.Now(), 50, 0, 1)
	}
	gate := make(chan struct{})
	started := make(chan struct{}, 32)
	client := &fakeBotLoadCapacityClient{responses: responses, errors: map[string]error{}}
	client.hook = func(_ context.Context, nodeUUID string) error {
		started <- struct{}{}
		<-gate
		return nil
	}
	directory := NewBotLoadCapacityDirectory(&fakeBotLoadCapacityRepository{nodes: nodes}, client, nil, clock)
	done := make(chan error, 1)
	go func() {
		_, err := directory.Refresh(context.Background(), 0)
		done <- err
	}()
	for i := 0; i < 16; i++ {
		<-started
	}
	require.EqualValues(t, 16, client.maxActive.Load())
	close(gate)
	require.NoError(t, <-done)

	timeoutClient := &fakeBotLoadCapacityClient{responses: map[string]BotLoadWorkerCapacity{}, errors: map[string]error{}}
	timeoutClient.hook = func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		remaining := time.Until(deadline)
		require.Greater(t, remaining, 2500*time.Millisecond)
		require.LessOrEqual(t, remaining, 3*time.Second)
		return context.DeadlineExceeded
	}
	timeoutDirectory := NewBotLoadCapacityDirectory(&fakeBotLoadCapacityRepository{nodes: []model.Node{onlineBotLoadNode(1)}}, timeoutClient, nil, clock)
	snapshot, err := timeoutDirectory.Refresh(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, BotLoadUnavailableCapacityTimeout, snapshot.NodeCapacities[0].UnavailableReason)
}

func TestBotLoadCapacityDirectory_UnavailableReasonsAndReservations(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	nodes := []model.Node{
		onlineBotLoadNode(1),
		{ID: 2, UUID: "node-2", Name: "离线", Status: model.NodeStatusOffline},
		func() model.Node { n := onlineBotLoadNode(3); n.Maintenance = true; return n }(),
		onlineBotLoadNode(4), onlineBotLoadNode(5), onlineBotLoadNode(6), onlineBotLoadNode(7), onlineBotLoadNode(8),
	}
	client := &fakeBotLoadCapacityClient{
		responses: map[string]BotLoadWorkerCapacity{
			"node-1": workerCapacity(clock.Now(), 50, 8, 7),
			"node-4": {Ready: true, MaxBots: 50, CapacityGeneration: 1, ObservedAt: clock.Now()},
			"node-5": {Ready: true, Legacy: true, MaxBots: 50, CapacityGeneration: 1, ObservedAt: clock.Now()},
			"node-6": {Ready: false, MaxBots: 50, CapacityGeneration: 2, Features: []string{"fleet-v1"}, ObservedAt: clock.Now(), UnavailableReason: "bot-worker 依赖缺失"},
			"node-7": workerCapacity(clock.Now().Add(-16*time.Second), 50, 0, 3),
		},
		errors: map[string]error{"node-8": errors.New("rpc unavailable")},
	}
	repo := &fakeBotLoadCapacityRepository{nodes: nodes, persistent: func(exclude uint) map[uint]int {
		if exclude == 99 {
			return map[uint]int{1: 8}
		}
		return map[uint]int{1: 13}
	}}
	reservations := NewBotLoadReservationStore(clock, time.Minute)
	_, err := reservations.Replace(99, map[uint]int{1: 5}, map[uint]int{1: 50})
	require.NoError(t, err)
	directory := NewBotLoadCapacityDirectory(repo, client, reservations, clock)

	snapshot, err := directory.Refresh(context.Background(), 0)
	require.NoError(t, err)
	byID := capacityByNodeID(snapshot.NodeCapacities)
	require.Equal(t, 18, byID[1].ReservedBots)
	require.Equal(t, 24, byID[1].AvailableBots)
	require.Equal(t, BotLoadUnavailableNodeOffline, byID[2].UnavailableReason)
	require.Equal(t, BotLoadUnavailableNodeMaintenance, byID[3].UnavailableReason)
	require.True(t, byID[4].Legacy)
	require.Equal(t, BotLoadUnavailableFleetFeature, byID[4].UnavailableReason)
	require.True(t, byID[5].Legacy)
	require.Equal(t, BotLoadUnavailableLegacyWorker, byID[5].UnavailableReason)
	require.Equal(t, "bot-worker 依赖缺失", byID[6].UnavailableReason)
	require.Equal(t, BotLoadUnavailableSnapshotStale, byID[7].UnavailableReason)
	require.Equal(t, BotLoadUnavailableCapacityRPC, byID[8].UnavailableReason)

	excludingOwnRun, err := directory.Snapshot(context.Background(), 99)
	require.NoError(t, err)
	own := capacityByNodeID(excludingOwnRun.NodeCapacities)[1]
	require.Equal(t, 8, own.ReservedBots)
	require.Equal(t, 34, own.AvailableBots)
}

func TestBotLoadCapacityDirectory_UsesLiveTunnelStatus(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	node := onlineBotLoadNode(1)
	client := &fakeBotLoadCapacityClient{
		responses: map[string]BotLoadWorkerCapacity{node.UUID: workerCapacity(clock.Now(), 50, 0, 1)},
		errors:    map[string]error{},
	}
	directory := NewBotLoadCapacityDirectory(&fakeBotLoadCapacityRepository{nodes: []model.Node{node}}, client, nil, clock)
	directory.SetTunnelStatus(fakeBotLoadTunnelStatus{node.UUID: true})

	snapshot, err := directory.Refresh(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, snapshot.NodeCapacities, 1)
	require.True(t, snapshot.NodeCapacities[0].TunnelConnected)
}

func TestGormBotLoadCapacityRepository_MergesRemainingPersistentBatches(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:bot-load-capacity?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}))
	node := onlineBotLoadNode(1)
	require.NoError(t, db.Create(&node).Error)
	instance := model.Instance{NodeID: node.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/target", StartCommand: "java"}
	require.NoError(t, db.Create(&instance).Error)
	run1 := model.BotStressSession{InstanceID: instance.ID, Name: "run-1", NamePrefix: "r1", BotCount: 50}
	run2 := model.BotStressSession{InstanceID: instance.ID, Name: "run-2", NamePrefix: "r2", BotCount: 50}
	require.NoError(t, db.Create(&run1).Error)
	require.NoError(t, db.Create(&run2).Error)
	batches := []model.BotLoadBatch{
		{StressSessionID: run1.ID, ExecutorNodeID: node.ID, Ordinal: 1, PlannedCount: 50, AcceptedCount: 30, FailedCount: 5, State: model.BotLoadBatchRunning, IdempotencyKey: "run1-1", ConnectStartAt: time.Now()},
		{StressSessionID: run2.ID, ExecutorNodeID: node.ID, Ordinal: 1, PlannedCount: 20, FailedCount: 2, State: model.BotLoadBatchPlanned, IdempotencyKey: "run2-1", ConnectStartAt: time.Now()},
		{StressSessionID: run2.ID, ExecutorNodeID: node.ID, Ordinal: 2, PlannedCount: 7, AcceptedCount: 3, State: model.BotLoadBatchDispatching, IdempotencyKey: "run2-2", ConnectStartAt: time.Now()},
		{StressSessionID: run2.ID, ExecutorNodeID: node.ID, Ordinal: 3, PlannedCount: 10, State: model.BotLoadBatchStopped, IdempotencyKey: "run2-3", ConnectStartAt: time.Now()},
		{StressSessionID: run2.ID, ExecutorNodeID: node.ID, Ordinal: 4, PlannedCount: 5, AcceptedCount: 4, FailedCount: 3, State: model.BotLoadBatchRunning, IdempotencyKey: "run2-4", ConnectStartAt: time.Now()},
	}
	require.NoError(t, db.Create(&batches).Error)
	repo := newGormBotLoadCapacityRepository(db)

	all, err := repo.PersistentBotLoadOccupancy(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, 37, all[node.ID])
	excluded, err := repo.PersistentBotLoadOccupancy(context.Background(), run1.ID)
	require.NoError(t, err)
	require.Equal(t, 22, excluded[node.ID])
}

func capacityByNodeID(items []BotLoadNodeCapacity) map[uint]BotLoadNodeCapacity {
	out := make(map[uint]BotLoadNodeCapacity, len(items))
	for _, item := range items {
		out[item.NodeID] = item
	}
	return out
}
