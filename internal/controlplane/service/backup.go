package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

var ErrBackupNotFound = errors.New("备份不存在")

// BackupService 备份服务。
type BackupService struct {
	db *gorm.DB
}

// NewBackupService 创建备份服务。
func NewBackupService(db *gorm.DB) *BackupService {
	return &BackupService{db: db}
}

// Create 创建备份。
func (s *BackupService) Create(instanceID uint, name string) (*model.Backup, error) {
	backup := &model.Backup{
		InstanceID: instanceID,
		Name:       name,
		Type:       model.BackupTypeManual,
		Status:     model.BackupStatusPending,
	}
	if err := s.db.Create(backup).Error; err != nil {
		return nil, fmt.Errorf("创建备份失败: %w", err)
	}
	// TODO: 异步执行备份（委托给 Worker Node）
	return backup, nil
}

// ListByInstance 按实例列出备份。
func (s *BackupService) ListByInstance(instanceID uint) ([]model.Backup, error) {
	var backups []model.Backup
	if err := s.db.Where("instance_id = ?", instanceID).Order("created_at DESC").Find(&backups).Error; err != nil {
		return nil, err
	}
	return backups, nil
}

// Restore 恢复备份。
func (s *BackupService) Restore(backupID uint) error {
	var backup model.Backup
	if err := s.db.First(&backup, backupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBackupNotFound
		}
		return err
	}
	if backup.Status != model.BackupStatusCompleted {
		return fmt.Errorf("备份未完成，无法恢复")
	}
	// TODO: 委托给 Worker Node 执行恢复
	return nil
}

// Delete 删除备份。
func (s *BackupService) Delete(backupID uint) error {
	return s.db.Delete(&model.Backup{}, backupID).Error
}
