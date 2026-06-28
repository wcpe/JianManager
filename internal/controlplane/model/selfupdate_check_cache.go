package model

import "time"

// SelfUpdateCheckCache 持久化最近一次「成功」检查更新的结果（FR-186，增强 FR-182/FR-081）。
//
// 仅一行（ID 固定 1 单行表）：每次 live 检查成功后 upsert 覆盖。整段 CheckResult 序列化为
// JSON blob 存 ResultJSON，而非拆字段——CheckResult 随版本演进加字段时无需迁移，反序列化
// 缺字段自然降级（见 spec §6 风险）。进系统更新页直接读此缓存即时回显，后台再静默触发 live 刷新；
// 刷新失败保留旧缓存（不清行），页面据 CheckedAt 标注「上次检查时间」。
type SelfUpdateCheckCache struct {
	// ID 固定为 1：本表为单行覆盖式缓存，不按时间累积多行。
	ID uint `gorm:"primaryKey" json:"id"`
	// ResultJSON 是上次成功 CheckResult 的 JSON 序列化（含 latestVersion/source/notes/各组件状态）。
	ResultJSON string `gorm:"type:text" json:"-"`
	// Source 冗余存更新源标识（github:owner/repo@channel | feed），便于诊断不必解 blob。
	Source string `gorm:"type:varchar(255)" json:"source"`
	// CheckedAt 该次成功检查的时刻（UTC），供前端展示「上次检查：<相对时间>」。
	CheckedAt time.Time `json:"checkedAt"`
}

// SelfUpdateCheckCacheID 是单行缓存的固定主键。
const SelfUpdateCheckCacheID = 1
