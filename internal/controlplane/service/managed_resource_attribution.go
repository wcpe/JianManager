package service

import (
	"fmt"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// managedResourceFreshWindow 与节点离线检测同为 90 秒，仅约束 FR-400 的受管资源归因。
const managedResourceFreshWindow = 90 * time.Second

// ResourceFreshness 是受管资源当前值的可用性状态。
type ResourceFreshness string

const (
	ResourceFreshnessFresh       ResourceFreshness = "fresh"
	ResourceFreshnessStale       ResourceFreshness = "stale"
	ResourceFreshnessOffline     ResourceFreshness = "offline"
	ResourceFreshnessUnavailable ResourceFreshness = "unavailable"
)

// ResourceAttributionQuery 控制受管资源归因 TopN 的排序与上限。
type ResourceAttributionQuery struct {
	Sort  string
	Limit int
}

// ResourceAttributionResult 是首页 Tooltip 的有界受管资源归因读模型。
type ResourceAttributionResult struct {
	SampledAt    *time.Time                    `json:"sampledAt"`
	Freshness    ResourceFreshness             `json:"freshness"`
	Nodes        []ResourceAttributionNode     `json:"nodes"`
	TopInstances []ResourceAttributionInstance `json:"topInstances"`
	TopProcesses []ResourceAttributionProcess  `json:"topProcesses"`
}

// ResourceAttributionNode 是单节点当前资源及本地 Worker 运行时快照。
type ResourceAttributionNode struct {
	NodeID                uint                         `json:"nodeId"`
	NodeUUID              string                       `json:"nodeUuid"`
	Name                  string                       `json:"name"`
	Status                ResourceFreshness            `json:"status"`
	ObservedAt            *time.Time                   `json:"observedAt"`
	CPUPct                *float64                     `json:"cpuPct"`
	LoadPct               *float64                     `json:"loadPct"`
	MemoryUsedBytes       *int64                       `json:"memoryUsedBytes"`
	MemoryTotalBytes      *int64                       `json:"memoryTotalBytes"`
	WorkerProcessRSSBytes *int64                       `json:"workerProcessRssBytes"`
	WorkerProcessCPUPct   *float64                     `json:"workerProcessCpuPct"`
	BotWorker             ResourceAttributionBotWorker `json:"botWorker"`
}

// ResourceAttributionBotWorker 表示共享 Bot Worker，而非任一 Bot 的资源。
type ResourceAttributionBotWorker struct {
	RSSBytes        *int64   `json:"rssBytes"`
	CPUPct          *float64 `json:"cpuPct"`
	ActiveCount     *int32   `json:"activeCount"`
	ConnectingCount *int32   `json:"connectingCount"`
	EventLoopP95MS  *float64 `json:"eventLoopP95Ms"`
	Available       bool     `json:"available"`
	Reason          string   `json:"reason"`
}

// ResourceAttributionInstance 是受管实例进程树的聚合观察值。
type ResourceAttributionInstance struct {
	InstanceID   uint      `json:"instanceId"`
	InstanceUUID string    `json:"instanceUuid"`
	InstanceName string    `json:"instanceName"`
	NodeID       uint      `json:"nodeId"`
	CPUPct       float64   `json:"cpuPct"`
	RSSBytes     uint64    `json:"rssBytes"`
	SampledAt    time.Time `json:"sampledAt"`
}

// ResourceAttributionProcess 是现有脱敏进程快照的首页安全子集。
type ResourceAttributionProcess struct {
	InstanceID   uint      `json:"instanceId"`
	InstanceUUID string    `json:"instanceUuid"`
	InstanceName string    `json:"instanceName"`
	NodeID       uint      `json:"nodeId"`
	PID          int32     `json:"pid"`
	Name         string    `json:"name"`
	CPUPercent   float64   `json:"cpuPercent"`
	RSSBytes     uint64    `json:"rssBytes"`
	SampledAt    time.Time `json:"sampledAt"`
}

type attributionProcessRow struct {
	NodeUUID     string
	InstanceUUID string
	InstanceID   uint
	InstanceName string
	NodeID       uint
	PID          int32 `gorm:"column:pid"`
	Name         string
	CPUPercent   float64
	RSSBytes     uint64
	SampledAt    time.Time
}

// ResourceAttribution 返回当前受管节点、实例进程树和进程 TopN。
func (s *MetricService) ResourceAttribution(query ResourceAttributionQuery) (ResourceAttributionResult, error) {
	return s.ResourceAttributionAt(time.Now().UTC(), query)
}

// ResourceAttributionAt 注入当前时间，确保鲜度边界可测试。
func (s *MetricService) ResourceAttributionAt(now time.Time, query ResourceAttributionQuery) (ResourceAttributionResult, error) {
	limit := query.Limit
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	var nodes []model.Node
	if err := s.db.Order("id ASC").Find(&nodes).Error; err != nil {
		return ResourceAttributionResult{}, fmt.Errorf("查询节点资源失败: %w", err)
	}
	result := ResourceAttributionResult{Nodes: make([]ResourceAttributionNode, 0, min(limit, len(nodes)))}
	freshNodes := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		status := resourceFreshnessAt(node, now)
		if result.SampledAt == nil || (node.LastHeartbeat != nil && node.LastHeartbeat.After(*result.SampledAt)) {
			result.SampledAt = node.LastHeartbeat
		}
		if status == ResourceFreshnessFresh {
			freshNodes[node.UUID] = struct{}{}
		}
		if len(result.Nodes) < limit {
			result.Nodes = append(result.Nodes, attributionNode(node, status))
		}
	}
	result.Freshness = overallFreshness(nodes, now)
	rows, err := s.freshAttributionProcessRows(now.Add(-managedResourceFreshWindow))
	if err != nil {
		return ResourceAttributionResult{}, err
	}
	rows = filterFreshAttributionRows(rows, freshNodes)
	result.TopInstances = aggregateAttributionInstances(rows, query.Sort, limit)
	result.TopProcesses = topAttributionProcesses(rows, query.Sort, limit)
	return result, nil
}

func resourceFreshnessAt(node model.Node, now time.Time) ResourceFreshness {
	if node.Status != model.NodeStatusOnline {
		return ResourceFreshnessOffline
	}
	if node.LastHeartbeat == nil {
		return ResourceFreshnessUnavailable
	}
	if now.Sub(*node.LastHeartbeat) > managedResourceFreshWindow {
		return ResourceFreshnessStale
	}
	return ResourceFreshnessFresh
}

func overallFreshness(nodes []model.Node, now time.Time) ResourceFreshness {
	for _, node := range nodes {
		if resourceFreshnessAt(node, now) == ResourceFreshnessFresh {
			return ResourceFreshnessFresh
		}
	}
	if len(nodes) == 0 {
		return ResourceFreshnessUnavailable
	}
	return ResourceFreshnessOffline
}

func attributionNode(node model.Node, status ResourceFreshness) ResourceAttributionNode {
	item := ResourceAttributionNode{NodeID: node.ID, NodeUUID: node.UUID, Name: node.Name, Status: status, ObservedAt: node.LastHeartbeat}
	item.BotWorker = ResourceAttributionBotWorker{Reason: node.BotUnavailableReason}
	if status != ResourceFreshnessFresh {
		item.BotWorker.Reason = "节点资源快照已过期"
		return item
	}
	cpu := float64(node.CPUUsage) * 100
	item.CPUPct = &cpu
	if node.CPUCores > 0 {
		load := node.LoadAvg1 / float64(node.CPUCores) * 100
		item.LoadPct = &load
	}
	used := node.MemoryUsedMB * 1024 * 1024
	total := node.MemoryMB * 1024 * 1024
	item.MemoryUsedBytes, item.MemoryTotalBytes = &used, &total
	item.WorkerProcessRSSBytes = node.WorkerProcessRSSBytes
	item.WorkerProcessCPUPct = node.WorkerProcessCPUPct
	item.BotWorker = ResourceAttributionBotWorker{
		RSSBytes: node.BotWorkerRSSBytes, CPUPct: node.BotWorkerCPUPct,
		ActiveCount: node.BotActiveCount, ConnectingCount: node.BotConnectingCount,
		EventLoopP95MS: node.BotEventLoopP95MS, Available: node.BotAvailable, Reason: node.BotUnavailableReason,
	}
	return item
}

func (s *MetricService) freshAttributionProcessRows(cutoff time.Time) ([]attributionProcessRow, error) {
	latest := s.db.Model(&model.ProcessMetricSnapshot{}).
		Select("instance_uuid, MAX(sampled_at) AS sampled_at").
		Where("sampled_at >= ?", cutoff).
		Group("instance_uuid")
	var rows []attributionProcessRow
	err := s.db.Table("process_metric_snapshots AS p").
		Select("p.node_uuid, p.instance_uuid, i.id AS instance_id, i.name AS instance_name, i.node_id, p.p_id AS pid, p.name, p.cpu_percent, p.rss_bytes, p.sampled_at").
		Joins("JOIN (?) AS latest ON p.instance_uuid = latest.instance_uuid AND p.sampled_at = latest.sampled_at", latest).
		Joins("JOIN instances AS i ON i.uuid = p.instance_uuid").
		Where("i.status = ?", model.InstanceStatusRunning).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询受管进程归因失败: %w", err)
	}
	return rows, nil
}

func filterFreshAttributionRows(rows []attributionProcessRow, freshNodes map[string]struct{}) []attributionProcessRow {
	result := make([]attributionProcessRow, 0, len(rows))
	for _, row := range rows {
		if _, ok := freshNodes[row.NodeUUID]; ok {
			result = append(result, row)
		}
	}
	return result
}

func aggregateAttributionInstances(rows []attributionProcessRow, sortBy string, limit int) []ResourceAttributionInstance {
	byUUID := make(map[string]ResourceAttributionInstance)
	for _, row := range rows {
		item := byUUID[row.InstanceUUID]
		if item.InstanceUUID == "" {
			item = ResourceAttributionInstance{InstanceID: row.InstanceID, InstanceUUID: row.InstanceUUID, InstanceName: row.InstanceName, NodeID: row.NodeID, SampledAt: row.SampledAt}
		}
		item.CPUPct += row.CPUPercent
		item.RSSBytes += row.RSSBytes
		byUUID[row.InstanceUUID] = item
	}
	items := make([]ResourceAttributionInstance, 0, len(byUUID))
	for _, item := range byUUID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if sortBy == "cpu" {
			return items[i].CPUPct > items[j].CPUPct
		}
		return items[i].RSSBytes > items[j].RSSBytes
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func topAttributionProcesses(rows []attributionProcessRow, sortBy string, limit int) []ResourceAttributionProcess {
	sort.Slice(rows, func(i, j int) bool {
		if sortBy == "cpu" {
			return rows[i].CPUPercent > rows[j].CPUPercent
		}
		return rows[i].RSSBytes > rows[j].RSSBytes
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	items := make([]ResourceAttributionProcess, 0, len(rows))
	for _, row := range rows {
		items = append(items, ResourceAttributionProcess{InstanceID: row.InstanceID, InstanceUUID: row.InstanceUUID, InstanceName: row.InstanceName, NodeID: row.NodeID, PID: row.PID, Name: row.Name, CPUPercent: row.CPUPercent, RSSBytes: row.RSSBytes, SampledAt: row.SampledAt})
	}
	return items
}
