package model

import "time"

// InstanceCrashSnapshot 实例崩溃快照（FR-313）：进程非正常退出的现场留存。
// Worker 在进程崩溃时经 gRPC ReportCrashSnapshot 上报，CP 持久化并按实例滚动保留
// 最近 5 条（写死不做配置，见 spec §6）；实例删除时级联清理。
type InstanceCrashSnapshot struct {
	ID         uint `gorm:"primaryKey" json:"id"`
	InstanceID uint `gorm:"not null;index" json:"instanceId"`
	// OccurredAt 崩溃发生时刻（Worker 侧时钟）。
	OccurredAt time.Time `json:"occurredAt"`
	// ExitCode 进程退出码；无法获知（Wait 出错/容器 Wait 错误）时为 -1。
	ExitCode int `json:"exitCode"`
	// Signal 终止信号名（Unix，如 killed/terminated）；Windows / 非信号退出为空。
	Signal string `gorm:"type:varchar(32)" json:"signal"`
	// DurationMs 本次运行时长（毫秒）。
	DurationMs int64 `json:"durationMs"`
	// TailOutput 崩溃前终端尾部输出（Worker 侧截取，≤200 行 / 64KB）。
	TailOutput string    `gorm:"type:text" json:"tailOutput"`
	CreatedAt  time.Time `json:"createdAt"`
}
