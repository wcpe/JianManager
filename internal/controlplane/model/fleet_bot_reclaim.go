package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FleetBotReclaimStatus 失效 Bot 自动回收处置状态（FR-460）。
//
// 与 FR-326 无主运行时处置范式对齐，但对象为「Fleet 归属、workerEpoch 换代后僵死的 Bot」。
type FleetBotReclaimStatus string

const (
	// FleetBotReclaimPending 首次判定失效，宽限观察中（宽限内 Bot 若被新世代认领则转 cancelled）。
	FleetBotReclaimPending FleetBotReclaimStatus = "pending"
	// FleetBotReclaimConfirmed 宽限期已过，待自动/手动处置（auto_reclaim 关或 V1 Bot 时停在此态）。
	FleetBotReclaimConfirmed FleetBotReclaimStatus = "confirmed"
	// FleetBotReclaimDisposed 已下发停用并完成 CP 账本去账。
	FleetBotReclaimDisposed FleetBotReclaimStatus = "disposed"
	// FleetBotReclaimCancelled 宽限期内 Bot 被新世代事件认领（epoch 追平），取消回收。
	FleetBotReclaimCancelled FleetBotReclaimStatus = "cancelled"
)

// FleetBotReclaim 记录按 workerEpoch 不匹配判定的失效 Bot 及其回收状态（FR-460）。
//
// 仅回收 Fleet 归属 Bot（load_batch_id / stress_session_id 非空）；V1 手动 Bot 只入列表等人工确认，
// 永不自动删除。回收只改 CP 账本（desired_state/status → stopped）并复用既有 Fleet RPC 去 Worker
// 侧 desired 记账，不物理删除 Bot 行（保留取证与审计）。
type FleetBotReclaim struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UUID    string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	BotID   uint   `gorm:"not null;index" json:"botId"`
	BotUUID string `gorm:"type:char(36);not null;index" json:"botUuid"`
	// NodeID 执行节点；0 表示 Bot 的 executor_node_id 为空（未解析出执行节点）。
	NodeID uint `gorm:"index" json:"nodeId"`
	// SessionID / BatchID 归属（Fleet）；0 表示对应关联为空。
	SessionID uint `gorm:"index" json:"sessionId"`
	BatchID   uint `gorm:"index" json:"batchId"`
	// FleetOwned 是否 Fleet 归属（load_batch_id 或 stress_session_id 非空）；V1 手动 Bot 为 false。
	FleetOwned bool `gorm:"not null;default:false" json:"fleetOwned"`
	// StaleReason 判据命中说明（epoch_mismatch / empty_epoch / node_missing）。
	StaleReason string `gorm:"type:varchar(32);not null" json:"staleReason"`
	// ObservedEpoch / ObservedEpochGeneration 判定时 Bot 侧记录的世代。
	ObservedEpoch           string `gorm:"type:varchar(36);not null;default:''" json:"observedEpoch"`
	ObservedEpochGeneration int64  `gorm:"not null;default:0" json:"observedEpochGeneration"`
	// CurrentEpochGeneration 判定时执行节点最近上报的世代（真源取自容量快照）。
	CurrentEpochGeneration int64                 `gorm:"not null;default:0" json:"currentEpochGeneration"`
	Status                 FleetBotReclaimStatus `gorm:"type:varchar(32);not null;index;default:pending" json:"status"`
	FirstSeenAt            time.Time             `gorm:"not null;index" json:"firstSeenAt"`
	LastSeenAt             time.Time             `gorm:"not null" json:"lastSeenAt"`
	// DisposedAt 处置完成时刻（自动或手动）。
	DisposedAt *time.Time `json:"disposedAt,omitempty"`
	// DisposeMode auto | manual；未处置为空。
	DisposeMode string `gorm:"type:varchar(16)" json:"disposeMode,omitempty"`
	// LastError 最近一次自动/手动处置失败原因（成功后清空）。
	LastError string    `gorm:"type:varchar(512)" json:"lastError,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BeforeCreate 创建前自动生成 UUID。
func (r *FleetBotReclaim) BeforeCreate(tx *gorm.DB) error {
	if r.UUID == "" {
		r.UUID = uuid.New().String()
	}
	return nil
}
