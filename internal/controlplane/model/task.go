package model

import "time"

// TaskState 任务状态。pending/running 为非终态，succeeded/failed 为终态。
type TaskState string

const (
	// TaskStatePending 任务已创建，尚未在 Worker 上开始执行。
	TaskStatePending TaskState = "pending"
	// TaskStateRunning 任务执行中。
	TaskStateRunning TaskState = "running"
	// TaskStateSucceeded 任务成功完成（终态）。
	TaskStateSucceeded TaskState = "succeeded"
	// TaskStateFailed 任务失败（终态）。
	TaskStateFailed TaskState = "failed"
)

// IsTerminal 报告状态是否为终态（成功或失败）。
func (s TaskState) IsTerminal() bool {
	return s == TaskStateSucceeded || s == TaskStateFailed
}

// 任务种类常量（kind）。新增长任务类型时在此登记。
const (
	// TaskKindJDKInstall JDK 一键下载安装任务（FR-183 首批载体，见 ADR-040）。
	TaskKindJDKInstall = "jdk_install"
)

// Task 一条长耗时跨进程任务（如 JDK 安装）。
// task_id 为业务唯一键（UUID），Worker 经心跳上报进度时据此 upsert（见 ADR-040）。
type Task struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	TaskID string `gorm:"type:varchar(64);uniqueIndex;not null" json:"taskId"`
	NodeID uint   `gorm:"index" json:"nodeId"`
	Kind   string `gorm:"type:varchar(64);not null;index" json:"kind"`
	// State 见 TaskState；以字符串存储便于跨进程一致。
	State    TaskState `gorm:"type:varchar(16);not null;default:pending;index" json:"state"`
	Progress int       `gorm:"not null;default:0" json:"progress"` // 0~100
	Title    string    `gorm:"type:varchar(256)" json:"title"`
	Detail   string    `gorm:"type:varchar(1024)" json:"detail"` // 发起参数摘要
	Error    string    `gorm:"type:text" json:"error"`           // 失败原因（仅 failed）
	Result   string    `gorm:"type:text" json:"result"`          // 成功结果 JSON（如安装出的 JDK 信息）
	// CreatedBy 发起用户 ID，用于归属隔离与完成站内信收件人（0=系统）。
	CreatedBy uint      `gorm:"index" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TaskLog 任务的一行滚动日志。按 task_id + seq 幂等去重（心跳可能重复携带同一行）。
type TaskLog struct {
	ID     uint      `gorm:"primaryKey" json:"id"`
	TaskID string    `gorm:"type:varchar(64);not null;index:idx_tasklog_task_seq,unique,priority:1" json:"taskId"`
	Seq    int       `gorm:"not null;index:idx_tasklog_task_seq,unique,priority:2" json:"seq"`
	Line   string    `gorm:"type:text" json:"line"`
	TS     time.Time `json:"ts"`
}
