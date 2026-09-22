package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SnapshotKind 快照类型（FR-466 §2.1）。
type SnapshotKind string

const (
	// SnapshotKindManual 运维手动创建的时间点快照。
	SnapshotKindManual SnapshotKind = "manual"
	// SnapshotKindPreRollback 回滚前自动创建的快照（**强制且不可关闭**，spec §2.3）。
	// 用途：保证「回滚仍是可回滚的」——否则误点回滚会把当时可用状态永久覆盖。
	SnapshotKindPreRollback SnapshotKind = "pre_rollback"
	// SnapshotKindScheduled 定时快照（预留；本版不实现调度器，字段先留）。
	SnapshotKindScheduled SnapshotKind = "scheduled"
)

// SnapshotState 快照生命周期状态。
type SnapshotState string

const (
	// SnapshotStatePending 已登记，归档尚未完成（不可回滚）。
	SnapshotStatePending SnapshotState = "pending"
	// SnapshotStateRunning 归档中。
	SnapshotStateRunning SnapshotState = "running"
	// SnapshotStateCompleted 归档完成，可作为回滚目标。
	SnapshotStateCompleted SnapshotState = "completed"
	// SnapshotStateFailed 归档失败（不可回滚）。
	SnapshotStateFailed SnapshotState = "failed"
	// SnapshotStateRolledBack 该快照已被用作回滚目标（留痕，仍可再次回滚）。
	SnapshotStateRolledBack SnapshotState = "rolled_back"
)

// SnapshotBackingState 快照底链（RootBackupID 指向的 Backup 行）的可用性。
//
// **读时派生**，不落库：底链会被人工删除、并发清理或（历史上）保留策略裁剪改变，
// 持久化该字段只会得到陈旧值——而陈旧值正是「列表显示可回滚、点下去报 record not found」
// 这个 B-1 缺陷的成因，故一律在读路径现算。
type SnapshotBackingState string

const (
	// SnapshotBackingOK 底链存在且未软删，可作为回滚目标（仍需 State 可回滚）。
	SnapshotBackingOK SnapshotBackingState = "ok"
	// SnapshotBackingMissing 底链已缺失（未登记 / 被删除 / 被裁剪），快照不可回滚。
	SnapshotBackingMissing SnapshotBackingState = "missing"
	// SnapshotBackingUnchecked 未校验（如只需状态语义、不涉及回放的单条读取）。
	SnapshotBackingUnchecked SnapshotBackingState = ""
)

// InstanceSnapshot 实例整机快照（FR-466）：一次快照 = 一个时间点语义的整机可回滚点。
//
// 设计决策（spec §2.1）：**不把 Kind 直接加到 Backup 表**。Backup 已承载全量/增量/
// 远程存储/校验和等归档语义，且其 ParentID 链语义与快照的「独立时间点」语义冲突
// （快照必须是自包含的全量，挂上增量链后链上任一环损坏都会让快照失效）。
// 故新增薄表，RootBackupID 指向本次落地的 Backup 记录，归档与回放全部复用既有实现。
type InstanceSnapshot struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// UUID 对外标识（任务详情、审计与前端 key）。
	UUID       string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID uint   `gorm:"not null;index" json:"instanceId"`
	Name       string `gorm:"type:varchar(128)" json:"name"`
	// Kind 见 SnapshotKind。
	Kind SnapshotKind `gorm:"type:varchar(24);not null;index" json:"kind"`
	// State 见 SnapshotState。仅 completed / rolled_back 可作回滚目标。
	State SnapshotState `gorm:"type:varchar(16);not null;default:pending;index" json:"state"`
	// RootBackupID 本次快照底层落地的 Backup 记录 ID（全量）。
	RootBackupID uint `gorm:"not null;index" json:"rootBackupId"`
	// BinaryName / BinarySHA256 快照时刻的启动二进制指纹（与 FR-468 协同）：
	// 保证「数据 + 版本」成对恢复时可核对，回滚时若不一致只提示不越权替换可执行文件。
	BinaryName   string `gorm:"type:varchar(255)" json:"binaryName"`
	BinarySHA256 string `gorm:"type:char(64)" json:"binarySha256"`
	// ConfigHash 快照时刻关键元数据摘要（启动命令 + 环境变量），用于回滚差异提示。
	ConfigHash string `gorm:"type:char(64)" json:"configHash"`
	// ConfigSummary 上述元数据的人可读摘要（前端展示「当时用的什么命令」）。
	ConfigSummary string `gorm:"type:varchar(512)" json:"configSummary"`
	// TriggeredBy 操作人用户 ID（0=系统）。
	TriggeredBy uint `gorm:"index" json:"triggeredBy"`
	// TriggeredByRollbackID 该快照是由哪次回滚触发的（仅 pre_rollback 有值），
	// 形成「回滚 → 退回」可视链（FR-466 §2.4）。
	TriggeredByRollbackID uint `gorm:"default:0" json:"triggeredByRollbackId"`
	// SizeMB 归档大小（MiB，来自底层 Backup）。
	SizeMB float64 `gorm:"default:0" json:"sizeMb"`
	// FailureReason 归档失败原因（仅 failed）。
	FailureReason string `gorm:"type:varchar(512)" json:"failureReason"`
	// Note 创建时的一致性提示（如「创建时实例未停止，MC 世界文件可能非一致」）。
	Note string `gorm:"type:varchar(512)" json:"note"`
	// RootBackupState 底链可用性（读时派生，见 SnapshotBackingState）。
	RootBackupState SnapshotBackingState `gorm:"-" json:"rootBackupState,omitempty"`
	// NotRollableReason 状态本可回滚但底链缺失时的显式原因；
	// 有值即表示「不可回滚」，前端据此禁用按钮并展示原因，而不是让用户点了才失败。
	NotRollableReason string         `gorm:"-" json:"notRollableReason,omitempty"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID（与 Backup 同口径）。
func (s *InstanceSnapshot) BeforeCreate(tx *gorm.DB) error {
	if s.UUID == "" {
		s.UUID = uuid.New().String()
	}
	return nil
}

// Rollable 报告该快照是否可作为回滚目标（仅看状态；底链可用性由服务层校验）。
func (s *InstanceSnapshot) Rollable() bool {
	return s.State == SnapshotStateCompleted || s.State == SnapshotStateRolledBack
}

// EffectiveRollable 报告「状态可回滚 **且** 底链可用」——即真正能点下去的回滚。
//
// 与 Rollable 的区别正是 B-1 的修复点：底链缺失时必须返回 false，不能让前端
// 依据 state 单独判断而显示成可回滚。
func (s *InstanceSnapshot) EffectiveRollable() bool {
	return s.Rollable() && s.NotRollableReason == ""
}
