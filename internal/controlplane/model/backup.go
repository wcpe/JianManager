package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BackupStatus 备份状态。
type BackupStatus int

const (
	BackupStatusPending    BackupStatus = 0 // 待处理
	BackupStatusInProgress BackupStatus = 1 // 进行中
	BackupStatusCompleted  BackupStatus = 2 // 完成
	BackupStatusFailed     BackupStatus = 3 // 失败
)

// BackupType 备份类型。
type BackupType int

const (
	BackupTypeManual    BackupType = 0 // 手动
	BackupTypeScheduled BackupType = 1 // 定时
)

// BackupMode 备份模式：全量或增量（FR-056）。
// 与 BackupType（触发来源）正交：手动/定时备份均可为全量或增量。
type BackupMode int

const (
	BackupModeFull        BackupMode = 0 // 全量：打包工作目录全部文件
	BackupModeIncremental BackupMode = 1 // 增量：仅打包相对父备份变化的文件
)

// Backup 备份记录。
type Backup struct {
	ID         uint         `gorm:"primaryKey" json:"id"`
	UUID       string       `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID uint         `gorm:"not null;index" json:"instanceId"`
	Name       string       `gorm:"type:varchar(128);not null" json:"name"`
	FilePath   string       `gorm:"type:varchar(512)" json:"filePath"`
	FileSizeMB float64      `gorm:"default:0" json:"fileSizeMb"`
	Type       BackupType   `gorm:"default:0" json:"type"`
	Mode       BackupMode   `gorm:"default:0" json:"mode"`
	Status     BackupStatus `gorm:"default:0" json:"status"`
	// ParentID 增量备份的父备份 ID，串成备份链；全量备份为 nil（FR-056）。
	ParentID *uint `gorm:"index" json:"parentId,omitempty"`
	// Manifest 本次备份完成后工作目录的完整文件清单（JSON 序列化的 manifest 数组）。
	// 作为下一次增量的基准与链式恢复的依据，由 Worker 返回、CP 持久化。
	Manifest string `gorm:"type:text" json:"-"`
	// StorageID 远程存储后端 ID；nil 表示存于节点本地数据根（FR-057）。
	StorageID *uint `gorm:"index" json:"storageId,omitempty"`
	// StorageKey 上传到远程后端的对象键；本地备份为空（FR-057）。
	StorageKey string         `gorm:"type:varchar(512)" json:"storageKey,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (b *Backup) BeforeCreate(tx *gorm.DB) error {
	if b.UUID == "" {
		b.UUID = uuid.New().String()
	}
	return nil
}
