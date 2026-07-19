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

const botFleetSnapshotDedupeLimit = 256

// BotFleetRuntimeDecision 表示一条 Fleet runtime 消息的结构化处理结果。
type BotFleetRuntimeDecision string

const (
	BotFleetRuntimeApplied                  BotFleetRuntimeDecision = "applied"
	BotFleetRuntimeSnapshotRequired         BotFleetRuntimeDecision = "snapshot_required"
	BotFleetRuntimeIgnoredBotMissing        BotFleetRuntimeDecision = "ignored_bot_missing"
	BotFleetRuntimeIgnoredExecutorMismatch  BotFleetRuntimeDecision = "ignored_executor_mismatch"
	BotFleetRuntimeIgnoredSessionMismatch   BotFleetRuntimeDecision = "ignored_session_mismatch"
	BotFleetRuntimeIgnoredStaleGeneration   BotFleetRuntimeDecision = "ignored_stale_generation"
	BotFleetRuntimeIgnoredConfigMismatch    BotFleetRuntimeDecision = "ignored_config_mismatch"
	BotFleetRuntimeIgnoredStaleEpoch        BotFleetRuntimeDecision = "ignored_stale_epoch"
	BotFleetRuntimeIgnoredDuplicateEvent    BotFleetRuntimeDecision = "ignored_duplicate_event"
	BotFleetRuntimeIgnoredConcurrentUpdate  BotFleetRuntimeDecision = "ignored_concurrent_update"
	BotFleetRuntimeIgnoredInvalidStatus     BotFleetRuntimeDecision = "ignored_invalid_status"
	BotFleetRuntimeActionApplied            BotFleetRuntimeDecision = "action_applied"
	BotFleetRuntimeIgnoredActionEvent       BotFleetRuntimeDecision = "ignored_action_event"
	BotFleetRuntimeIgnoredStaleSubscription BotFleetRuntimeDecision = "ignored_stale_subscription"
)

// BotFleetRuntimeResult 包含决策、诊断和是否需要拉取完整快照。
type BotFleetRuntimeResult struct {
	Decision         BotFleetRuntimeDecision
	BotUUID          string
	Diagnostic       string
	SnapshotRequired bool
}

// BotFleetRuntimeRepository 封装 Bot runtime 身份读取、baseline 收敛与原子账本更新。
type BotFleetRuntimeRepository interface {
	FindBotRuntime(ctx context.Context, botUUID string) (*model.Bot, error)
	ApplyBotRuntime(ctx context.Context, bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, status model.BotStatus, observedAt time.Time, baseline bool) (bool, error)
	ConvergeMissingRuntime(ctx context.Context, executorNodeID uint, sessionUUID string, presentBotUUIDs []string, observedAt time.Time) error
}

type gormBotFleetRuntimeRepository struct{ db *gorm.DB }

func newGormBotFleetRuntimeRepository(db *gorm.DB) *gormBotFleetRuntimeRepository {
	return &gormBotFleetRuntimeRepository{db: db}
}

func (r *gormBotFleetRuntimeRepository) FindBotRuntime(ctx context.Context, botUUID string) (*model.Bot, error) {
	var bot model.Bot
	err := r.db.WithContext(ctx).Preload("Instance").Preload("StressSession").Where("uuid = ?", botUUID).First(&bot).Error
	return &bot, err
}

func (r *gormBotFleetRuntimeRepository) ApplyBotRuntime(ctx context.Context, bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, status model.BotStatus, observedAt time.Time, baseline bool) (bool, error) {
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		changed, err := r.applyRuntimeTransition(tx, bot, executorNodeID, snapshot, status, observedAt, baseline)
		if err != nil || !changed {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("更新 Bot Fleet runtime 失败: %w", err)
	}
	return applied, nil
}

func (r *gormBotFleetRuntimeRepository) applyRuntimeTransition(tx *gorm.DB, bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, status model.BotStatus, observedAt time.Time, baseline bool) (bool, error) {
	updates := runtimeUpdateValues(snapshot, status, observedAt)
	if status == model.BotStatusConnected {
		changed, err := r.updateRuntimeWhereStatus(tx, bot, executorNodeID, snapshot, updates, baseline, "status <> ?", model.BotStatusConnected)
		if err != nil || changed {
			return changed, r.adjustConnectedCount(tx, bot.LoadBatchID, 1, err)
		}
		return r.updateRuntimeWhereStatus(tx, bot, executorNodeID, snapshot, updates, baseline, "status = ?", model.BotStatusConnected)
	}
	changed, err := r.updateRuntimeWhereStatus(tx, bot, executorNodeID, snapshot, updates, baseline, "status = ?", model.BotStatusConnected)
	if err != nil || changed {
		return changed, r.adjustConnectedCount(tx, bot.LoadBatchID, -1, err)
	}
	return r.updateRuntimeWhereStatus(tx, bot, executorNodeID, snapshot, updates, baseline, "status <> ?", model.BotStatusConnected)
}

func (r *gormBotFleetRuntimeRepository) updateRuntimeWhereStatus(tx *gorm.DB, bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, updates map[string]any, baseline bool, statusQuery string, status model.BotStatus) (bool, error) {
	result := r.runtimeUpdateQuery(tx, bot, executorNodeID, snapshot, baseline).Where(statusQuery, status).Updates(updates)
	return result.RowsAffected == 1, result.Error
}

func (r *gormBotFleetRuntimeRepository) adjustConnectedCount(tx *gorm.DB, batchID *uint, delta int, prior error) error {
	if prior != nil || batchID == nil || delta == 0 {
		return prior
	}
	expression := gorm.Expr("connected_count + 1")
	if delta < 0 {
		expression = gorm.Expr("CASE WHEN connected_count > 0 THEN connected_count - 1 ELSE 0 END")
	}
	return tx.Model(&model.BotLoadBatch{}).Where("id = ?", *batchID).Update("connected_count", expression).Error
}

func (r *gormBotFleetRuntimeRepository) runtimeUpdateQuery(tx *gorm.DB, bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, baseline bool) *gorm.DB {
	query := tx.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Where("desired_state_generation = ?", snapshot.Generation).
		Where("config_hash = ?", snapshot.ConfigHash)
	if !baseline {
		query = query.Where("(worker_epoch_generation < ?) OR (worker_epoch_generation = ? AND last_event_seq < ?)", snapshot.WorkerEpochGeneration, snapshot.WorkerEpochGeneration, snapshot.EventSeq)
	}
	query = runtimeSessionCondition(query, bot)
	return runtimeExecutorCondition(query, bot, executorNodeID)
}

func (r *gormBotFleetRuntimeRepository) ConvergeMissingRuntime(ctx context.Context, executorNodeID uint, sessionUUID string, presentBotUUIDs []string, observedAt time.Time) error {
	var session model.BotStressSession
	if err := r.db.WithContext(ctx).Where("uuid = ?", sessionUUID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	stopping := session.Status == model.BotStressSessionStopped || botLoadStopIntentRecorded(session.LastError)
	targetStatus := model.BotStatusDisconnected
	eligibleStatuses := []model.BotStatus{model.BotStatusConnecting, model.BotStatusConnected}
	if stopping {
		targetStatus = model.BotStatusStopped
		eligibleStatuses = []model.BotStatus{
			model.BotStatusPending, model.BotStatusConnecting, model.BotStatusConnected,
			model.BotStatusDisconnected, model.BotStatusError,
		}
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.convergeMissingRuntime(tx, executorNodeID, session.ID, presentBotUUIDs, eligibleStatuses, targetStatus, observedAt); err != nil {
			return err
		}
		return r.recountConnectedRuntime(tx, executorNodeID, session.ID)
	})
}

func (r *gormBotFleetRuntimeRepository) convergeMissingRuntime(tx *gorm.DB, executorNodeID, sessionID uint, presentBotUUIDs []string, eligibleStatuses []model.BotStatus, targetStatus model.BotStatus, observedAt time.Time) error {
	query := tx.Model(&model.Bot{}).Where("stress_session_id = ? AND status IN ?", sessionID, eligibleStatuses)
	query = runtimeExecutorScope(query, executorNodeID)
	if len(presentBotUUIDs) > 0 {
		query = query.Where("uuid NOT IN ?", presentBotUUIDs)
	}
	return query.Updates(map[string]any{
		"status":       targetStatus,
		"last_seen_at": gorm.Expr("CASE WHEN last_seen_at IS NULL OR last_seen_at < ? THEN ? ELSE last_seen_at END", observedAt, observedAt),
	}).Error
}

func (r *gormBotFleetRuntimeRepository) recountConnectedRuntime(tx *gorm.DB, executorNodeID, sessionID uint) error {
	batchIDs := tx.Model(&model.Bot{}).Select("DISTINCT load_batch_id").
		Where("stress_session_id = ? AND load_batch_id IS NOT NULL", sessionID)
	batchIDs = runtimeExecutorScope(batchIDs, executorNodeID)
	return tx.Model(&model.BotLoadBatch{}).Where("id IN (?)", batchIDs).Update(
		"connected_count",
		gorm.Expr("(SELECT COUNT(*) FROM bots WHERE bots.load_batch_id = bot_load_batches.id AND bots.status = ?)", model.BotStatusConnected),
	).Error
}

func runtimeSessionCondition(query *gorm.DB, bot *model.Bot) *gorm.DB {
	if bot.StressSessionID == nil {
		return query.Where("stress_session_id IS NULL")
	}
	return query.Where("stress_session_id = ?", *bot.StressSessionID)
}

func runtimeExecutorCondition(query *gorm.DB, bot *model.Bot, executorNodeID uint) *gorm.DB {
	if bot.ExecutorNodeID != nil {
		return query.Where("executor_node_id = ?", executorNodeID)
	}
	return query.Where("executor_node_id IS NULL").Where("instance_id IN (?)", query.Session(&gorm.Session{NewDB: true}).Model(&model.Instance{}).Select("id").Where("node_id = ?", executorNodeID))
}

func runtimeExecutorScope(query *gorm.DB, executorNodeID uint) *gorm.DB {
	subquery := query.Session(&gorm.Session{NewDB: true}).Model(&model.Instance{}).Select("id").Where("node_id = ?", executorNodeID)
	return query.Where("executor_node_id = ? OR (executor_node_id IS NULL AND instance_id IN (?))", executorNodeID, subquery)
}

func runtimeUpdateValues(snapshot *workerpb.BotRuntimeSnapshot, status model.BotStatus, observedAt time.Time) map[string]any {
	updates := map[string]any{
		"status": status, "last_error": snapshot.LastError,
		"worker_epoch": snapshot.WorkerEpoch, "worker_epoch_generation": snapshot.WorkerEpochGeneration,
		"last_event_seq": snapshot.EventSeq,
		"last_seen_at": gorm.Expr(
			"CASE WHEN last_seen_at IS NULL OR last_seen_at < ? THEN ? ELSE last_seen_at END",
			observedAt, observedAt,
		),
	}
	if status == model.BotStatusConnected {
		updates["connected_at"] = gorm.Expr("COALESCE(connected_at, ?)", observedAt)
	}
	return updates
}

// BotFleetRuntimeService 按 desired generation、子进程世代和事件序号归真 Bot runtime。
type BotFleetRuntimeService struct {
	repository BotFleetRuntimeRepository
	clock      BotLoadClock
}

// NewBotFleetRuntimeService 使用 GORM 仓储创建 Fleet runtime 归真服务。
func NewBotFleetRuntimeService(db *gorm.DB, clock BotLoadClock) *BotFleetRuntimeService {
	return NewBotFleetRuntimeServiceWithRepository(newGormBotFleetRuntimeRepository(db), clock)
}

// NewBotFleetRuntimeServiceWithRepository 使用可注入仓储创建归真服务。
func NewBotFleetRuntimeServiceWithRepository(repository BotFleetRuntimeRepository, clock BotLoadClock) *BotFleetRuntimeService {
	return &BotFleetRuntimeService{repository: repository, clock: normalizeBotLoadClock(clock)}
}

// Ingest 校验来源与三层世代后，以数据库条件更新保证普通事件不会倒退。
func (s *BotFleetRuntimeService) Ingest(ctx context.Context, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot) (BotFleetRuntimeResult, error) {
	return s.ingest(ctx, executorNodeID, snapshot, false)
}

// IngestBaseline 接受当前订阅的完整快照，允许 Worker 重启后重置本地 epoch 基线。
func (s *BotFleetRuntimeService) IngestBaseline(ctx context.Context, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot) (BotFleetRuntimeResult, error) {
	return s.ingest(ctx, executorNodeID, snapshot, true)
}

func (s *BotFleetRuntimeService) ingest(ctx context.Context, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, baseline bool) (BotFleetRuntimeResult, error) {
	if snapshot == nil {
		return runtimeResult(BotFleetRuntimeIgnoredBotMissing, "", "runtime snapshot 为空"), nil
	}
	bot, err := s.repository.FindBotRuntime(ctx, snapshot.BotUuid)
	if err != nil {
		return s.findErrorResult(snapshot.BotUuid, err)
	}
	if result, accepted := classifyRuntimeSnapshot(bot, executorNodeID, snapshot, baseline); !accepted {
		return result, nil
	}
	status, ok := runtimeStatus(snapshot.Status)
	if !ok {
		return runtimeResult(BotFleetRuntimeIgnoredInvalidStatus, snapshot.BotUuid, "runtime status 不在冻结枚举内"), nil
	}
	applied, err := s.repository.ApplyBotRuntime(ctx, bot, executorNodeID, snapshot, status, s.observedAt(snapshot), baseline)
	if err != nil || applied {
		return appliedRuntimeResult(snapshot.BotUuid, applied), err
	}
	return s.classifyConcurrentResult(ctx, executorNodeID, snapshot, baseline)
}

// ConvergeMissingBaseline 将完整快照中缺失的活动 Bot 收敛为 disconnected；停止意图下收敛为 stopped。
func (s *BotFleetRuntimeService) ConvergeMissingBaseline(ctx context.Context, executorNodeID uint, sessionUUID string, presentBotUUIDs []string, observedAtUnixMs int64) error {
	observedAt := s.clock.Now().UTC()
	if observedAtUnixMs > 0 {
		observedAt = time.UnixMilli(observedAtUnixMs).UTC()
	}
	return s.repository.ConvergeMissingRuntime(ctx, executorNodeID, sessionUUID, presentBotUUIDs, observedAt)
}

func (s *BotFleetRuntimeService) findErrorResult(botUUID string, err error) (BotFleetRuntimeResult, error) {
	if err == gorm.ErrRecordNotFound {
		return runtimeResult(BotFleetRuntimeIgnoredBotMissing, botUUID, "Bot UUID 不存在"), nil
	}
	return BotFleetRuntimeResult{}, fmt.Errorf("查询 Bot Fleet runtime 失败: %w", err)
}

func (s *BotFleetRuntimeService) observedAt(snapshot *workerpb.BotRuntimeSnapshot) time.Time {
	if snapshot.ObservedAtUnixMs > 0 {
		return time.UnixMilli(snapshot.ObservedAtUnixMs).UTC()
	}
	return s.clock.Now().UTC()
}

func (s *BotFleetRuntimeService) classifyConcurrentResult(ctx context.Context, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, baseline bool) (BotFleetRuntimeResult, error) {
	latest, err := s.repository.FindBotRuntime(ctx, snapshot.BotUuid)
	if err != nil {
		return s.findErrorResult(snapshot.BotUuid, err)
	}
	if result, accepted := classifyRuntimeSnapshot(latest, executorNodeID, snapshot, baseline); !accepted {
		return result, nil
	}
	return runtimeResult(BotFleetRuntimeIgnoredConcurrentUpdate, snapshot.BotUuid, "数据库条件更新未命中，消息已被并发状态取代"), nil
}

func classifyRuntimeSnapshot(bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, baseline bool) (BotFleetRuntimeResult, bool) {
	if runtimeExecutorNodeID(bot) != executorNodeID {
		return runtimeResult(BotFleetRuntimeIgnoredExecutorMismatch, snapshot.BotUuid, "runtime 来源执行节点与 Bot 路由真源不匹配"), false
	}
	if runtimeSessionUUID(bot) != snapshot.SessionUuid {
		return runtimeResult(BotFleetRuntimeIgnoredSessionMismatch, snapshot.BotUuid, "runtime session 与 Bot desired session 不匹配"), false
	}
	if snapshot.Generation < bot.DesiredStateGeneration {
		return runtimeResult(BotFleetRuntimeIgnoredStaleGeneration, snapshot.BotUuid, "runtime desired generation 已过期"), false
	}
	if snapshot.Generation > bot.DesiredStateGeneration {
		return snapshotRuntimeResult(BotFleetRuntimeSnapshotRequired, snapshot.BotUuid, "runtime desired generation 超前，需要完整快照协调"), false
	}
	if bot.ConfigHash != snapshot.ConfigHash {
		return snapshotRuntimeResult(BotFleetRuntimeIgnoredConfigMismatch, snapshot.BotUuid, "runtime configHash 与 desired 配置不匹配"), false
	}
	if baseline {
		return BotFleetRuntimeResult{}, true
	}
	return classifyRuntimeEpoch(bot, snapshot)
}

func classifyRuntimeEpoch(bot *model.Bot, snapshot *workerpb.BotRuntimeSnapshot) (BotFleetRuntimeResult, bool) {
	if snapshot.WorkerEpochGeneration < bot.WorkerEpochGeneration {
		return runtimeResult(BotFleetRuntimeIgnoredStaleEpoch, snapshot.BotUuid, "Bot Worker 子进程世代已过期"), false
	}
	if snapshot.WorkerEpochGeneration == bot.WorkerEpochGeneration && snapshot.EventSeq <= bot.LastEventSeq {
		return runtimeResult(BotFleetRuntimeIgnoredDuplicateEvent, snapshot.BotUuid, "eventSeq 重复或乱序"), false
	}
	return BotFleetRuntimeResult{}, true
}

func runtimeExecutorNodeID(bot *model.Bot) uint {
	if bot.ExecutorNodeID != nil {
		return *bot.ExecutorNodeID
	}
	return bot.Instance.NodeID
}

func runtimeSessionUUID(bot *model.Bot) string {
	if bot.StressSessionID == nil {
		return ""
	}
	return bot.StressSession.UUID
}

func runtimeStatus(status string) (model.BotStatus, bool) {
	switch model.BotStatus(strings.ToLower(strings.TrimSpace(status))) {
	case model.BotStatusPending, model.BotStatusConnecting, model.BotStatusConnected,
		model.BotStatusDisconnected, model.BotStatusError, model.BotStatusStopped:
		return model.BotStatus(strings.ToLower(strings.TrimSpace(status))), true
	default:
		return "", false
	}
}

func runtimeResult(decision BotFleetRuntimeDecision, botUUID, diagnostic string) BotFleetRuntimeResult {
	return BotFleetRuntimeResult{Decision: decision, BotUUID: botUUID, Diagnostic: diagnostic}
}

func snapshotRuntimeResult(decision BotFleetRuntimeDecision, botUUID, diagnostic string) BotFleetRuntimeResult {
	result := runtimeResult(decision, botUUID, diagnostic)
	result.SnapshotRequired = true
	return result
}

func appliedRuntimeResult(botUUID string, applied bool) BotFleetRuntimeResult {
	if applied {
		return runtimeResult(BotFleetRuntimeApplied, botUUID, "runtime 已按原子世代条件更新")
	}
	return BotFleetRuntimeResult{}
}

// BotFleetRuntimeStream 是 Fleet 流消费所需的最小接口。
type BotFleetRuntimeStream interface {
	Recv() (*workerpb.BotFleetEvent, error)
}

// BotFleetRuntimeClient 隔离连接池与生成的 gRPC client，便于验证快照/重连顺序。
type BotFleetRuntimeClient interface {
	GetBotFleetSnapshot(ctx context.Context, nodeUUID, sessionUUID string) (*workerpb.GetBotFleetSnapshotResponse, error)
	StreamBotFleetEvents(ctx context.Context, nodeUUID, sessionUUID string) (BotFleetRuntimeStream, error)
}

// BotFleetCapacityGenerationSink 接收 FleetSnapshot 的容量世代，不在 Bot 表另建持久化字段。
type BotFleetCapacityGenerationSink interface {
	ObserveBotFleetCapacityGeneration(nodeID uint, generation int64)
}

// BotFleetSnapshotReconciler 把完整快照交给 dispatch/reconcile 层处理 desired 差异。
// 实现方只能依据 Control Plane desired 真源重放 assignment，不能用 runtime 覆盖 desired 字段。
type BotFleetSnapshotReconciler interface {
	ReconcileBotFleetSnapshot(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, snapshot *workerpb.GetBotFleetSnapshotResponse) error
}

// BotFleetRuntimeObserver 在可信 runtime 更新后收束批次、会话和订阅生命周期。
type BotFleetRuntimeObserver interface {
	ReconcileBotFleetRuntimeState(ctx context.Context, sessionUUID string) error
}

// BotFleetActionEventHandler 消费类型化动作事件，不让动作载荷污染 Bot runtime 账本。
type BotFleetActionEventHandler interface {
	Ingest(ctx context.Context, executorNodeID uint, expectedSessionUUID string, event *workerpb.BotActionEvent) (ActionResultIngestResult, error)
}

type poolBotFleetRuntimeClient struct{ pool *cpgrpc.ClientPool }

func (c poolBotFleetRuntimeClient) GetBotFleetSnapshot(ctx context.Context, nodeUUID, sessionUUID string) (*workerpb.GetBotFleetSnapshotResponse, error) {
	client, err := c.worker(nodeUUID)
	if err != nil {
		return nil, err
	}
	return client.GetBotFleetSnapshot(ctx, &workerpb.GetBotFleetSnapshotRequest{SessionUuid: sessionUUID})
}

func (c poolBotFleetRuntimeClient) StreamBotFleetEvents(ctx context.Context, nodeUUID, sessionUUID string) (BotFleetRuntimeStream, error) {
	client, err := c.worker(nodeUUID)
	if err != nil {
		return nil, err
	}
	return client.StreamBotFleetEvents(ctx, &workerpb.StreamBotFleetEventsRequest{SessionUuid: sessionUUID})
}

func (c poolBotFleetRuntimeClient) worker(nodeUUID string) (workerpb.WorkerServiceClient, error) {
	if c.pool == nil {
		return nil, errBotLoadWorkerMissing
	}
	client, ok := c.pool.Get(nodeUUID)
	if !ok || client.Worker == nil {
		return nil, errBotLoadWorkerMissing
	}
	return client.Worker, nil
}

type botFleetSnapshotCall struct {
	done chan struct{}
	err  error
}

// BotFleetRuntimeCoordinator 协调 runtime 流、完整快照和有界去重，不启动全局后台守护。
type BotFleetRuntimeCoordinator struct {
	ingester      *BotFleetRuntimeService
	client        BotFleetRuntimeClient
	capacitySink  BotFleetCapacityGenerationSink
	reconciler    BotFleetSnapshotReconciler
	observer      BotFleetRuntimeObserver
	actionHandler BotFleetActionEventHandler

	mu            sync.Mutex
	snapshotCalls map[string]*botFleetSnapshotCall
	snapshotOrder []string
}

// NewBotFleetRuntimeCoordinator 创建可注入 Worker client 的 Fleet 协调器。
func NewBotFleetRuntimeCoordinator(ingester *BotFleetRuntimeService, client BotFleetRuntimeClient, capacitySink BotFleetCapacityGenerationSink) *BotFleetRuntimeCoordinator {
	return &BotFleetRuntimeCoordinator{
		ingester: ingester, client: client, capacitySink: capacitySink,
		snapshotCalls: make(map[string]*botFleetSnapshotCall),
	}
}

// SetSnapshotReconciler 注入 dispatch/reconcile 层，供完整快照触发 desired 收敛。
func (c *BotFleetRuntimeCoordinator) SetSnapshotReconciler(reconciler BotFleetSnapshotReconciler) {
	c.reconciler = reconciler
}

// SetRuntimeObserver 注入批次、会话与订阅生命周期收束器。
func (c *BotFleetRuntimeCoordinator) SetRuntimeObserver(observer BotFleetRuntimeObserver) {
	c.observer = observer
}

// SetActionEventHandler 注入动作结果账本与后续场景事件协调入口。
func (c *BotFleetRuntimeCoordinator) SetActionEventHandler(handler BotFleetActionEventHandler) {
	c.actionHandler = handler
}

// ActionEventHandler 返回当前装配的动作事件处理器。
func (c *BotFleetRuntimeCoordinator) ActionEventHandler() BotFleetActionEventHandler {
	if c == nil {
		return nil
	}
	return c.actionHandler
}

// SnapshotReconciler 返回当前装配的 desired 协调器。
func (c *BotFleetRuntimeCoordinator) SnapshotReconciler() BotFleetSnapshotReconciler {
	if c == nil {
		return nil
	}
	return c.reconciler
}

// RuntimeObserver 返回当前装配的运行态收束器。
func (c *BotFleetRuntimeCoordinator) RuntimeObserver() BotFleetRuntimeObserver {
	if c == nil {
		return nil
	}
	return c.observer
}

// NewGRPCBotFleetRuntimeCoordinator 使用既有隧道优先连接池装配协调器。
func NewGRPCBotFleetRuntimeCoordinator(db *gorm.DB, pool *cpgrpc.ClientPool, capacitySink BotFleetCapacityGenerationSink, clock BotLoadClock) *BotFleetRuntimeCoordinator {
	ingester := NewBotFleetRuntimeService(db, clock)
	return NewBotFleetRuntimeCoordinator(ingester, poolBotFleetRuntimeClient{pool: pool}, capacitySink)
}

// HandleEvent 将 runtime_snapshot 与 action_event 分流到各自账本。
func (c *BotFleetRuntimeCoordinator) HandleEvent(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, event *workerpb.BotFleetEvent) (BotFleetRuntimeResult, error) {
	if event != nil && event.GetActionEvent() != nil {
		return c.handleActionEvent(ctx, nodeID, sessionUUID, event.GetActionEvent())
	}
	result, observed, err := c.handleEvent(ctx, nodeID, nodeUUID, sessionUUID, event, false)
	if err != nil || !observed {
		return result, err
	}
	return result, c.observeRuntimeState(ctx, sessionUUID)
}

func (c *BotFleetRuntimeCoordinator) handleActionEvent(ctx context.Context, nodeID uint, sessionUUID string, event *workerpb.BotActionEvent) (BotFleetRuntimeResult, error) {
	if c.actionHandler == nil {
		return ignoredFleetEventResult(&workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: event}}), nil
	}
	result, err := c.actionHandler.Ingest(ctx, nodeID, sessionUUID, event)
	if err != nil {
		return BotFleetRuntimeResult{}, err
	}
	if result.Decision == ActionResultApplied {
		return runtimeResult(BotFleetRuntimeActionApplied, result.BotUUID, result.Diagnostic), nil
	}
	return runtimeResult(BotFleetRuntimeIgnoredActionEvent, result.BotUUID, result.Diagnostic), nil
}

func (c *BotFleetRuntimeCoordinator) handleEvent(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, event *workerpb.BotFleetEvent, observeSnapshot bool) (BotFleetRuntimeResult, bool, error) {
	if event == nil || event.GetRuntimeSnapshot() == nil {
		return ignoredFleetEventResult(event), false, nil
	}
	result, err := c.ingester.Ingest(ctx, nodeID, event.GetRuntimeSnapshot())
	if err != nil {
		return result, false, err
	}
	observed := result.Decision == BotFleetRuntimeApplied
	if !result.SnapshotRequired {
		return result, observed, nil
	}
	generation := event.GetRuntimeSnapshot().Generation
	if err := c.requestSnapshotOnce(ctx, nodeID, nodeUUID, sessionUUID, generation, observeSnapshot); err != nil {
		return result, false, err
	}
	return result, true, nil
}

func ignoredFleetEventResult(event *workerpb.BotFleetEvent) BotFleetRuntimeResult {
	botUUID := ""
	if event != nil && event.GetActionEvent() != nil {
		botUUID = event.GetActionEvent().BotUuid
	}
	return runtimeResult(BotFleetRuntimeIgnoredActionEvent, botUUID, "action_event 已安全转交后续 FR，不更新 Bot runtime")
}

// RefreshSnapshot 拉取完整 baseline，先归真 runtime 与缺失项，再处理 desired 差异。
func (c *BotFleetRuntimeCoordinator) RefreshSnapshot(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string) error {
	snapshot, err := c.fetchSnapshot(ctx, nodeUUID, sessionUUID)
	if err != nil {
		return err
	}
	if err := c.applySnapshotState(ctx, nodeID, nodeUUID, sessionUUID, snapshot); err != nil {
		return err
	}
	return c.observeRuntimeState(ctx, sessionUUID)
}

func (c *BotFleetRuntimeCoordinator) fetchSnapshot(ctx context.Context, nodeUUID, sessionUUID string) (*workerpb.GetBotFleetSnapshotResponse, error) {
	if c.client == nil {
		return nil, fmt.Errorf("Bot Fleet runtime client 未装配")
	}
	snapshot, err := c.client.GetBotFleetSnapshot(ctx, nodeUUID, sessionUUID)
	if err != nil {
		return nil, fmt.Errorf("拉取 Bot FleetSnapshot 失败: %w", err)
	}
	if snapshot == nil {
		snapshot = &workerpb.GetBotFleetSnapshotResponse{}
	}
	return snapshot, nil
}

func (c *BotFleetRuntimeCoordinator) applySnapshotState(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, snapshot *workerpb.GetBotFleetSnapshotResponse) error {
	if err := c.applySnapshot(ctx, nodeID, sessionUUID, snapshot); err != nil {
		return err
	}
	if c.reconciler == nil {
		return nil
	}
	if err := c.reconciler.ReconcileBotFleetSnapshot(ctx, nodeID, nodeUUID, sessionUUID, snapshot); err != nil {
		return fmt.Errorf("协调 Bot FleetSnapshot desired 差异失败: %w", err)
	}
	return nil
}

func (c *BotFleetRuntimeCoordinator) observeRuntimeState(ctx context.Context, sessionUUID string) error {
	if c.observer == nil {
		return nil
	}
	if err := c.observer.ReconcileBotFleetRuntimeState(ctx, sessionUUID); err != nil {
		return fmt.Errorf("收束 Bot Fleet runtime 状态失败: %w", err)
	}
	return nil
}

func (c *BotFleetRuntimeCoordinator) applySnapshot(ctx context.Context, nodeID uint, sessionUUID string, snapshot *workerpb.GetBotFleetSnapshotResponse) error {
	if snapshot == nil {
		snapshot = &workerpb.GetBotFleetSnapshotResponse{}
	}
	if c.capacitySink != nil {
		c.capacitySink.ObserveBotFleetCapacityGeneration(nodeID, snapshot.CapacityGeneration)
	}
	present := make([]string, 0, len(snapshot.Bots))
	for _, bot := range snapshot.Bots {
		if bot == nil {
			continue
		}
		present = append(present, bot.BotUuid)
		if _, err := c.ingester.IngestBaseline(ctx, nodeID, bot); err != nil {
			return err
		}
	}
	if err := c.ingester.ConvergeMissingBaseline(ctx, nodeID, sessionUUID, present, snapshot.ObservedAtUnixMs); err != nil {
		return fmt.Errorf("收敛 Bot FleetSnapshot 缺失 runtime 失败: %w", err)
	}
	return nil
}

// OpenStream 按活动 session 与执行节点建立一条 Fleet 类型化流。
func (c *BotFleetRuntimeCoordinator) OpenStream(ctx context.Context, nodeUUID, sessionUUID string) (BotFleetRuntimeStream, error) {
	if c.client == nil {
		return nil, fmt.Errorf("Bot Fleet runtime client 未装配")
	}
	return c.client.StreamBotFleetEvents(ctx, nodeUUID, sessionUUID)
}

// ConsumeUntilDisconnectAndReconnect 消费到断流后先建快照基线，再建立新流。
func (c *BotFleetRuntimeCoordinator) ConsumeUntilDisconnectAndReconnect(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, stream BotFleetRuntimeStream) (BotFleetRuntimeStream, error) {
	disconnected, err := c.consumeUntilDisconnect(ctx, nodeID, nodeUUID, sessionUUID, stream)
	if !disconnected {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := c.RefreshSnapshot(ctx, nodeID, nodeUUID, sessionUUID); err != nil {
		return nil, err
	}
	return c.OpenStream(ctx, nodeUUID, sessionUUID)
}

func (c *BotFleetRuntimeCoordinator) consumeUntilDisconnect(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, stream BotFleetRuntimeStream) (bool, error) {
	if stream == nil {
		return false, fmt.Errorf("Bot Fleet runtime stream 未装配")
	}
	for {
		event, err := stream.Recv()
		if err != nil {
			return true, err
		}
		if _, err := c.HandleEvent(ctx, nodeID, nodeUUID, sessionUUID, event); err != nil {
			return false, err
		}
	}
}

func (c *BotFleetRuntimeCoordinator) requestSnapshotOnce(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, generation int64, observe bool) error {
	key := fmt.Sprintf("%s\x00%s\x00%d", nodeUUID, sessionUUID, generation)
	call, leader := c.snapshotCall(key)
	if !leader {
		select {
		case <-call.done:
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	snapshot, err := c.fetchSnapshot(ctx, nodeUUID, sessionUUID)
	if err == nil {
		err = c.applySnapshotState(ctx, nodeID, nodeUUID, sessionUUID, snapshot)
	}
	if err == nil && observe {
		err = c.observeRuntimeState(ctx, sessionUUID)
	}
	call.err = err
	c.finishSnapshotCall(key, call)
	return call.err
}

func (c *BotFleetRuntimeCoordinator) snapshotCall(key string) (*botFleetSnapshotCall, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if call, ok := c.snapshotCalls[key]; ok {
		return call, false
	}
	call := &botFleetSnapshotCall{done: make(chan struct{})}
	c.snapshotCalls[key] = call
	c.snapshotOrder = append(c.snapshotOrder, key)
	return call, true
}

func (c *BotFleetRuntimeCoordinator) finishSnapshotCall(key string, call *botFleetSnapshotCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(call.done)
	if call.err != nil {
		delete(c.snapshotCalls, key)
		return
	}
	c.pruneSnapshotCalls()
}

func (c *BotFleetRuntimeCoordinator) pruneSnapshotCalls() {
	checks := len(c.snapshotOrder)
	for len(c.snapshotOrder) > botFleetSnapshotDedupeLimit && checks > 0 {
		checks--
		oldest := c.snapshotOrder[0]
		c.snapshotOrder = c.snapshotOrder[1:]
		call, ok := c.snapshotCalls[oldest]
		if !ok {
			continue
		}
		select {
		case <-call.done:
			delete(c.snapshotCalls, oldest)
		default:
			c.snapshotOrder = append(c.snapshotOrder, oldest)
		}
	}
}

// BotFleetSubscriptionTarget 唯一标识一个会话在一个执行节点上的 Fleet 订阅。
type BotFleetSubscriptionTarget struct {
	NodeID      uint
	NodeUUID    string
	SessionUUID string
}

// BotFleetSubscriptionController 是 ExecutionService 所需的订阅生命周期接口。
type BotFleetSubscriptionController interface {
	Ensure(target BotFleetSubscriptionTarget)
	Restore(targets []BotFleetSubscriptionTarget)
	StopSession(sessionUUID string)
}

type botFleetSubscriptionSlot struct {
	gate       sync.Mutex
	target     BotFleetSubscriptionTarget
	generation uint64
	active     bool
	cancel     context.CancelFunc
}

// BotFleetSubscriptionManager 按 session/node 去重并维持 snapshot→stream 订阅顺序。
type BotFleetSubscriptionManager struct {
	coordinator *BotFleetRuntimeCoordinator
	rootCtx     context.Context
	cancel      context.CancelFunc

	mu     sync.Mutex
	slots  map[string]*botFleetSubscriptionSlot
	closed bool
	wg     sync.WaitGroup
}

// NewBotFleetSubscriptionManager 创建进程级共享的 Fleet 订阅管理器。
func NewBotFleetSubscriptionManager(coordinator *BotFleetRuntimeCoordinator) *BotFleetSubscriptionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &BotFleetSubscriptionManager{
		coordinator: coordinator, rootCtx: ctx, cancel: cancel,
		slots: make(map[string]*botFleetSubscriptionSlot),
	}
}

// RuntimeCoordinator 返回订阅实际使用的共享 Fleet 协调器。
func (m *BotFleetSubscriptionManager) RuntimeCoordinator() *BotFleetRuntimeCoordinator {
	if m == nil {
		return nil
	}
	return m.coordinator
}

// Ensure 为 session/node 建立唯一活动订阅；新订阅从完整 baseline 开始。
func (m *BotFleetSubscriptionManager) Ensure(target BotFleetSubscriptionTarget) {
	if m == nil || m.coordinator == nil || target.NodeID == 0 || target.NodeUUID == "" || target.SessionUUID == "" {
		return
	}
	slot := m.subscriptionSlot(target)
	if slot == nil {
		return
	}
	slot.gate.Lock()
	m.mu.Lock()
	if m.closed || slot.active {
		m.mu.Unlock()
		slot.gate.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(m.rootCtx)
	slot.target = target
	slot.generation++
	slot.active = true
	slot.cancel = cancel
	generation := slot.generation
	m.wg.Add(1)
	m.mu.Unlock()
	slot.gate.Unlock()
	go m.runSubscription(ctx, slot, generation)
}

func (m *BotFleetSubscriptionManager) subscriptionSlot(target BotFleetSubscriptionTarget) *botFleetSubscriptionSlot {
	key := botFleetSubscriptionKey(target)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if slot := m.slots[key]; slot != nil {
		return slot
	}
	slot := &botFleetSubscriptionSlot{target: target}
	m.slots[key] = slot
	return slot
}

// Restore 恢复持久化运行仍需要的订阅；Ensure 保证重复启动与重连回调幂等。
func (m *BotFleetSubscriptionManager) Restore(targets []BotFleetSubscriptionTarget) {
	for _, target := range targets {
		m.Ensure(target)
	}
}

// StopSession 取消该会话的全部节点订阅；停止完成后不保留活动流。
func (m *BotFleetSubscriptionManager) StopSession(sessionUUID string) {
	if m == nil || sessionUUID == "" {
		return
	}
	for _, slot := range m.subscriptionSlots() {
		slot.gate.Lock()
		if slot.target.SessionUUID == sessionUUID && slot.active {
			slot.active = false
			slot.cancel()
		}
		slot.gate.Unlock()
	}
}

// Close 取消所有订阅并等待消费 goroutine 退出。
func (m *BotFleetSubscriptionManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.wg.Wait()
		return
	}
	m.closed = true
	m.cancel()
	slots := make([]*botFleetSubscriptionSlot, 0, len(m.slots))
	for _, slot := range m.slots {
		slots = append(slots, slot)
	}
	m.mu.Unlock()
	for _, slot := range slots {
		slot.gate.Lock()
		slot.active = false
		if slot.cancel != nil {
			slot.cancel()
		}
		slot.gate.Unlock()
	}
	m.wg.Wait()
}

func (m *BotFleetSubscriptionManager) subscriptionSlots() []*botFleetSubscriptionSlot {
	m.mu.Lock()
	defer m.mu.Unlock()
	slots := make([]*botFleetSubscriptionSlot, 0, len(m.slots))
	for _, slot := range m.slots {
		slots = append(slots, slot)
	}
	return slots
}

func (m *BotFleetSubscriptionManager) runSubscription(ctx context.Context, slot *botFleetSubscriptionSlot, generation uint64) {
	defer m.wg.Done()
	defer m.markSubscriptionInactive(slot, generation)
	for {
		stream, err := m.openSubscription(ctx, slot, generation)
		if err == nil {
			err = m.consumeSubscription(ctx, slot, generation, stream)
		}
		if ctx.Err() != nil || !m.subscriptionCurrent(slot, generation) {
			return
		}
		target := m.subscriptionTarget(slot)
		slog.Debug("Bot Fleet 订阅断开，准备重建 baseline", "nodeId", target.NodeID, "sessionUuid", target.SessionUUID, "error", err)
		if !waitBotFleetSubscriptionRetry(ctx) {
			return
		}
		var ok bool
		generation, ok = m.advanceSubscriptionGeneration(slot, generation)
		if !ok {
			return
		}
	}
}

func (m *BotFleetSubscriptionManager) openSubscription(ctx context.Context, slot *botFleetSubscriptionSlot, generation uint64) (BotFleetRuntimeStream, error) {
	target, ok := m.currentSubscriptionTarget(slot, generation)
	if !ok || ctx.Err() != nil {
		return nil, context.Canceled
	}
	snapshot, err := m.coordinator.fetchSnapshot(ctx, target.NodeUUID, target.SessionUUID)
	if err != nil {
		return nil, err
	}
	slot.gate.Lock()
	if ctx.Err() != nil || !subscriptionCurrentLocked(slot, generation) {
		slot.gate.Unlock()
		return nil, context.Canceled
	}
	if err := m.coordinator.applySnapshotState(ctx, target.NodeID, target.NodeUUID, target.SessionUUID, snapshot); err != nil {
		slot.gate.Unlock()
		return nil, err
	}
	stream, err := m.coordinator.OpenStream(ctx, target.NodeUUID, target.SessionUUID)
	slot.gate.Unlock()
	if err != nil {
		return nil, err
	}
	if err := m.coordinator.observeRuntimeState(ctx, target.SessionUUID); err != nil {
		return nil, err
	}
	return stream, nil
}

func (m *BotFleetSubscriptionManager) consumeSubscription(ctx context.Context, slot *botFleetSubscriptionSlot, generation uint64, stream BotFleetRuntimeStream) error {
	if stream == nil {
		return fmt.Errorf("Bot Fleet runtime stream 未装配")
	}
	for {
		event, err := stream.Recv()
		if err != nil {
			return err
		}
		target := m.subscriptionTarget(slot)
		if _, err := m.handleSubscriptionEvent(ctx, target, generation, event); err != nil {
			return err
		}
	}
}

func (m *BotFleetSubscriptionManager) handleSubscriptionEvent(ctx context.Context, target BotFleetSubscriptionTarget, generation uint64, event *workerpb.BotFleetEvent) (BotFleetRuntimeResult, error) {
	m.mu.Lock()
	slot := m.slots[botFleetSubscriptionKey(target)]
	m.mu.Unlock()
	if slot == nil {
		return staleBotFleetSubscriptionResult(event), nil
	}
	slot.gate.Lock()
	if !subscriptionCurrentLocked(slot, generation) {
		slot.gate.Unlock()
		return staleBotFleetSubscriptionResult(event), nil
	}
	result, observed, err := m.coordinator.handleEvent(ctx, target.NodeID, target.NodeUUID, target.SessionUUID, event, false)
	slot.gate.Unlock()
	if err != nil || !observed {
		return result, err
	}
	return result, m.coordinator.observeRuntimeState(ctx, target.SessionUUID)
}

func (m *BotFleetSubscriptionManager) advanceSubscriptionGeneration(slot *botFleetSubscriptionSlot, generation uint64) (uint64, bool) {
	slot.gate.Lock()
	defer slot.gate.Unlock()
	if !subscriptionCurrentLocked(slot, generation) {
		return 0, false
	}
	slot.generation++
	return slot.generation, true
}

func (m *BotFleetSubscriptionManager) currentSubscriptionTarget(slot *botFleetSubscriptionSlot, generation uint64) (BotFleetSubscriptionTarget, bool) {
	slot.gate.Lock()
	defer slot.gate.Unlock()
	return slot.target, subscriptionCurrentLocked(slot, generation)
}

func (m *BotFleetSubscriptionManager) subscriptionTarget(slot *botFleetSubscriptionSlot) BotFleetSubscriptionTarget {
	slot.gate.Lock()
	defer slot.gate.Unlock()
	return slot.target
}

func (m *BotFleetSubscriptionManager) subscriptionCurrent(slot *botFleetSubscriptionSlot, generation uint64) bool {
	slot.gate.Lock()
	defer slot.gate.Unlock()
	return subscriptionCurrentLocked(slot, generation)
}

func subscriptionCurrentLocked(slot *botFleetSubscriptionSlot, generation uint64) bool {
	return slot.active && slot.generation == generation
}

func (m *BotFleetSubscriptionManager) markSubscriptionInactive(slot *botFleetSubscriptionSlot, generation uint64) {
	slot.gate.Lock()
	defer slot.gate.Unlock()
	if slot.generation == generation {
		slot.active = false
	}
}

func (m *BotFleetSubscriptionManager) activeSubscriptionCount() int {
	count := 0
	for _, slot := range m.subscriptionSlots() {
		slot.gate.Lock()
		if slot.active {
			count++
		}
		slot.gate.Unlock()
	}
	return count
}

func (m *BotFleetSubscriptionManager) subscriptionGeneration(target BotFleetSubscriptionTarget) uint64 {
	m.mu.Lock()
	slot := m.slots[botFleetSubscriptionKey(target)]
	m.mu.Unlock()
	if slot == nil {
		return 0
	}
	slot.gate.Lock()
	defer slot.gate.Unlock()
	return slot.generation
}

func botFleetSubscriptionKey(target BotFleetSubscriptionTarget) string {
	return fmt.Sprintf("%s\x00%s", target.SessionUUID, target.NodeUUID)
}

func staleBotFleetSubscriptionResult(event *workerpb.BotFleetEvent) BotFleetRuntimeResult {
	botUUID := ""
	if event != nil && event.GetRuntimeSnapshot() != nil {
		botUUID = event.GetRuntimeSnapshot().BotUuid
	}
	return runtimeResult(BotFleetRuntimeIgnoredStaleSubscription, botUUID, "旧 Fleet 订阅世代事件已丢弃")
}

func waitBotFleetSubscriptionRetry(ctx context.Context) bool {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
