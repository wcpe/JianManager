package model

import "time"

// InstanceConfigVersion 配置版本记录。
// 同一实例的同一文件可保存多条历史版本（无联合唯一约束）。
// RollbackOfVersionID 记录本次写入是否由回滚触发，指向被回滚的源版本。
type InstanceConfigVersion struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	InstanceID         uint      `gorm:"not null;index:idx_cfg_version_instance_file" json:"instanceId"`
	FilePath           string    `gorm:"type:varchar(512);not null;index:idx_cfg_version_instance_file" json:"filePath"`
	ContentHash        string    `gorm:"type:char(64);not null;index" json:"contentHash"`
	Content            string    `gorm:"type:longtext;not null" json:"content"`
	Message            string    `gorm:"type:varchar(255)" json:"message"`
	AuthorID           uint      `gorm:"index" json:"authorId"`
	RollbackOfVersionID *uint    `gorm:"index" json:"rollbackOfVersionId,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
}
