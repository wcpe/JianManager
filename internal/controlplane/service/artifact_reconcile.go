package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/blobstore"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrReconcileInProgress 同渠道对账在途，拒绝并发触发（单渠道显式 409，全局触发跳过）。
	ErrReconcileInProgress = errors.New("该渠道对账正在进行中")
	// ErrReconcileChannelUnsupported local 渠道不参与对账（本地文件系统由 CAS 自管）。
	ErrReconcileChannelUnsupported = errors.New("本机存储渠道不参与对账")
	// ErrReconcileNoChannel 全局触发时无任何 s3 渠道。
	ErrReconcileNoChannel = errors.New("暂无 s3 存储渠道，无需对账")
	// ErrReconcileRunNotFound 对账运行记录不存在。
	ErrReconcileRunNotFound = errors.New("对账运行记录不存在")
	// ErrReconcileRunRunning 对账仍在进行中，差异报告未生成，处置被拒。
	ErrReconcileRunRunning = errors.New("对账仍在进行中，请等待完成")
	// ErrReconcileInvalidInterval 定期周期越界。
	ErrReconcileInvalidInterval = errors.New("定期对账周期须在 1~720 小时之间")
)

const (
	// artifactReconcileScope 对象侧遍历的键前缀：渠道 prefix 下的 CAS client-file 命名空间。
	// 命名空间外对象（probe/ 探测残留、共 bucket 他方对象）不参与比对、不算孤儿（spec §3.1）。
	artifactReconcileScope = "var/artifacts/client-file/"
	// artifactReconcileIntervalMin / Max / Default 定期周期钳制区间与默认（每日）。
	artifactReconcileIntervalMin     = 1
	artifactReconcileIntervalMax     = 720
	artifactReconcileIntervalDefault = 24
	// artifactReconcileListPageSize 对象遍历页大小（ListObjectsV2 单页上限同级）。
	artifactReconcileListPageSize = 1000
)

// ArtifactReconcileService 制品索引 ↔ S3 对象清单一致性对账服务（FR-349）。
//
// 逐 s3 渠道比对「Asset 索引键集合」vs「渠道 prefix 下 CAS 命名空间对象清单」，产出
// 缺失/孤儿差异报告落库；处置走显式按钮（标记失效 / 二次确认清理），**不做全自动修复**。
// 定期调度形态沿平台级后台 goroutine 先例（Scheduler/MetricService：分钟级 tick + Start/Stop）。
type ArtifactReconcileService struct {
	db       *gorm.DB
	channels *ArtifactStorageChannelService
	// audit 审计服务；nil 时审计静默跳过（沿 RuntimeAssetsHandler 约定）。
	audit *AuditService

	mu       sync.Mutex
	inflight map[uint]bool // channelID → 对账在途（进程内去重；当前部署恒单 CP）
	// storeFor 渠道 → BlobStore 解析；默认 channels.StoreFor，测试注入假 store。
	storeFor func(*model.ArtifactStorageChannel) (blobstore.Store, error)
	// pageSize ListPage 页大小；测试注小值验证跨页遍历。
	pageSize int
	// now 可注入时钟（定期调度与运行时间戳的确定性测试）。
	now func() time.Time

	loopMu  sync.Mutex
	stopCh  chan struct{}
	running bool
}

// NewArtifactReconcileService 创建对账服务。
func NewArtifactReconcileService(db *gorm.DB, channels *ArtifactStorageChannelService) *ArtifactReconcileService {
	s := &ArtifactReconcileService{
		db:       db,
		channels: channels,
		inflight: map[uint]bool{},
		pageSize: artifactReconcileListPageSize,
		now:      time.Now,
	}
	s.storeFor = channels.StoreFor
	return s
}

// SetAudit 注入审计服务（处置/触发/设置变更留痕）。
func (s *ArtifactReconcileService) SetAudit(a *AuditService) { s.audit = a }

// Start 启动定期调度循环（分钟级 tick）并清障：CP 上次崩溃/重启遗留的 running 运行行置 failed。
func (s *ArtifactReconcileService) Start() {
	s.loopMu.Lock()
	if s.running {
		s.loopMu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	stop := s.stopCh
	s.loopMu.Unlock()

	s.markInterruptedRuns()

	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				s.checkScheduled(now)
			}
		}
	}()
	slog.Info("制品对账定期调度已启动")
}

// Stop 停止定期调度循环。
func (s *ArtifactReconcileService) Stop() {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	if !s.running {
		return
	}
	close(s.stopCh)
	s.running = false
}

// markInterruptedRuns 启动清障：遗留 running 的运行行置 failed（防 CP 崩溃留假在途）。
func (s *ArtifactReconcileService) markInterruptedRuns() {
	now := s.now().UTC()
	res := s.db.Model(&model.ArtifactReconcileRun{}).
		Where("status = ?", model.ArtifactReconcileRunning).
		Updates(map[string]any{
			"status":        model.ArtifactReconcileFailed,
			"error_message": "CP 重启中断",
			"finished_at":   &now,
		})
	if res.Error != nil {
		slog.Error("清理中断对账运行失败", "error", res.Error)
	} else if res.RowsAffected > 0 {
		slog.Warn("已将中断的对账运行置为失败", "count", res.RowsAffected)
	}
}

// checkScheduled 定期调度判定（每分钟调用；导出结构便于确定性测试直接调用）。
// NextRunAt 为空 → 置 now+interval（首个周期从启用/启动时刻起算，不在启动瞬间扫存储）；
// 到点 → TriggerAll(scheduled) 并推进 NextRunAt。
func (s *ArtifactReconcileService) checkScheduled(now time.Time) {
	setting, err := s.Settings()
	if err != nil {
		slog.Error("读取定期对账设置失败", "error", err)
		return
	}
	if !setting.Enabled {
		return
	}
	interval := time.Duration(clampReconcileInterval(setting.IntervalHours)) * time.Hour
	if setting.NextRunAt == nil {
		next := now.Add(interval)
		if uerr := s.db.Model(&model.ArtifactReconcileSetting{}).Where("id = ?", setting.ID).
			Update("next_run_at", &next).Error; uerr != nil {
			slog.Error("初始化定期对账下次执行时间失败", "error", uerr)
		}
		return
	}
	if now.Before(*setting.NextRunAt) {
		return
	}
	next := now.Add(interval)
	if uerr := s.db.Model(&model.ArtifactReconcileSetting{}).Where("id = ?", setting.ID).
		Update("next_run_at", &next).Error; uerr != nil {
		slog.Error("推进定期对账下次执行时间失败", "error", uerr)
	}
	started, skipped, terr := s.TriggerAll(model.ArtifactReconcileTriggerScheduled)
	if terr != nil && !errors.Is(terr, ErrReconcileNoChannel) {
		slog.Error("定期对账触发失败", "error", terr)
		return
	}
	if len(started) > 0 || len(skipped) > 0 {
		slog.Info("定期对账已触发", "started", len(started), "skipped", len(skipped))
	}
}

// Settings 读取定期对账设置（单行 id=1，缺失时按默认 seed）。
func (s *ArtifactReconcileService) Settings() (*model.ArtifactReconcileSetting, error) {
	var setting model.ArtifactReconcileSetting
	err := s.db.First(&setting, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		setting = model.ArtifactReconcileSetting{ID: 1, Enabled: true, IntervalHours: artifactReconcileIntervalDefault}
		if cerr := s.db.Create(&setting).Error; cerr != nil {
			return nil, fmt.Errorf("初始化定期对账设置失败: %w", cerr)
		}
		return &setting, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询定期对账设置失败: %w", err)
	}
	return &setting, nil
}

// UpdateSettings 更新定期对账设置：周期钳 [1,720] 小时；变更即重算 NextRunAt
//（启用 → now+interval；禁用 → nil）。
func (s *ArtifactReconcileService) UpdateSettings(enabled bool, intervalHours int) (*model.ArtifactReconcileSetting, error) {
	if intervalHours < artifactReconcileIntervalMin || intervalHours > artifactReconcileIntervalMax {
		return nil, fmt.Errorf("%w: %d", ErrReconcileInvalidInterval, intervalHours)
	}
	setting, err := s.Settings()
	if err != nil {
		return nil, err
	}
	updates := map[string]any{"enabled": enabled, "interval_hours": intervalHours}
	if enabled {
		next := s.now().Add(time.Duration(intervalHours) * time.Hour)
		updates["next_run_at"] = &next
	} else {
		updates["next_run_at"] = nil
	}
	if uerr := s.db.Model(&model.ArtifactReconcileSetting{}).Where("id = ?", setting.ID).Updates(updates).Error; uerr != nil {
		return nil, fmt.Errorf("更新定期对账设置失败: %w", uerr)
	}
	return s.Settings()
}

// ReconcileSkipped 全局触发时被跳过的渠道（对账在途）。
type ReconcileSkipped struct {
	ChannelID   uint   `json:"channelId"`
	ChannelName string `json:"channelName"`
	Reason      string `json:"reason"`
}

// Trigger 异步触发单渠道对账：建 running 运行行 + goroutine 执行，即刻返回（前端轮询）。
// 在途重复触发 ErrReconcileInProgress；local 渠道 ErrReconcileChannelUnsupported。
func (s *ArtifactReconcileService) Trigger(channelID uint, triggeredBy string) (*model.ArtifactReconcileRun, error) {
	run, ch, store, err := s.beginRun(channelID, triggeredBy)
	if err != nil {
		return nil, err
	}
	go s.executeRun(run, ch, store)
	return run, nil
}

// ReconcileSync 同步对账一个渠道（测试与需要终态的调用方用），返回终态运行记录。
func (s *ArtifactReconcileService) ReconcileSync(channelID uint, triggeredBy string) (*model.ArtifactReconcileRun, error) {
	run, ch, store, err := s.beginRun(channelID, triggeredBy)
	if err != nil {
		return nil, err
	}
	s.executeRun(run, ch, store)
	return s.GetRun(run.ID)
}

// TriggerAll 触发全部 s3 渠道对账：在途渠道跳过并回报；无 s3 渠道 ErrReconcileNoChannel。
func (s *ArtifactReconcileService) TriggerAll(triggeredBy string) ([]model.ArtifactReconcileRun, []ReconcileSkipped, error) {
	chs, err := s.channels.List()
	if err != nil {
		return nil, nil, err
	}
	started := make([]model.ArtifactReconcileRun, 0)
	skipped := make([]ReconcileSkipped, 0)
	s3Count := 0
	for i := range chs {
		if chs[i].Type != model.ArtifactStorageS3 {
			continue
		}
		s3Count++
		run, terr := s.Trigger(chs[i].ID, triggeredBy)
		if terr != nil {
			skipped = append(skipped, ReconcileSkipped{ChannelID: chs[i].ID, ChannelName: chs[i].Name, Reason: terr.Error()})
			continue
		}
		started = append(started, *run)
	}
	if s3Count == 0 {
		return nil, nil, ErrReconcileNoChannel
	}
	return started, skipped, nil
}

// beginRun 触发前置：渠道校验 + 在途互斥登记 + 建 running 运行行。
func (s *ArtifactReconcileService) beginRun(channelID uint, triggeredBy string) (*model.ArtifactReconcileRun, *model.ArtifactStorageChannel, blobstore.Store, error) {
	ch, err := s.channels.GetByID(channelID)
	if err != nil {
		return nil, nil, nil, err
	}
	if ch.Type != model.ArtifactStorageS3 {
		return nil, nil, nil, ErrReconcileChannelUnsupported
	}
	store, err := s.storeFor(ch)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("构造渠道存储后端失败: %w", err)
	}

	s.mu.Lock()
	if s.inflight[channelID] {
		s.mu.Unlock()
		return nil, nil, nil, ErrReconcileInProgress
	}
	s.inflight[channelID] = true
	s.mu.Unlock()

	run := &model.ArtifactReconcileRun{
		ChannelID:   ch.ID,
		ChannelName: ch.Name,
		Status:      model.ArtifactReconcileRunning,
		TriggeredBy: triggeredBy,
		StartedAt:   s.now().UTC(),
	}
	if cerr := s.db.Create(run).Error; cerr != nil {
		s.release(channelID)
		return nil, nil, nil, fmt.Errorf("创建对账运行记录失败: %w", cerr)
	}
	return run, ch, store, nil
}

func (s *ArtifactReconcileService) release(channelID uint) {
	s.mu.Lock()
	delete(s.inflight, channelID)
	s.mu.Unlock()
}

// executeRun 执行一次对账（异步 goroutine / 同步测试入口共用），终态写回运行行。
// 对账本身零写入存储：只读索引 + 只读 List；全部变更集中在处置端点（spec §3.5）。
func (s *ArtifactReconcileService) executeRun(run *model.ArtifactReconcileRun, ch *model.ArtifactStorageChannel, store blobstore.Store) {
	defer s.release(ch.ID)

	// 索引侧：该渠道全部 s3 client-file 资产（含 lost——lost 键仍参与孤儿排除，防误删）。
	var assets []model.Asset
	if err := s.db.Where("type = ? AND storage_backend = ? AND storage_channel_id = ?",
		model.AssetTypeClientFile, model.AssetBackendS3, ch.ID).Find(&assets).Error; err != nil {
		s.finishFailed(run, fmt.Errorf("查询渠道制品索引失败: %w", err))
		return
	}
	indexByKey := make(map[string]*model.Asset, len(assets))
	for i := range assets {
		indexByKey[assets[i].RelPath] = &assets[i]
	}

	// 对象侧：分页全量遍历 CAS client-file 命名空间（命名空间外不参与，spec §3.1）。
	ctx := context.Background()
	objectKeys := make(map[string]bool)
	var orphans []model.ArtifactReconcileDiff
	objectCount := 0
	token := ""
	for {
		items, next, lerr := store.ListPage(ctx, artifactReconcileScope, s.pageSize, token)
		if lerr != nil {
			s.finishFailed(run, fmt.Errorf("遍历渠道对象清单失败: %w", lerr))
			return
		}
		for _, it := range items {
			// 防御：适配器按 prefix 过滤，此处再校验命名空间（共 bucket 前缀畸形键兜底）。
			if !strings.HasPrefix(it.Key, artifactReconcileScope) {
				continue
			}
			objectCount++
			objectKeys[it.Key] = true
			if _, ok := indexByKey[it.Key]; ok {
				continue
			}
			mod := it.ModTime
			var modPtr *time.Time
			if !mod.IsZero() {
				m := mod.UTC()
				modPtr = &m
			}
			orphans = append(orphans, model.ArtifactReconcileDiff{
				RunID: run.ID, ChannelID: ch.ID, Kind: model.ArtifactDiffOrphan,
				ObjectKey: it.Key, Size: it.Size, LastModified: modPtr,
				Status: model.ArtifactDiffOpen,
			})
		}
		if next == "" {
			break
		}
		token = next
	}

	// 缺失：索引中非 lost 资产的键未在对象集合出现（已 lost 的不重复报，列表红标持续可见）。
	var missing []model.ArtifactReconcileDiff
	matched := 0
	for i := range assets {
		a := &assets[i]
		if objectKeys[a.RelPath] {
			if a.StorageState != model.AssetStorageLost {
				matched++
			}
			continue
		}
		if a.StorageState == model.AssetStorageLost {
			continue
		}
		missing = append(missing, model.ArtifactReconcileDiff{
			RunID: run.ID, ChannelID: ch.ID, Kind: model.ArtifactDiffMissing,
			AssetID: a.ID, SHA256: a.SHA256, ObjectKey: a.RelPath, Size: a.Size,
			Status: model.ArtifactDiffOpen,
		})
	}

	// 终态：一个事务写差异明细 + 更新运行行。
	now := s.now().UTC()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		diffs := append(missing, orphans...)
		if len(diffs) > 0 {
			if derr := tx.CreateInBatches(diffs, 200).Error; derr != nil {
				return fmt.Errorf("落差异明细失败: %w", derr)
			}
		}
		return tx.Model(&model.ArtifactReconcileRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status":        model.ArtifactReconcileSucceeded,
			"finished_at":   &now,
			"index_count":   len(assets),
			"object_count":  objectCount,
			"matched_count": matched,
			"missing_count": len(missing),
			"orphan_count":  len(orphans),
		}).Error
	})
	if err != nil {
		s.finishFailed(run, err)
		return
	}
	if len(missing) > 0 || len(orphans) > 0 {
		slog.Warn("制品对账发现差异", "channel", ch.Name, "runId", run.ID,
			"missing", len(missing), "orphan", len(orphans))
	}
}

// finishFailed 运行行置 failed 并记原因（截断 512）。
func (s *ArtifactReconcileService) finishFailed(run *model.ArtifactReconcileRun, cause error) {
	msg := cause.Error()
	if len(msg) > 512 {
		msg = msg[:512]
	}
	now := s.now().UTC()
	if err := s.db.Model(&model.ArtifactReconcileRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status":        model.ArtifactReconcileFailed,
		"error_message": msg,
		"finished_at":   &now,
	}).Error; err != nil {
		slog.Error("写对账失败终态失败", "runId", run.ID, "error", err)
	}
	slog.Error("制品对账失败", "runId", run.ID, "error", cause)
}

// ListRuns 最近运行记录（id desc）。channelID=0 不过滤；limit 默认 20 上限 100。
func (s *ArtifactReconcileService) ListRuns(channelID uint, limit int) ([]model.ArtifactReconcileRun, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := s.db.Model(&model.ArtifactReconcileRun{})
	if channelID > 0 {
		q = q.Where("channel_id = ?", channelID)
	}
	var runs []model.ArtifactReconcileRun
	if err := q.Order("id desc").Limit(limit).Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("查询对账运行记录失败: %w", err)
	}
	return runs, nil
}

// GetRun 按 ID 取运行记录。
func (s *ArtifactReconcileService) GetRun(id uint) (*model.ArtifactReconcileRun, error) {
	var run model.ArtifactReconcileRun
	if err := s.db.First(&run, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReconcileRunNotFound
		}
		return nil, fmt.Errorf("查询对账运行记录失败: %w", err)
	}
	return &run, nil
}

// ListDiffs 分页查询某 run 的差异明细。kind 空=全部；pageSize 默认 50 上限 200。
func (s *ArtifactReconcileService) ListDiffs(runID uint, kind string, page, pageSize int) ([]model.ArtifactReconcileDiff, int64, error) {
	if _, err := s.GetRun(runID); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	q := s.db.Model(&model.ArtifactReconcileDiff{}).Where("run_id = ?", runID)
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计差异明细失败: %w", err)
	}
	var diffs []model.ArtifactReconcileDiff
	if err := q.Order("id asc").Limit(pageSize).Offset((page - 1) * pageSize).Find(&diffs).Error; err != nil {
		return nil, 0, fmt.Errorf("查询差异明细失败: %w", err)
	}
	return diffs, total, nil
}

// ResolveMissingResult 「标记失效」处置结果。
type ResolveMissingResult struct {
	// Marked 已标记失效的资产数。
	Marked int `json:"marked"`
	// Stale 守卫命中的过时明细数（资产已删/已迁走，不动资产）。
	Stale int `json:"stale"`
}

// ResolveMissing 把 run 内全部待处置缺失明细的资产标记失效（StorageState=lost）。
// 守卫（spec §3.5）：资产仍存在且仍为「s3 + 同渠道 + 同键」才标；否则明细翻 stale。
func (s *ArtifactReconcileService) ResolveMissing(runID uint) (*ResolveMissingResult, error) {
	run, err := s.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if run.Status == model.ArtifactReconcileRunning {
		return nil, ErrReconcileRunRunning
	}
	var diffs []model.ArtifactReconcileDiff
	if err := s.db.Where("run_id = ? AND kind = ? AND status = ?",
		runID, model.ArtifactDiffMissing, model.ArtifactDiffOpen).Find(&diffs).Error; err != nil {
		return nil, fmt.Errorf("查询缺失明细失败: %w", err)
	}
	res := &ResolveMissingResult{}
	now := s.now().UTC()
	for i := range diffs {
		d := &diffs[i]
		var asset model.Asset
		gerr := s.db.First(&asset, d.AssetID).Error
		valid := gerr == nil &&
			asset.StorageBackend == model.AssetBackendS3 &&
			asset.StorageChannelID == run.ChannelID &&
			asset.RelPath == d.ObjectKey
		action := model.ArtifactDiffActionStale
		if valid {
			if uerr := s.db.Model(&model.Asset{}).Where("id = ?", asset.ID).
				Update("storage_state", model.AssetStorageLost).Error; uerr != nil {
				return res, fmt.Errorf("标记资产失效失败(asset %d): %w", asset.ID, uerr)
			}
			action = model.ArtifactDiffActionMarkedLost
			res.Marked++
		} else {
			res.Stale++
		}
		if uerr := s.db.Model(&model.ArtifactReconcileDiff{}).Where("id = ?", d.ID).Updates(map[string]any{
			"status":          model.ArtifactDiffResolved,
			"resolved_at":     &now,
			"resolved_action": action,
		}).Error; uerr != nil {
			return res, fmt.Errorf("更新差异明细失败(diff %d): %w", d.ID, uerr)
		}
	}
	return res, nil
}

// CleanupOrphansResult 「清理孤儿」处置结果。
type CleanupOrphansResult struct {
	// Cleaned 已删除的 S3 对象数。
	Cleaned int `json:"cleaned"`
	// Stale 过时守卫命中数（run 后同键已被新上传合法引用，不删）。
	Stale int `json:"stale"`
	// Failed 删除失败数（明细保持 open + ResolveError，可重试）。
	Failed int `json:"failed"`
}

// CleanupOrphans 删除 run 内全部待处置孤儿对象（二次确认后的显式处置）。
// 过时守卫（spec §3.5）：当前索引已有该渠道同键资产（run 之后新上传同内容）→ 翻 stale 不删，
// 防止把刚上传的合法对象清掉；删除失败保持 open 供重试。
func (s *ArtifactReconcileService) CleanupOrphans(runID uint) (*CleanupOrphansResult, error) {
	run, err := s.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if run.Status == model.ArtifactReconcileRunning {
		return nil, ErrReconcileRunRunning
	}
	ch, err := s.channels.GetByID(run.ChannelID)
	if err != nil {
		return nil, fmt.Errorf("解析对账渠道失败: %w", err)
	}
	store, err := s.storeFor(ch)
	if err != nil {
		return nil, fmt.Errorf("构造渠道存储后端失败: %w", err)
	}
	var diffs []model.ArtifactReconcileDiff
	if err := s.db.Where("run_id = ? AND kind = ? AND status = ?",
		runID, model.ArtifactDiffOrphan, model.ArtifactDiffOpen).Find(&diffs).Error; err != nil {
		return nil, fmt.Errorf("查询孤儿明细失败: %w", err)
	}
	res := &CleanupOrphansResult{}
	ctx := context.Background()
	for i := range diffs {
		d := &diffs[i]
		// 过时守卫：同渠道同键当前已有索引记录 → 对象已被合法引用，绝不删。
		var refs int64
		if cerr := s.db.Model(&model.Asset{}).
			Where("storage_channel_id = ? AND rel_path = ? AND storage_backend = ?",
				run.ChannelID, d.ObjectKey, model.AssetBackendS3).
			Count(&refs).Error; cerr != nil {
			return res, fmt.Errorf("孤儿过时守卫查询失败(diff %d): %w", d.ID, cerr)
		}
		now := s.now().UTC()
		if refs > 0 {
			if uerr := s.resolveDiff(d.ID, model.ArtifactDiffActionStale, &now, ""); uerr != nil {
				return res, uerr
			}
			res.Stale++
			continue
		}
		if derr := store.Delete(ctx, d.ObjectKey); derr != nil {
			msg := derr.Error()
			if len(msg) > 512 {
				msg = msg[:512]
			}
			if uerr := s.db.Model(&model.ArtifactReconcileDiff{}).Where("id = ?", d.ID).
				Update("resolve_error", msg).Error; uerr != nil {
				return res, fmt.Errorf("记录清理失败原因失败(diff %d): %w", d.ID, uerr)
			}
			res.Failed++
			continue
		}
		if uerr := s.resolveDiff(d.ID, model.ArtifactDiffActionCleaned, &now, ""); uerr != nil {
			return res, uerr
		}
		res.Cleaned++
	}
	return res, nil
}

// resolveDiff 把一条差异明细翻 resolved。
func (s *ArtifactReconcileService) resolveDiff(id uint, action string, at *time.Time, resolveErr string) error {
	if err := s.db.Model(&model.ArtifactReconcileDiff{}).Where("id = ?", id).Updates(map[string]any{
		"status":          model.ArtifactDiffResolved,
		"resolved_at":     at,
		"resolved_action": action,
		"resolve_error":   resolveErr,
	}).Error; err != nil {
		return fmt.Errorf("更新差异明细失败(diff %d): %w", id, err)
	}
	return nil
}

// clampReconcileInterval 读路径周期兜底钳制（存量脏数据防御；写路径已校验区间）。
func clampReconcileInterval(h int) int {
	if h < artifactReconcileIntervalMin {
		return artifactReconcileIntervalDefault
	}
	if h > artifactReconcileIntervalMax {
		return artifactReconcileIntervalMax
	}
	return h
}
