package model

import "time"

// RollingPolicy 滚动编排策略（FR-457）。
type RollingPolicy struct {
	// BatchSize 每批台数；0=不分批（退化为现并发语义，向后兼容旧 Batch()）。
	BatchSize int `json:"batchSize"`
	// BatchIntervalSec 批间隔（秒）。
	BatchIntervalSec int `json:"batchIntervalSec"`
	// FailFast 失败即停。
	FailFast bool `json:"failFast"`
	// Ratio 灰度比例（0<ratio<1 时按稳定序抽样；<=0 或 >=1 表示全量）。
	Ratio float64 `json:"ratio"`
}

// RollingState 滚动编排会话状态。
type RollingState string

const (
	RollingStatePending  RollingState = "pending"
	RollingStateRunning  RollingState = "running"
	RollingStatePaused   RollingState = "paused"
	RollingStateDone     RollingState = "done"
	RollingStateCanceled RollingState = "canceled"
)

// RollingError 单条失败明细。
type RollingError struct {
	InstanceID uint   `json:"instanceId"`
	Error      string `json:"error"`
}

// InstanceRollingOp 滚动/分批/灰度编排会话（FR-457）。
// 持久化进度（游标/计数/状态），使暂停/继续/取消在 CP 重启后仍可恢复（镜像 bot_load run 会话思路）。
type InstanceRollingOp struct {
	ID               uint           `gorm:"primaryKey" json:"id"`
	Action           string         `gorm:"type:varchar(32);not null" json:"action"`
	Command          string         `gorm:"type:text" json:"command,omitempty"`
	BatchSize        int            `gorm:"default:0" json:"batchSize"`
	BatchIntervalSec int            `gorm:"default:0" json:"batchIntervalSec"`
	FailFast         bool           `gorm:"default:false" json:"failFast"`
	Ratio            float64        `gorm:"default:0" json:"ratio"`
	// CreatedBy 记录创建者用户 ID（FR-457 越权修复：用于会话归属与审计追溯）。
	CreatedBy        uint           `gorm:"default:0;index" json:"createdBy"`
	TargetsJSON      string         `gorm:"type:text" json:"-"`
	Targets          []uint         `gorm:"-" json:"targets"`
	Cursor           int            `gorm:"default:0" json:"cursor"`
	State            RollingState   `gorm:"type:varchar(16);default:pending;index" json:"state"`
	Requested        int            `gorm:"default:0" json:"requested"`
	Succeeded        int            `gorm:"default:0" json:"succeeded"`
	Failed           int            `gorm:"default:0" json:"failed"`
	Skipped          int            `gorm:"default:0" json:"skipped"`
	ErrorsJSON       string         `gorm:"type:text" json:"-"`
	Errors           []RollingError `gorm:"-" json:"errors"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}
