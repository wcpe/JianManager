package model

import "time"

// BusinessEventCursor 业务事件位点账本（ADR-107，属的旁路能力）。
//
// **只存投递元数据，绝不存 payload**——这是与 ADR-106 的分界线：
// 位点是「信封的运输单据」，payload 是「信封里的信」。一旦本表长出 payload 列，
// 紧接着的需求必然是按 payload 查询与聚合，那正是花力气退役掉的能力。
// 故本文件**不得**新增任何 payload / output / 业务字段；内容重放的正确归属是
// ServerProbe 的 EventSpillStore，不是控制面。
//
// 两个位点的语义必须分清（docs/specs/business-event-passthrough/spec.md §2.1）：
//   - StreamSeq：CP 分配的全局位点（与 SSE 帧 `id` 同源），跨实例可比；
//   - Seq：探针分配的 per-domain 序号，**跨实例不可比**，只在同一 (实例, 域) 内单调。
//
// 行数体量与保留期强相关，默认 7 天并由后台巡检裁剪（见 service.BusinessCursorService）。
type BusinessEventCursor struct {
	ID uint `gorm:"primaryKey"`
	// StreamSeq CP 全局位点（= SSE `id`）。全局唯一：同一帧不可能被记两次，
	// 唯一索引同时也是批量写入的幂等兜底（重试/重启后重复写入被拒绝而非产生重复行）。
	StreamSeq int64 `gorm:"not null;uniqueIndex:ux_bec_seq,priority:1"`
	// InstanceID 来源实例（CP 侧 DB ID；信封里的 UUID 已由 BusinessStreamService 解析）。
	InstanceID uint `gorm:"not null;index:idx_bec_scope,priority:1"`
	// Domain 业务域。JM 只当字符串标签，不理解其语义（ADR-106）。
	Domain string `gorm:"type:varchar(64);not null;index:idx_bec_scope,priority:2"`
	// Seq 探针分配的 per-domain 序号；探针未上报时为 0（信封缺字段属正常，不是错误）。
	Seq int64 `gorm:"not null;index:idx_bec_scope,priority:3"`
	// DedupKey 插件提供的幂等锚点（原样透传）。
	DedupKey string `gorm:"type:varchar(128)"`
	// OccurredAt 事件发生时间（epoch 毫秒，探针侧时钟）。
	OccurredAt int64 `gorm:"not null"`
	// CreatedAt CP 接收并记账的时间（本机时钟）：裁剪按它算保留期，
	// 因为探针时钟可能严重偏斜，用它做保留期会把行永不删除或立刻删除。
	CreatedAt time.Time
}

// TableName 固定表名，避免 GORM 命名策略变化导致索引名/迁移漂移。
func (BusinessEventCursor) TableName() string { return "business_event_cursors" }
