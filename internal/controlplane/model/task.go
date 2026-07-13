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
	// TaskStateCanceled 任务被强制停止（终态，FR-227）。Worker 操作真被中断后由心跳上报。
	TaskStateCanceled TaskState = "canceled"
)

// IsTerminal 报告状态是否为终态（成功/失败/已取消）。
func (s TaskState) IsTerminal() bool {
	return s == TaskStateSucceeded || s == TaskStateFailed || s == TaskStateCanceled
}

// 任务种类常量（kind）。新增长任务类型时在此登记。
const (
	// TaskKindJDKInstall JDK 一键下载安装任务（FR-183 首批载体，见 ADR-040）。
	TaskKindJDKInstall = "jdk_install"
	// TaskKindPkgInstall 全局包安装/升级任务（FR-307）。
	TaskKindPkgInstall = "pkg_install"
	// TaskKindRuntimeInstall 非 JDK 运行时一键下载安装任务（FR-299，首批 Node.js）。
	// 复用 jdk_install 的异步任务模式；终态副作用落 model.NodeRuntime。
	TaskKindRuntimeInstall = "runtime_install"
	// TaskKindProvision 一键搭建后端子服任务（FR-319）：与其它 kind 不同，执行体在 CP 后台
	// goroutine（下载在 worker、编排在 CP），进度/终态由 CP 直写而非 worker 心跳快照。
	TaskKindProvision = "provision"
	// TaskKindImport 导入现有服务器（migrate 搬迁）任务（FR-323，CP 后台 goroutine）。
	TaskKindImport = "import"
	// TaskKindClone 克隆实例（拷贝工作目录）任务（FR-323，CP 后台 goroutine）。
	TaskKindClone = "clone"
	// TaskKindBackupCreate 备份创建（打包工作目录）任务（FR-323，CP 后台 goroutine）。
	TaskKindBackupCreate = "backup_create"
	// TaskKindBackupRestore 备份恢复（回放备份链）任务（FR-323，CP 后台 goroutine）。
	TaskKindBackupRestore = "backup_restore"
)

// Task 一条长耗时跨进程任务（如 JDK 安装）。
// task_id 为业务唯一键（UUID），Worker 经心跳上报进度时据此 upsert（见 ADR-040）。
type Task struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	TaskID string `gorm:"type:varchar(64);uniqueIndex;not null" json:"taskId"`
	NodeID uint   `gorm:"index" json:"nodeId"`
	Kind   string `gorm:"type:varchar(64);not null;index" json:"kind"`
	// InstanceID 关联实例（FR-319 provision 任务用）；0=无关联。
	// 启动闸据此拦截「核心还在下载就点启动」的实例（在途 provision 任务未终态即拒启）。
	InstanceID uint `gorm:"index" json:"instanceId"`
	// State 见 TaskState；以字符串存储便于跨进程一致。
	State    TaskState `gorm:"type:varchar(16);not null;default:pending;index" json:"state"`
	Progress int       `gorm:"not null;default:0" json:"progress"` // 0~100
	Title    string    `gorm:"type:varchar(256)" json:"title"`
	Detail   string    `gorm:"type:varchar(1024)" json:"detail"` // 发起参数摘要
	Error    string    `gorm:"type:text" json:"error"`           // 失败原因（仅 failed）
	Result   string    `gorm:"type:text" json:"result"`          // 成功结果 JSON（如安装出的 JDK 信息）
	// CancelRequested 标记用户已请求强制停止（FR-227）：CP 据此每拍经心跳向节点下发 cancel_task_ids，
	// 直到 Worker 确认中断并上报 canceled 终态。pending 未起 / 节点离线时直接置 canceled、不设此标记。
	CancelRequested bool `gorm:"not null;default:false" json:"cancelRequested"`
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
