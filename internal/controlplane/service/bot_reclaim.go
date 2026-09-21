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

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-460：失效 Bot 自动回收与容量自动补足。
const (
	// defaultBotReclaimGracePeriod 默认宽限期：Bot 生命周期短，容忍一次世代迁移窗口即可（短于 FR-326 的 10m）。
	defaultBotReclaimGracePeriod = 2 * time.Minute
	// defaultBotReclaimAuto 默认自动回收：仅对 Fleet 归属 Bot 生效；V1 手动 Bot 永不自动回收。
	defaultBotReclaimAuto   = true
	botReclaimSweepInterval = 60 * time.Second
	// botReclaimFreshnessWindow 复用 FR-365 的 botFreshnessMissingWindow 口径（90s）。
	botReclaimFreshnessWindow = botFreshnessMissingWindow
)

// 失效判据命中说明（§2.1 条件 3）。
const (
	// BotReclaimStaleEpochMismatch worker_epoch_generation 落后于执行节点最近上报世代。
	BotReclaimStaleEpochMismatch = "epoch_mismatch"
	// BotReclaimStaleEmptyEpoch worker_epoch 为空（分片重启前残留，从未被任何世代认领）。
	BotReclaimStaleEmptyEpoch = "empty_epoch"
	// BotReclaimStaleNodeMissing executor_node_id 为空或对应节点已不存在。
	BotReclaimStaleNodeMissing = "node_missing"
)

var (
	// ErrBotReclaimNotFound 失效 Bot 回收记录不存在。
	ErrBotReclaimNotFound = errors.New("失效 Bot 回收记录不存在")
	// ErrBotReclaimNotActive 记录已终态（disposed/cancelled），不可再处置。
	ErrBotReclaimNotActive = errors.New("失效 Bot 回收记录已终态，不可处置")
	// ErrBotReclaimNotFleetOwned 非 Fleet 归属 Bot 不支持自动/手动回收（V1 手动 Bot 属用户资源）。
	ErrBotReclaimNotFleetOwned = errors.New("非 Fleet 归属 Bot 不支持回收，请使用 Bot 删除接口")
	// ErrBotReclaimWorkerOffline 执行节点未连接，无法下发停用。
	ErrBotReclaimWorkerOffline = errors.New("节点未连接，无法回收失效 Bot")
	// ErrBotReclaimDisposeFailed Worker 返回失败或 gRPC 错误。
	ErrBotReclaimDisposeFailed = errors.New("回收失效 Bot 失败")
)

// BotReclaimNodeEpoch 是判定失效所需的执行节点世代视图（真源取自容量快照）。
type BotReclaimNodeEpoch struct {
	NodeID                uint
	Exists                bool
	Online                bool
	WorkerEpoch           string
	WorkerEpochGeneration int64
}

// BotReclaimCapacitySource 提供执行节点当前世代；生产实现包装 BotLoadCapacityDirectory。
type BotReclaimCapacitySource interface {
	NodeEpochs(ctx context.Context) (map[uint]BotReclaimNodeEpoch, error)
}

// directoryBotReclaimCapacity 包装容量目录的快照，读取节点 WorkerEpochGeneration。
type directoryBotReclaimCapacity struct {
	directory *BotLoadCapacityDirectory
}

// NewDirectoryBotReclaimCapacity 使用既有容量目录装配世代来源（不新增 RPC）。
func NewDirectoryBotReclaimCapacity(directory *BotLoadCapacityDirectory) BotReclaimCapacitySource {
	return directoryBotReclaimCapacity{directory: directory}
}

func (d directoryBotReclaimCapacity) NodeEpochs(ctx context.Context) (map[uint]BotReclaimNodeEpoch, error) {
	if d.directory == nil {
		return nil, fmt.Errorf("Bot 负载容量目录未装配")
	}
	snapshot, err := d.directory.Snapshot(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[uint]BotReclaimNodeEpoch, len(snapshot.NodeCapacities))
	for _, capacity := range snapshot.NodeCapacities {
		out[capacity.NodeID] = BotReclaimNodeEpoch{
			NodeID: capacity.NodeID, Exists: true, Online: capacity.Online,
			WorkerEpoch: capacity.WorkerEpoch, WorkerEpochGeneration: capacity.WorkerEpochGeneration,
		}
	}
	return out, nil
}

// BotReclaimStopper 抽象「向执行节点下发单个 Fleet Bot 停用」；生产实现走 Fleet RPC。
//
// 生产实现使用 ApplyBotBatch(desired=stopped)——这是 CP→Worker 唯一的 Fleet 停用路径
// （Worker 的 StopBotBatch 由 applyBotBatch 内的 dispatchBotStops 触发；DeleteBot 对
// Fleet Bot 会因 legacy fleet mutation 被拒）。skipped=true 表示 Worker 报告该 Bot 已不存在/已停。
type BotReclaimStopper interface {
	StopFleetBot(ctx context.Context, nodeUUID string, assignment *workerpb.BotAssignment) (skipped bool, err error)
}

// poolBotReclaimStopper 经连接池用 ApplyBotBatch 下发 stopped assignment。
type poolBotReclaimStopper struct {
	pool *cpgrpc.ClientPool
}

func (s poolBotReclaimStopper) StopFleetBot(ctx context.Context, nodeUUID string, assignment *workerpb.BotAssignment) (bool, error) {
	if s.pool == nil {
		return false, ErrBotReclaimWorkerOffline
	}
	client, ok := s.pool.Get(nodeUUID)
	if !ok || client == nil || client.Worker == nil {
		return false, ErrBotReclaimWorkerOffline
	}
	identity := fmt.Sprintf("bot-reclaim|%s|%d", assignment.BotUuid, assignment.Generation)
	request := &workerpb.ApplyBotBatchRequest{
		BatchId:        stableBotLoadUUID(identity),
		IdempotencyKey: "bot-reclaim-" + stableBotLoadDigest(identity),
		Assignments:    []*workerpb.BotAssignment{assignment},
	}
	response, err := client.Worker.ApplyBotBatch(ctx, request)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrBotReclaimDisposeFailed, err)
	}
	if response == nil {
		return false, fmt.Errorf("%w: Worker 未返回回执", ErrBotReclaimDisposeFailed)
	}
	var result *workerpb.ApplyBotBatchItemResult
	for _, item := range response.Results {
		if item != nil && item.BotUuid == assignment.BotUuid {
			result = item
			break
		}
	}
	// 未返回逐项回执：分片已无该 Bot 时也可能如此，按幂等成功处理。
	if result == nil {
		return true, nil
	}
	if result.Accepted {
		return result.Skipped, nil
	}
	// 幂等：Worker 已无该 Bot 或已处于 stopped，视为回收成功。
	switch strings.ToLower(result.ErrorCode) {
	case "already_stopped", "not_found", "bot_not_found", "missing":
		return true, nil
	}
	msg := result.Error
	if msg == "" {
		msg = result.Status
	}
	return false, fmt.Errorf("%w: %s", ErrBotReclaimDisposeFailed, msg)
}

// botReclaimStale 是单条命中失效判据的 Bot 及其判据上下文。
type botReclaimStale struct {
	bot                    model.Bot
	reason                 string
	currentEpochGeneration int64
}

// BotReclaimService 失效 Bot 自动回收与容量自动补足（FR-460）。
//
// 每拍：按 workerEpoch 不匹配 + 状态判据识别失效 Fleet Bot → 宽限观察（pending）→ 宽限后
// confirmed → auto 且 Fleet 归属则下发停用并 CP 账本去账 → 触发容量补足回 planned_count 目标。
// V1 手动 Bot（无 load_batch_id/stress_session_id）只入列表等人工确认，永不自动处置。
type BotReclaimService struct {
	db         *gorm.DB
	settings   SettingsReader
	capacities BotReclaimCapacitySource
	stopper    BotReclaimStopper
	audit      *AuditService
	resolver   *BotExecutorResolver
	// refill 复用执行核心的补足助手（rebuildRunningAssignment / ApplyBotBatch / 结果回写）。
	refill *BotLoadExecutionService
	now    func() time.Time
	// stopTimeout 单次停用下发 RPC 超时。
	stopTimeout time.Duration
}

// NewBotReclaimService 创建失效 Bot 回收服务。settings/stopper/execution 可为 nil（单测裁剪）。
func NewBotReclaimService(db *gorm.DB, settings SettingsReader, capacities BotReclaimCapacitySource, stopper BotReclaimStopper, execution *BotLoadExecutionService) *BotReclaimService {
	return &BotReclaimService{
		db: db, settings: settings, capacities: capacities, stopper: stopper,
		resolver: NewBotExecutorResolver(db), refill: execution, now: time.Now,
		stopTimeout: 30 * time.Second,
	}
}

// NewGRPCBotReclaimService 使用连接池与既有容量目录/执行核心装配生产实例。
func NewGRPCBotReclaimService(db *gorm.DB, settings SettingsReader, directory *BotLoadCapacityDirectory, pool *cpgrpc.ClientPool, execution *BotLoadExecutionService) *BotReclaimService {
	return NewBotReclaimService(db, settings, NewDirectoryBotReclaimCapacity(directory), poolBotReclaimStopper{pool: pool}, execution)
}

// SetAudit 注入审计服务（自动/手动回收与补足均记）；nil 时跳过审计。
func (s *BotReclaimService) SetAudit(a *AuditService) { s.audit = a }

// SetNow 注入时钟（测试用）。
func (s *BotReclaimService) SetNow(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// gracePeriod 生效宽限期（默认 2m）。
func (s *BotReclaimService) gracePeriod() time.Duration {
	if s.settings == nil {
		return defaultBotReclaimGracePeriod
	}
	raw := s.settings.EffectiveValue(SettingKeyBotReclaimGracePeriod)
	if raw == "" {
		return defaultBotReclaimGracePeriod
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultBotReclaimGracePeriod
	}
	return d
}

// autoReclaim 是否自动回收（默认 true，仅对 Fleet 归属 Bot 生效）。
func (s *BotReclaimService) autoReclaim() bool {
	if s.settings == nil {
		return defaultBotReclaimAuto
	}
	raw := s.settings.EffectiveValue(SettingKeyBotReclaimAuto)
	if raw == "" {
		return defaultBotReclaimAuto
	}
	return raw == "true"
}

// Sweep 执行一轮巡检：判定失效 → 宽限状态机 → 自动处置 → 容量补足。
// 各阶段独立推进，前段失败不阻断后段（便于容量 RPC 抖动时仍能补足）。
func (s *BotReclaimService) Sweep(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	now := s.now().UTC()
	stale, err := s.detectStale(ctx, now)
	if err != nil {
		slog.Warn("失效 Bot 判定失败", "error", err)
	} else if err := s.applyGrace(ctx, stale, now); err != nil {
		slog.Warn("失效 Bot 宽限推进失败", "error", err)
	}
	if err := s.disposeConfirmed(ctx); err != nil {
		slog.Warn("失效 Bot 自动回收失败", "error", err)
	}
	if err := s.refillRunningSessions(ctx); err != nil {
		slog.Warn("Bot 容量补足失败", "error", err)
	}
	return nil
}

// detectStale 命中 §2.1 判据的失效 Bot 集合（SQL 粗筛 + 内存世代精判）。
func (s *BotReclaimService) detectStale(ctx context.Context, now time.Time) ([]botReclaimStale, error) {
	epochs := map[uint]BotReclaimNodeEpoch{}
	if s.capacities != nil {
		observed, err := s.capacities.NodeEpochs(ctx)
		if err != nil {
			return nil, fmt.Errorf("查询执行节点世代失败: %w", err)
		}
		epochs = observed
	}
	cutoff := now.Add(-botReclaimFreshnessWindow)
	var bots []model.Bot
	if err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("desired_state = ?", model.BotDesiredRunning).
		Where("status IN ?", []model.BotStatus{model.BotStatusError, model.BotStatusDisconnected, model.BotStatusConnecting}).
		Where("last_seen_at IS NULL OR last_seen_at < ?", cutoff).
		Find(&bots).Error; err != nil {
		return nil, fmt.Errorf("查询失效 Bot 候选失败: %w", err)
	}
	out := make([]botReclaimStale, 0, len(bots))
	for i := range bots {
		reason, ok := staleBotReclaimReason(&bots[i], epochs, s.capacities != nil)
		if !ok {
			continue
		}
		entry := botReclaimStale{bot: bots[i], reason: reason}
		if bots[i].ExecutorNodeID != nil {
			entry.currentEpochGeneration = epochs[*bots[i].ExecutorNodeID].WorkerEpochGeneration
		}
		out = append(out, entry)
	}
	return out, nil
}

// staleBotReclaimReason 判定单条 Bot 的世代失效原因（§2.1 条件 3）。
// epochFromCapacity=false（未装配世代来源）时只判空 epoch/空节点，保守避免误回收。
func staleBotReclaimReason(bot *model.Bot, epochs map[uint]BotReclaimNodeEpoch, epochFromCapacity bool) (string, bool) {
	if bot == nil || bot.DesiredState != model.BotDesiredRunning {
		return "", false
	}
	switch bot.Status {
	case model.BotStatusError, model.BotStatusDisconnected, model.BotStatusConnecting:
	default:
		return "", false
	}
	if bot.WorkerEpoch == "" {
		return BotReclaimStaleEmptyEpoch, true
	}
	if bot.ExecutorNodeID == nil {
		return BotReclaimStaleNodeMissing, true
	}
	if !epochFromCapacity {
		return "", false
	}
	node, ok := epochs[*bot.ExecutorNodeID]
	if !ok || !node.Exists {
		return BotReclaimStaleNodeMissing, true
	}
	if bot.WorkerEpochGeneration < node.WorkerEpochGeneration {
		return BotReclaimStaleEpochMismatch, true
	}
	return "", false
}

// applyGrace 对失效集合推进 pending/confirmed 状态机，并取消已被新世代认领的跟踪。
func (s *BotReclaimService) applyGrace(ctx context.Context, stale []botReclaimStale, now time.Time) error {
	grace := s.gracePeriod()
	seen := make(map[uint]struct{}, len(stale))
	for i := range stale {
		seen[stale[i].bot.ID] = struct{}{}
		if err := s.observeStale(ctx, &stale[i], now, grace); err != nil {
			slog.Warn("记录失效 Bot 失败", "botId", stale[i].bot.ID, "error", err)
		}
	}
	return s.cancelResolved(ctx, seen, now)
}

func (s *BotReclaimService) observeStale(ctx context.Context, stale *botReclaimStale, now time.Time, grace time.Duration) error {
	bot := &stale.bot
	var rec model.FleetBotReclaim
	err := s.db.WithContext(ctx).Where("bot_id = ?", bot.ID).Order("id DESC").First(&rec).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) ||
		rec.Status == model.FleetBotReclaimDisposed || rec.Status == model.FleetBotReclaimCancelled {
		// 终态历史存在时仍可新建一轮 pending（Bot 再次失效）。
		return s.db.WithContext(ctx).Create(&model.FleetBotReclaim{
			BotID: bot.ID, BotUUID: bot.UUID, NodeID: botReclaimNodeID(bot),
			SessionID: botReclaimSessionID(bot), BatchID: botReclaimBatchID(bot), FleetOwned: isFleetOwnedBot(bot),
			StaleReason: stale.reason, ObservedEpoch: bot.WorkerEpoch,
			ObservedEpochGeneration: bot.WorkerEpochGeneration, CurrentEpochGeneration: stale.currentEpochGeneration,
			Status: model.FleetBotReclaimPending, FirstSeenAt: now, LastSeenAt: now,
		}).Error
	}
	updates := map[string]any{
		"last_seen_at":              now,
		"stale_reason":              stale.reason,
		"observed_epoch":            bot.WorkerEpoch,
		"observed_epoch_generation": bot.WorkerEpochGeneration,
		"current_epoch_generation":  stale.currentEpochGeneration,
		"fleet_owned":               isFleetOwnedBot(bot),
	}
	if rec.Status == model.FleetBotReclaimPending && !now.Before(rec.FirstSeenAt.Add(grace)) {
		updates["status"] = model.FleetBotReclaimConfirmed
		slog.Warn("失效 Bot 宽限期已过", "botId", bot.ID, "botUuid", bot.UUID,
			"staleReason", stale.reason, "firstSeenAt", rec.FirstSeenAt, "gracePeriod", grace.String(),
			"fleetOwned", isFleetOwnedBot(bot))
	}
	return s.db.WithContext(ctx).Model(&rec).Updates(updates).Error
}

// cancelResolved 将本拍未见（世代已追平/被新世代认领）的活跃跟踪标记 cancelled（镜像 FR-326）。
func (s *BotReclaimService) cancelResolved(ctx context.Context, seen map[uint]struct{}, now time.Time) error {
	var rows []model.FleetBotReclaim
	if err := s.db.WithContext(ctx).
		Where("status IN ?", []string{string(model.FleetBotReclaimPending), string(model.FleetBotReclaimConfirmed)}).
		Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		if _, ok := seen[rows[i].BotID]; ok {
			continue
		}
		if err := s.db.WithContext(ctx).Model(&rows[i]).Updates(map[string]any{
			"status": model.FleetBotReclaimCancelled, "last_seen_at": now, "last_error": "",
		}).Error; err != nil {
			slog.Warn("取消失效 Bot 跟踪失败", "reclaimUuid", rows[i].UUID, "error", err)
		}
	}
	return nil
}

// disposeConfirmed 对已确认且 Fleet 归属的失效 Bot 下发停用（auto=true 时）。
func (s *BotReclaimService) disposeConfirmed(ctx context.Context) error {
	if !s.autoReclaim() {
		return nil
	}
	var rows []model.FleetBotReclaim
	if err := s.db.WithContext(ctx).
		Where("status = ? AND fleet_owned = ?", model.FleetBotReclaimConfirmed, true).
		Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		if err := s.disposeOne(ctx, &rows[i], "auto", 0, ""); err != nil {
			slog.Warn("自动回收失效 Bot 失败", "reclaimUuid", rows[i].UUID, "botId", rows[i].BotID, "error", err)
		}
	}
	return nil
}

func (s *BotReclaimService) disposeOne(ctx context.Context, rec *model.FleetBotReclaim, mode string, userID uint, ip string) error {
	if rec.Status == model.FleetBotReclaimDisposed {
		return nil // 幂等：已处置。
	}
	if rec.Status != model.FleetBotReclaimPending && rec.Status != model.FleetBotReclaimConfirmed {
		return ErrBotReclaimNotActive
	}
	var bot model.Bot
	if err := s.db.WithContext(ctx).First(&bot, rec.BotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Bot 已不存在（分片早已清掉）：幂等收敛为 disposed。
			return s.finishDispose(ctx, rec, mode, userID, ip, "Bot 记录已不存在，视为已回收")
		}
		return err
	}
	if !isFleetOwnedBot(&bot) {
		return ErrBotReclaimNotFleetOwned
	}
	nodeUUID := s.resolveExecutorNodeUUID(ctx, &bot)
	if s.stopper == nil || nodeUUID == "" {
		msg := "执行节点未连接，待节点回归后重试"
		s.markDisposeError(ctx, rec, msg)
		s.recordDisposeAudit(userID, ip, &bot, rec, mode, false, msg)
		return ErrBotReclaimWorkerOffline
	}
	generation := bot.DesiredStateGeneration
	if generation <= 0 {
		generation = 1
	}
	rpcCtx, cancel := context.WithTimeout(ctx, s.stopTimeout)
	defer cancel()
	skipped, err := s.stopper.StopFleetBot(rpcCtx, nodeUUID, &workerpb.BotAssignment{
		BotUuid: bot.UUID, SessionUuid: s.sessionUUIDForBot(ctx, &bot), Generation: generation,
		DesiredState: "stopped", ConfigHash: bot.ConfigHash,
	})
	if err != nil {
		s.markDisposeError(ctx, rec, err.Error())
		s.recordDisposeAudit(userID, ip, &bot, rec, mode, false, err.Error())
		return err
	}
	// 账本去账：desired_state/status → stopped、清 worker_epoch，并修正批次 connected_count。
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]any{
			"desired_state": model.BotDesiredStopped, "status": model.BotStatusStopped,
			"worker_epoch": "", "last_error": "",
		}).Error; err != nil {
			return err
		}
		if bot.LoadBatchID != nil {
			return recountBotReclaimBatch(tx, *bot.LoadBatchID)
		}
		return nil
	}); err != nil {
		return err
	}
	slog.Info("失效 Bot 已回收", "botUuid", bot.UUID, "nodeUuid", nodeUUID, "mode", mode,
		"reclaimUuid", rec.UUID, "skipped", skipped, "staleReason", rec.StaleReason)
	s.recordDisposeAudit(userID, ip, &bot, rec, mode, true, "")
	return s.finishDispose(ctx, rec, mode, userID, ip, "")
}

// finishDispose 将回收记录置 disposed（清 last_error）。
func (s *BotReclaimService) finishDispose(ctx context.Context, rec *model.FleetBotReclaim, mode string, userID uint, ip string, note string) error {
	disposedAt := s.now().UTC()
	if err := s.db.WithContext(ctx).Model(rec).Updates(map[string]any{
		"status": model.FleetBotReclaimDisposed, "disposed_at": disposedAt, "dispose_mode": mode, "last_error": "",
	}).Error; err != nil {
		return err
	}
	rec.Status = model.FleetBotReclaimDisposed
	rec.DisposeMode = mode
	rec.DisposedAt = &disposedAt
	if note != "" {
		slog.Info("失效 Bot 回收收敛", "reclaimUuid", rec.UUID, "botUuid", rec.BotUUID, "note", note)
	}
	return nil
}

func (s *BotReclaimService) markDisposeError(ctx context.Context, rec *model.FleetBotReclaim, msg string) {
	if len(msg) > 512 {
		msg = msg[:512]
	}
	_ = s.db.WithContext(ctx).Model(rec).Update("last_error", msg).Error
}

func (s *BotReclaimService) recordDisposeAudit(userID uint, ip string, bot *model.Bot, rec *model.FleetBotReclaim, mode string, success bool, errMsg string) {
	if s.audit == nil {
		return
	}
	action := "bot_reclaim.dispose_manual"
	if mode == "auto" {
		action = "bot_reclaim.dispose_auto"
	}
	botUUID := rec.BotUUID
	if bot != nil {
		botUUID = bot.UUID
	}
	detail := fmt.Sprintf(`{"botUuid":%q,"reclaimUuid":%q,"nodeId":%d,"sessionId":%d,"batchId":%d,"staleReason":%q,"mode":%q}`,
		botUUID, rec.UUID, rec.NodeID, rec.SessionID, rec.BatchID, rec.StaleReason, mode)
	s.audit.RecordResultSafe(userID, action, "bot_reclaim", rec.UUID, detail, ip, success, errMsg)
}

func (s *BotReclaimService) resolveExecutorNodeUUID(ctx context.Context, bot *model.Bot) string {
	node, _, err := s.resolver.Resolve(bot)
	if err != nil || node == nil {
		return ""
	}
	return node.UUID
}

func (s *BotReclaimService) sessionUUIDForBot(ctx context.Context, bot *model.Bot) string {
	if bot.StressSessionID != nil {
		var session model.BotStressSession
		if err := s.db.WithContext(ctx).Select("uuid").First(&session, *bot.StressSessionID).Error; err == nil {
			return session.UUID
		}
	}
	if bot.LoadBatchID != nil {
		var batch model.BotLoadBatch
		if err := s.db.WithContext(ctx).Select("stress_session_id").First(&batch, *bot.LoadBatchID).Error; err == nil {
			var session model.BotStressSession
			if err := s.db.WithContext(ctx).Select("uuid").First(&session, batch.StressSessionID).Error; err == nil {
				return session.UUID
			}
		}
	}
	return ""
}

func recountBotReclaimBatch(tx *gorm.DB, batchID uint) error {
	return tx.Model(&model.BotLoadBatch{}).Where("id = ?", batchID).Update("connected_count",
		gorm.Expr("(SELECT COUNT(*) FROM bots WHERE bots.load_batch_id = bot_load_batches.id AND bots.status = ? AND bots.deleted_at IS NULL)",
			model.BotStatusConnected)).Error
}

// refillRunningSessions 对每个 running 会话按 planned_count 目标补足缺失/被回收的 Bot。
func (s *BotReclaimService) refillRunningSessions(ctx context.Context) error {
	if s.refill == nil || s.db == nil {
		return nil
	}
	var sessions []model.BotStressSession
	if err := s.db.WithContext(ctx).
		Where("status = ? AND deleted_at IS NULL", model.BotStressSessionRunning).
		Find(&sessions).Error; err != nil {
		return fmt.Errorf("查询 running Bot 压测会话失败: %w", err)
	}
	var errs []error
	for i := range sessions {
		if botLoadStopIntentRecorded(sessions[i].LastError) {
			continue
		}
		if err := s.refillSession(ctx, &sessions[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// refillSession 把单个会话的在线 Bot 补回 Σ planned_count：重建缺失 ordinal、重新武装被回收 Bot。
func (s *BotReclaimService) refillSession(ctx context.Context, session *model.BotStressSession) error {
	plan, err := decodeStartAllocationPlan(session)
	if err != nil {
		return nil // 无服务端计划的会话（历史 V1）不补足。
	}
	config, err := parseBotLoadConnectionConfig(session.Config)
	if err != nil {
		return nil
	}
	cohortAssignments, cohortJSON, cohortBudgetMS, err := prepareBotLoadScenarioAssignments(session)
	if err != nil {
		return nil
	}
	prepared := &botLoadStartPreparation{
		session: session, plan: plan, config: config,
		cohortAssignments: cohortAssignments, cohortJSON: cohortJSON, cohortBudgetMS: cohortBudgetMS,
	}
	var batches []model.BotLoadBatch
	if err := s.db.WithContext(ctx).Where("stress_session_id = ?", session.ID).Find(&batches).Error; err != nil {
		return fmt.Errorf("查询 Bot 负载批次失败: %w", err)
	}
	if len(batches) == 0 {
		return nil
	}
	batchesByOrdinal := make(map[int]model.BotLoadBatch, len(batches))
	target := 0
	for _, batch := range batches {
		batchesByOrdinal[batch.Ordinal] = batch
		target += batch.PlannedCount
	}
	if target <= 0 {
		return nil
	}
	var actual int64
	if err := s.db.WithContext(ctx).Model(&model.Bot{}).
		Where("stress_session_id = ? AND deleted_at IS NULL AND desired_state = ? AND status = ?",
			session.ID, model.BotDesiredRunning, model.BotStatusConnected).
		Count(&actual).Error; err != nil {
		return fmt.Errorf("统计 Bot 负载在线数失败: %w", err)
	}
	if int(actual) >= target {
		return nil // 已达标：不抖动补足。
	}
	expected, err := expectedBotLoadBots(prepared, batchesByOrdinal)
	if err != nil {
		return err
	}
	// 幂等补齐计划内全部 Bot 行（OnConflict DoNothing），重建被物理删除的缺失 ordinal。
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return materializeBotLoadBots(tx, session.ID, expected)
	}); err != nil {
		return err
	}
	return s.refillDispatch(ctx, session, expected)
}

// refillDispatch 对「当前非在线/连接中」的计划内 Bot 重新下发 running assignment（补足在线数）。
//
// 选路：仅处理未在连接/已连接中的 Bot（避免与 ReconcileBotFleetSnapshot 重复派发与抖振）；被回收
// （desired=stopped）或重建（pending）的 Bot 一并恢复 desired=running 后下发，使 Worker 与 CP 同时回到
// 目标集合。
func (s *BotReclaimService) refillDispatch(ctx context.Context, session *model.BotStressSession, expected []model.Bot) error {
	var bots []model.Bot
	if err := s.db.WithContext(ctx).
		Where("stress_session_id = ? AND deleted_at IS NULL", session.ID).Find(&bots).Error; err != nil {
		return fmt.Errorf("查询补足候选 Bot 失败: %w", err)
	}
	var rearm []model.Bot
	for i := range bots {
		bot := bots[i]
		if bot.Status == model.BotStatusConnected || bot.Status == model.BotStatusConnecting {
			continue // 不重复派发：在线/连接中的不动。
		}
		rearm = append(rearm, bot)
	}
	if len(rearm) == 0 {
		return nil
	}
	// 先恢复 CP desired=running，避免 reconcile 视其为 stopped 再次停用。
	ids := make([]uint, 0, len(rearm))
	for i := range rearm {
		ids = append(ids, rearm[i].ID)
	}
	if err := s.db.WithContext(ctx).Model(&model.Bot{}).Where("id IN ?", ids).
		Update("desired_state", model.BotDesiredRunning).Error; err != nil {
		return err
	}
	dispatched, err := s.dispatchRefillAssignments(ctx, session, rearm)
	if dispatched > 0 {
		s.recordRefillAudit(session, dispatched, len(expected))
	}
	return err
}

func (s *BotReclaimService) dispatchRefillAssignments(ctx context.Context, session *model.BotStressSession, bots []model.Bot) (int, error) {
	groups := make(map[string][]botLoadReconcileItem)
	for i := range bots {
		bot := bots[i]
		nodeUUID := s.resolveExecutorNodeUUID(ctx, &bot)
		if nodeUUID == "" {
			continue
		}
		assignment, err := s.refill.rebuildRunningAssignment(ctx, session, &bot)
		if err != nil || assignment == nil {
			continue
		}
		groups[nodeUUID] = append(groups[nodeUUID], botLoadReconcileItem{
			assignment: assignment, bot: &bot, mode: botLoadReconcileRunning,
		})
	}
	var errs []error
	dispatched := 0
	for nodeUUID, items := range groups {
		for start := 0; start < len(items); start += maxBotLoadBatchSize {
			end := min(start+maxBotLoadBatchSize, len(items))
			chunk := items[start:end]
			request := buildBotLoadReconcileRequest(session.UUID, 0, chunk)
			response, rpcErr := s.refill.applyBotLoadBatch(ctx, nodeUUID, request)
			if err := s.refill.persistBotLoadReconcileResult(ctx, chunk, request, response, rpcErr); err != nil {
				errs = append(errs, err)
				continue
			}
			dispatched += len(chunk)
		}
	}
	return dispatched, errors.Join(errs...)
}

func (s *BotReclaimService) recordRefillAudit(session *model.BotStressSession, refilled, target int) {
	if s.audit == nil || session == nil {
		return
	}
	detail := fmt.Sprintf(`{"sessionUuid":%q,"sessionId":%d,"refilled":%d,"target":%d}`,
		session.UUID, session.ID, refilled, target)
	s.audit.RecordSafe(0, "bot_reclaim.refill", "bot_stress_session", session.UUID, detail, "")
}

// List 列出失效 Bot 回收记录。status 空=全部；activeOnly 时仅 pending+confirmed。
func (s *BotReclaimService) List(status string, activeOnly bool, limit int) ([]model.FleetBotReclaim, error) {
	if limit <= 0 {
		limit = 100
	}
	q := s.db.Model(&model.FleetBotReclaim{}).Order("first_seen_at DESC").Limit(limit)
	if status != "" {
		q = q.Where("status = ?", status)
	} else if activeOnly {
		q = q.Where("status IN ?", []string{
			string(model.FleetBotReclaimPending), string(model.FleetBotReclaimConfirmed),
		})
	}
	var items []model.FleetBotReclaim
	if err := q.Find(&items).Error; err != nil {
		return nil, fmt.Errorf("查询失效 Bot 列表失败: %w", err)
	}
	if items == nil {
		items = []model.FleetBotReclaim{}
	}
	return items, nil
}

// Get 按 UUID 取单条。
func (s *BotReclaimService) Get(reclaimUUID string) (*model.FleetBotReclaim, error) {
	var rec model.FleetBotReclaim
	if err := s.db.Where("uuid = ?", reclaimUUID).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotReclaimNotFound
		}
		return nil, fmt.Errorf("查询失效 Bot 记录失败: %w", err)
	}
	return &rec, nil
}

// ConfirmReclaim 管理员手动确认回收（auto 关闭或 V1 场景的主路径）。
func (s *BotReclaimService) ConfirmReclaim(reclaimUUID string, userID uint, ip string) (*model.FleetBotReclaim, error) {
	rec, err := s.Get(reclaimUUID)
	if err != nil {
		return nil, err
	}
	if rec.Status != model.FleetBotReclaimPending && rec.Status != model.FleetBotReclaimConfirmed {
		return nil, ErrBotReclaimNotActive
	}
	if err := s.disposeOne(context.Background(), rec, "manual", userID, ip); err != nil {
		return rec, err
	}
	return s.Get(reclaimUUID)
}

func botReclaimNodeID(bot *model.Bot) uint {
	if bot.ExecutorNodeID == nil {
		return 0
	}
	return *bot.ExecutorNodeID
}

func botReclaimSessionID(bot *model.Bot) uint {
	if bot.StressSessionID == nil {
		return 0
	}
	return *bot.StressSessionID
}

func botReclaimBatchID(bot *model.Bot) uint {
	if bot.LoadBatchID == nil {
		return 0
	}
	return *bot.LoadBatchID
}

// BotReclaimSweeper 周期性巡检失效 Bot 并自动回收/补足；Stop 后释放 goroutine。
type BotReclaimSweeper struct {
	service  *BotReclaimService
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewBotReclaimSweeper 创建默认 60 秒周期的失效 Bot 巡检器。
func NewBotReclaimSweeper(service *BotReclaimService) *BotReclaimSweeper {
	return &BotReclaimSweeper{service: service, interval: botReclaimSweepInterval}
}

// Start 启动后台巡检；重复 Start 幂等。
func (s *BotReclaimSweeper) Start() {
	if s == nil || s.service == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	interval := s.interval
	if interval <= 0 {
		interval = botReclaimSweepInterval
	}
	go s.loop(ctx, interval, done)
}

func (s *BotReclaimSweeper) loop(ctx context.Context, interval time.Duration, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.service.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("失效 Bot 巡检失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Stop 取消巡检并等待 goroutine 退出。
func (s *BotReclaimSweeper) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
