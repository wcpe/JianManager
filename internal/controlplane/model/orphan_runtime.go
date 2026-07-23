package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OrphanRuntimeStatus 无主运行时处置状态（FR-326）。
type OrphanRuntimeStatus string

const (
	// OrphanRuntimePending 首次发现，宽限观察中。
	OrphanRuntimePending OrphanRuntimeStatus = "pending"
	// OrphanRuntimeConfirmed 宽限期已过，待自动/手动处置（auto_dispose 关时停在此态）。
	OrphanRuntimeConfirmed OrphanRuntimeStatus = "confirmed"
	// OrphanRuntimeDisposed 已下发并完成处置（进程停、Worker 本地运行态已清）。
	OrphanRuntimeDisposed OrphanRuntimeStatus = "disposed"
	// OrphanRuntimeCancelled 宽限期内 CP 又出现对应实例记录，取消处置。
	OrphanRuntimeCancelled OrphanRuntimeStatus = "cancelled"
)

// OrphanRuntime 记录 Worker 有、CP 无实例记录的无主运行时（FR-326 反向对账）。
// 不重建业务实例，仅跟踪发现/宽限/处置；处置后终态保留供列表与审计对照。
type OrphanRuntime struct {
	ID           uint                `gorm:"primaryKey" json:"id"`
	UUID         string              `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	NodeUUID     string              `gorm:"type:char(36);not null;index:idx_orphan_node_inst,priority:1" json:"nodeUuid"`
	InstanceUUID string              `gorm:"type:char(36);not null;index:idx_orphan_node_inst,priority:2" json:"instanceUuid"`
	// WorkerState 最近一次心跳上报的状态摘要（RUNNING/STOPPED/…）。
	WorkerState string `gorm:"type:varchar(32)" json:"workerState"`
	// WorkerPID 最近一次心跳上报的进程 PID（0=未知）。
	// 列名显式 worker_pid：避免 GORM 把 PID 缩写蛇形化为 worker_p_id（同 grpc_port 先例）。
	WorkerPID int `gorm:"column:worker_pid;default:0" json:"workerPid"`
	Status      OrphanRuntimeStatus `gorm:"type:varchar(32);not null;index;default:pending" json:"status"`
	FirstSeenAt time.Time           `gorm:"not null;index" json:"firstSeenAt"`
	LastSeenAt  time.Time           `gorm:"not null" json:"lastSeenAt"`
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
func (o *OrphanRuntime) BeforeCreate(tx *gorm.DB) error {
	if o.UUID == "" {
		o.UUID = uuid.New().String()
	}
	return nil
}
