package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const (
	botRuntimeMetricSampleInterval = 30 * time.Second
	botRuntimeMetricFreshWindow    = 90 * time.Second
)

// BotRuntimeMetricSampler 将 FR-400 的已认证 Heartbeat 当前快照沉淀为 ADR-013 时序。
// 它只读取 Control Plane 数据库，绝不调用 RPC、唤起 Bot Worker 或扫描任意 OS 进程。
type BotRuntimeMetricSampler struct {
	db      *gorm.DB
	metrics *MetricService

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewBotRuntimeMetricSampler 创建共享 Bot Worker 运行时历史采样器。
func NewBotRuntimeMetricSampler(db *gorm.DB, metrics *MetricService) *BotRuntimeMetricSampler {
	return &BotRuntimeMetricSampler{db: db, metrics: metrics}
}

// Start 启动 30 秒采样循环；重复调用幂等。
func (s *BotRuntimeMetricSampler) Start() {
	if s == nil || s.db == nil || s.metrics == nil {
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

// Stop 停止采样循环；重复调用幂等。
func (s *BotRuntimeMetricSampler) Stop() {
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

func (s *BotRuntimeMetricSampler) loop(ctx context.Context) {
	defer s.wg.Done()
	s.sample(time.Now().UTC())
	ticker := time.NewTicker(botRuntimeMetricSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.sample(now.UTC())
		}
	}
}

func (s *BotRuntimeMetricSampler) sample(now time.Time) {
	if err := s.SampleAt(now); err != nil {
		slog.Warn("Bot Worker 运行时历史采样失败", "error", err)
	}
}

// SampleAt 在指定时刻把所有节点当前受管运行时快照写入时序库。
// 节点离线、心跳/快照陈旧或字段缺失时写 NULL 断点，不以零值代替不可用数据。
func (s *BotRuntimeMetricSampler) SampleAt(now time.Time) error {
	if s == nil || s.db == nil || s.metrics == nil {
		return errors.New("Bot Worker 运行时采样器未初始化")
	}
	var nodes []model.Node
	if err := s.db.Order("id ASC").Find(&nodes).Error; err != nil {
		return err
	}
	samples := make([]Sample, 0, len(nodes)*8)
	for _, node := range nodes {
		samples = append(samples, botRuntimeSamples(node, now)...)
	}
	return s.metrics.Ingest(samples)
}

func botRuntimeSamples(node model.Node, now time.Time) []Sample {
	values := botRuntimeMetricValues(node, now)
	return []Sample{
		botRuntimeSample(node.UUID, model.MetricWorkerProcessRSSBytes, "bytes", now, values.workerRSS),
		botRuntimeSample(node.UUID, model.MetricWorkerProcessCPUPct, "percent", now, values.workerCPU),
		botRuntimeSample(node.UUID, model.MetricBotWorkerRSSBytes, "bytes", now, values.botRSS),
		botRuntimeSample(node.UUID, model.MetricBotWorkerCPUPct, "percent", now, values.botCPU),
		botRuntimeSample(node.UUID, model.MetricBotActiveCount, "count", now, values.active),
		botRuntimeSample(node.UUID, model.MetricBotConnectingCount, "count", now, values.connecting),
		botRuntimeSample(node.UUID, model.MetricBotCapacityMax, "count", now, values.capacity),
		botRuntimeSample(node.UUID, model.MetricBotEventLoopP95MS, "ms", now, values.eventLoop),
	}
}

type botRuntimeValues struct {
	workerRSS, workerCPU *float64
	botRSS, botCPU       *float64
	active, connecting   *float64
	capacity, eventLoop  *float64
}

func botRuntimeMetricValues(node model.Node, now time.Time) botRuntimeValues {
	if !botRuntimeSnapshotFresh(node, now) {
		return botRuntimeValues{}
	}
	values := botRuntimeValues{
		workerRSS: ptrInt64AsFloat(node.WorkerProcessRSSBytes),
		workerCPU: node.WorkerProcessCPUPct,
	}
	if !node.BotAvailable {
		return values
	}
	values.botRSS = ptrInt64AsFloat(node.BotWorkerRSSBytes)
	values.botCPU = node.BotWorkerCPUPct
	values.active = ptrInt32AsFloat(node.BotActiveCount)
	values.connecting = ptrInt32AsFloat(node.BotConnectingCount)
	values.capacity = ptrInt32AsFloat(node.BotCapacityMax)
	values.eventLoop = node.BotEventLoopP95MS
	return values
}

func botRuntimeSnapshotFresh(node model.Node, now time.Time) bool {
	return node.Status == model.NodeStatusOnline && node.LastHeartbeat != nil &&
		node.ManagedRuntimeObservedAt != nil &&
		!node.LastHeartbeat.Before(now.Add(-botRuntimeMetricFreshWindow)) &&
		!node.ManagedRuntimeObservedAt.Before(now.Add(-botRuntimeMetricFreshWindow))
}

func botRuntimeSample(nodeUUID, metricKey, unit string, now time.Time, value *float64) Sample {
	return Sample{NodeUUID: nodeUUID, Scope: model.MetricScopeNode, MetricKey: metricKey, Unit: unit, TS: now.UTC(), Value: value}
}

func ptrInt64AsFloat(value *int64) *float64 {
	if value == nil {
		return nil
	}
	result := float64(*value)
	return &result
}

func ptrInt32AsFloat(value *int32) *float64 {
	if value == nil {
		return nil
	}
	result := float64(*value)
	return &result
}
