package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

var ErrBackupNotFound = errors.New("备份不存在")

// BackupService 备份服务。
type BackupService struct {
	db   *gorm.DB
	pool *grpc.ClientPool
}

// NewBackupService 创建备份服务。
func NewBackupService(db *gorm.DB, pool *grpc.ClientPool) *BackupService {
	return &BackupService{db: db, pool: pool}
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

	// 查找实例和节点
	var instance model.Instance
	if err := s.db.First(&instance, backup.InstanceID).Error; err != nil {
		s.db.Model(backup).Update("status", model.BackupStatusFailed)
		slog.Error("备份失败：实例不存在", "backupId", backup.UUID, "error", err)
		return
	}

	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		s.db.Model(backup).Update("status", model.BackupStatusFailed)
		slog.Error("备份失败：节点不存在", "backupId", backup.UUID, "error", err)
		return
	}

	// 通过 gRPC 委托给 Worker Node 执行备份
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		s.db.Model(backup).Update("status", model.BackupStatusFailed)
		slog.Error("备份失败：节点未连接", "nodeUUID", node.UUID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 使用 SendCommand 发送备份命令给实例
	_, err := client.Worker.SendCommand(ctx, &workerpb.SendCommandRequest{
		InstanceUuid: instance.UUID,
		Command:      fmt.Sprintf("backup %s", backup.UUID),
	})

	if err != nil {
		s.db.Model(backup).Update("status", model.BackupStatusFailed)
		slog.Error("备份执行失败", "backupId", backup.UUID, "error", err)
		return
	}

	// 更新备份完成
	s.db.Model(backup).Updates(map[string]interface{}{
		"status":       model.BackupStatusCompleted,
		"file_size_mb": 0,
		"file_path":    fmt.Sprintf("backups/%d/%s.tar.gz", backup.InstanceID, backup.UUID),
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
	// 查找实例和节点
	var instance model.Instance
	if err := s.db.First(&instance, backup.InstanceID).Error; err != nil {
		slog.Error("恢复失败：实例不存在", "backupId", backup.UUID, "error", err)
		return
	}

	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		slog.Error("恢复失败：节点不存在", "backupId", backup.UUID, "error", err)
		return
	}

	// 通过 gRPC 委托给 Worker Node 执行恢复
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		slog.Error("恢复失败：节点未连接", "nodeUUID", node.UUID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_, err := client.Worker.SendCommand(ctx, &workerpb.SendCommandRequest{
		InstanceUuid: instance.UUID,
		Command:      fmt.Sprintf("restore %s", backup.FilePath),
	})

	if err != nil {
		slog.Error("恢复执行失败", "backupId", backup.UUID, "error", err)
		return
	}

	slog.Info("恢复已完成", "backupId", backup.UUID, "instanceId", backup.InstanceID)
}

// Delete 删除备份。
func (s *BackupService) Delete(backupID uint) error {
	return s.db.Delete(&model.Backup{}, backupID).Error
}
