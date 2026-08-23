package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/blobstore"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

var (
	// ErrArtifactMigrationInFlight 已有迁移任务在途（一次仅允许一个，FR-348）。
	ErrArtifactMigrationInFlight = errors.New("已有制品迁移任务在途，同一时间仅允许一个迁移任务")
	// ErrArtifactMigrationTargetUnavailable 目标渠道真连探测失败，发起被拒（快失败守卫）。
	ErrArtifactMigrationTargetUnavailable = errors.New("目标渠道连通性探测失败")
	// ErrArtifactStorageMigrationInFlight 迁移在途禁止删除任何存储渠道（源集合由逐条记录
	// 自述、随迁移动态收敛，静态判定「此渠道无关」不可靠，故粗粒度全禁，FR-348）。
	ErrArtifactStorageMigrationInFlight = errors.New("存量迁移任务在途，禁止删除存储渠道")
)

const (
	// artifactMigrationTimeout 迁移执行超时：存量可达数百 GB，远超默认 30min；
	// 超时任务置 failed，重新发起即续跑（已迁条跳过）。
	artifactMigrationTimeout = 12 * time.Hour
	// artifactMigrationFailureListCap 失败明细单次返回上限（总失败数看计数行 failed）。
	artifactMigrationFailureListCap = 500
)

// ArtifactMigrationService 制品存量迁移服务（FR-348，底座见 ADR-073）。
//
// 把全部 client-file 存量逐条搬到目标渠道：源读取（sha256 复核）→ 写入目标 →
// **先改 Asset 记录 → 再删源**。位置由记录自述（ADR-073），迁一条改一条，随时中断
// 不破读取；重新发起同目标迁移 = 幂等续跑（已在目标的跳过）。执行体为 CP 后台
// goroutine（沿 FR-323 形态，NodeID=0），进度/强停接入全局任务中心。
type ArtifactMigrationService struct {
	db       *gorm.DB
	root     *dataroot.Root
	channels *ArtifactStorageChannelService
	tasks    *TaskService
	// mu 发起临界区：与 DB 在途检查合成防并发发起（单 CP 进程内互斥即足够）。
	mu sync.Mutex
}

// NewArtifactMigrationService 创建迁移服务。root 提供中转临时文件目录（cache/，同 Ingest）。
func NewArtifactMigrationService(db *gorm.DB, root *dataroot.Root, channels *ArtifactStorageChannelService, tasks *TaskService) *ArtifactMigrationService {
	return &ArtifactMigrationService{db: db, root: root, channels: channels, tasks: tasks}
}

// RecoverOrphans 启动孤儿清扫（main 装配时调用）：CP 重启后 goroutine 已死但任务行
// 滞留非终态，批量置 failed——保证「DB 非终态 ⇔ 本进程内真在跑」的不变式，
// 发起 409 守卫与渠道删除守卫都建立在此不变式上。
func (s *ArtifactMigrationService) RecoverOrphans() error {
	res := s.db.Model(&model.Task{}).
		Where("kind = ? AND state IN ?", model.TaskKindArtifactMigrate, artifactMigrationLiveStates()).
		Updates(map[string]any{
			"state": model.TaskStateFailed,
			"error": "主控重启导致迁移中断；重新发起同目标迁移即续跑（已完成的制品自动跳过）",
		})
	if res.Error != nil {
		return fmt.Errorf("清扫迁移孤儿任务失败: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		slog.Info("已清扫制品迁移孤儿任务", "count", res.RowsAffected)
	}
	return nil
}

// Start 发起「迁移到渠道 targetID」后台任务，返回 taskID。
// 守卫顺序：渠道存在 → 一次一个在途（409）→ 目标真连探测（探测失败即拒，422）。
func (s *ArtifactMigrationService) Start(targetID, createdBy uint) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	target, err := s.channels.GetByID(targetID)
	if err != nil {
		return "", err
	}
	inflight, err := artifactMigrationInFlight(s.db)
	if err != nil {
		return "", err
	}
	if inflight {
		return "", ErrArtifactMigrationInFlight
	}
	// 目标真连探测（顺带刷新渠道 LastTest*）：s3=写探测对象，local=数据根可写。
	probe, err := s.channels.TestSaved(targetID)
	if err != nil {
		return "", err
	}
	if !probe.OK {
		return "", fmt.Errorf("%w: %s", ErrArtifactMigrationTargetUnavailable, probe.Message)
	}
	// 目标 BlobStore 解析一次（凭证解密失败在此快失败）；在途期间编辑目标渠道不影响本次连接。
	targetStore, err := s.channels.StoreFor(target)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrArtifactMigrationTargetUnavailable, err)
	}

	taskID := uuid.New().String()
	title := fmt.Sprintf("制品存量迁移 → %s", target.Name)
	if _, cerr := s.tasks.CreateTask(taskID, 0, model.TaskKindArtifactMigrate, title, "排队中", createdBy); cerr != nil {
		return "", cerr
	}
	// 登记行与任务同步落库（手写生命周期而非 RunAsync 的原因：发起返回前四计数行必须已存在，
	// 否则 goroutine 首条计数更新与前端首次查询都有丢失窗口）。
	if cerr := s.db.Create(&model.ArtifactMigration{TaskID: taskID, TargetChannelID: target.ID}).Error; cerr != nil {
		s.markTaskFailed(taskID, "创建迁移登记失败: "+cerr.Error())
		return "", fmt.Errorf("创建迁移登记失败: %w", cerr)
	}
	go s.run(taskID, target, targetStore)
	return taskID, nil
}

// run 后台执行体：快照存量 → 分拣跳过 → 逐条搬运（每条前检查强停/超时）→ 终态收尾。
func (s *ArtifactMigrationService) run(taskID string, target *model.ArtifactStorageChannel, targetStore blobstore.Store) {
	ctx, cancel := context.WithTimeout(context.Background(), artifactMigrationTimeout)
	defer cancel()
	if err := s.tasks.MarkRunning(taskID); err != nil {
		slog.Warn("标记制品迁移任务运行中失败", "taskId", taskID, "error", err)
	}

	// 快照存量（发起后新上传按活跃渠道已落对位置，不进本次快照）。
	var assets []model.Asset
	if err := s.db.Where("type = ?", model.AssetTypeClientFile).Order("id asc").Find(&assets).Error; err != nil {
		s.markTaskFailed(taskID, "查询存量制品失败: "+err.Error())
		return
	}
	var eligible []model.Asset
	skipped := 0
	for i := range assets {
		if assetAtChannel(&assets[i], target) {
			skipped++
		} else {
			eligible = append(eligible, assets[i])
		}
	}
	total := len(assets)
	s.updateRegistry(taskID, map[string]any{"total": total, "skipped": skipped})

	migrated, failed := 0, 0
	lastPct := -1
	stage := func() {
		pct := 100
		if len(eligible) > 0 {
			pct = (migrated + failed) * 100 / len(eligible)
		}
		// 只在整数百分比推进时落阶段（TaskLog 留轨迹又不被逐条日志膨胀）；
		// 精确四计数由登记行持久承载，前端从 GET /artifact-storages/migration 读取。
		if pct != lastPct {
			lastPct = pct
			s.tasks.SetStage(taskID, pct, fmt.Sprintf("共 %d · 已迁 %d · 失败 %d · 跳过 %d", total, migrated, failed, skipped))
		}
	}
	stage()

	for i := range eligible {
		if ctx.Err() != nil {
			s.markTaskFailed(taskID, "迁移执行超时中断；重新发起同目标迁移即续跑（已完成的制品自动跳过）")
			return
		}
		// 强停（FR-227 复用）：NodeID=0 的取消由任务中心直接置 canceled，此处逐条检查退出。
		if s.taskHalted(taskID) {
			slog.Info("制品迁移已被停止，循环退出", "taskId", taskID)
			return
		}
		a := eligible[i]
		if err := s.migrateOne(ctx, &a, target, targetStore); err != nil {
			failed++
			s.recordFailure(taskID, &a, err)
			slog.Warn("制品迁移单条失败", "taskId", taskID, "asset", a.ID, "sha256", a.SHA256, "error", err)
		} else {
			migrated++
		}
		s.updateRegistry(taskID, map[string]any{"migrated": migrated, "failed": failed})
		stage()
	}

	if failed > 0 {
		s.markTaskFailed(taskID, fmt.Sprintf("%d 条制品迁移失败，详见失败明细；重新发起同目标迁移可重试（成功条自动跳过）", failed))
		return
	}
	result, err := json.Marshal(map[string]int{"total": total, "migrated": migrated, "failed": failed, "skipped": skipped})
	if err != nil {
		s.markTaskFailed(taskID, "生成迁移结果失败: "+err.Error())
		return
	}
	if err := s.tasks.MarkSucceeded(taskID, string(result)); err != nil {
		slog.Warn("标记制品迁移任务成功失败", "taskId", taskID, "error", err)
	}
}

func (s *ArtifactMigrationService) markTaskFailed(taskID, reason string) {
	if err := s.tasks.MarkFailed(taskID, reason); err != nil {
		slog.Warn("标记制品迁移任务失败状态失败", "taskId", taskID, "error", err)
	}
}

// migrateOne 单条搬运，顺序不可变（FR-348）：源读取（sha256 复核）→ 写目标 →
// **先改记录 → 再删源**。返回 err = 该条失败（不删源、记录不动、读取不受影响）。
func (s *ArtifactMigrationService) migrateOne(ctx context.Context, a *model.Asset, target *model.ArtifactStorageChannel, targetStore blobstore.Store) error {
	src, err := s.channels.StoreForAsset(a)
	if err != nil {
		return fmt.Errorf("解析源存储失败: %w", err)
	}
	rc, err := src.Open(ctx, a.RelPath)
	if err != nil {
		if errors.Is(err, blobstore.ErrBlobNotFound) {
			return fmt.Errorf("源对象不存在（%s），既有损伤待对账", a.RelPath)
		}
		return fmt.Errorf("读取源对象失败: %w", err)
	}

	// 中转临时文件（同 Ingest 落 cache/）：边拷边算 sha256 复核，防搬运损毁内容。
	cacheDir := s.root.CacheDir()
	if merr := os.MkdirAll(cacheDir, 0o755); merr != nil {
		_ = rc.Close()
		return fmt.Errorf("创建缓存目录失败: %w", merr)
	}
	tmp, err := os.CreateTemp(cacheDir, "migrate-*.part")
	if err != nil {
		_ = rc.Close()
		return fmt.Errorf("创建迁移临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	// 失败路径统一清理；成功路径 local 目标已 Rename 走，Remove 命中不存在被忽略。
	defer func() { _ = os.Remove(tmpPath) }()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), rc)
	_ = rc.Close()
	if cerr := tmp.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("读取源对象失败: %w", err)
	}
	if size != a.Size {
		return fmt.Errorf("源内容校验不符：实际大小 %d ≠ 记录 %d", size, a.Size)
	}
	if sum := hex.EncodeToString(hasher.Sum(nil)); !strings.EqualFold(sum, a.SHA256) {
		return fmt.Errorf("源内容 sha256 校验不符：实际 %s ≠ 记录 %s", sum, a.SHA256)
	}

	if err := targetStore.PutFile(ctx, a.RelPath, tmpPath, size); err != nil {
		return fmt.Errorf("写入目标失败: %w", err)
	}

	// 先改记录（乐观守卫：仅记录仍指向读取时的源位置才生效）。目标副本不回收——
	// 内容寻址残留无害归 FR-349 对账；主动回收在「双渠道误配同一物理桶」下反有删源风险。
	newBackend, newChannelID, newState := model.AssetBackendLocal, uint(0), model.AssetStorageHot
	if target.Type == model.ArtifactStorageS3 {
		newBackend, newChannelID, newState = model.AssetBackendS3, target.ID, model.AssetStorageExternal
	}
	res := s.db.Model(&model.Asset{}).
		Where("id = ? AND storage_backend = ? AND storage_channel_id = ?", a.ID, a.StorageBackend, a.StorageChannelID).
		Updates(map[string]any{
			"storage_backend":    newBackend,
			"storage_channel_id": newChannelID,
			"storage_state":      newState,
		})
	if res.Error != nil {
		return fmt.Errorf("更新制品记录失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("制品记录已变更或已删除，未删源")
	}

	// 再删源。防呆：源/目标指向同一物理位置（两 s3 渠道误配同 endpoint/bucket/prefix）时
	// 跳过——CAS 键相同即同一对象，删源=删刚写的目标副本。删源失败不判该条失败：
	// 记录已指向新位置读取正确，残留归 FR-349 对账（与既有删除尽力语义一致）。
	if skip, why := s.sourceSharesTargetLocation(a, target); skip {
		slog.Warn("迁移跳过删源（源与目标同物理位置）", "asset", a.ID, "reason", why)
	} else if derr := src.Delete(ctx, a.RelPath); derr != nil {
		slog.Warn("迁移删源失败（残留待对账）", "asset", a.ID, "key", a.RelPath, "error", derr)
	}
	return nil
}

// sourceSharesTargetLocation 报告源渠道与目标渠道是否指向同一物理位置（删源防呆）。
// a 为迁移前快照（StorageBackend/StorageChannelID 仍是源值）。
func (s *ArtifactMigrationService) sourceSharesTargetLocation(a *model.Asset, target *model.ArtifactStorageChannel) (bool, string) {
	if a.StorageBackend != model.AssetBackendS3 || target.Type != model.ArtifactStorageS3 {
		return false, "" // local↔s3 物理空间天然不重叠；local→local 已被跳过分拣挡住。
	}
	srcCh, err := s.channels.GetByID(a.StorageChannelID)
	if err != nil {
		return false, ""
	}
	if strings.EqualFold(strings.TrimSpace(srcCh.Endpoint), strings.TrimSpace(target.Endpoint)) &&
		srcCh.Bucket == target.Bucket && srcCh.Prefix == target.Prefix {
		return true, fmt.Sprintf("渠道 %d 与 %d 同 endpoint/bucket/prefix", srcCh.ID, target.ID)
	}
	return false, ""
}

// taskHalted 报告任务是否已被停止（canceled 终态或已请求取消）。查询失败按未停止处理（防误停）。
func (s *ArtifactMigrationService) taskHalted(taskID string) bool {
	var t model.Task
	if err := s.db.Select("state", "cancel_requested").Where("task_id = ?", taskID).First(&t).Error; err != nil {
		return false
	}
	return t.State.IsTerminal() || t.CancelRequested
}

// updateRegistry 持久化迁移计数（单写者=迁移 goroutine，直接覆盖写）。
func (s *ArtifactMigrationService) updateRegistry(taskID string, fields map[string]any) {
	if err := s.db.Model(&model.ArtifactMigration{}).Where("task_id = ?", taskID).Updates(fields).Error; err != nil {
		slog.Warn("更新迁移计数失败", "taskId", taskID, "error", err)
	}
}

// recordFailure 落一条失败明细（sha256+原因，FR-348 可查可重试的失败面）。
func (s *ArtifactMigrationService) recordFailure(taskID string, a *model.Asset, cause error) {
	f := &model.ArtifactMigrationFailure{
		TaskID: taskID, AssetID: a.ID, SHA256: a.SHA256,
		Filename: a.Filename, Size: a.Size, Reason: cause.Error(),
	}
	if err := s.db.Create(f).Error; err != nil {
		slog.Warn("落迁移失败明细失败", "taskId", taskID, "asset", a.ID, "error", err)
	}
}

// assetAtChannel 报告制品记录是否已在目标渠道（跳过分拣）：目标=local 时非 s3 记录
// 均视为已在本地；目标=s3 时须后端与渠道 ID 双匹配。
func assetAtChannel(a *model.Asset, ch *model.ArtifactStorageChannel) bool {
	if ch.Type == model.ArtifactStorageLocal {
		return a.StorageBackend != model.AssetBackendS3
	}
	return a.StorageBackend == model.AssetBackendS3 && a.StorageChannelID == ch.ID
}

// artifactMigrationLiveStates 非终态集合（在途判定共用）。
func artifactMigrationLiveStates() []model.TaskState {
	return []model.TaskState{model.TaskStatePending, model.TaskStateRunning}
}

// artifactMigrationInFlight 报告是否存在非终态迁移任务。
// 发起 409 守卫与渠道删除守卫共用；启动孤儿清扫保证该查询即真相。
func artifactMigrationInFlight(db *gorm.DB) (bool, error) {
	var n int64
	if err := db.Model(&model.Task{}).
		Where("kind = ? AND state IN ?", model.TaskKindArtifactMigrate, artifactMigrationLiveStates()).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("检查迁移任务失败: %w", err)
	}
	return n > 0, nil
}

// ArtifactMigrationInfo 迁移登记与实时计数（GET /artifact-storages/migration 响应体，spec §3.5）。
type ArtifactMigrationInfo struct {
	TaskID          string `json:"taskId"`
	TargetChannelID uint   `json:"targetChannelId"`
	// TargetName 目标渠道当前名称（迁移后渠道被删时为空串）。
	TargetName string `json:"targetName"`
	Total      int    `json:"total"`
	Migrated   int    `json:"migrated"`
	Failed     int    `json:"failed"`
	Skipped    int    `json:"skipped"`
}

// ArtifactMigrationStatus 最近一次迁移状态（任务 + 计数；从未迁移过则双 nil）。
type ArtifactMigrationStatus struct {
	Task      *model.Task            `json:"task"`
	Migration *ArtifactMigrationInfo `json:"migration"`
}

// Latest 最近一次迁移（按任务创建时间倒序取一）：渠道页在途进度与上次摘要的数据源。
func (s *ArtifactMigrationService) Latest() (*ArtifactMigrationStatus, error) {
	var t model.Task
	err := s.db.Where("kind = ?", model.TaskKindArtifactMigrate).
		Order("created_at DESC, id DESC").First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &ArtifactMigrationStatus{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询迁移任务失败: %w", err)
	}
	status := &ArtifactMigrationStatus{Task: &t}
	var reg model.ArtifactMigration
	if rerr := s.db.Where("task_id = ?", t.TaskID).First(&reg).Error; rerr == nil {
		info := &ArtifactMigrationInfo{
			TaskID: reg.TaskID, TargetChannelID: reg.TargetChannelID,
			Total: reg.Total, Migrated: reg.Migrated, Failed: reg.Failed, Skipped: reg.Skipped,
		}
		if ch, cerr := s.channels.GetByID(reg.TargetChannelID); cerr == nil {
			info.TargetName = ch.Name
		}
		status.Migration = info
	}
	return status, nil
}

// Failures 某次迁移任务的失败明细（id 升序，上限 500 条；总失败数看计数行 failed）。
func (s *ArtifactMigrationService) Failures(taskID string) ([]model.ArtifactMigrationFailure, error) {
	out := make([]model.ArtifactMigrationFailure, 0)
	if err := s.db.Where("task_id = ?", taskID).Order("id asc").
		Limit(artifactMigrationFailureListCap).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("查询迁移失败明细失败: %w", err)
	}
	return out, nil
}
