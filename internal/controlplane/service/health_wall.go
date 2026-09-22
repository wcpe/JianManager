package service

import (
	"fmt"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 健康墙单格分级（FR-461）。严重度递增，用于着色与排序。
const (
	HealthLevelHealthy  = "healthy"
	HealthLevelDegraded = "degraded"
	HealthLevelStale    = "stale"
	HealthLevelOffline  = "offline"
)

// healthWallNodeLimit 健康墙一次返回的节点上限（矩阵一次查询给全，去 N+1；远超 60+ 常见规模）。
const healthWallNodeLimit = 500

// degradedResourcePct 资源水位达到该百分比即视为降级。
const degradedResourcePct = 90

// HealthWallNode 是健康墙单格：一台节点的当前只读快照。
type HealthWallNode struct {
	NodeID uint   `json:"nodeId"`
	UUID   string `json:"nodeUuid"`
	Name   string `json:"name"`
	// Zone 节点分组/大区。当前 Node 模型未携带分组字段，保留占位以便后续接入。
	Zone string `json:"zone,omitempty"`
	// Freshness 快照鲜度：fresh|stale|offline（复用 resourceFreshnessAt 口径）。
	Freshness string `json:"freshness"`
	// CPUPct/MemPct/DiskPct 仅鲜活节点给出，陈旧/离线为 nil（不把 DB 陈旧值当当前水位）。
	CPUPct  *float64 `json:"cpuPct"`
	MemPct  *float64 `json:"memPct"`
	DiskPct *float64 `json:"diskPct"`
	// 实例计数。
	Running int `json:"running"`
	Crashed int `json:"crashed"`
	Stopped int `json:"stopped"`
	// Degraded 是「活着但已不可服务」的实例数（FR-459/FR-461 验收 7）：状态仍为 RUNNING
	// 但巡检已写入 status_reason（假死 / 崩溃熔断等）。假死进程不会退出、状态也不会转 CRASHED，
	// 只按 status 分级会让这类实例在健康墙上完全不可见，故单列计数并参与降级判定。
	Degraded int `json:"degraded"`
	// ActiveAlerts 归属本节点的未解决告警数（含本节点上的实例告警）。
	ActiveAlerts int `json:"activeAlerts"`
	// BotActive/BotConnecting 共享 Bot Worker 运行时计数（缺测为 nil）。
	BotActive     *int32 `json:"botActive"`
	BotConnecting *int32 `json:"botConnecting"`
	// Level 分级：offline|stale|degraded|healthy。
	Level string `json:"level"`
	// Href 一键定位：/monitoring?node=<uuid>。
	Href string `json:"href"`
}

// HealthWall 是逐台健康矩阵读模型（FR-461）。
type HealthWall struct {
	Nodes []HealthWallNode `json:"nodes"`
	// Truncated 为 true 表示节点数超过 healthWallNodeLimit，响应已被按 severity 截断。
	Truncated bool `json:"truncated"`
}

// HealthWall 返回逐台健康矩阵（默认按 severity 降序）。只读 CP 快照，绝不触发 Worker RPC。
func (s *PlatformObservabilityService) HealthWall(sortKey string) (HealthWall, error) {
	return s.HealthWallAt(time.Now().UTC(), sortKey)
}

// HealthWallAt 注入当前时刻，确保鲜度/分级口径可测。
func (s *PlatformObservabilityService) HealthWallAt(now time.Time, sortKey string) (HealthWall, error) {
	nodes, err := s.loadHealthWallNodes(now)
	if err != nil {
		return HealthWall{}, err
	}
	for i := range nodes {
		nodes[i].Level = healthLevelAt(nodes[i])
	}
	// 先全量排序再按 severity 截断：避免「按 id 截断」丢最严重节点，并如实回报截断标记。
	sortHealthWall(nodes, sortKey)
	truncated := false
	if len(nodes) > healthWallNodeLimit {
		nodes = nodes[:healthWallNodeLimit]
		truncated = true
	}
	return HealthWall{Nodes: nodes, Truncated: truncated}, nil
}

// loadHealthWallNodes 全量载入节点快照（数量受 fleet 规模约束，不做逐台查询），并以常数条
// 聚合查询补齐实例计数与活跃告警。节点数不再在查询层按 id 截断——截断推迟到按 severity
// 排序之后，避免丢失最严重节点。
func (s *PlatformObservabilityService) loadHealthWallNodes(now time.Time) ([]HealthWallNode, error) {
	var rawNodes []model.Node
	if err := s.db.Order("id ASC").Find(&rawNodes).Error; err != nil {
		return nil, fmt.Errorf("查询健康墙节点失败: %w", err)
	}
	if len(rawNodes) == 0 {
		return []HealthWallNode{}, nil
	}

	instances, err := s.healthWallInstances()
	if err != nil {
		return nil, err
	}
	alertCounts, err := s.healthWallAlertCounts()
	if err != nil {
		return nil, err
	}

	out := make([]HealthWallNode, 0, len(rawNodes))
	for i := range rawNodes {
		node := rawNodes[i]
		item := HealthWallNode{
			NodeID: node.ID,
			UUID:   node.UUID,
			Name:   node.Name,
			Href:   "/monitoring?node=" + node.UUID,
		}
		freshness := resourceFreshnessAt(node, now)
		item.Freshness = healthWallFreshness(freshness)
		item.Running = instances.running[node.ID]
		item.Crashed = instances.crashed[node.ID]
		item.Stopped = instances.stopped[node.ID]
		item.Degraded = instances.degraded[node.ID]
		item.ActiveAlerts = alertCounts[node.ID]
		if freshness == ResourceFreshnessFresh {
			item.CPUPct = float64Pointer(float64(node.CPUUsage) * 100)
			if node.MemoryMB > 0 {
				item.MemPct = float64Pointer(float64(node.MemoryUsedMB) / float64(node.MemoryMB) * 100)
			}
			item.DiskPct = float64Pointer(float64(node.DiskUsage) * 100)
			item.BotActive = node.BotActiveCount
			item.BotConnecting = node.BotConnectingCount
		}
		out = append(out, item)
	}
	return out, nil
}

// healthWallInstanceAgg 汇总每节点的实例状态计数。
type healthWallInstanceAgg struct {
	running, crashed, stopped, degraded map[uint]int
}

// healthWallInstances 以 GROUP BY (node_id, status) 在库侧聚合各节点实例状态计数（有界聚合，
// 替代全表加载逐行累加）。
//
// degraded 取 RUNNING 且 status_reason 非空的行数（FR-459：假死实例状态仍是 RUNNING，
// 只看 status 分级会让它对健康墙完全不可见）。
func (s *PlatformObservabilityService) healthWallInstances() (healthWallInstanceAgg, error) {
	agg := healthWallInstanceAgg{
		running:  map[uint]int{},
		crashed:  map[uint]int{},
		stopped:  map[uint]int{},
		degraded: map[uint]int{},
	}
	type row struct {
		NodeID uint
		Status model.InstanceStatus
		Count  int
		// Reasoned 是该 (node,status) 分组内 status_reason 非空的行数。
		Reasoned int
	}
	var rows []row
	if err := s.db.Model(&model.Instance{}).
		Select("node_id, status, COUNT(*) AS count, " +
			"SUM(CASE WHEN status_reason IS NOT NULL AND status_reason <> '' THEN 1 ELSE 0 END) AS reasoned").
		Group("node_id, status").Find(&rows).Error; err != nil {
		return agg, fmt.Errorf("查询健康墙实例计数失败: %w", err)
	}
	for _, r := range rows {
		switch r.Status {
		case model.InstanceStatusRunning:
			agg.running[r.NodeID] += r.Count
			agg.degraded[r.NodeID] += r.Reasoned
		case model.InstanceStatusCrashed:
			agg.crashed[r.NodeID] += r.Count
		case model.InstanceStatusStopped:
			agg.stopped[r.NodeID] += r.Count
		}
	}
	return agg, nil
}

// healthWallAlertCounts 统计归属到每台节点的未解决告警数：节点级规则按其 target、
// 实例级规则经 instances 表把实例 ID 映射回所属节点（借规则 Scope/TargetType/触发类型
// 消歧 node/instance 的 ID 空间重叠；维度缺失时按触发类型推断，仍无法判别则不归因，
// 避免把实例 ID 当节点 ID 错归因到某台节点）。
func (s *PlatformObservabilityService) healthWallAlertCounts() (map[uint]int, error) {
	counts := map[uint]int{}
	type row struct {
		TargetID       uint
		TargetType     string
		Scope          string
		TriggerType    string
		InstanceNodeID *uint
	}
	var rows []row
	err := s.db.Table("alert_events AS e").
		Select("e.target_id, COALESCE(r.target_type, '') AS target_type, COALESCE(r.scope, '') AS scope, COALESCE(e.trigger_type, '') AS trigger_type, i.node_id AS instance_node_id").
		Joins("JOIN alert_rules AS r ON r.id = e.rule_id").
		Joins("LEFT JOIN instances AS i ON i.id = e.target_id AND i.deleted_at IS NULL").
		Where("e.resolved = ?", false).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询健康墙活跃告警失败: %w", err)
	}
	for _, r := range rows {
		if nodeID, ok := attributeAlertNode(r.TargetID, r.TargetType, r.Scope, r.TriggerType, r.InstanceNodeID); ok {
			counts[nodeID]++
		}
	}
	return counts, nil
}

// attributeAlertNode 把一条活跃告警归因到节点 ID。维度判定优先级：显式 Scope > TargetType >
// 触发类型家族（node_offline 恒为节点，实例家族恒为实例）。
//   - 节点维度：target 直接作节点 ID；
//   - 实例维度：经 instances 映射回所属节点，映射不到则不归因；
//   - 维度完全缺失（scope/target_type 皆空，如前 FR-462 存量行的 baseline/saturation）：node 与
//     instance 的 target_id 数字空间重叠，无法安全判别，故不归因（宁可少计，也不把 instance 的
//     target_id 当节点 ID 错归因到某台节点）。
func attributeAlertNode(targetID uint, targetType, scope, triggerType string, instanceNodeID *uint) (uint, bool) {
	if targetType == "node" || scope == "node" || triggerType == model.AlertTriggerNodeOffline {
		return targetID, true
	}
	if targetType == "instance" || scope == "instance" || isInstanceScopedTrigger(triggerType) {
		if instanceNodeID != nil {
			return *instanceNodeID, true
		}
		return 0, false
	}
	return 0, false
}

// isInstanceScopedTrigger 判断触发类型是否天然作用于实例维度。
func isInstanceScopedTrigger(triggerType string) bool {
	switch triggerType {
	case model.AlertTriggerInstanceCrash, model.AlertTriggerLogKeyword,
		model.AlertTriggerPlayerEvent, model.AlertTriggerBackupFailed:
		return true
	}
	return false
}

// healthWallFreshness 把资源鲜度映射为健康墙三态（online 但无心跳视为 stale）。
func healthWallFreshness(f ResourceFreshness) string {
	switch f {
	case ResourceFreshnessFresh:
		return "fresh"
	case ResourceFreshnessOffline:
		return "offline"
	default: // stale / unavailable
		return "stale"
	}
}

// healthLevelAt 判定单格分级：offline > stale > degraded > healthy。
func healthLevelAt(item HealthWallNode) string {
	switch item.Freshness {
	case "offline":
		return HealthLevelOffline
	case "stale":
		return HealthLevelStale
	}
	if item.Crashed > 0 || item.Degraded > 0 || item.ActiveAlerts > 0 {
		return HealthLevelDegraded
	}
	for _, v := range []*float64{item.CPUPct, item.MemPct, item.DiskPct} {
		if v != nil && *v >= degradedResourcePct {
			return HealthLevelDegraded
		}
	}
	return HealthLevelHealthy
}

// healthLevelRank 分级严重度（越大越严重），供排序。
func healthLevelRank(level string) int {
	switch level {
	case HealthLevelOffline:
		return 3
	case HealthLevelStale:
		return 2
	case HealthLevelDegraded:
		return 1
	default:
		return 0
	}
}

// sortHealthWall 按 sortKey 排序：主键降序，同级按 severity 降序、节点 ID 升序。
func sortHealthWall(nodes []HealthWallNode, sortKey string) {
	metricDesc := func(v *float64) float64 {
		if v == nil {
			return -1 // 缺测排在最后
		}
		return *v
	}
	primary := func(a, b HealthWallNode) int {
		switch sortKey {
		case "cpu":
			return compareFloatDesc(metricDesc(a.CPUPct), metricDesc(b.CPUPct))
		case "mem":
			return compareFloatDesc(metricDesc(a.MemPct), metricDesc(b.MemPct))
		case "disk":
			return compareFloatDesc(metricDesc(a.DiskPct), metricDesc(b.DiskPct))
		case "instances":
			if d := compareIntDesc(a.Running, b.Running); d != 0 {
				return d
			}
			return compareIntDesc(a.Crashed, b.Crashed)
		default: // level
			return compareIntDesc(healthLevelRank(a.Level), healthLevelRank(b.Level))
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if d := primary(a, b); d != 0 {
			return d > 0
		}
		if d := compareIntDesc(healthLevelRank(a.Level), healthLevelRank(b.Level)); d != 0 {
			return d > 0
		}
		if d := compareFloatDesc(healthWallMaxWater(a), healthWallMaxWater(b)); d != 0 {
			return d > 0
		}
		return a.NodeID < b.NodeID
	})
}

// healthWallMaxWater 取节点资源水位最大值（排序次级键）。
func healthWallMaxWater(n HealthWallNode) float64 {
	max := -1.0
	for _, v := range []*float64{n.CPUPct, n.MemPct, n.DiskPct} {
		if v != nil && *v > max {
			max = *v
		}
	}
	return max
}

func compareFloatDesc(a, b float64) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	default:
		return 0
	}
}

func compareIntDesc(a, b int) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	default:
		return 0
	}
}
