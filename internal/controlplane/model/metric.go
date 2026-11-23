package model

import "time"

// MetricScope 时序指标的作用域维度。
type MetricScope string

const (
	// MetricScopeNode 节点级指标（CPU/内存/磁盘/网络），来自心跳。
	MetricScopeNode MetricScope = "node"
	// MetricScopeInstance 实例级指标，来自 ServerProbe（TPS/MSPT/堆/线程/CPU/uptime）。
	MetricScopeInstance MetricScope = "instance"
	// MetricScopeWorld 实例下单个世界的负载，来自 ServerProbe serverprobe_world_*。
	MetricScopeWorld MetricScope = "world"
	// MetricScopePlatform 平台汇总维度（平台级 SLO）：仅作查询维度，不作为序列身份存储。
	MetricScopePlatform MetricScope = "platform"
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
	MetricNodeLoad      = "node_load" // 1 分钟 load average
	// MetricNodeMemPct / MetricNodeDiskPct 是内存与磁盘的**利用率百分比**（0-100，）。
	//
	// 为什么需要单独指标而不是让告警自己算 used/total：
	// 告警规则引擎只支持「单个指标值与阈值比较」（见 alert_evaluator 的
	// evaluateNodeMetricRule），不支持跨两个指标的除法。如果不在落库时算好百分比，
	// 内存/磁盘的高水位告警就根本写不出来——只能写绝对字节阈值，
	// 而节点之间磁盘大小差异很大，同一个字节阈值在一个节点上是告警、在另一个上是常态。
	//
	// 量纲与 node_cpu_pct 一致（都是 0-100 的百分比），可直接参与百分比触发。
	MetricNodeMemPct  = "node_mem_pct"
	MetricNodeDiskPct = "node_disk_pct"
	// MetricNodeIOWait 是 CPU 等 IO 的占比（pct，）。
	// 与 cpu_pct 同为 0..100 的百分比量纲，可直接参与百分比触发。
	MetricNodeIOWait = "node_iowait"
	// MetricLogIngestHealthy 日志采集健康：1=采集运行时在运行，0=未运行/创建失败。
	MetricLogIngestHealthy = "log_ingest_healthy"

	// ── 日志存储层（VL）磁盘占用（2026-10-29）──
	//
	// 为什么要进时序库而不是只做一次性告警：磁盘增长是**趋势问题**，
	// 单点阈值只能回答「现在超没超」，看不出「还有几天会满」。
	// 进了时序库就能用现有的容量预测/趋势面板。
	//
	// MetricLogVLDiskUsed 是 VL 数据目录当前占用；MetricLogVLDiskLimit 是配置的上限（0=未配）。
	MetricLogVLDiskUsed  = "log_vl_disk_used"
	MetricLogVLDiskLimit = "log_vl_disk_limit"
	// MetricLogVLDiskPct 是已用/上限的百分比（0-100）。上限未配时不落点。
	// 单拎出它是因为告警规则写百分比比写两个绝对值的比较直观得多。
	MetricLogVLDiskPct = "log_vl_disk_pct"
	// MetricLogVLRowsDropped 是因**超保留期**被 VL 丢弃的行数**速率**（rows/sec）。
	//
	// 只统计 reason="too_small_timestamp"：另外三个 reason 与保留无关
	// （debug=VL 自身调试日志、too_many_fields=字段超限、too_big_timestamp=时钟错乱）。
	MetricLogVLRowsDropped = "log_vl_rows_dropped"
	// MetricLogVLPartitions 是当前日分区数。
	//
	// 存在的意义：VL 至少保留最近 2 天，分区数 <=2 时即使超上限也不会丢。
	// 告警据此区分「危险」与「必然超限但无事可做」。
	MetricLogVLPartitions = "log_vl_partitions"

	// 由已认证 Heartbeat 当前快照沉淀的共享 Bot Worker 运行时指标。
	// 它们属于节点维度，不代表任一 Bot 或压测会话独占的资源。
	MetricWorkerProcessRSSBytes = "worker_process_rss_bytes"
	MetricWorkerProcessCPUPct   = "worker_process_cpu_pct"
	MetricBotWorkerRSSBytes     = "bot_worker_rss_bytes"
	MetricBotWorkerCPUPct       = "bot_worker_cpu_pct"
	MetricBotActiveCount        = "bot_active_count"
	MetricBotConnectingCount    = "bot_connecting_count"
	MetricBotCapacityMax        = "bot_capacity_max"
	MetricBotEventLoopP95MS     = "bot_event_loop_p95_ms"

	MetricInstTPS           = "inst_tps"
	MetricInstMSPT          = "inst_mspt"
	MetricInstPlayersOnline = "inst_players_online"
	// MetricInstPlayersMax 在线人数上限：来自 SLP `players.max` / Query `maxplayers`。
	// 供侧栏与列表显示「当前/最大」；与 `inst_players_online` 成对消费，单看它会误导（重启后归零等）。
	MetricInstPlayersMax = "inst_players_max"
	MetricInstHeapUsed   = "inst_heap_used"
	MetricInstHeapMax    = "inst_heap_max"
	MetricInstThreads    = "inst_threads"
	MetricInstCPUPct     = "inst_cpu_pct"
	MetricInstUptime     = "inst_uptime"
	// GC 累计 counter 经 CP 相邻心跳差推导的速率（探针累计值不直接当曲线）。
	MetricInstGCCount = "inst_gc_count"   // GC 次数速率（count_per_sec）
	MetricInstGCTime  = "inst_gc_time_ms" // GC 暂停占用（ms_per_sec）

	// ── 业务桥（ServerProbe ↔ JM 插件桥）中转指标 ────────────────────
	//
	// ## 为什么这几项要进时序库，而不是只看 bridge_status
	//
	// `core.bridge_status` 只能查到**当下这一瞬**的队列深度与计数。
	// 它回答不了「下午三点那次业务高峰堵了没有」——而那才是运维真正要问的问题。
	// 入时序库后才有曲线、才能做告警阈值、才能回查历史拐点。
	//
	// ## 指标语义（与 ServerProbe SenderStatus 一一对应）
	//
	// 本通道的约定是「不可靠 / 幂等 / 不堵塞」：丢弃是**设计内的降级**，
	// 但必须可观测——静默丢数据比丢数据本身更危险。
	//
	// | metric_key | 来源 | 含义 |
	// |---|---|---|
	// | bridge_queue_depth | sender.{reliable,bestEffort}Queue | 当前积压（帧） |
	// | bridge_rejected_rate | sender.{reliableRejected,bestEffortDropped} 差分 | 拒绝/丢弃速率 |
	// | bridge_receipt_rejected | sender.commandResultRejected | 回执被拒累计——回执被拒会让 JM 误判业务失败 |
	// | bridge_write_failures | sender.writeFailures/writeTimeouts | 写出失败——链路质量直接信号 |
	//
	// 队列深度与拒绝速率成对出现才有意义：只有深度高而拒绝为 0 说明消费跟得上（排队但在消化）；
	// 深度高且拒绝率高才是真的承不住了。
	MetricBridgeReliableQueue   = "bridge_reliable_queue"    // 帧，gauge：可靠事件队列深度
	MetricBridgeBestEffortQueue = "bridge_best_effort_queue" // 帧，gauge：可丢事件队列深度
	MetricBridgeRejectedRate    = "bridge_rejected_rate"     // 条/秒，rate：拒绝+丢弃总速率
	MetricBridgeReceiptRejected = "bridge_receipt_rejected"  // 条，累计：回执被拒（JM 会误判业务失败）
	MetricBridgeWriteFailures   = "bridge_write_failures"    // 次/秒，rate：写失败+写超时

	MetricWorldLoadedChunks = "world_loaded_chunks"
	MetricWorldEntities     = "world_entities"
	MetricWorldTileEntities = "world_tile_entities"
)

// MetricSeries 时序序列的身份维度表。
// 一条序列 = (node_uuid, instance_id?, scope, metric_key, world?)；
// 样本/卷积表只引用 series_id，对新增指标键与动态世界天然可扩展。
type MetricSeries struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// NodeUUID 所属节点。
	NodeUUID string `gorm:"type:varchar(64);not null;uniqueIndex:idx_metric_series_identity,priority:1" json:"nodeUuid"`
	// InstanceID 实例级/世界级序列才有；节点级为空字符串。
	InstanceID string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_metric_series_identity,priority:2" json:"instanceId"`
	// Scope 作用域：node / instance / world。
	// 与前缀 idx_metric_series_scope_metric 联合成 (scope, metric_key) 索引（M5）：
	// 排行/SLO/趋势等按「某 scope 的某指标」全表筛序列，无该索引时只能全表扫。
	Scope MetricScope `gorm:"type:varchar(16);not null;uniqueIndex:idx_metric_series_identity,priority:3;index:idx_metric_series_scope_metric,priority:1" json:"scope"`
	// MetricKey 指标键，见上方常量。
	MetricKey string `gorm:"type:varchar(48);not null;uniqueIndex:idx_metric_series_identity,priority:4;index:idx_metric_series_scope_metric,priority:2" json:"metricKey"`
	// World scope=world 时的世界名，其余为空字符串。
	World string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_metric_series_identity,priority:5" json:"world"`
	// Unit 单位：pct/bytes/bytes_per_sec/count/ms/tps/seconds/load/count_per_sec/ms_per_sec。
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

// ProcessMetricSnapshot 受管实例进程 TOPN 短期快照。
type ProcessMetricSnapshot struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// NodeUUID 所属节点。
	NodeUUID string `gorm:"type:varchar(64);not null;index:idx_process_metric_node_sampled,priority:1" json:"nodeUuid"`
	// InstanceUUID 受管实例 UUID。
	InstanceUUID string `gorm:"type:varchar(64);not null;index:idx_process_metric_instance_sampled,priority:1" json:"instanceUuid"`
	// PID 显式列名 pid：避免 GORM 把 PID 缩写蛇形化为 p_id，与 managedProcessHistory 的
	// WHERE pid = ? 查询对齐（真机验收抓到：SQL logic error: no such column: pid）。
	PID              int32     `gorm:"column:pid;not null" json:"pid"`
	Name             string    `gorm:"type:varchar(128)" json:"name"`
	CPUPercent       float64   `json:"cpuPercent"`
	RSSBytes         uint64    `json:"rssBytes"`
	ReadBytesPerSec  uint64    `json:"readBytesPerSec"`
	WriteBytesPerSec uint64    `json:"writeBytesPerSec"`
	User             string    `gorm:"type:varchar(128)" json:"user"`
	CommandSummary   string    `gorm:"type:varchar(160)" json:"commandSummary"`
	SampledAt        time.Time `gorm:"not null;index:idx_process_metric_instance_sampled,priority:2;index:idx_process_metric_node_sampled,priority:2" json:"sampledAt"`
	CreatedAt        time.Time `json:"createdAt"`
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
