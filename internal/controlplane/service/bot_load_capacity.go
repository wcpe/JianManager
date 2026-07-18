package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const (
	botLoadCapacityCacheTTL    = 15 * time.Second
	botLoadCapacitySnapshotAge = 15 * time.Second
	botLoadCapacityNodeTimeout = 3 * time.Second
	botLoadCapacityConcurrency = 16
)

var errBotLoadWorkerMissing = errors.New("Worker 未连接")

// BotLoadCapacityRepository 提供候选节点与数据库持久批次占用。
type BotLoadCapacityRepository interface {
	ListBotLoadNodes(ctx context.Context) ([]model.Node, error)
	PersistentBotLoadOccupancy(ctx context.Context, excludeRunID uint) (map[uint]int, error)
}

// BotLoadCapacityClient 隔离连接池/proto，使目录可用 fake client 验证并发和超时。
type BotLoadCapacityClient interface {
	GetBotCapacity(ctx context.Context, nodeUUID string) (BotLoadWorkerCapacity, error)
}

// BotLoadTunnelStatusProvider 提供节点反向隧道的实时在线状态。
type BotLoadTunnelStatusProvider interface {
	Connected(nodeUUID string) bool
}

type gormBotLoadCapacityRepository struct {
	db *gorm.DB
}

func newGormBotLoadCapacityRepository(db *gorm.DB) *gormBotLoadCapacityRepository {
	return &gormBotLoadCapacityRepository{db: db}
}

func (r *gormBotLoadCapacityRepository) ListBotLoadNodes(ctx context.Context) ([]model.Node, error) {
	var nodes []model.Node
	if err := r.db.WithContext(ctx).Order("id ASC").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询 Bot 发压节点失败: %w", err)
	}
	return nodes, nil
}

func (r *gormBotLoadCapacityRepository) PersistentBotLoadOccupancy(ctx context.Context, excludeRunID uint) (map[uint]int, error) {
	var batches []model.BotLoadBatch
	query := r.db.WithContext(ctx).Select("executor_node_id", "planned_count", "accepted_count", "failed_count").
		Where("state IN ?", []model.BotLoadBatchState{model.BotLoadBatchPlanned, model.BotLoadBatchDispatching, model.BotLoadBatchRunning})
	if excludeRunID != 0 {
		query = query.Where("stress_session_id <> ?", excludeRunID)
	}
	if err := query.Find(&batches).Error; err != nil {
		return nil, fmt.Errorf("查询 Bot 运行批次占用失败: %w", err)
	}
	return sumPersistentBotLoadOccupancy(batches), nil
}

func sumPersistentBotLoadOccupancy(batches []model.BotLoadBatch) map[uint]int {
	out := make(map[uint]int)
	for _, batch := range batches {
		remaining := max(0, batch.PlannedCount-batch.AcceptedCount-batch.FailedCount)
		out[batch.ExecutorNodeID] += remaining
	}
	return out
}

type poolBotLoadCapacityClient struct {
	pool *cpgrpc.ClientPool
}

func (c poolBotLoadCapacityClient) GetBotCapacity(ctx context.Context, nodeUUID string) (BotLoadWorkerCapacity, error) {
	if c.pool == nil {
		return BotLoadWorkerCapacity{}, errBotLoadWorkerMissing
	}
	client, ok := c.pool.Get(nodeUUID)
	if !ok || client.Worker == nil {
		return BotLoadWorkerCapacity{}, errBotLoadWorkerMissing
	}
	response, err := client.Worker.GetBotCapacity(ctx, &workerpb.GetBotCapacityRequest{})
	if err != nil {
		return BotLoadWorkerCapacity{}, err
	}
	return botLoadWorkerCapacityFromProto(response), nil
}

func botLoadWorkerCapacityFromProto(response *workerpb.GetBotCapacityResponse) BotLoadWorkerCapacity {
	if response == nil {
		return BotLoadWorkerCapacity{}
	}
	observedAt := time.Time{}
	if response.ObservedAtUnixMs > 0 {
		observedAt = time.UnixMilli(response.ObservedAtUnixMs).UTC()
	}
	return BotLoadWorkerCapacity{
		Ready: response.Ready, Legacy: response.Legacy, MaxBots: int(response.MaxBots),
		ActiveBots: int(response.ActiveBots), ConnectingBots: int(response.ConnectingBots),
		CapacityGeneration: response.CapacityGeneration, WorkerEpoch: response.WorkerEpoch,
		WorkerEpochGeneration: response.WorkerEpochGeneration, BotWorkerVersion: response.BotWorkerVersion,
		RSSBytes: response.RssBytes, EventLoopP95MS: response.EventLoopP95Ms, ObservedAt: observedAt,
		UnavailableReason: response.UnavailableReason, Features: append([]string(nil), response.Features...),
	}
}

type botLoadCachedCapacity struct {
	capacity  BotLoadWorkerCapacity
	err       error
	fetchedAt time.Time
}

type botLoadCapacityRefresh struct {
	done   chan struct{}
	result botLoadCachedCapacity
}

// BotLoadCapacityDirectory 并发聚合 Worker 容量、15 秒缓存和软/持久预留。
type BotLoadCapacityDirectory struct {
	repository   BotLoadCapacityRepository
	client       BotLoadCapacityClient
	tunnelStatus BotLoadTunnelStatusProvider
	reservations *BotLoadReservationStore
	clock        BotLoadClock
	cacheTTL     time.Duration
	snapshotAge  time.Duration
	nodeTimeout  time.Duration
	semaphore    chan struct{}

	mu         sync.Mutex
	cache      map[string]botLoadCachedCapacity
	refreshing map[string]*botLoadCapacityRefresh
}

// NewBotLoadCapacityDirectory 创建可注入仓储/client/时钟的容量目录。
func NewBotLoadCapacityDirectory(repository BotLoadCapacityRepository, client BotLoadCapacityClient, reservations *BotLoadReservationStore, clock BotLoadClock) *BotLoadCapacityDirectory {
	return &BotLoadCapacityDirectory{
		repository: repository, client: client, reservations: reservations, clock: normalizeBotLoadClock(clock),
		cacheTTL: botLoadCapacityCacheTTL, snapshotAge: botLoadCapacitySnapshotAge,
		nodeTimeout: botLoadCapacityNodeTimeout, semaphore: make(chan struct{}, botLoadCapacityConcurrency),
		cache: make(map[string]botLoadCachedCapacity), refreshing: make(map[string]*botLoadCapacityRefresh),
	}
}

// NewGRPCBotLoadCapacityDirectory 使用 GORM 和既有隧道优先连接池装配目录。
func NewGRPCBotLoadCapacityDirectory(db *gorm.DB, pool *cpgrpc.ClientPool, reservations *BotLoadReservationStore, clock BotLoadClock) *BotLoadCapacityDirectory {
	return NewBotLoadCapacityDirectory(newGormBotLoadCapacityRepository(db), poolBotLoadCapacityClient{pool: pool}, reservations, clock)
}

// SetTunnelStatus 注入反向隧道实时状态源。
func (d *BotLoadCapacityDirectory) SetTunnelStatus(status BotLoadTunnelStatusProvider) {
	d.tunnelStatus = status
}

// Snapshot 返回缓存优先的目录，并实时合并 DB/内存预留。
func (d *BotLoadCapacityDirectory) Snapshot(ctx context.Context, excludeRunID uint) (*BotLoadCapacitySnapshot, error) {
	return d.snapshot(ctx, excludeRunID, false)
}

// Refresh 强制刷新 Worker 快照；同节点并发刷新仍折叠成一次 RPC。
func (d *BotLoadCapacityDirectory) Refresh(ctx context.Context, excludeRunID uint) (*BotLoadCapacitySnapshot, error) {
	return d.snapshot(ctx, excludeRunID, true)
}

func (d *BotLoadCapacityDirectory) snapshot(ctx context.Context, excludeRunID uint, force bool) (*BotLoadCapacitySnapshot, error) {
	if d == nil || d.repository == nil {
		return nil, fmt.Errorf("Bot 负载容量目录未装配")
	}
	nodes, err := d.repository.ListBotLoadNodes(ctx)
	if err != nil {
		return nil, err
	}
	d.pruneCache(nodes)
	persistent, err := d.repository.PersistentBotLoadOccupancy(ctx, excludeRunID)
	if err != nil {
		return nil, err
	}
	memory := map[uint]int{}
	if d.reservations != nil {
		memory = d.reservations.Snapshot(excludeRunID)
	}
	capacities := d.collectWorkerCapacities(ctx, nodes, force)
	return d.mergeCapacitySnapshot(nodes, capacities, persistent, memory), nil
}

func (d *BotLoadCapacityDirectory) collectWorkerCapacities(ctx context.Context, nodes []model.Node, force bool) map[uint]botLoadCachedCapacity {
	candidates := selectableBotLoadCapacityNodes(nodes)
	out := make(map[uint]botLoadCachedCapacity, len(candidates))
	jobs := make(chan model.Node)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range min(len(candidates), botLoadCapacityConcurrency) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for node := range jobs {
				result := d.loadWorkerCapacity(ctx, node.UUID, force)
				mu.Lock()
				out[node.ID] = result
				mu.Unlock()
			}
		}()
	}
	for _, node := range candidates {
		jobs <- node
	}
	close(jobs)
	wg.Wait()
	return out
}

func selectableBotLoadCapacityNodes(nodes []model.Node) []model.Node {
	out := make([]model.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Status == model.NodeStatusOnline && !node.Maintenance {
			out = append(out, node)
		}
	}
	return out
}

func (d *BotLoadCapacityDirectory) pruneCache(nodes []model.Node) {
	current := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		current[node.UUID] = struct{}{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for nodeUUID := range d.cache {
		if _, ok := current[nodeUUID]; !ok {
			delete(d.cache, nodeUUID)
		}
	}
}

func (d *BotLoadCapacityDirectory) loadWorkerCapacity(ctx context.Context, nodeUUID string, force bool) botLoadCachedCapacity {
	d.mu.Lock()
	if cached, ok := d.cache[nodeUUID]; ok && !force && d.clock.Now().Sub(cached.fetchedAt) < d.cacheTTL {
		d.mu.Unlock()
		return cached
	}
	if refresh, ok := d.refreshing[nodeUUID]; ok {
		d.mu.Unlock()
		select {
		case <-refresh.done:
			return refresh.result
		case <-ctx.Done():
			return botLoadCachedCapacity{err: ctx.Err(), fetchedAt: d.clock.Now()}
		}
	}
	refresh := &botLoadCapacityRefresh{done: make(chan struct{})}
	d.refreshing[nodeUUID] = refresh
	d.mu.Unlock()

	result := d.fetchWorkerCapacity(ctx, nodeUUID)
	d.mu.Lock()
	d.cache[nodeUUID] = result
	refresh.result = result
	delete(d.refreshing, nodeUUID)
	close(refresh.done)
	d.mu.Unlock()
	return result
}

func (d *BotLoadCapacityDirectory) fetchWorkerCapacity(ctx context.Context, nodeUUID string) botLoadCachedCapacity {
	if d.client == nil {
		return botLoadCachedCapacity{err: errBotLoadWorkerMissing, fetchedAt: d.clock.Now()}
	}
	select {
	case d.semaphore <- struct{}{}:
		defer func() { <-d.semaphore }()
	case <-ctx.Done():
		return botLoadCachedCapacity{err: ctx.Err(), fetchedAt: d.clock.Now()}
	}
	nodeCtx, cancel := context.WithTimeout(ctx, d.nodeTimeout)
	defer cancel()
	capacity, err := d.client.GetBotCapacity(nodeCtx, nodeUUID)
	return botLoadCachedCapacity{capacity: capacity, err: err, fetchedAt: d.clock.Now()}
}

func (d *BotLoadCapacityDirectory) mergeCapacitySnapshot(nodes []model.Node, raw map[uint]botLoadCachedCapacity, persistent, memory map[uint]int) *BotLoadCapacitySnapshot {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	snapshot := &BotLoadCapacitySnapshot{
		NodeCapacities:    make([]BotLoadNodeCapacity, 0, len(nodes)),
		ReservationLimits: make(map[uint]int, len(nodes)), UpdatedAt: d.clock.Now().UTC(),
	}
	for _, node := range nodes {
		reserved := persistent[node.ID] + memory[node.ID]
		capacity, limit := d.mergeNodeCapacity(node, raw[node.ID], reserved, persistent[node.ID])
		snapshot.NodeCapacities = append(snapshot.NodeCapacities, capacity)
		snapshot.ReservationLimits[node.ID] = limit
	}
	return snapshot
}

func (d *BotLoadCapacityDirectory) tunnelConnected(nodeUUID string) bool {
	return d.tunnelStatus != nil && d.tunnelStatus.Connected(nodeUUID)
}

func (d *BotLoadCapacityDirectory) mergeNodeCapacity(node model.Node, cached botLoadCachedCapacity, reserved, persistent int) (BotLoadNodeCapacity, int) {
	capacity := BotLoadNodeCapacity{
		NodeID: node.ID, NodeUUID: node.UUID, NodeName: node.Name,
		Online: node.Status == model.NodeStatusOnline, TunnelConnected: d.tunnelConnected(node.UUID),
		ReservedBots: reserved, LastHeartbeatAt: node.LastHeartbeat,
	}
	if !capacity.Online {
		capacity.UnavailableReason = BotLoadUnavailableNodeOffline
		return capacity, 0
	}
	if node.Maintenance {
		capacity.UnavailableReason = BotLoadUnavailableNodeMaintenance
		return capacity, 0
	}
	if cached.err != nil {
		capacity.UnavailableReason = botLoadCapacityErrorReason(cached.err)
		return capacity, 0
	}
	d.applyWorkerCapacity(&capacity, cached.capacity)
	if capacity.UnavailableReason != "" {
		return capacity, 0
	}
	limit := max(0, capacity.MaxBots-capacity.ActiveBots-persistent)
	capacity.AvailableBots = max(0, capacity.MaxBots-capacity.ActiveBots-capacity.ReservedBots)
	return capacity, limit
}

func (d *BotLoadCapacityDirectory) applyWorkerCapacity(capacity *BotLoadNodeCapacity, worker BotLoadWorkerCapacity) {
	capacity.BotWorkerReady = worker.Ready
	capacity.Legacy = worker.Legacy
	capacity.MaxBots = max(0, worker.MaxBots)
	capacity.ActiveBots = max(0, worker.ActiveBots)
	capacity.CapacityGeneration = worker.CapacityGeneration
	capacity.WorkerEpoch = worker.WorkerEpoch
	capacity.BotWorkerVersion = worker.BotWorkerVersion
	capacity.RuntimeSource = "worker-grpc"
	capacity.RSSBytes = worker.RSSBytes
	capacity.EventLoopP95MS = worker.EventLoopP95MS
	switch {
	case worker.Legacy:
		capacity.UnavailableReason = BotLoadUnavailableLegacyWorker
	case !containsBotLoadFeature(worker.Features, "fleet-v1"):
		capacity.Legacy = true
		capacity.UnavailableReason = BotLoadUnavailableFleetFeature
	case !worker.Ready:
		capacity.UnavailableReason = worker.UnavailableReason
		if capacity.UnavailableReason == "" {
			capacity.UnavailableReason = BotLoadUnavailableAdmission
		}
	case worker.ObservedAt.IsZero() || d.clock.Now().Sub(worker.ObservedAt) > d.snapshotAge:
		capacity.UnavailableReason = BotLoadUnavailableSnapshotStale
	}
}

func containsBotLoadFeature(features []string, expected string) bool {
	for _, feature := range features {
		if feature == expected {
			return true
		}
	}
	return false
}

func botLoadCapacityErrorReason(err error) string {
	switch {
	case errors.Is(err, errBotLoadWorkerMissing):
		return BotLoadUnavailableWorkerMissing
	case errors.Is(err, context.DeadlineExceeded):
		return BotLoadUnavailableCapacityTimeout
	default:
		return BotLoadUnavailableCapacityRPC
	}
}
