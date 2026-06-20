package model

import "time"

// LogSource 日志来源类别。
type LogSource string

const (
	// LogSourceInstance 实例运行日志（采集自 Worker 转发的进程 stdout/stderr）。
	LogSourceInstance LogSource = "instance"
	// LogSourceControlPlane 平台 Control Plane 自身结构化日志。
	LogSourceControlPlane LogSource = "control_plane"
	// LogSourceWorker 平台 Worker Node 自身结构化日志（经 gRPC 上报或本地直采，预留）。
	LogSourceWorker LogSource = "worker"
)

// LogLevel 日志级别。实例 stdout 默认归为 info、stderr 归为 error；平台日志沿用 slog 级别。
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// LogEntry 是单条持久化日志（实例运行日志或平台运行日志）。
//
// 这是高写入、按条件检索的表，因此刻意不内嵌外键关联对象（不像 AuditLog 预加载 User）：
//   - 以 InstanceID/NodeID 的整型外键 + 复合索引承接「按实例/节点 + 时间」的高频过滤；
//   - 关键字检索走 Message 上的 DB 谓词（LIKE），不在应用层全量序列化（FR-049/FR-050）；
//   - InstanceUUID 冗余保存，便于采集侧（仅持有 UUID 的 Worker 事件流）直接落库而不必反查。
//
// 归档与保留由 LogService 负责：超阈值的旧条目滚动落盘到数据根 var/log 后从表中清理
// （参见 ADR-005 单二进制不引 ELK、ADR-010 数据根布局）。
type LogEntry struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Source 区分实例日志与平台日志，过滤维度之一。
	Source LogSource `gorm:"type:varchar(16);not null;index:idx_logs_source_time,priority:1" json:"source"`
	// Level 日志级别，过滤维度之一。
	Level LogLevel `gorm:"type:varchar(8);not null;index:idx_logs_level_time,priority:1" json:"level"`
	// InstanceID 实例主键（实例日志非零）；与 Time 组成复合索引承接按实例时间检索。
	InstanceID uint `gorm:"index:idx_logs_instance_time,priority:1" json:"instanceId"`
	// InstanceUUID 实例 UUID 冗余列，采集侧仅持有 UUID 时直接落库。
	InstanceUUID string `gorm:"type:char(36);index" json:"instanceUuid"`
	// NodeID 节点主键（实例/Worker 日志非零）；与 Time 组成复合索引承接按节点时间检索。
	NodeID uint `gorm:"index:idx_logs_node_time,priority:1" json:"nodeId"`
	// Stream 原始流名（stdout/stderr/""），仅实例日志使用，辅助前端区分。
	Stream string `gorm:"type:varchar(8)" json:"stream,omitempty"`
	// Message 日志正文（已去除行尾换行）。关键字检索在此列上做 DB 侧 LIKE。
	Message string `gorm:"type:text;not null" json:"message"`
	// Time 日志产生时间（采集/写入时刻）。所有复合索引的次序列，统一按时间倒序检索。
	Time time.Time `gorm:"not null;index:idx_logs_source_time,priority:2;index:idx_logs_level_time,priority:2;index:idx_logs_instance_time,priority:2;index:idx_logs_node_time,priority:2" json:"time"`
}

// TableName 固定表名为 logs。
func (LogEntry) TableName() string { return "logs" }
