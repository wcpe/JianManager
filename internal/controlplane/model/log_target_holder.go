package model

import "time"

// LogTargetHolder 是 CP 侧日志目标目录元数据，不保存日志内容。
// 同一实例可以有多个 holder；Current 表示当前归属，历史 holder 保留用于跨节点查询。
type LogTargetHolder struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	InstanceID       uint       `gorm:"index;not null" json:"instanceId"`
	WorkerUUID       string     `gorm:"type:varchar(128);index;not null" json:"workerUuid"`
	SourceGeneration string     `gorm:"type:varchar(128);index;not null" json:"sourceGeneration"`
	StorageNamespace string     `gorm:"type:varchar(256);not null" json:"storageNamespace"`
	Current          bool       `gorm:"index;not null" json:"current"`
	ValidFrom        time.Time  `json:"validFrom"`
	ValidTo          *time.Time `json:"validTo,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}
