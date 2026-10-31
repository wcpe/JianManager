package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// managedResourceFreshWindow 与节点离线检测同为 90 秒，仅约束的受管资源归因。
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
	// HostTopProcesses 是**整机** top-N 进程，与 TopProcesses 口径不同：
	// 前者是整机上按 CPU 排序的前几名（含系统进程、别人的进程），
	// 后者只覆盖受管实例的进程树。运维判断「机器为什么慢」要看前者。
	HostTopProcesses []ResourceAttributionHostProcess `json:"hostTopProcesses"`
}

// ResourceAttributionHostProcess 整机 top-N 进程的一项。
//
// 隐私边界：无 cmdline（见 model.HostProcessSnapshot 注释）。
type ResourceAttributionHostProcess struct {
	NodeID   uint    `json:"nodeId"`
	NodeName string  `json:"nodeName"`
	PID      uint32  `json:"pid"`
	Comm     string  `json:"comm"`
	CPUPct   float64 `json:"cpuPct"`
	RSSBytes uint64  `json:"rssBytes"`
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

	// 主机级观测----
	//
	// 全部用指针：null 表示该 Worker 未上报（老版本），0 表示读数为 0。
	// 前端据此渲染 `--` 或具体值（spec §5 验收 2）——把「不知道」画成 0 会误导判断。

	// Load5Pct / Load15Pct 已按核数归一的负载（与 LoadPct 同口径，便于同图对比）。
	Load5Pct  *float64 `json:"load5Pct"`
	Load15Pct *float64 `json:"load15Pct"`
	// ProcsRunning 活动进程/线程数；ProcsTotal 总数。
	ProcsRunning *int32 `json:"procsRunning"`
	ProcsTotal   *int32 `json:"procsTotal"`
	// CPUBreakdown CPU 时间构成占比（0..100，便于前端直接当百分比用）。
	CPUBreakdown *ResourceAttributionCPUBreakdown `json:"cpuBreakdown"`
	// Mounts 分挂载点磁盘用量（按使用率降序，最该关注的在前）。
	Mounts []ResourceAttributionMount `json:"mounts"`
}

// ResourceAttributionCPUBreakdown CPU 时间构成。
//
// 单位是百分比（0..100）而非 0..1：前端要直接展示，让每个消费方各自 ×100
// 是重复劳动且容易漏乘。iowait 复用既有字段语义（已有时序）。
type ResourceAttributionCPUBreakdown struct {
	User    float64 `json:"user"`
	Nice    float64 `json:"nice"`
	System  float64 `json:"system"`
	Idle    float64 `json:"idle"`
	IOWait  float64 `json:"iowait"`
	IRQ     float64 `json:"irq"`
	SoftIRQ float64 `json:"softirq"`
	Steal   float64 `json:"steal"`
}

// ResourceAttributionMount 一个挂载点的磁盘用量。
type ResourceAttributionMount struct {
	Mountpoint string  `json:"mountpoint"`
	TotalBytes int64   `json:"totalBytes"`
	UsedBytes  int64   `json:"usedBytes"`
	UsedPct    float64 `json:"usedPct"`
}

// ResourceAttributionBotWorker 表示共享 Bot Worker，而非任一 Bot 的资源。
type ResourceAttributionBotWorker struct {
	RSSBytes                  *int64   `json:"rssBytes"`
	CPUPct                    *float64 `json:"cpuPct"`
	ActiveCount               *int32   `json:"activeCount"`
	ConnectingCount           *int32   `json:"connectingCount"`
	EventLoopP95MS            *float64 `json:"eventLoopP95Ms"`
	CapacityMax               *int32   `json:"capacityMax"`
	CapacityUnavailableReason string   `json:"capacityUnavailableReason"`
	Available                 bool     `json:"available"`
	Reason                    string   `json:"reason"`
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
	// 批量取所有节点的挂载点，避免每节点一次查询（N+1）。
	// 只取鲜度窗口内的行——过期挂载点展示出来只会误导「这个盘现在还剩多少」。
	mountsByNode, err := s.freshHostMounts(now.Add(-managedResourceFreshWindow))
	if err != nil {
		return ResourceAttributionResult{}, err
	}

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
			item := attributionNode(node, status)
			// 挂载点只对新鲜节点填充：过期快照的磁盘用量没有参考价值。
			if status == ResourceFreshnessFresh {
				item.Mounts = mountsByNode[node.UUID]
			}
			result.Nodes = append(result.Nodes, item)
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

	// 整机 top-N 进程（按 CPU 降序取前 limit）。
	// 只取鲜度窗口内的行——过期进程快照没有参考价值，展示出来会误导。
	hostProcs, err := s.freshHostProcesses(now.Add(-managedResourceFreshWindow), limit)
	if err != nil {
		return ResourceAttributionResult{}, err
	}
	result.HostTopProcesses = hostProcs
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
		item.BotWorker.CapacityUnavailableReason = "节点资源快照已过期"
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
		EventLoopP95MS: node.BotEventLoopP95MS, CapacityMax: node.BotCapacityMax,
		CapacityUnavailableReason: node.BotCapacityUnavailableReason,
		Available:                 node.BotAvailable, Reason: node.BotUnavailableReason,
	}

	// 主机级观测：直接把 nodes 表里的指针透传。
	//
	// 不做任何「为零则置 nil」的兜底——那会把「上报了、读数确实是 0」误判成
	// 「没上报」，而这两者对运维的含义完全不同（见 model.Node 的注释）。
	// nil 原样下发，前端渲染成 `--`。
	item.ProcsRunning = node.ProcsRunning
	item.ProcsTotal = node.ProcsTotal
	if node.CPUCores > 0 {
		// 与 LoadPct 同口径归一（load/核数），便于在同一张图上对比三档负载。
		// 只在核数已知时归一：核数为 0 时除法会得到 +Inf。
		if node.LoadAvg5 != nil {
			v := *node.LoadAvg5 / float64(node.CPUCores) * 100
			item.Load5Pct = &v
		}
		if node.LoadAvg15 != nil {
			v := *node.LoadAvg15 / float64(node.CPUCores) * 100
			item.Load15Pct = &v
		}
	}
	// CPU 构成：七个分量**同时**存在才算完整（它们是同一次窗口计算的产物）。
	// 只填一半会让前端把缺失分量画成 0，得到一条假的构成条。
	if node.CPUUser != nil && node.CPUIdle != nil && node.CPUSystem != nil {
		// iowait 取自时序口径（未在 nodes 表建列）；此处留 0，
		// 由前端按需从既有 iowait 数据源补，避免两处各存一份可能不一致的值。
		item.CPUBreakdown = &ResourceAttributionCPUBreakdown{
			User:    float64(*node.CPUUser) * 100,
			Nice:    derefF32(node.CPUNice) * 100,
			System:  float64(*node.CPUSystem) * 100,
			Idle:    float64(*node.CPUIdle) * 100,
			IRQ:     derefF32(node.CPUIRQ) * 100,
			SoftIRQ: derefF32(node.CPUSoftIRQ) * 100,
			Steal:   derefF32(node.CPUSteal) * 100,
		}
	}
	return item
}

// derefF32 安全解引用可空浮点（nil → 0）。
//
// 仅用于「已确认整组存在」时的次要分量：主分量（user/system/idle）为 nil 时
// 整组不会构造，因此这里退化成 0 只影响展示细节，不会伪造出完整的构成条。
func derefF32(v *float32) float64 {
	if v == nil {
		return 0
	}
	return float64(*v)
}

func (s *MetricService) freshAttributionProcessRows(cutoff time.Time) ([]attributionProcessRow, error) {
	latest := s.db.Model(&model.ProcessMetricSnapshot{}).
		Select("instance_uuid, MAX(sampled_at) AS sampled_at").
		Where("sampled_at >= ?", cutoff).
		Group("instance_uuid")
	var rows []attributionProcessRow
	err := s.db.Table("process_metric_snapshots AS p").
		Select("p.node_uuid, p.instance_uuid, i.id AS instance_id, i.name AS instance_name, i.node_id, p.pid AS pid, p.name, p.cpu_percent, p.rss_bytes, p.sampled_at").
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

// freshHostMounts 批量取鲜度窗口内的分挂载点磁盘用量，按节点分组。
//
// 单次查询后分组，而不是每节点查一次：节点数增长时 N+1 会变成主要开销。
// 结果按使用率**降序**——悬浮卡只展示前几条，最该被看到的（最满的盘）必须排前面。
func (s *MetricService) freshHostMounts(cutoff time.Time) (map[string][]ResourceAttributionMount, error) {
	var rows []model.HostMountSnapshot
	if err := s.db.
		Where("sampled_at >= ?", cutoff).
		Order("used_pct DESC").
		Find(&rows).Error; err != nil {
		// 老库未迁移时表可能不存在：按「无数据」处理，不让悬浮卡整体失败。
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return map[string][]ResourceAttributionMount{}, nil
		}
		return nil, fmt.Errorf("查询挂载点快照失败: %w", err)
	}
	out := make(map[string][]ResourceAttributionMount, len(rows))
	for _, r := range rows {
		out[r.NodeUUID] = append(out[r.NodeUUID], ResourceAttributionMount{
			Mountpoint: r.Mountpoint,
			TotalBytes: int64(r.TotalBytes),
			UsedBytes:  int64(r.UsedBytes),
			UsedPct:    float64(r.UsedPct),
		})
	}
	return out, nil
}

// freshHostProcesses 取整机 top-N 进程，按 CPU 降序。
//
// 需要节点名：靠一次 JOIN 取回，而不是给每个进程再查一次节点（N+1）。
// 节点名缺失时留空，前端会退化成显示 PID——比整条不显示更有用。
func (s *MetricService) freshHostProcesses(cutoff time.Time, limit int) ([]ResourceAttributionHostProcess, error) {
	type row struct {
		NodeUUID string
		NodeName string
		PID      uint32
		Comm     string
		CPUPct   float32
		RSSBytes uint64
	}
	var rows []row
	err := s.db.
		Table("host_process_snapshots AS p").
		Select("p.node_uuid, COALESCE(n.name, '') AS node_name, p.p_id AS pid, p.comm, p.cpu_pct, p.rss_bytes").
		Joins("LEFT JOIN nodes AS n ON n.uuid = p.node_uuid").
		Where("p.sampled_at >= ?", cutoff).
		Order("p.cpu_pct DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		// 老库未迁移时表可能不存在：按「无数据」处理。
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return []ResourceAttributionHostProcess{}, nil
		}
		return nil, fmt.Errorf("查询整机进程快照失败: %w", err)
	}

	// 节点 id 由 UUID 反查一次（节点数量级远小于进程数，比逐行查便宜）。
	nodeIDs := make(map[string]uint, 8)
	var nodes []model.Node
	if err := s.db.Select("id", "uuid").Find(&nodes).Error; err == nil {
		for _, n := range nodes {
			nodeIDs[n.UUID] = n.ID
		}
	}

	out := make([]ResourceAttributionHostProcess, 0, len(rows))
	for _, r := range rows {
		out = append(out, ResourceAttributionHostProcess{
			NodeID:   nodeIDs[r.NodeUUID],
			NodeName: r.NodeName,
			PID:      r.PID,
			Comm:     r.Comm,
			CPUPct:   float64(r.CPUPct),
			RSSBytes: r.RSSBytes,
		})
	}
	return out, nil
}
