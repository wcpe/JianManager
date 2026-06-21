package model

import "time"

// MetricScope 时序指标的作用域维度（ADR-013）。
type MetricScope string

const (
	// MetricScopeNode 节点级指标（CPU/内存/磁盘/网络），来自心跳。
	MetricScopeNode MetricScope = "node"
	// MetricScopeInstance 实例级指标，来自 ServerProbe（TPS/MSPT/堆/线程/CPU/uptime）。
	MetricScopeInstance MetricScope = "instance"
	// MetricScopeWorld 实例下单个世界的负载，来自 ServerProbe serverprobe_world_*。
	MetricScopeWorld MetricScope = "world"
)

// ValidMetricScope 校验 scope 是否在允许枚举内。
func ValidMetricScope(s MetricScope) bool {
	switch s {
	case MetricScopeNode, MetricScopeInstance, MetricScopeWorld:
		return true
	}
	return false
}

// 指标键（metric_key）。完整语义见 docs/specs/timeseries-metrics/api.md。
const (
	MetricNodeCPUPct    = "node_cpu_pct"
	MetricNodeMemUsed   = "node_mem_used"
	MetricNodeMemTotal  = "node_mem_total"
	MetricNodeDiskUsed  = "node_disk_used"
	MetricNodeDiskTotal = "node_disk_total"
	MetricNodeNetRxRate = "node_net_rx_rate"
	MetricNodeNetTxRate = "node_net_tx_rate"
	MetricNodeLoad      = "node_load" // 1 分钟 load average（FR-062）

	MetricInstTPS           = "inst_tps"
	MetricInstMSPT          = "inst_mspt"
	MetricInstPlayersOnline = "inst_players_online"
	MetricInstHeapUsed      = "inst_heap_used"
	MetricInstHeapMax       = "inst_heap_max"
	MetricInstThreads       = "inst_threads"
	MetricInstCPUPct        = "inst_cpu_pct"
	MetricInstUptime        = "inst_uptime"

	MetricWorldLoadedChunks = "world_loaded_chunks"
	MetricWorldEntities     = "world_entities"
	MetricWorldTileEntities = "world_tile_entities"
)

// MetricSeries 时序序列的身份维度表（ADR-013）。
// 一条序列 = (node_uuid, instance_id?, scope, metric_key, world?)；
// 样本/卷积表只引用 series_id，对新增指标键与动态世界天然可扩展。
type MetricSeries struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// NodeUUID 所属节点。
	NodeUUID string `gorm:"type:varchar(64);not null;uniqueIndex:idx_metric_series_identity,priority:1" json:"nodeUuid"`
	// InstanceID 实例级/世界级序列才有；节点级为空字符串。
	InstanceID string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_metric_series_identity,priority:2" json:"instanceId"`
	// Scope 作用域：node / instance / world。
	Scope MetricScope `gorm:"type:varchar(16);not null;uniqueIndex:idx_metric_series_identity,priority:3" json:"scope"`
	// MetricKey 指标键，见上方常量。
	MetricKey string `gorm:"type:varchar(48);not null;uniqueIndex:idx_metric_series_identity,priority:4" json:"metricKey"`
	// World scope=world 时的世界名，其余为空字符串。
	World string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_metric_series_identity,priority:5" json:"world"`
	// Unit 单位：pct/bytes/bytes_per_sec/count/ms/tps/seconds。
	Unit       string    `gorm:"type:varchar(16)" json:"unit"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

// MetricSampleRaw 原始样本（30s 粒度，留 ~48h）。
// Value 为指针：nil 表示缺测（采集源不可达），查询时渲染为断点，不补假值。
type MetricSampleRaw struct {
	ID       uint      `gorm:"primaryKey" json:"id"`
	SeriesID uint      `gorm:"not null;index:idx_metric_raw_series_ts,priority:1" json:"seriesId"`
	TS       time.Time `gorm:"not null;index:idx_metric_raw_series_ts,priority:2" json:"ts"`
	Value    *float64  `json:"value"`
}

// MetricRollup5m 5 分钟降采样档（留 ~30d）。仅在桶内有非空样本时生成，缺测为缺桶。
type MetricRollup5m struct {
	ID       uint      `gorm:"primaryKey" json:"id"`
	SeriesID uint      `gorm:"not null;index:idx_metric_5m_series_bucket,priority:1" json:"seriesId"`
	BucketTS time.Time `gorm:"not null;index:idx_metric_5m_series_bucket,priority:2" json:"bucketTs"`
	Avg      float64   `json:"avg"`
	Min      float64   `json:"min"`
	Max      float64   `json:"max"`
	Last     float64   `json:"last"`
	Count    int       `json:"count"`
}

// MetricRollup1h 1 小时降采样档（留 ≥1 年）。结构同 5m 档，独立表便于按 TTL 清理。
type MetricRollup1h struct {
	ID       uint      `gorm:"primaryKey" json:"id"`
	SeriesID uint      `gorm:"not null;index:idx_metric_1h_series_bucket,priority:1" json:"seriesId"`
	BucketTS time.Time `gorm:"not null;index:idx_metric_1h_series_bucket,priority:2" json:"bucketTs"`
	Avg      float64   `json:"avg"`
	Min      float64   `json:"min"`
	Max      float64   `json:"max"`
	Last     float64   `json:"last"`
	Count    int       `json:"count"`
}
