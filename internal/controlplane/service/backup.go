package service

import (
	"errors"
	"fmt"
	"log/slog"

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

	// 异步执行备份
	go s.executeBackup(backup)

	return backup, nil
}

// executeBackup 异步执行备份。
func (s *BackupService) executeBackup(backup *model.Backup) {
	// 更新状态为进行中
	s.db.Model(backup).Update("status", model.BackupStatusInProgress)

	// TODO: 通过 gRPC 委托给 Worker Node 执行实际备份
	// Worker Node 需要:
	// 1. 停止实例（或在运行时创建快照）
	// 2. 压缩工作目录
	// 3. 上传备份文件
	// 4. 返回文件大小

	// 模拟备份完成
	s.db.Model(backup).Updates(map[string]interface{}{
		"status":        model.BackupStatusCompleted,
		"file_size_mb":  0,
		"file_path":     fmt.Sprintf("backups/%d/%s.tar.gz", backup.InstanceID, backup.UUID),
	})

	slog.Info("备份已完成", "backupId", backup.UUID, "instanceId", backup.InstanceID)
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

	// 异步执行恢复
	go s.executeRestore(&backup)

	return nil
}

// executeRestore 异步执行恢复。
func (s *BackupService) executeRestore(backup *model.Backup) {
	// TODO: 通过 gRPC 委托给 Worker Node 执行恢复
	// Worker Node 需要:
	// 1. 停止实例
	// 2. 解压备份文件到工作目录
	// 3. 重新启动实例
	slog.Info("恢复已启动", "backupId", backup.UUID, "instanceId", backup.InstanceID)
}

// Delete 删除备份。
func (s *BackupService) Delete(backupID uint) error {
	return s.db.Delete(&model.Backup{}, backupID).Error
}
