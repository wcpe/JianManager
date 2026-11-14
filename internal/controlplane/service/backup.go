package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// backupRetentionTick 备份保留裁剪巡检周期。备份非高频，按小时级巡检足够及时且开销低。
const backupRetentionTick = time.Hour

var (
	// ErrBackupNotFound 备份不存在。
	ErrBackupNotFound = errors.New("备份不存在")
	// ErrNoFullBaseForIncremental 增量备份缺少可作基准的已完成父备份。
	ErrNoFullBaseForIncremental = errors.New("没有可用于增量基准的已完成备份")
	// ErrBackupHasIncrementalChildren 备份被增量子备份依赖，删除会割裂恢复链。
	ErrBackupHasIncrementalChildren = errors.New("备份被增量备份依赖")
	// ErrInstanceNotStopped 实例进程可能存活，拒绝恢复备份。
	ErrInstanceNotStopped = errors.New("实例未停止，请先停止实例再恢复备份")
)

// BackupService 备份服务。
// 全量备份基础上支持增量备份（备份链）与远程存储。
type BackupService struct {
	db   *gorm.DB
	pool *grpc.ClientPool
	// storages 提供远程存储后端解析；nil 表示仅本地备份。
	storages *BackupStorageService
	// settings 提供保留天数生效值（backup.retention_days），驱动定期裁剪；
	// 为 nil 时裁剪循环不启动（无消费者，行为同改造前）。
	settings SettingsReader

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}

	// onBackupFailed 备份失败回调（告警触发源）；nil 表示未接入告警。
	onBackupFailed func(backup *model.Backup, msg string)

	// tasks 任务中心：备份创建/恢复经任务异步且可见进度；nil 时回退旧 goroutine（测试）。
	tasks *TaskService

	// inFlightMu 保护下面两张表。
	//
	// 【为什么两张表共用一把锁】恢复与创建要互斥（两者都读写同一工作目录），
	// 若各用一把锁，beginRestore 与 beginBackup 会形成反向获取顺序
	// （A 持锁 1 拿锁 2、B 持锁 2 拿锁 1）——典型的死锁配方。
	// 一把锁同时守两张表，就没有可死锁的第二把锁。
	inFlightMu sync.Mutex
	// restoreInFlight 实例级恢复互斥：恢复会解包覆盖实例工作目录，
	// 同一实例并发恢复会互相覆盖文件（结果不确定且可能损坏）。
	//
	// 为什么不用 DB 行锁：恢复是「HTTP 立即返回 + 后台任务执行」的异步流程，
	// 事务一提交锁就释放，挡不住后续请求。这里用进程内集合覆盖整个异步窗口；
	// 多副本部署时需换成 DB/分布式锁（当前为单 CP 部署）。
	restoreInFlight map[uint]struct{}
	// createInFlight 实例级创建互斥：同一实例不得并发跑两个备份任务。
	// 并发创建会同时打包同一目录（抢 IO），并产出两份语义重复的归档；
	// 双击按钮即可触发。
	createInFlight map[uint]struct{}
}

// NewBackupService 创建备份服务。
func NewBackupService(db *gorm.DB, pool *grpc.ClientPool) *BackupService {
	return &BackupService{db: db, pool: pool}
}

// SetTaskService 注入任务中心（main 接线）：非 nil 后备份创建/恢复走后台任务 + 进度。
func (s *BackupService) SetTaskService(t *TaskService) { s.tasks = t }

// backupNodeID 解析备份所属实例的节点 id（任务归属展示用；失败回 0 不阻断）。
func (s *BackupService) backupNodeID(instanceID uint) uint {
	var inst model.Instance
	if err := s.db.Select("node_id").First(&inst, instanceID).Error; err != nil {
		return 0
	}
	return inst.NodeID
}

// SetStorageService 注入远程存储服务。在 main 装配阶段调用，避免构造期循环依赖。
func (s *BackupService) SetStorageService(ss *BackupStorageService) {
	s.storages = ss
}

// SetSettingsReader 注入平台设置读取器，启用按保留天数定期裁剪。
func (s *BackupService) SetSettingsReader(r SettingsReader) {
	s.settings = r
}

// SetBackupFailedHook 注入备份失败回调，供告警体系订阅备份失败触发。
func (s *BackupService) SetBackupFailedHook(fn func(backup *model.Backup, msg string)) {
	s.onBackupFailed = fn
}

// CreateOptions 创建备份的可选参数（向后兼容旧的仅 name 调用）。
type CreateOptions struct {
	// Incremental 为 true 时创建增量备份，自动挂到该实例最近一次已完成备份之后形成链。
	Incremental bool
	// StorageID 指定远程存储后端；nil 表示本地（远程存储解析另行接入）。
	StorageID *uint
}

// Create 创建全量手动备份（向后兼容入口）。
func (s *BackupService) Create(instanceID uint, name string) (*model.Backup, error) {
	return s.CreateWithOptions(instanceID, name, CreateOptions{})
}

// CreateWithOptions 按选项创建备份：支持增量（挂链）与远程存储位置。
func (s *BackupService) CreateWithOptions(instanceID uint, name string, opts CreateOptions) (*model.Backup, error) {
	/*
		实例级创建互斥：同一实例同时跑两个备份任务会同时打包同一工作目录——
		既互相抢 IO，也会产出两份语义重复的归档（其中一份几乎必然被用户立刻删掉），
		而“删除中”的备份又会被增量链护栏拦住。双击按钮就能触发，不是理论风险。

		用进程内集合覆盖整个异步窗口（与 restoreInFlight 同一取舍：
		HTTP 立即返回、后台执行，DB 行锁盖不住这个窗口）。
	*/
	release, ok := s.beginBackup(instanceID)
	if !ok {
		return nil, fmt.Errorf("%w：该实例已有备份任务在创建中", ErrBackupInFlight)
	}
	committed := false
	defer func() {
		if !committed {
			release()
		}
	}()

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

	// 后台任务在独立副本上回写状态/结果，绝不复用返回给调用方的实例：
	// 调用方（HTTP handler）会用 encoding/json 序列化返回的 backup，而 executeBackup
	// 经 GORM Model(...).Update 会写回模型字段（autoUpdateTime 等），二者并发读写
	// 同一结构体构成数据竞态（go test -race 可稳定复现）。复制后两者内存隔离。
	async := *backup
	// 纳入任务中心：进度/终态在任务中心可见，Backup record 状态仍由 executeBackup 自更
	// （备份列表页不回归）。无任务中心（测试）回退旧后台 goroutine。
	if s.tasks == nil {
		go func() {
			defer release()
			if err := s.executeBackup(&async, nil); err != nil {
				slog.Error("后台执行备份失败", "backupId", async.ID, "error", err)
			}
		}()
		committed = true
		return backup, nil
	}
	s.tasks.RunAsync(RunSpec{
		NodeID: s.backupNodeID(instanceID), InstanceID: instanceID, Kind: model.TaskKindBackupCreate,
		Title: fmt.Sprintf("备份 %s", name),
	}, func(_ context.Context, stage func(int, string)) (string, error) {
		defer release()
		return "", s.executeBackup(&async, stage)
	})
	committed = true
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

// executeBackup 执行备份：经 gRPC 委托 Worker 打包工作目录（增量传基准清单），
// 完成后回写归档路径/大小/清单，远程目标再记录对象键。stage 非 nil 上报阶段（任务中心）；
// 返回 error 供 RunAsync MarkFailed（Backup record 状态仍在此内自更，备份列表页不回归）。
func (s *BackupService) executeBackup(backup *model.Backup, stage func(int, string)) error {
	report := func(p int, text string) {
		if stage != nil {
			stage(p, text)
		}
	}
	s.db.Model(backup).Update("status", model.BackupStatusInProgress)

	instance, node, err := s.resolveInstanceNode(backup.InstanceID)
	if err != nil {
		return s.failBackup(backup, "解析实例/节点失败", err)
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return s.failBackup(backup, "节点未连接", fmt.Errorf("nodeUUID=%s", node.UUID))
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
			return s.failBackup(backup, "构建增量基准失败", berr)
		}
		req.BaseManifest = baseManifest
	}

	// 远程存储后端解析（接入）。
	if spec, serr := s.storageSpec(backup.StorageID); serr != nil {
		return s.failBackup(backup, "解析存储后端失败", serr)
	} else {
		req.Storage = spec
	}

	report(30, "打包工作目录…")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	resp, err := client.Worker.CreateBackup(ctx, req)
	if err != nil {
		return s.failBackup(backup, "备份执行失败", err)
	}
	if !resp.Success {
		return s.failBackup(backup, "备份执行失败", errors.New(resp.Error))
	}

	manifestJSON, err := json.Marshal(resp.Manifest)
	if err != nil {
		return s.failBackup(backup, "序列化备份清单失败", err)
	}
	s.db.Model(backup).Updates(map[string]interface{}{
		"status":        model.BackupStatusCompleted,
		"file_size_mb":  float64(resp.SizeBytes) / (1024 * 1024),
		"file_path":     resp.RelPath,
		"manifest":      string(manifestJSON),
		"storage_key":   resp.StorageKey,
		"checksum":      resp.ChecksumSha256,
		"checksum_algo": resp.ChecksumAlgo,
	})

	slog.Info("备份已完成", "backupId", backup.UUID, "instanceId", backup.InstanceID,
		"mode", backup.Mode, "files", resp.FileCount, "sizeBytes", resp.SizeBytes)
	return nil
}

// ListByInstance 按实例列出备份（含类型/模式/父链字段，供前端展示链关系）。
func (s *BackupService) ListByInstance(instanceID uint) ([]model.Backup, error) {
	var backups []model.Backup
	if err := s.db.Where("instance_id = ?", instanceID).Order("created_at DESC").Find(&backups).Error; err != nil {
		return nil, err
	}
	return backups, nil
}

// GetByID 按 id 读取备份元数据（权限门禁用：备份→实例→组）。
func (s *BackupService) GetByID(backupID uint) (*model.Backup, error) {
	var backup model.Backup
	if err := s.db.First(&backup, backupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBackupNotFound
		}
		return nil, err
	}
	return &backup, nil
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

	// 运行态守卫：恢复会解包覆盖工作目录，进程可能存活时必须拒绝——运行中的 MC 服
	// 世界数据持在内存，下次自动存档会把刚恢复的文件覆盖回去（恢复静默失效），且对
	// 存活进程的文件树做覆盖有损坏风险。CRASHED 进程已退出，放行（崩溃后恢复是主场景）。
	var instance model.Instance
	if err := s.db.First(&instance, backup.InstanceID).Error; err != nil {
		return fmt.Errorf("实例不存在: %w", err)
	}
	switch instance.Status {
	case model.InstanceStatusStarting, model.InstanceStatusRunning, model.InstanceStatusStopping:
		return fmt.Errorf("%w（当前状态 %s）", ErrInstanceNotStopped, instance.Status)
	}

	chain, err := s.resolveChain(&backup)
	if err != nil {
		return err
	}

	// 实例级互斥：必须在启动异步任务**之前**占用，否则两个并发请求
	// 都能通过前面的状态/链校验并各自启动一个恢复，互相覆盖工作目录。
	release, ok := s.beginRestore(backup.InstanceID)
	if !ok {
		return fmt.Errorf("%w：该实例已有恢复任务在途", ErrBackupInFlight)
	}

	// 纳入任务中心：恢复进度/终态可见。无任务中心（测试）回退旧后台 goroutine。
	if s.tasks == nil {
		go func() {
			defer release()
			if err := s.executeRestore(&backup, chain, nil); err != nil {
				slog.Error("后台执行备份恢复失败", "backupId", backup.ID, "error", err)
			}
		}()
		return nil
	}
	s.tasks.RunAsync(RunSpec{
		NodeID: instance.NodeID, InstanceID: backup.InstanceID, Kind: model.TaskKindBackupRestore,
		Title: fmt.Sprintf("恢复备份 %s", backup.Name),
	}, func(_ context.Context, stage func(int, string)) (string, error) {
		// 无论成败都释放名额，否则一次失败就把该实例永久卡在「恢复中」。
		defer release()
		return "", s.executeRestore(&backup, chain, stage)
	})
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

// executeRestore 执行链式恢复：委托 Worker 按链顺序回放归档到工作目录。
// stage 非 nil 上报阶段（任务中心）；返回 error 供 RunAsync MarkFailed。
func (s *BackupService) executeRestore(backup *model.Backup, chain []model.Backup, stage func(int, string)) error {
	report := func(p int, text string) {
		if stage != nil {
			stage(p, text)
		}
	}
	instance, node, err := s.resolveInstanceNode(backup.InstanceID)
	if err != nil {
		slog.Error("恢复失败：解析实例/节点", "backupId", backup.UUID, "error", err)
		return fmt.Errorf("解析实例/节点失败: %w", err)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		slog.Error("恢复失败：节点未连接", "nodeUUID", node.UUID)
		return fmt.Errorf("节点未连接: %s", node.UUID)
	}

	relPaths, storageKeys, checksums := backupChainRestoreArgs(chain)

	spec, serr := s.storageSpec(backup.StorageID)
	if serr != nil {
		slog.Error("恢复失败：解析存储后端", "backupId", backup.UUID, "error", serr)
		return fmt.Errorf("解析存储后端失败: %w", serr)
	}

	report(30, "回放备份链…")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	resp, err := client.Worker.RestoreBackup(ctx, &workerpb.RestoreBackupRequest{
		InstanceUuid:   instance.UUID,
		RelPaths:       relPaths,
		Storage:        spec,
		StorageKeys:    storageKeys,
		ChecksumSha256: checksums,
	})
	if err != nil {
		slog.Error("恢复执行失败", "backupId", backup.UUID, "error", err)
		return fmt.Errorf("恢复执行失败: %w", err)
	}
	if !resp.Success {
		slog.Error("恢复执行失败", "backupId", backup.UUID, "error", resp.Error)
		return fmt.Errorf("恢复执行失败: %s", resp.Error)
	}

	slog.Info("恢复已完成", "backupId", backup.UUID, "instanceId", backup.InstanceID,
		"chainLen", len(chain), "restoredFiles", resp.RestoredFiles, "workDir", instance.WorkDir)
	return nil
}

func backupChainRestoreArgs(chain []model.Backup) ([]string, []string, []string) {
	relPaths := make([]string, 0, len(chain))
	storageKeys := make([]string, 0, len(chain))
	checksums := make([]string, 0, len(chain))
	for _, b := range chain {
		relPaths = append(relPaths, b.FilePath)
		storageKeys = append(storageKeys, b.StorageKey)
		checksums = append(checksums, b.Checksum)
	}
	return relPaths, storageKeys, checksums
}

// RegisterFull 登记一条**全量**备份记录（Pending），不启动任何后台执行。
//
// 用途：快照服务需要「先登记 Backup、再由自己的任务驱动执行」——因为快照的任务阶段
// 文案（停服 → 建回滚前快照 → 回放数据 → 校验）与备份任务不同，复用 CreateWithOptions
// 会注册出第二个任务（同一份工作出现两条任务记录）。归档/回放实现仍是同一份。
//
// Origin 固定为 BackupOriginSnapshot：快照底链必须豁免 backup.retention_days 裁剪
// （见 pruneExpiredOnce 注释），否则快照会沦为指向软删行的死链。
func (s *BackupService) RegisterFull(instanceID uint, name string) (*model.Backup, error) {
	backup := &model.Backup{
		InstanceID: instanceID,
		Name:       name,
		Type:       model.BackupTypeManual,
		Mode:       model.BackupModeFull,
		Status:     model.BackupStatusPending,
		Origin:     model.BackupOriginSnapshot,
	}
	if err := s.db.Create(backup).Error; err != nil {
		return nil, fmt.Errorf("创建备份失败: %w", err)
	}
	return backup, nil
}

// ExecuteBackup 同步执行一条已登记的全量备份（快照创建调用）。
// stage 为 nil 时不上报阶段。返回 error 表示归档失败（Backup 记录状态已在内部置 failed）。
func (s *BackupService) ExecuteBackup(backup *model.Backup, stage func(int, string)) error {
	return s.executeBackup(backup, stage)
}

// ExecuteRestore 同步回放一条已完成的备份及其链（快照回滚调用）。
//
// 运行态守卫**刻意不做**：快照回滚的语义是「由快照服务负责停服编排」，停服在调用方
// 完成后再调本函数；若此处再拦一次，会出现「已停服却因状态收敛滞后被拒」的假失败。
// 调用方（SnapshotService.Rollback）必须保证实例已停。
func (s *BackupService) ExecuteRestore(backup *model.Backup, stage func(int, string)) error {
	chain, err := s.resolveChain(backup)
	if err != nil {
		return err
	}
	return s.executeRestore(backup, chain, stage)
}

// ResolveRestoreChain 解析备份的完整回放链（全量基在前），供快照回滚预检与测试断言。
func (s *BackupService) ResolveRestoreChain(backupID uint) ([]model.Backup, error) {
	backup, err := s.GetByID(backupID)
	if err != nil {
		return nil, err
	}
	return s.resolveChain(backup)
}

// Delete 删除备份。被增量子备份引用时拒绝，避免割裂备份链使后续增量不可恢复。
func (s *BackupService) Delete(backupID uint) error {
	backup, err := s.GetByID(backupID)
	if err != nil {
		return err
	}
	// 异步进行中的备份不得删除：创建任务还在写归档并会回写本行，
	// 删了之后任务仍会继续写远端文件与更新已不存在的记录，
	// 结果是用户看不到但空间被占用的孤儿归档。
	// 注意：这里看的是**任务是否在途**，而不是 status 是否为 pending——
	// 历史数据里存在从未启动任务、status 永远停在 pending 的残留行，
	// 拿 status 当门禁会让它们永远删不掉。
	if backup.Status == model.BackupStatusInProgress {
		return fmt.Errorf("%w：备份正在创建", ErrBackupInFlight)
	}
	// 恢复中的备份同样不得删除：replay 链已解析并在执行。
	if s.isRestoreInFlight(backup.InstanceID) {
		return fmt.Errorf("%w：该实例正在恢复", ErrBackupInFlight)
	}
	var children int64
	if err := s.db.Model(&model.Backup{}).Where("parent_id = ?", backupID).Count(&children).Error; err != nil {
		return err
	}
	if children > 0 {
		return fmt.Errorf("%w：该备份被 %d 个增量备份依赖，请先删除其子备份", ErrBackupHasIncrementalChildren, children)
	}
	return s.db.Delete(&model.Backup{}, backupID).Error
}

// ErrBackupInFlight 备份正在创建或恢复中，不允许删除/重复恢复。
var ErrBackupInFlight = errors.New("备份操作正在进行中")

// DeleteForSnapshotCascade 删除快照底链备份（仅限快照级联删除调用）。
//
// 与 Delete 的区别：**不检查备份自身的 status**。
// 理由：快照的终态门禁已由调用方（SnapshotService.Delete）把关（非 completed/rolled_back
// 的快照根本不会走到这里），而底链备份的 status 在历史数据里可能是 pending
// （早期版本未回写），把它当「在途」拦下来会让快照永远删不掉。
// 但「被增量链引用」的护栏仍然保留——那是数据完整性约束，不是任务状态。
func (s *BackupService) DeleteForSnapshotCascade(backupID uint) error {
	var children int64
	if err := s.db.Model(&model.Backup{}).Where("parent_id = ?", backupID).Count(&children).Error; err != nil {
		return err
	}
	if children > 0 {
		return fmt.Errorf("%w：该备份被 %d 个增量备份依赖，请先删除其子备份", ErrBackupHasIncrementalChildren, children)
	}
	return s.db.Delete(&model.Backup{}, backupID).Error
}

// isRestoreInFlight 查询某实例是否正在恢复。
func (s *BackupService) isRestoreInFlight(instanceID uint) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	_, ok := s.restoreInFlight[instanceID]
	return ok
}

// beginRestore 尝试占用某实例的恢复名额；成功返回释放函数。
//
// 为什么必须在提交异步任务**之前**占用：若先返回 202 再占用，中间的窗口
// 足够两个并发请求都通过检查并各自启动一个恢复任务。
func (s *BackupService) beginRestore(instanceID uint) (func(), bool) {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if s.restoreInFlight == nil {
		s.restoreInFlight = map[uint]struct{}{}
	}
	if _, ok := s.restoreInFlight[instanceID]; ok {
		return nil, false
	}
	// 与备份创建也互斥：恢复在解包覆盖工作目录，而备份在打包同一个目录，
	// 两者并发会让备份归档到一半新一半旧的混合快照（恢复出来才发现对不上）。
	// 两张表在同一把锁下，所以直接查内存，不能再调 isCreateInFlight（会重入死锁）。
	if _, creating := s.createInFlight[instanceID]; creating {
		return nil, false
	}
	s.restoreInFlight[instanceID] = struct{}{}
	return func() {
		s.inFlightMu.Lock()
		defer s.inFlightMu.Unlock()
		delete(s.restoreInFlight, instanceID)
	}, true
}

// isCreateInFlight 查询某实例是否已有备份任务在创建中。
func (s *BackupService) isCreateInFlight(instanceID uint) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	_, ok := s.createInFlight[instanceID]
	return ok
}

// beginBackup 尝试占用某实例的「创建备份」名额；成功返回释放函数。
//
// 与 beginRestore 同一套时序要求：必须在返回 202 / 提交异步任务**之前**占用，
// 否则两个并发请求会都通过检查、各自打包同一工作目录。
func (s *BackupService) beginBackup(instanceID uint) (func(), bool) {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if s.createInFlight == nil {
		s.createInFlight = map[uint]struct{}{}
	}
	if _, ok := s.createInFlight[instanceID]; ok {
		return nil, false
	}
	// 恢复在途时不允许开新备份：否则会归档到一半新一半旧的混合快照。
	if _, restoring := s.restoreInFlight[instanceID]; restoring {
		return nil, false
	}
	s.createInFlight[instanceID] = struct{}{}
	return func() {
		s.inFlightMu.Lock()
		defer s.inFlightMu.Unlock()
		delete(s.createInFlight, instanceID)
	}, true
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
// storageID 为 nil 或未注入 storages 时返回 nil，表示本地备份。
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

// failBackup 标记备份失败并记录日志，触发备份失败告警钩子。
// failBackup 回写失败状态 + 告警 + 失败钩子，并返回组合错误（：供 RunAsync MarkFailed）。
func (s *BackupService) failBackup(backup *model.Backup, msg string, err error) error {
	s.db.Model(backup).Update("status", model.BackupStatusFailed)
	slog.Error("备份失败："+msg, "backupId", backup.UUID, "error", err)
	if s.onBackupFailed != nil {
		s.onBackupFailed(backup, msg)
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// Start 启动按保留天数定期裁剪旧备份的后台巡检（：backup.retention_days 真生效）。
// 未注入设置读取器则不启动（无消费者）。幂等：重复调用只启动一次。
func (s *BackupService) Start() {
	if s.settings == nil {
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	stop := s.stopCh
	s.mu.Unlock()
	go s.runRetentionLoop(stop)
}

// Stop 停止保留裁剪巡检。
func (s *BackupService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stopCh)
}

// runRetentionLoop 周期裁剪：启动后先跑一轮（避免重启后久未清理），其后每 tick 一轮。
func (s *BackupService) runRetentionLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(backupRetentionTick)
	defer ticker.Stop()

	s.pruneExpiredOnce()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.pruneExpiredOnce()
		}
	}
}

// pruneExpiredOnce 裁剪一轮：每轮重新取保留天数生效值（运行时改设置即下轮生效），
// 删除 CreatedAt 早于 now-N天 的备份。retentionDays<=0 视为不裁剪（保留全部）。
// 逐个走 Delete（含删文件 + 拒删被增量子链引用者，保链完整）；单条失败仅记录不中断。
// 返回成功删除的条数，便于测试断言。
//
// **快照底链豁免**（§2.2）：origin=snapshot 的备份不参与本策略裁剪。
// 快照是全量自包含的时间点，其生命周期只能由 snapshot.retention_days 决定；
// 若让 backup.retention_days（默认 14）先裁掉它，快照会变成指向软删行的死链，
// 而列表仍显示「可回滚」，直到用户点下去才发现失败。
func (s *BackupService) pruneExpiredOnce() int {
	days := s.retentionDays()
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)

	var expired []model.Backup
	if err := s.db.Where("created_at < ? AND (origin IS NULL OR origin <> ?)", cutoff, model.BackupOriginSnapshot).
		Order("created_at ASC, id ASC").Find(&expired).Error; err != nil {
		slog.Error("查询超期备份失败", "err", err)
		return 0
	}

	deleted := 0
	for i := range expired {
		if err := s.Delete(expired[i].ID); err != nil {
			// 被未超期的增量子备份引用等：本轮跳过，待子备份超期后再裁剪（保链不可恢复）。
			slog.Warn("裁剪超期备份失败（跳过）", "backupId", expired[i].UUID, "err", err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		slog.Info("按保留天数裁剪旧备份", "days", days, "deleted", deleted)
	}
	return deleted
}

// retentionDays 取保留天数生效值（平台设置 backup.retention_days）。解析失败返回 0（不裁剪）。
func (s *BackupService) retentionDays() int {
	if s.settings == nil {
		return 0
	}
	n, err := strconv.Atoi(s.settings.EffectiveValue(SettingKeyBackupRetentionDays))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
