package service

import (
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const platformObservabilityLimit = 5

// PlatformObservabilityService 聚合平台管理员首页所需的有界受管观测读模型。
// 它只查询 Control Plane 已持久化快照，绝不触发 Worker RPC。
type PlatformObservabilityService struct {
	db *gorm.DB
}

// NewPlatformObservabilityService 创建平台全景观测读服务。
func NewPlatformObservabilityService(db *gorm.DB) *PlatformObservabilityService {
	return &PlatformObservabilityService{db: db}
}

// PlatformObservabilityOverview 是首页平台管理员区域的有界读模型。
type PlatformObservabilityOverview struct {
	SampledAt  *time.Time                     `json:"sampledAt"`
	Health     PlatformObservabilityHealth    `json:"health"`
	Resources  PlatformObservabilityResources `json:"resources"`
	Bots       PlatformObservabilityBots      `json:"bots"`
	Alerts     []PlatformObservabilityAlert   `json:"alerts"`
	Tasks      []PlatformObservabilityTask    `json:"tasks"`
	Exceptions []PlatformObservabilityItem    `json:"exceptions"`
}

// PlatformObservabilityHealth 汇总节点和实例当前健康计数。
type PlatformObservabilityHealth struct {
	NodeCount            int `json:"nodeCount"`
	OnlineNodeCount      int `json:"onlineNodeCount"`
	StaleNodeCount       int `json:"staleNodeCount"`
	OfflineNodeCount     int `json:"offlineNodeCount"`
	RunningInstanceCount int `json:"runningInstanceCount"`
	CrashedInstanceCount int `json:"crashedInstanceCount"`
	StoppedInstanceCount int `json:"stoppedInstanceCount"`
}

// PlatformObservabilityResources 汇总鲜活节点的当前资源观察值。
type PlatformObservabilityResources struct {
	CPUPct           *float64          `json:"cpuPct"`
	LoadPct          *float64          `json:"loadPct"`
	MemoryUsedBytes  *int64            `json:"memoryUsedBytes"`
	MemoryTotalBytes *int64            `json:"memoryTotalBytes"`
	Freshness        ResourceFreshness `json:"freshness"`
}

// PlatformObservabilityBots 汇总共享 Bot Worker 进程观察值，不归属到单个 Bot 或会话。
type PlatformObservabilityBots struct {
	SharedRuntime         bool                    `json:"sharedRuntime"`
	Notice                string                  `json:"notice"`
	NodeCount             int                     `json:"nodeCount"`
	BotWorkerRSSBytes     *int64                  `json:"botWorkerRssBytes"`
	BotWorkerCPUPct       *float64                `json:"botWorkerCpuPct"`
	WorkerProcessRSSBytes *int64                  `json:"workerProcessRssBytes"`
	WorkerProcessCPUPct   *float64                `json:"workerProcessCpuPct"`
	ActiveCount           *int32                  `json:"activeCount"`
	ConnectingCount       *int32                  `json:"connectingCount"`
	EventLoopP95MS        *float64                `json:"eventLoopP95Ms"`
	Unavailable           []BotRuntimeUnavailable `json:"unavailable"`
}

// PlatformObservabilityAlert 是首页展示的未解决告警最小字段集。
type PlatformObservabilityAlert struct {
	ID        uint      `json:"id"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

// PlatformObservabilityTask 是首页展示的进行中或失败任务最小字段集。
type PlatformObservabilityTask struct {
	ID        uint            `json:"id"`
	State     model.TaskState `json:"state"`
	Title     string          `json:"title"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// PlatformObservabilityItem 是可下钻的有界异常项。
type PlatformObservabilityItem struct {
	Kind       string `json:"kind"`
	NodeID     uint   `json:"nodeId,omitempty"`
	InstanceID uint   `json:"instanceId,omitempty"`
	Title      string `json:"title"`
	Href       string `json:"href"`
}

// Overview 返回当前平台受管资源和异常的有界读模型。
func (s *PlatformObservabilityService) Overview() (PlatformObservabilityOverview, error) {
	return s.OverviewAt(time.Now().UTC())
}

// OverviewAt 注入当前时刻，确保陈旧判断与聚合口径可测。
func (s *PlatformObservabilityService) OverviewAt(now time.Time) (PlatformObservabilityOverview, error) {
	var nodes []model.Node
	if err := s.db.Order("id ASC").Find(&nodes).Error; err != nil {
		return PlatformObservabilityOverview{}, fmt.Errorf("查询平台节点概览失败: %w", err)
	}
	result := platformOverviewFromNodes(nodes, now)
	instanceHealth, err := s.instanceHealth()
	if err != nil {
		return PlatformObservabilityOverview{}, err
	}
	result.Health.RunningInstanceCount = instanceHealth.running
	result.Health.CrashedInstanceCount = instanceHealth.crashed
	result.Health.StoppedInstanceCount = instanceHealth.stopped
	alerts, err := s.platformAlerts()
	if err != nil {
		return PlatformObservabilityOverview{}, err
	}
	tasks, err := s.platformTasks()
	if err != nil {
		return PlatformObservabilityOverview{}, err
	}
	exceptions, err := s.platformExceptions(nodes, now)
	if err != nil {
		return PlatformObservabilityOverview{}, err
	}
	result.Alerts, result.Tasks, result.Exceptions = alerts, tasks, exceptions
	return result, nil
}

func platformOverviewFromNodes(nodes []model.Node, now time.Time) PlatformObservabilityOverview {
	result := PlatformObservabilityOverview{
		Resources:  PlatformObservabilityResources{Freshness: overallFreshness(nodes, now)},
		Bots:       PlatformObservabilityBots{SharedRuntime: true, Notice: botRuntimeSharedNotice, Unavailable: []BotRuntimeUnavailable{}},
		Alerts:     []PlatformObservabilityAlert{},
		Tasks:      []PlatformObservabilityTask{},
		Exceptions: []PlatformObservabilityItem{},
	}
	var cpuSum, loadSum float64
	var loadCount, freshCount int
	var memoryUsed, memoryTotal int64
	var botRSS, workerRSS int64
	var botCPU, workerCPU float64
	var active, connecting int32
	var maxEventLoop float64
	for _, node := range nodes {
		result.Health.NodeCount++
		switch resourceFreshnessAt(node, now) {
		case ResourceFreshnessFresh:
			result.Health.OnlineNodeCount++
			freshCount++
			cpuSum += float64(node.CPUUsage) * 100
			memoryUsed += node.MemoryUsedMB * 1024 * 1024
			memoryTotal += node.MemoryMB * 1024 * 1024
			if node.CPUCores > 0 {
				loadSum += node.LoadAvg1 / float64(node.CPUCores) * 100
				loadCount++
			}
		case ResourceFreshnessStale:
			result.Health.StaleNodeCount++
		default:
			result.Health.OfflineNodeCount++
		}
		if result.SampledAt == nil || (node.LastHeartbeat != nil && node.LastHeartbeat.After(*result.SampledAt)) {
			result.SampledAt = node.LastHeartbeat
		}
		reason := botRuntimeUnavailableReason(node, now)
		if reason != "" || !completeBotRuntimeSnapshot(node) {
			if reason == "" {
				reason = "Bot Worker 运行时字段缺失"
			}
			result.Bots.Unavailable = append(result.Bots.Unavailable, BotRuntimeUnavailable{NodeID: node.ID, Reason: reason})
			continue
		}
		result.Bots.NodeCount++
		botRSS += *node.BotWorkerRSSBytes
		workerRSS += *node.WorkerProcessRSSBytes
		botCPU += *node.BotWorkerCPUPct
		workerCPU += *node.WorkerProcessCPUPct
		active += *node.BotActiveCount
		connecting += *node.BotConnectingCount
		if *node.BotEventLoopP95MS > maxEventLoop {
			maxEventLoop = *node.BotEventLoopP95MS
		}
	}
	if freshCount > 0 {
		result.Resources.CPUPct = float64Pointer(cpuSum / float64(freshCount))
		result.Resources.MemoryUsedBytes = int64Pointer(memoryUsed)
		result.Resources.MemoryTotalBytes = int64Pointer(memoryTotal)
	}
	if loadCount > 0 {
		result.Resources.LoadPct = float64Pointer(loadSum / float64(loadCount))
	}
	if result.Bots.NodeCount > 0 {
		result.Bots.BotWorkerRSSBytes = int64Pointer(botRSS)
		result.Bots.WorkerProcessRSSBytes = int64Pointer(workerRSS)
		result.Bots.BotWorkerCPUPct = float64Pointer(botCPU)
		result.Bots.WorkerProcessCPUPct = float64Pointer(workerCPU)
		result.Bots.ActiveCount = int32Pointer(active)
		result.Bots.ConnectingCount = int32Pointer(connecting)
		result.Bots.EventLoopP95MS = float64Pointer(maxEventLoop)
	} else if len(result.Bots.Unavailable) == 0 {
		result.Bots.Unavailable = append(result.Bots.Unavailable, BotRuntimeUnavailable{Reason: "暂无可用 Bot Worker 运行时快照"})
	}
	return result
}

func completeBotRuntimeSnapshot(node model.Node) bool {
	return node.BotAvailable && node.WorkerProcessRSSBytes != nil && node.WorkerProcessCPUPct != nil &&
		node.BotWorkerRSSBytes != nil && node.BotWorkerCPUPct != nil && node.BotActiveCount != nil &&
		node.BotConnectingCount != nil && node.BotEventLoopP95MS != nil
}

type platformInstanceHealth struct{ running, crashed, stopped int }

func (s *PlatformObservabilityService) instanceHealth() (platformInstanceHealth, error) {
	type row struct {
		Status model.InstanceStatus
		Count  int
	}
	var rows []row
	if err := s.db.Model(&model.Instance{}).Select("status, COUNT(*) AS count").Group("status").Find(&rows).Error; err != nil {
		return platformInstanceHealth{}, fmt.Errorf("查询实例健康概览失败: %w", err)
	}
	result := platformInstanceHealth{}
	for _, row := range rows {
		switch row.Status {
		case model.InstanceStatusRunning:
			result.running = row.Count
		case model.InstanceStatusCrashed:
			result.crashed = row.Count
		case model.InstanceStatusStopped:
			result.stopped = row.Count
		}
	}
	return result, nil
}

func (s *PlatformObservabilityService) platformAlerts() ([]PlatformObservabilityAlert, error) {
	var rows []model.AlertEvent
	err := s.db.Where("resolved = ?", false).
		Order("CASE level WHEN 'critical' THEN 0 WHEN 'warn' THEN 1 ELSE 2 END").
		Order("COALESCE(last_fired_at, fired_at) DESC").Order("id DESC").Limit(platformObservabilityLimit).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询活跃告警概览失败: %w", err)
	}
	items := make([]PlatformObservabilityAlert, 0, len(rows))
	for _, row := range rows {
		items = append(items, PlatformObservabilityAlert{ID: row.ID, Severity: row.Level, Title: row.Message, CreatedAt: row.FiredAt})
	}
	return items, nil
}

func (s *PlatformObservabilityService) platformTasks() ([]PlatformObservabilityTask, error) {
	var rows []model.Task
	err := s.db.Where("state IN ?", []model.TaskState{model.TaskStatePending, model.TaskStateRunning, model.TaskStateFailed}).
		Order("CASE state WHEN 'failed' THEN 0 ELSE 1 END").Order("updated_at DESC").Order("id DESC").Limit(platformObservabilityLimit).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询进行中任务概览失败: %w", err)
	}
	items := make([]PlatformObservabilityTask, 0, len(rows))
	for _, row := range rows {
		items = append(items, PlatformObservabilityTask{ID: row.ID, State: row.State, Title: row.Title, UpdatedAt: row.UpdatedAt})
	}
	return items, nil
}

func (s *PlatformObservabilityService) platformExceptions(nodes []model.Node, now time.Time) ([]PlatformObservabilityItem, error) {
	items := nodeExceptions(nodes, now)
	crashed, err := s.crashedInstanceExceptions()
	if err != nil {
		return nil, err
	}
	items = append(items, crashed...)
	resources, err := s.resourceExceptions(now)
	if err != nil {
		return nil, err
	}
	items = append(items, resources...)
	if len(items) > platformObservabilityLimit {
		items = items[:platformObservabilityLimit]
	}
	return items, nil
}

func nodeExceptions(nodes []model.Node, now time.Time) []PlatformObservabilityItem {
	items := make([]PlatformObservabilityItem, 0)
	for _, node := range nodes {
		switch resourceFreshnessAt(node, now) {
		case ResourceFreshnessOffline:
			items = append(items, PlatformObservabilityItem{Kind: "node_offline", NodeID: node.ID, Title: node.Name + " 节点离线", Href: "/monitoring?node=" + node.UUID})
		case ResourceFreshnessStale, ResourceFreshnessUnavailable:
			items = append(items, PlatformObservabilityItem{Kind: "node_stale", NodeID: node.ID, Title: node.Name + " 心跳陈旧", Href: "/monitoring?node=" + node.UUID})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == "node_offline"
		}
		return items[i].NodeID < items[j].NodeID
	})
	return items
}

func (s *PlatformObservabilityService) crashedInstanceExceptions() ([]PlatformObservabilityItem, error) {
	var rows []model.Instance
	if err := s.db.Select("id", "name").Where("status = ?", model.InstanceStatusCrashed).Order("updated_at DESC").Order("id ASC").Limit(platformObservabilityLimit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询崩溃实例概览失败: %w", err)
	}
	items := make([]PlatformObservabilityItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, PlatformObservabilityItem{Kind: "instance_crashed", InstanceID: row.ID, Title: row.Name + " 实例崩溃", Href: fmt.Sprintf("/instances/%d", row.ID)})
	}
	return items, nil
}

func (s *PlatformObservabilityService) resourceExceptions(now time.Time) ([]PlatformObservabilityItem, error) {
	type row struct {
		InstanceID   uint
		InstanceName string
		RSSBytes     uint64
	}
	var rows []row
	if err := s.db.Table("process_metric_snapshots AS p").
		Select("i.id AS instance_id, i.name AS instance_name, p.rss_bytes").
		Joins("JOIN instances AS i ON i.uuid = p.instance_uuid").
		Where("i.status = ? AND p.sampled_at >= ?", model.InstanceStatusRunning, now.Add(-managedResourceFreshWindow)).
		Order("p.rss_bytes DESC").Order("i.id ASC").Limit(platformObservabilityLimit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询实例资源异常概览失败: %w", err)
	}
	items := make([]PlatformObservabilityItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, PlatformObservabilityItem{Kind: "resource_rss", InstanceID: row.InstanceID, Title: fmt.Sprintf("%s RSS %d", row.InstanceName, row.RSSBytes), Href: fmt.Sprintf("/instances/%d", row.InstanceID)})
	}
	return items, nil
}

func float64Pointer(value float64) *float64 { return &value }
func int64Pointer(value int64) *int64       { return &value }
func int32Pointer(value int32) *int32       { return &value }
