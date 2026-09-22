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

// BackupOrigin 备份的来源（FR-466 §2.2「快照必须自包含」）。
//
// 用途：快照的底层全量备份**必须**能独立于普通备份的保留策略存活——
// 否则 backup.retention_days（默认 14）会先于 snapshot.retention_days（默认 30）
// 把快照的底链裁掉，快照退化成一个指向软删行的死链，而列表仍显示「可回滚」。
type BackupOrigin string

const (
	// BackupOriginManual 普通备份，由运维/API 直接创建（受 backup.retention_days 裁剪）。
	BackupOriginManual BackupOrigin = ""
	// BackupOriginSnapshot 快照的底层全量备份：不受时间裁剪，生命周期由快照自身决定。
	BackupOriginSnapshot BackupOrigin = "snapshot"
)

// Backup 备份记录。
type Backup struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UUID       string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID uint   `gorm:"not null;index" json:"instanceId"`
	// Origin 备份来源（空=普通备份）。snapshot 来源的备份被快照保留策略独占管理。
	Origin     BackupOrigin `gorm:"type:varchar(24);not null;default:'';index" json:"origin,omitempty"`
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
	StorageKey string `gorm:"type:varchar(512)" json:"storageKey,omitempty"`
	// Checksum 归档 tar.gz 的 SHA-256，用于恢复前完整性校验（FR-171）。
	Checksum string `gorm:"type:char(64)" json:"checksum,omitempty"`
	// ChecksumAlgo 校验算法，当前固定 sha256（FR-171）。
	ChecksumAlgo string         `gorm:"type:varchar(16)" json:"checksumAlgo,omitempty"`
	CreatedAt    time.Time      `json:"createdAt"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (b *Backup) BeforeCreate(tx *gorm.DB) error {
	if b.UUID == "" {
		b.UUID = uuid.New().String()
	}
	return nil
}
