package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const botLoadMetricSampleInterval = 5 * time.Second

// BotLoadMetricSampler 每 5s 为活跃压测会话写入一条聚合样本（FR-370）。
// 首版只聚合 Bot 状态计数与命令 checkpoint 终态计数；延迟百分位/探针 targetLegacy 后续迭代。
type BotLoadMetricSampler struct {
	db              *gorm.DB
	clock           BotLoadClock
	capacities      BotLoadCapacityProvider
	targetResources BotLoadTargetResourceProvider

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewBotLoadMetricSampler 创建采样器。
func NewBotLoadMetricSampler(db *gorm.DB, clock BotLoadClock) *BotLoadMetricSampler {
	return &BotLoadMetricSampler{db: db, clock: normalizeBotLoadClock(clock)}
}

// BotLoadTargetProcessResource 是目标实例根进程及子进程树的 Worker 采样快照。
type BotLoadTargetProcessResource struct {
	ProcessRSSBytes *int64
	CPUPercent      *float64
	UptimeSeconds   *float64
}

// BotLoadTargetResourceProvider 隔离目标实例资源 gRPC，采样器不直接访问 Worker。
type BotLoadTargetResourceProvider interface {
	GetTargetProcessResource(ctx context.Context, nodeUUID, instanceUUID string) (BotLoadTargetProcessResource, error)
}

type grpcBotLoadTargetResourceProvider struct {
	pool *cpgrpc.ClientPool
}

// NewGRPCBotLoadTargetResourceProvider 创建通过既有 CP→Worker 连接池采集目标实例资源的提供方。
func NewGRPCBotLoadTargetResourceProvider(pool *cpgrpc.ClientPool) BotLoadTargetResourceProvider {
	return grpcBotLoadTargetResourceProvider{pool: pool}
}

func (p grpcBotLoadTargetResourceProvider) GetTargetProcessResource(ctx context.Context, nodeUUID, instanceUUID string) (BotLoadTargetProcessResource, error) {
	if p.pool == nil {
		return BotLoadTargetProcessResource{}, errBotLoadWorkerMissing
	}
	client, ok := p.pool.Get(nodeUUID)
	if !ok || client.Worker == nil {
		return BotLoadTargetProcessResource{}, errBotLoadWorkerMissing
	}
	resourceCtx, cancel := context.WithTimeout(ctx, botLoadCapacityNodeTimeout)
	defer cancel()
	response, err := client.Worker.GetInstanceResourceSnapshot(resourceCtx, &workerpb.GetInstanceResourceSnapshotRequest{InstanceUuid: instanceUUID})
	if err != nil || response == nil {
		if err != nil {
			return BotLoadTargetProcessResource{}, err
		}
		return BotLoadTargetProcessResource{}, errors.New("目标实例资源快照为空")
	}
	resource := BotLoadTargetProcessResource{}
	if response.RssAvailable {
		resource.ProcessRSSBytes = ptrInt64(response.ProcessRssBytes)
	}
	if response.CpuAvailable {
		resource.CPUPercent = ptrFloat64(response.CpuPercent)
	}
	if response.UptimeAvailable {
		resource.UptimeSeconds = ptrFloat64(response.UptimeSeconds)
	}
	return resource, nil
}

// SetCapacityProvider 注入现有容量目录，供采样器读取已绑定执行节点的 bot-worker 资源。
func (s *BotLoadMetricSampler) SetCapacityProvider(provider BotLoadCapacityProvider) {
	if s != nil {
		s.capacities = provider
	}
}

// SetTargetResourceProvider 注入目标实例进程树资源源。
func (s *BotLoadMetricSampler) SetTargetResourceProvider(provider BotLoadTargetResourceProvider) {
	if s != nil {
		s.targetResources = provider
	}
}

// Start 启动后台 5s 循环；重复调用幂等。
func (s *BotLoadMetricSampler) Start() {
	if s == nil || s.db == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go s.loop(ctx)
}

// Stop 停止后台循环。
func (s *BotLoadMetricSampler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *BotLoadMetricSampler) loop(ctx context.Context) {
	defer s.wg.Done()
	// 启动后立即采一轮，避免首窗空窗过长。
	s.sampleActive(ctx)
	ticker := time.NewTicker(botLoadMetricSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleActive(ctx)
		}
	}
}

func (s *BotLoadMetricSampler) sampleActive(ctx context.Context) {
	ids, err := s.listActiveSessionIDs(ctx)
	if err != nil {
		slog.Warn("压测指标采样：列举活跃会话失败", "error", err)
		return
	}
	for _, id := range ids {
		if err := s.SampleSession(ctx, id); err != nil {
			slog.Debug("压测指标采样失败", "sessionId", id, "error", err)
		}
	}
}

func (s *BotLoadMetricSampler) listActiveSessionIDs(ctx context.Context) ([]uint, error) {
	var ids []uint
	// V1 status=running 或 V2 run_state in (running,degraded,starting,stopping)
	err := s.db.WithContext(ctx).Model(&model.BotStressSession{}).
		Where("status = ? OR run_state IN ?", model.BotStressSessionRunning,
			[]model.BotLoadRunState{
				model.BotLoadRunRunning, model.BotLoadRunDegraded,
				model.BotLoadRunStarting, model.BotLoadRunStopping,
			}).
		Pluck("id", &ids).Error
	return ids, err
}

// SampleSession 对单会话写一条 5s 对齐样本（同秒幂等 upsert）。
func (s *BotLoadMetricSampler) SampleSession(ctx context.Context, sessionID uint) error {
	if s == nil || s.db == nil {
		return errors.New("指标采样器未初始化")
	}
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBotStressSessionNotFound
		}
		return fmt.Errorf("查询压测会话失败: %w", err)
	}
	now := s.clock.Now().UTC().Truncate(botLoadMetricSampleInterval)
	stage := 0
	if sess.CurrentStage != nil {
		stage = *sess.CurrentStage
	}

	counts, err := s.aggregateBotCounts(ctx, sessionID, sess.BotCount)
	if err != nil {
		return err
	}
	command, err := s.aggregateCommandCounts(ctx, sessionID)
	if err != nil {
		return err
	}
	countsJSON, err := json.Marshal(counts)
	if err != nil {
		return err
	}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		return err
	}
	executorJSON, err := s.collectExecutorResources(ctx, sessionID, now)
	if err != nil {
		return err
	}
	targetLegacyJSON, err := s.collectTargetLegacy(ctx, sess.InstanceID, now)
	if err != nil {
		return err
	}
	// 屏障、延迟和错误的真实源仍由后续压测迭代补充。
	emptyObj := []byte(`{}`)
	latency := map[string]any{
		"connectP50Ms": nil, "connectP95Ms": nil, "connectP99Ms": nil,
		"scheduleLagP50Ms": nil, "scheduleLagP95Ms": nil, "scheduleLagP99Ms": nil,
		"barrierReleaseLagP50Ms": nil, "barrierReleaseLagP95Ms": nil, "barrierReleaseLagP99Ms": nil,
	}
	latencyJSON, err := json.Marshal(latency)
	if err != nil {
		return fmt.Errorf("序列化 Bot 延迟指标失败: %w", err)
	}

	row := model.BotLoadMetricSample{
		StressSessionID:  sessionID,
		SampledAt:        now,
		StageIndex:       stage,
		CountsJSON:       string(countsJSON),
		CommandJSON:      string(commandJSON),
		BarrierJSON:      string(emptyObj),
		ExecutorJSON:     string(executorJSON),
		LatencyJSON:      string(latencyJSON),
		ErrorsJSON:       string(emptyObj),
		TargetLegacyJSON: targetLegacyJSON,
	}
	// unique(session, sampled_at) 冲突时覆盖 JSON 字段（同窗重采幂等）。
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "stress_session_id"}, {Name: "sampled_at"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"stage_index", "counts_json", "command_json", "barrier_json",
			"executor_json", "latency_json", "errors_json", "target_legacy_json",
		}),
	}).Create(&row).Error
}

const botLoadMetricStaleAfter = 30 * time.Second

type botLoadExecutorMetric struct {
	NodeID                uint     `json:"nodeId"`
	ActiveBots            *int64   `json:"activeBots"`
	BotWorkerRSSBytes     *int64   `json:"botWorkerRssBytes"`
	EventLoopP95MS        *float64 `json:"eventLoopP95Ms"`
	NodeMemUsedBytes      *int64   `json:"nodeMemUsedBytes"`
	NodeMemTotalBytes     *int64   `json:"nodeMemTotalBytes"`
	NodeCPUPercent        *float64 `json:"nodeCpuPercent"`
	WorkerProcessRSSBytes *int64   `json:"workerProcessRssBytes"`
	Health                *string  `json:"health"`
	Unavailable           []string `json:"unavailable"`
}

type botLoadTargetResourceMetric struct {
	ProcessRSSBytes   *int64   `json:"processRssBytes"`
	HeapUsedBytes     *int64   `json:"heapUsedBytes"`
	HeapMaxBytes      *int64   `json:"heapMaxBytes"`
	CPUPercent        *float64 `json:"cpuPercent"`
	UptimeSeconds     *float64 `json:"uptimeSeconds"`
	HostMemUsedBytes  *int64   `json:"hostMemUsedBytes"`
	HostMemTotalBytes *int64   `json:"hostMemTotalBytes"`
	TPS               *float64 `json:"tps"`
	MSPT              *float64 `json:"mspt"`
	OnlinePlayers     *int64   `json:"onlinePlayers"`
	Unavailable       []string `json:"unavailable"`
}

type botLoadMetricValue struct {
	NodeUUID   string
	InstanceID string
	MetricKey  string
	TS         time.Time
	Value      float64
}

func (s *BotLoadMetricSampler) collectExecutorResources(ctx context.Context, sessionID uint, sampledAt time.Time) ([]byte, error) {
	var batches []model.BotLoadBatch
	if err := s.db.WithContext(ctx).Select("executor_node_id").
		Where("stress_session_id = ?", sessionID).Group("executor_node_id").Find(&batches).Error; err != nil {
		return nil, fmt.Errorf("查询压测执行节点失败: %w", err)
	}
	if len(batches) == 0 {
		return json.Marshal([]botLoadExecutorMetric{})
	}
	nodeIDs := make([]uint, 0, len(batches))
	for _, batch := range batches {
		nodeIDs = append(nodeIDs, batch.ExecutorNodeID)
	}
	var nodes []model.Node
	if err := s.db.WithContext(ctx).Where("id IN ?", nodeIDs).Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询压测执行节点信息失败: %w", err)
	}
	nodesByID := make(map[uint]model.Node, len(nodes))
	nodeUUIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodesByID[node.ID] = node
		nodeUUIDs = append(nodeUUIDs, node.UUID)
	}
	active, err := s.aggregateExecutorActiveBots(ctx, sessionID, nodeIDs)
	if err != nil {
		return nil, err
	}
	metricValues, err := s.latestNodeMetricValues(ctx, nodeUUIDs, sampledAt)
	if err != nil {
		return nil, err
	}
	capacityByNode, capacityReason := s.snapshotExecutorCapacities(ctx, sessionID)
	result := make([]botLoadExecutorMetric, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		node, found := nodesByID[nodeID]
		item := botLoadExecutorMetric{NodeID: nodeID, Unavailable: []string{}}
		if count, ok := active[nodeID]; ok {
			item.ActiveBots = ptrInt64(count)
		} else {
			item.ActiveBots = ptrInt64(0)
		}
		if !found {
			item.Unavailable = append(item.Unavailable, "node:NOT_FOUND")
			result = append(result, item)
			continue
		}
		capacity, capacityFound := capacityByNode[nodeID]
		s.applyExecutorCapacity(&item, capacity, capacityFound, capacityReason)
		s.applyNodeResourceMetrics(&item, node, metricValues, sampledAt)
		result = append(result, item)
	}
	return json.Marshal(result)
}

func (s *BotLoadMetricSampler) aggregateExecutorActiveBots(ctx context.Context, sessionID uint, nodeIDs []uint) (map[uint]int64, error) {
	type row struct {
		ExecutorNodeID uint
		Count          int64
	}
	var rows []row
	if err := s.db.WithContext(ctx).Model(&model.Bot{}).
		Select("executor_node_id, COUNT(*) AS count").
		Where("stress_session_id = ? AND executor_node_id IN ? AND status IN ?", sessionID, nodeIDs,
			[]model.BotStatus{model.BotStatusConnected, model.BotStatusConnecting}).
		Group("executor_node_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合执行节点活跃 Bot 失败: %w", err)
	}
	out := make(map[uint]int64, len(rows))
	for _, row := range rows {
		out[row.ExecutorNodeID] = row.Count
	}
	return out, nil
}

func (s *BotLoadMetricSampler) snapshotExecutorCapacities(ctx context.Context, sessionID uint) (map[uint]BotLoadNodeCapacity, string) {
	if s.capacities == nil {
		return map[uint]BotLoadNodeCapacity{}, "CAPACITY_UNAVAILABLE"
	}
	snapshot, err := s.capacities.Snapshot(ctx, sessionID)
	if err != nil || snapshot == nil {
		return map[uint]BotLoadNodeCapacity{}, "CAPACITY_RPC_FAILED"
	}
	out := make(map[uint]BotLoadNodeCapacity, len(snapshot.NodeCapacities))
	for _, capacity := range snapshot.NodeCapacities {
		out[capacity.NodeID] = capacity
	}
	return out, ""
}

func (s *BotLoadMetricSampler) applyExecutorCapacity(item *botLoadExecutorMetric, capacity BotLoadNodeCapacity, capacityFound bool, snapshotReason string) {
	if snapshotReason != "" {
		item.Unavailable = append(item.Unavailable,
			"botWorkerRssBytes:"+snapshotReason, "eventLoopP95Ms:"+snapshotReason,
			"workerProcessRssBytes:"+snapshotReason, "health:"+snapshotReason)
		return
	}
	if !capacityFound {
		item.Unavailable = append(item.Unavailable,
			"botWorkerRssBytes:CAPACITY_UNAVAILABLE", "eventLoopP95Ms:CAPACITY_UNAVAILABLE",
			"workerProcessRssBytes:CAPACITY_UNAVAILABLE", "health:CAPACITY_UNAVAILABLE")
		return
	}
	if capacity.UnavailableReason != "" {
		health := "unhealthy"
		item.Health = &health
		item.Unavailable = append(item.Unavailable,
			"botWorkerRssBytes:"+capacity.UnavailableReason, "eventLoopP95Ms:"+capacity.UnavailableReason,
			"workerProcessRssBytes:"+capacity.UnavailableReason)
		return
	}
	health := "ready"
	if capacity.Legacy {
		health = "legacy"
	}
	item.Health = &health
	if capacity.RSSBytes > 0 {
		item.BotWorkerRSSBytes = ptrInt64(capacity.RSSBytes)
	} else {
		item.Unavailable = append(item.Unavailable, "botWorkerRssBytes:CAPACITY_VALUE_MISSING")
	}
	item.EventLoopP95MS = ptrFloat64(capacity.EventLoopP95MS)
	if capacity.WorkerProcessRSSBytes != nil {
		item.WorkerProcessRSSBytes = capacity.WorkerProcessRSSBytes
	} else {
		reason := capacity.WorkerProcessRSSUnavailableReason
		if reason == "" {
			reason = "WORKER_PROCESS_RSS_UNAVAILABLE"
		}
		item.Unavailable = append(item.Unavailable, "workerProcessRssBytes:"+reason)
	}
}

func (s *BotLoadMetricSampler) applyNodeResourceMetrics(item *botLoadExecutorMetric, node model.Node, values map[string]botLoadMetricValue, sampledAt time.Time) {
	if node.MemoryMB > 0 {
		item.NodeMemTotalBytes = ptrInt64(node.MemoryMB * 1024 * 1024)
	} else {
		item.Unavailable = append(item.Unavailable, "nodeMemTotalBytes:NODE_MEMORY_UNKNOWN")
	}
	if value, ok := values[metricValueMapKey(node.UUID, "", model.MetricNodeMemUsed)]; ok && !metricValueStale(value, sampledAt) {
		item.NodeMemUsedBytes = ptrInt64(int64(value.Value))
	} else {
		item.Unavailable = append(item.Unavailable, "nodeMemUsedBytes:"+metricUnavailableReason(ok, value, sampledAt))
	}
	if value, ok := values[metricValueMapKey(node.UUID, "", model.MetricNodeCPUPct)]; ok && !metricValueStale(value, sampledAt) {
		item.NodeCPUPercent = ptrFloat64(value.Value)
	} else {
		item.Unavailable = append(item.Unavailable, "nodeCpuPercent:"+metricUnavailableReason(ok, value, sampledAt))
	}
}

func (s *BotLoadMetricSampler) latestNodeMetricValues(ctx context.Context, nodeUUIDs []string, sampledAt time.Time) (map[string]botLoadMetricValue, error) {
	if len(nodeUUIDs) == 0 {
		return map[string]botLoadMetricValue{}, nil
	}
	return s.latestMetricValues(ctx, s.db.WithContext(ctx).Table("metric_sample_raws AS raw").
		Select("series.node_uuid, series.instance_id, series.metric_key, raw.ts, raw.value").
		Joins("JOIN metric_series AS series ON series.id = raw.series_id").
		Where("series.scope = ? AND series.node_uuid IN ? AND series.instance_id = ? AND series.metric_key IN ? AND raw.value IS NOT NULL AND raw.ts <= ?",
			model.MetricScopeNode, nodeUUIDs, "", []string{model.MetricNodeMemUsed, model.MetricNodeCPUPct}, sampledAt))
}

func (s *BotLoadMetricSampler) latestMetricValues(ctx context.Context, query *gorm.DB) (map[string]botLoadMetricValue, error) {
	var rows []botLoadMetricValue
	if err := query.WithContext(ctx).Order("raw.ts DESC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询资源时序指标失败: %w", err)
	}
	out := make(map[string]botLoadMetricValue, len(rows))
	for _, row := range rows {
		key := metricValueMapKey(row.NodeUUID, row.InstanceID, row.MetricKey)
		if _, exists := out[key]; !exists {
			out[key] = row
		}
	}
	return out, nil
}

func metricValueMapKey(nodeUUID, instanceID, metricKey string) string {
	return nodeUUID + "\x00" + instanceID + "\x00" + metricKey
}

func metricValueStale(value botLoadMetricValue, sampledAt time.Time) bool {
	return value.TS.Before(sampledAt.Add(-botLoadMetricStaleAfter))
}

func metricUnavailableReason(found bool, value botLoadMetricValue, sampledAt time.Time) string {
	if found && metricValueStale(value, sampledAt) {
		return "METRIC_STALE"
	}
	return "METRIC_UNAVAILABLE"
}

func (s *BotLoadMetricSampler) collectTargetLegacy(ctx context.Context, instanceID uint, sampledAt time.Time) (*string, error) {
	var instance model.Instance
	if err := s.db.WithContext(ctx).Preload("Node").First(&instance, instanceID).Error; err != nil {
		return nil, fmt.Errorf("查询压测目标实例失败: %w", err)
	}
	values, err := s.latestInstanceMetricValues(ctx, instance.Node.UUID, instance.UUID, sampledAt)
	if err != nil {
		return nil, err
	}
	resource := botLoadTargetResourceMetric{Unavailable: []string{}}
	s.applyTargetProcessResource(ctx, &resource, instance)
	s.applyTargetNodeResourceMetrics(&resource, instance.Node, values, sampledAt)
	s.applyTargetInstanceMetricValues(&resource, instance.Node.UUID, instance.UUID, values, sampledAt)
	payload := map[string]any{
		"tps": resource.TPS, "mspt": resource.MSPT, "msptP95": resource.MSPT, "onlinePlayers": resource.OnlinePlayers,
		"targetResource": resource,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	encoded := string(raw)
	return &encoded, nil
}

func (s *BotLoadMetricSampler) applyTargetProcessResource(ctx context.Context, resource *botLoadTargetResourceMetric, instance model.Instance) {
	if s.targetResources == nil {
		resource.Unavailable = append(resource.Unavailable,
			"processRssBytes:TARGET_RESOURCE_UNAVAILABLE", "cpuPercent:TARGET_RESOURCE_UNAVAILABLE", "uptimeSeconds:TARGET_RESOURCE_UNAVAILABLE")
		return
	}
	value, err := s.targetResources.GetTargetProcessResource(ctx, instance.Node.UUID, instance.UUID)
	if err != nil {
		resource.Unavailable = append(resource.Unavailable,
			"processRssBytes:TARGET_RESOURCE_UNAVAILABLE", "cpuPercent:TARGET_RESOURCE_UNAVAILABLE", "uptimeSeconds:TARGET_RESOURCE_UNAVAILABLE")
		return
	}
	resource.ProcessRSSBytes, resource.CPUPercent, resource.UptimeSeconds = value.ProcessRSSBytes, value.CPUPercent, value.UptimeSeconds
	if value.ProcessRSSBytes == nil {
		resource.Unavailable = append(resource.Unavailable, "processRssBytes:TARGET_RESOURCE_UNAVAILABLE")
	}
	if value.CPUPercent == nil {
		resource.Unavailable = append(resource.Unavailable, "cpuPercent:TARGET_RESOURCE_UNAVAILABLE")
	}
	if value.UptimeSeconds == nil {
		resource.Unavailable = append(resource.Unavailable, "uptimeSeconds:TARGET_RESOURCE_UNAVAILABLE")
	}
}

func (s *BotLoadMetricSampler) applyTargetNodeResourceMetrics(resource *botLoadTargetResourceMetric, node model.Node, values map[string]botLoadMetricValue, sampledAt time.Time) {
	if node.MemoryMB > 0 {
		resource.HostMemTotalBytes = ptrInt64(node.MemoryMB * 1024 * 1024)
	} else {
		resource.Unavailable = append(resource.Unavailable, "hostMemTotalBytes:NODE_MEMORY_UNKNOWN")
	}
	if value, ok := values[metricValueMapKey(node.UUID, "", model.MetricNodeMemUsed)]; ok && !metricValueStale(value, sampledAt) {
		resource.HostMemUsedBytes = ptrInt64(int64(value.Value))
	} else {
		resource.Unavailable = append(resource.Unavailable, "hostMemUsedBytes:"+metricUnavailableReason(ok, value, sampledAt))
	}
}

func (s *BotLoadMetricSampler) applyTargetInstanceMetricValues(resource *botLoadTargetResourceMetric, nodeUUID, instanceUUID string, values map[string]botLoadMetricValue, sampledAt time.Time) {
	resource.HeapUsedBytes = metricInt64(values, nodeUUID, instanceUUID, model.MetricInstHeapUsed, sampledAt, "heapUsedBytes", &resource.Unavailable)
	resource.HeapMaxBytes = metricInt64(values, nodeUUID, instanceUUID, model.MetricInstHeapMax, sampledAt, "heapMaxBytes", &resource.Unavailable)
	resource.TPS = metricFloat64(values, nodeUUID, instanceUUID, model.MetricInstTPS, sampledAt, "tps", &resource.Unavailable)
	resource.MSPT = metricFloat64(values, nodeUUID, instanceUUID, model.MetricInstMSPT, sampledAt, "mspt", &resource.Unavailable)
	resource.OnlinePlayers = metricInt64(values, nodeUUID, instanceUUID, model.MetricInstPlayersOnline, sampledAt, "onlinePlayers", &resource.Unavailable)
}

func (s *BotLoadMetricSampler) latestInstanceMetricValues(ctx context.Context, nodeUUID, instanceUUID string, sampledAt time.Time) (map[string]botLoadMetricValue, error) {
	return s.latestMetricValues(ctx, s.db.WithContext(ctx).Table("metric_sample_raws AS raw").
		Select("series.node_uuid, series.instance_id, series.metric_key, raw.ts, raw.value").
		Joins("JOIN metric_series AS series ON series.id = raw.series_id").
		Where("series.scope = ? AND series.node_uuid = ? AND series.instance_id = ? AND series.metric_key IN ? AND raw.value IS NOT NULL AND raw.ts <= ?",
			model.MetricScopeInstance, nodeUUID, instanceUUID,
			[]string{model.MetricInstHeapUsed, model.MetricInstHeapMax, model.MetricInstTPS, model.MetricInstMSPT, model.MetricInstPlayersOnline}, sampledAt))
}

func metricInt64(values map[string]botLoadMetricValue, nodeUUID, instanceUUID, key string, sampledAt time.Time, field string, unavailable *[]string) *int64 {
	value, ok := values[metricValueMapKey(nodeUUID, instanceUUID, key)]
	if !ok || metricValueStale(value, sampledAt) {
		*unavailable = append(*unavailable, field+":"+metricUnavailableReason(ok, value, sampledAt))
		return nil
	}
	return ptrInt64(int64(value.Value))
}

func metricFloat64(values map[string]botLoadMetricValue, nodeUUID, instanceUUID, key string, sampledAt time.Time, field string, unavailable *[]string) *float64 {
	value, ok := values[metricValueMapKey(nodeUUID, instanceUUID, key)]
	if !ok || metricValueStale(value, sampledAt) {
		*unavailable = append(*unavailable, field+":"+metricUnavailableReason(ok, value, sampledAt))
		return nil
	}
	return ptrFloat64(value.Value)
}

func ptrInt64(value int64) *int64 { return &value }

func ptrFloat64(value float64) *float64 { return &value }

func (s *BotLoadMetricSampler) aggregateBotCounts(ctx context.Context, sessionID uint, planned int) (map[string]int64, error) {
	out := map[string]int64{
		"planned": int64(planned),
		"total":   0,
	}
	type row struct {
		Status string
		Cnt    int64
	}
	var rows []row
	if err := s.db.WithContext(ctx).Model(&model.Bot{}).
		Select("status, COUNT(*) AS cnt").
		Where("stress_session_id = ?", sessionID).
		Group("status").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合 Bot 状态失败: %w", err)
	}
	for _, r := range rows {
		out["total"] += r.Cnt
		out[r.Status] = r.Cnt
	}
	return out, nil
}

func (s *BotLoadMetricSampler) aggregateCommandCounts(ctx context.Context, sessionID uint) (map[string]int64, error) {
	out := map[string]int64{
		"planned": 0, "prepared": 0, "scheduled": 0,
		"sent": 0, "failed": 0, "timed_out": 0, "cancelled": 0,
	}
	type row struct {
		Status string
		Cnt    int64
	}
	var rows []row
	if err := s.db.WithContext(ctx).Model(&model.BotLoadCommandCheckpoint{}).
		Select("status, COUNT(*) AS cnt").
		Where("stress_session_id = ?", sessionID).
		Group("status").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合命令 checkpoint 失败: %w", err)
	}
	for _, r := range rows {
		out["planned"] += r.Cnt
		out[r.Status] = r.Cnt
	}
	return out, nil
}

// ---- 查询面 ----

// BotLoadMetricPointView 与规格/前端 BotLoadMetricPoint 对齐的读模型。
type BotLoadMetricPointView struct {
	Timestamp    time.Time      `json:"timestamp"`
	StageIndex   int            `json:"stageIndex"`
	Counts       map[string]any `json:"counts"`
	Command      map[string]any `json:"command"`
	Barrier      map[string]any `json:"barrier"`
	Executor     []any          `json:"executor"`
	Latency      map[string]any `json:"latency"`
	Errors       map[string]any `json:"errors"`
	TargetLegacy map[string]any `json:"targetLegacy,omitempty"`
}

// BotLoadMetricListResult GET metrics 响应。
type BotLoadMetricListResult struct {
	Items      []BotLoadMetricPointView `json:"items"`
	From       time.Time                `json:"from"`
	To         time.Time                `json:"to"`
	Resolution string                   `json:"resolution"`
}

// ListMetrics 读取样本；resolution=raw|15s|1m|5m，默认 raw，最多 1200 点。
func (s *BotLoadMetricSampler) ListMetrics(ctx context.Context, sessionID uint, from, to *time.Time, resolution string) (*BotLoadMetricListResult, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("指标采样器未初始化")
	}
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, err
	}
	res := normalizeMetricResolution(resolution)
	q := s.db.WithContext(ctx).Model(&model.BotLoadMetricSample{}).
		Where("stress_session_id = ?", sessionID)
	if from != nil {
		q = q.Where("sampled_at >= ?", from.UTC())
	}
	if to != nil {
		q = q.Where("sampled_at <= ?", to.UTC())
	}
	var rows []model.BotLoadMetricSample
	if err := q.Order("sampled_at ASC").Limit(5000).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询指标样本失败: %w", err)
	}
	points := make([]BotLoadMetricPointView, 0, len(rows))
	for _, r := range rows {
		points = append(points, metricSampleToView(r))
	}
	points = downsampleMetricPoints(points, res, 1200)
	result := &BotLoadMetricListResult{Items: points, Resolution: res}
	if len(points) > 0 {
		result.From = points[0].Timestamp
		result.To = points[len(points)-1].Timestamp
	} else {
		now := s.clock.Now().UTC()
		result.From, result.To = now, now
		if from != nil {
			result.From = from.UTC()
		}
		if to != nil {
			result.To = to.UTC()
		}
	}
	return result, nil
}

func normalizeMetricResolution(raw string) string {
	switch raw {
	case "15s", "1m", "5m", "raw":
		return raw
	default:
		return "raw"
	}
}

func metricSampleToView(r model.BotLoadMetricSample) BotLoadMetricPointView {
	v := BotLoadMetricPointView{
		Timestamp:  r.SampledAt.UTC(),
		StageIndex: r.StageIndex,
		Counts:     decodeJSONMap(r.CountsJSON),
		Command:    decodeJSONMap(r.CommandJSON),
		Barrier:    decodeJSONMap(r.BarrierJSON),
		Latency:    decodeJSONMap(r.LatencyJSON),
		Errors:     decodeJSONMap(r.ErrorsJSON),
		Executor:   decodeJSONArray(r.ExecutorJSON),
	}
	if r.TargetLegacyJSON != nil && *r.TargetLegacyJSON != "" {
		v.TargetLegacy = decodeJSONMap(*r.TargetLegacyJSON)
	}
	return v
}

func decodeJSONMap(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func decodeJSONArray(raw string) []any {
	if raw == "" {
		return []any{}
	}
	var a []any
	if err := json.Unmarshal([]byte(raw), &a); err != nil || a == nil {
		return []any{}
	}
	return a
}

func downsampleMetricPoints(points []BotLoadMetricPointView, resolution string, maxPoints int) []BotLoadMetricPointView {
	if len(points) == 0 {
		return points
	}
	step := time.Duration(0)
	switch resolution {
	case "15s":
		step = 15 * time.Second
	case "1m":
		step = time.Minute
	case "5m":
		step = 5 * time.Minute
	}
	out := points
	if step > 0 {
		// 每桶取最后一点（raw 5s 已对齐）。
		var reduced []BotLoadMetricPointView
		var bucket time.Time
		var last *BotLoadMetricPointView
		for i := range out {
			p := out[i]
			b := p.Timestamp.UTC().Truncate(step)
			if last == nil {
				bucket = b
				cp := p
				last = &cp
				continue
			}
			if !b.Equal(bucket) {
				reduced = append(reduced, *last)
				bucket = b
				cp := p
				last = &cp
				continue
			}
			cp := p
			last = &cp
		}
		if last != nil {
			reduced = append(reduced, *last)
		}
		out = reduced
	}
	if maxPoints > 0 && len(out) > maxPoints {
		// 均匀抽稀：保留首尾。
		stride := (len(out) + maxPoints - 1) / maxPoints
		if stride < 1 {
			stride = 1
		}
		var slim []BotLoadMetricPointView
		for i := 0; i < len(out); i += stride {
			slim = append(slim, out[i])
		}
		if slim[len(slim)-1].Timestamp != out[len(out)-1].Timestamp {
			slim = append(slim, out[len(out)-1])
		}
		out = slim
	}
	return out
}
