package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BanScope 封禁范围（FR-054）。
type BanScope string

const (
	// BanScopeNetwork 群组级封禁：对一个 Network 内的全部后端子服下发封禁。
	BanScopeNetwork BanScope = "network"
	// BanScopeInstance 单实例封禁：仅对一个后端子服下发封禁。
	BanScopeInstance BanScope = "instance"
	// BanScopeGlobal 全局封禁：对当前操作可达的全部后端子服下发封禁。
	BanScopeGlobal BanScope = "global"
)

// BanRecord 玩家封禁记录（FR-054）。
//
// RCON 是无状态命令通道，封禁执行后服务端自身的 banned-players 文件才是权威来源；
// 本表是 JianManager 侧的可查询审计台账：谁、何时、为何、对哪个范围封禁了某玩家，
// 以及该封禁当前是否仍被本平台视为生效（解封时置 Active=false 而非删除，保留历史）。
type BanRecord struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UUID       string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	// PlayerName 被封禁玩家名（大小写按客户端原样存储）。
	PlayerName string `gorm:"type:varchar(64);not null;index" json:"playerName"`
	// Reason 封禁原因（下发给 RCON ban 命令并入库留档）。
	Reason string `gorm:"type:varchar(512)" json:"reason"`
	// Scope 封禁范围：network / instance / global。
	Scope BanScope `gorm:"type:varchar(16);not null;default:global" json:"scope"`
	// ScopeID 范围目标 ID：network 时为 NetworkID，instance 时为 InstanceID，global 时为 0。
	ScopeID uint `gorm:"default:0" json:"scopeId"`
	// OperatorID 执行封禁的用户 ID（关联审计）。
	OperatorID uint `gorm:"not null;index" json:"operatorId"`
	// Active 是否仍被本平台视为生效；解封置 false，保留记录用于追溯。
	Active bool `gorm:"default:true;index" json:"active"`
	// CreatedAt 封禁时间。
	CreatedAt time.Time `json:"createdAt"`
	// UnbannedAt 解封时间；未解封为 nil。
	UnbannedAt *time.Time `json:"unbannedAt"`
	// Operator 关联用户，便于列表展示操作者用户名。
	Operator User `gorm:"foreignKey:OperatorID" json:"operator,omitempty"`
}

// BeforeCreate 创建前自动生成 UUID。
func (b *BanRecord) BeforeCreate(tx *gorm.DB) error {
	if b.UUID == "" {
		b.UUID = uuid.New().String()
	}
	return nil
}
