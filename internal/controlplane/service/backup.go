package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

var (
	// ErrBackupNotFound 备份不存在。
	ErrBackupNotFound = errors.New("备份不存在")
	// ErrNoFullBaseForIncremental 增量备份缺少可作基准的已完成父备份。
	ErrNoFullBaseForIncremental = errors.New("没有可用于增量基准的已完成备份")
)

// BackupService 备份服务。
// FR-013 全量备份基础上支持 FR-056 增量备份（备份链）与 FR-057 远程存储。
type BackupService struct {
	db   *gorm.DB
	pool *grpc.ClientPool
	// storages 提供远程存储后端解析（FR-057）；nil 表示仅本地备份。
	storages *BackupStorageService
}

// NewBackupService 创建备份服务。
func NewBackupService(db *gorm.DB, pool *grpc.ClientPool) *BackupService {
	return &BackupService{db: db, pool: pool}
}

// SetStorageService 注入远程存储服务（FR-057）。在 main 装配阶段调用，避免构造期循环依赖。
func (s *BackupService) SetStorageService(ss *BackupStorageService) {
	s.storages = ss
}

// CreateOptions 创建备份的可选参数（向后兼容旧的仅 name 调用）。
type CreateOptions struct {
	// Incremental 为 true 时创建增量备份，自动挂到该实例最近一次已完成备份之后形成链。
	Incremental bool
	// StorageID 指定远程存储后端；nil 表示本地（FR-057，存储解析在该 FR 接入）。
	StorageID *uint
}

// Create 创建全量手动备份（向后兼容入口）。
func (s *BackupService) Create(instanceID uint, name string) (*model.Backup, error) {
	return s.CreateWithOptions(instanceID, name, CreateOptions{})
}

// CreateWithOptions 按选项创建备份：支持增量（挂链）与远程存储位置。
func (s *BackupService) CreateWithOptions(instanceID uint, name string, opts CreateOptions) (*model.Backup, error) {
	backup := &model.Backup{
		InstanceID: instanceID,
		Name:       name,
		Type:       model.BackupTypeManual,
		Status:     model.BackupStatusPending,
		StorageID:  opts.StorageID,
	}

	if opts.Incremental {
		parent, err := s.latestCompleted(instanceID)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			return nil, ErrNoFullBaseForIncremental
		}
		backup.Mode = model.BackupModeIncremental
		backup.ParentID = &parent.ID
		// 增量继承父备份的存储位置，保证整条链落在同一后端，便于链式恢复。
		backup.StorageID = parent.StorageID
	}

	if err := s.db.Create(backup).Error; err != nil {
		return nil, fmt.Errorf("创建备份失败: %w", err)
	}

	go s.executeBackup(backup)
	return backup, nil
}

// latestCompleted 返回实例最近一次已完成的备份（作为增量父），无则返回 nil。
func (s *BackupService) latestCompleted(instanceID uint) (*model.Backup, error) {
	var b model.Backup
	err := s.db.Where("instance_id = ? AND status = ?", instanceID, model.BackupStatusCompleted).
		Order("created_at DESC, id DESC").First(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// executeBackup 异步执行备份：经 gRPC 委托 Worker 打包工作目录（增量传基准清单），
// 完成后回写归档路径/大小/清单，远程目标再记录对象键。
func (s *BackupService) executeBackup(backup *model.Backup) {
	s.db.Model(backup).Update("status", model.BackupStatusInProgress)

	instance, node, err := s.resolveInstanceNode(backup.InstanceID)
	if err != nil {
		s.failBackup(backup, "解析实例/节点失败", err)
		return
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		s.failBackup(backup, "节点未连接", fmt.Errorf("nodeUUID=%s", node.UUID))
		return
	}

	req := &workerpb.CreateBackupRequest{
		InstanceUuid: instance.UUID,
		BackupUuid:   backup.UUID,
		Incremental:  backup.Mode == model.BackupModeIncremental,
	}

	// 增量：合并父链各备份清单作为基准，仅打包变化文件。
	if backup.Mode == model.BackupModeIncremental {
		baseManifest, berr := s.chainManifest(backup.ParentID)
		if berr != nil {
			s.failBackup(backup, "构建增量基准失败", berr)
			return
		}
		req.BaseManifest = baseManifest
	}

	// 远程存储后端解析（FR-057 接入）。
	if spec, serr := s.storageSpec(backup.StorageID); serr != nil {
		s.failBackup(backup, "解析存储后端失败", serr)
		return
	} else {
		req.Storage = spec
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	resp, err := client.Worker.CreateBackup(ctx, req)
	if err != nil {
		s.failBackup(backup, "备份执行失败", err)
		return
	}
	if !resp.Success {
		s.failBackup(backup, "备份执行失败", errors.New(resp.Error))
		return
	}

	manifestJSON, _ := json.Marshal(resp.Manifest)
	s.db.Model(backup).Updates(map[string]interface{}{
		"status":       model.BackupStatusCompleted,
		"file_size_mb": float64(resp.SizeBytes) / (1024 * 1024),
		"file_path":    resp.RelPath,
		"manifest":     string(manifestJSON),
		"storage_key":  resp.StorageKey,
	})

	slog.Info("备份已完成", "backupId", backup.UUID, "instanceId", backup.InstanceID,
		"mode", backup.Mode, "files", resp.FileCount, "sizeBytes", resp.SizeBytes)
}

// ListByInstance 按实例列出备份（含类型/模式/父链字段，供前端展示链关系）。
func (s *BackupService) ListByInstance(instanceID uint) ([]model.Backup, error) {
	var backups []model.Backup
	if err := s.db.Where("instance_id = ?", instanceID).Order("created_at DESC").Find(&backups).Error; err != nil {
		return nil, err
	}
	return backups, nil
}

// Restore 恢复备份：解析备份链（全量基 + 各增量）并委托 Worker 按序回放。
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

	chain, err := s.resolveChain(&backup)
	if err != nil {
		return err
	}

	go s.executeRestore(&backup, chain)
	return nil
}

// resolveChain 自目标备份沿 ParentID 回溯到全量基，返回按回放顺序（全量基在前）排列的备份链。
// 链中任一备份未完成则报错（无法保证可恢复）。
func (s *BackupService) resolveChain(target *model.Backup) ([]model.Backup, error) {
	var reversed []model.Backup
	cur := target
	// 防御异常自引用/超长链的保险阈值。
	for i := 0; i < 4096; i++ {
		if cur.Status != model.BackupStatusCompleted {
			return nil, fmt.Errorf("备份链含未完成备份: %s", cur.UUID)
		}
		reversed = append(reversed, *cur)
		if cur.ParentID == nil {
			// 反转为「全量基在前」。
			chain := make([]model.Backup, len(reversed))
			for j := range reversed {
				chain[len(reversed)-1-j] = reversed[j]
			}
			return chain, nil
		}
		var parent model.Backup
		if err := s.db.First(&parent, *cur.ParentID).Error; err != nil {
			return nil, fmt.Errorf("备份链断裂，父备份缺失: %w", err)
		}
		cur = &parent
	}
	return nil, fmt.Errorf("备份链过长或存在环")
}

// executeRestore 异步执行链式恢复：委托 Worker 按链顺序回放归档到工作目录。
func (s *BackupService) executeRestore(backup *model.Backup, chain []model.Backup) {
	instance, node, err := s.resolveInstanceNode(backup.InstanceID)
	if err != nil {
		slog.Error("恢复失败：解析实例/节点", "backupId", backup.UUID, "error", err)
		return
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		slog.Error("恢复失败：节点未连接", "nodeUUID", node.UUID)
		return
	}

	relPaths := make([]string, 0, len(chain))
	storageKeys := make([]string, 0, len(chain))
	for _, b := range chain {
		relPaths = append(relPaths, b.FilePath)
		storageKeys = append(storageKeys, b.StorageKey)
	}

	spec, serr := s.storageSpec(backup.StorageID)
	if serr != nil {
		slog.Error("恢复失败：解析存储后端", "backupId", backup.UUID, "error", serr)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	resp, err := client.Worker.RestoreBackup(ctx, &workerpb.RestoreBackupRequest{
		InstanceUuid: instance.UUID,
		RelPaths:     relPaths,
		Storage:      spec,
		StorageKeys:  storageKeys,
	})
	if err != nil {
		slog.Error("恢复执行失败", "backupId", backup.UUID, "error", err)
		return
	}
	if !resp.Success {
		slog.Error("恢复执行失败", "backupId", backup.UUID, "error", resp.Error)
		return
	}

	slog.Info("恢复已完成", "backupId", backup.UUID, "instanceId", backup.InstanceID,
		"chainLen", len(chain), "restoredFiles", resp.RestoredFiles, "workDir", instance.WorkDir)
}

// Delete 删除备份。被增量子备份引用时拒绝，避免割裂备份链使后续增量不可恢复。
func (s *BackupService) Delete(backupID uint) error {
	var children int64
	if err := s.db.Model(&model.Backup{}).Where("parent_id = ?", backupID).Count(&children).Error; err != nil {
		return err
	}
	if children > 0 {
		return fmt.Errorf("该备份被 %d 个增量备份依赖，请先删除其子备份", children)
	}
	return s.db.Delete(&model.Backup{}, backupID).Error
}

// chainManifest 合并自 parentID 回溯到全量基的整条链的文件清单，得到增量基准的「当前完整视图」。
// 后出现的备份（更靠近父）覆盖先前同路径条目，反映文件的最新指纹。
func (s *BackupService) chainManifest(parentID *uint) ([]*workerpb.BackupManifestEntry, error) {
	if parentID == nil {
		return nil, nil
	}
	var parent model.Backup
	if err := s.db.First(&parent, *parentID).Error; err != nil {
		return nil, fmt.Errorf("父备份缺失: %w", err)
	}
	chain, err := s.resolveChain(&parent)
	if err != nil {
		return nil, err
	}
	merged := map[string]*workerpb.BackupManifestEntry{}
	for _, b := range chain {
		if b.Manifest == "" {
			continue
		}
		var entries []*workerpb.BackupManifestEntry
		if uerr := json.Unmarshal([]byte(b.Manifest), &entries); uerr != nil {
			return nil, fmt.Errorf("解析备份清单失败: %w", uerr)
		}
		for _, e := range entries {
			merged[e.Path] = e
		}
	}
	out := make([]*workerpb.BackupManifestEntry, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	return out, nil
}

// storageSpec 把存储后端 ID 解析为下发 Worker 的传输参数（凭证从 ${ENV_VAR} 解析）。
// storageID 为 nil 或未注入 storages 时返回 nil，表示本地备份（FR-057）。
func (s *BackupService) storageSpec(storageID *uint) (*workerpb.StorageBackendSpec, error) {
	if storageID == nil || s.storages == nil {
		return nil, nil
	}
	return s.storages.ResolveSpec(*storageID)
}

// resolveInstanceNode 加载备份目标实例及其所在节点。
func (s *BackupService) resolveInstanceNode(instanceID uint) (*model.Instance, *model.Node, error) {
	var instance model.Instance
	if err := s.db.First(&instance, instanceID).Error; err != nil {
		return nil, nil, fmt.Errorf("实例不存在: %w", err)
	}
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return nil, nil, fmt.Errorf("节点不存在: %w", err)
	}
	return &instance, &node, nil
}

// failBackup 标记备份失败并记录日志。
func (s *BackupService) failBackup(backup *model.Backup, msg string, err error) {
	s.db.Model(backup).Update("status", model.BackupStatusFailed)
	slog.Error("备份失败："+msg, "backupId", backup.UUID, "error", err)
}
