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
	sortHealthWall(nodes, sortKey)
	return HealthWall{Nodes: nodes}, nil
}

// loadHealthWallNodes 以固定条数查询聚合出每台节点的健康快照（无逐台查询）。
func (s *PlatformObservabilityService) loadHealthWallNodes(now time.Time) ([]HealthWallNode, error) {
	var rawNodes []model.Node
	if err := s.db.Order("id ASC").Limit(healthWallNodeLimit).Find(&rawNodes).Error; err != nil {
		return nil, fmt.Errorf("查询健康墙节点失败: %w", err)
	}
	if len(rawNodes) == 0 {
		return []HealthWallNode{}, nil
	}

	instances, err := s.healthWallInstances()
	if err != nil {
		return nil, err
	}
	alertCounts, err := s.healthWallAlertCounts(instances.nodeByInstance)
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

// healthWallInstanceAgg 汇总每节点的实例状态计数与 instance_id→node_id 归属。
type healthWallInstanceAgg struct {
	running, crashed, stopped map[uint]int
	nodeByInstance            map[uint]uint
}

// healthWallInstances 一次查询全量实例，按节点聚合状态计数（替代逐台 GROUP BY）。作有界聚合。
func (s *PlatformObservabilityService) healthWallInstances() (healthWallInstanceAgg, error) {
	agg := healthWallInstanceAgg{
		running:        map[uint]int{},
		crashed:        map[uint]int{},
		stopped:        map[uint]int{},
		nodeByInstance: map[uint]uint{},
	}
	type row struct {
		ID     uint
		NodeID uint
		Status model.InstanceStatus
	}
	var rows []row
	if err := s.db.Model(&model.Instance{}).Select("id, node_id, status").Find(&rows).Error; err != nil {
		return agg, fmt.Errorf("查询健康墙实例计数失败: %w", err)
	}
	for _, r := range rows {
		agg.nodeByInstance[r.ID] = r.NodeID
		switch r.Status {
		case model.InstanceStatusRunning:
			agg.running[r.NodeID]++
		case model.InstanceStatusCrashed:
			agg.crashed[r.NodeID]++
		case model.InstanceStatusStopped:
			agg.stopped[r.NodeID]++
		}
	}
	return agg, nil
}

// healthWallAlertCounts 统计归属到每台节点的未解决告警数：节点级规则按其 target、
// 实例级规则按实例所属节点归因（借规则 TargetType 消歧 node/instance 的 ID 空间重叠）。
func (s *PlatformObservabilityService) healthWallAlertCounts(nodeByInstance map[uint]uint) (map[uint]int, error) {
	counts := map[uint]int{}
	type row struct {
		TargetID    uint
		TargetType  string
		TriggerType string
	}
	var rows []row
	err := s.db.Table("alert_events AS e").
		Select("e.target_id, r.target_type, e.trigger_type").
		Joins("JOIN alert_rules AS r ON r.id = e.rule_id").
		Where("e.resolved = ?", false).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询健康墙活跃告警失败: %w", err)
	}
	for _, r := range rows {
		switch {
		case r.TriggerType == model.AlertTriggerNodeOffline || r.TargetType == "node":
			counts[r.TargetID]++
		case r.TargetType == "instance":
			if nodeID, ok := nodeByInstance[r.TargetID]; ok {
				counts[nodeID]++
			}
		}
	}
	return counts, nil
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
	if item.Crashed > 0 || item.ActiveAlerts > 0 {
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
