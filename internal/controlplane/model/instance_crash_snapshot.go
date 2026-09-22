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
	TailOutput string `gorm:"type:text" json:"tailOutput"`
	// RootCause 根因归类（FR-470，见 service/crash_classify.go 的枚举）。
	// 旧快照（FR-470 之前入库）为空，读侧按 unknown 降级。
	RootCause string `gorm:"type:varchar(32);index" json:"rootCause"`
	// Signature 归一化「同类指纹」（Exception 类名 + 首行去除变量噪声），用于同类聚合。
	Signature string `gorm:"type:varchar(255);index" json:"signature"`
	// Evidence 命中规则的原文行（JSON 数组文本）；前端「证据」区展开展示。
	Evidence string `gorm:"type:text" json:"evidence"`
	// Confidence 归类置信度 0~1；多规则命中取最高分规则。
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"createdAt"`
}
