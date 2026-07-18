package service

import (
	"context"
	"fmt"
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
	BotFleetRuntimeApplied                 BotFleetRuntimeDecision = "applied"
	BotFleetRuntimeSnapshotRequired        BotFleetRuntimeDecision = "snapshot_required"
	BotFleetRuntimeIgnoredBotMissing       BotFleetRuntimeDecision = "ignored_bot_missing"
	BotFleetRuntimeIgnoredExecutorMismatch BotFleetRuntimeDecision = "ignored_executor_mismatch"
	BotFleetRuntimeIgnoredSessionMismatch  BotFleetRuntimeDecision = "ignored_session_mismatch"
	BotFleetRuntimeIgnoredStaleGeneration  BotFleetRuntimeDecision = "ignored_stale_generation"
	BotFleetRuntimeIgnoredConfigMismatch   BotFleetRuntimeDecision = "ignored_config_mismatch"
	BotFleetRuntimeIgnoredStaleEpoch       BotFleetRuntimeDecision = "ignored_stale_epoch"
	BotFleetRuntimeIgnoredDuplicateEvent   BotFleetRuntimeDecision = "ignored_duplicate_event"
	BotFleetRuntimeIgnoredConcurrentUpdate BotFleetRuntimeDecision = "ignored_concurrent_update"
	BotFleetRuntimeIgnoredInvalidStatus    BotFleetRuntimeDecision = "ignored_invalid_status"
	BotFleetRuntimeIgnoredActionEvent      BotFleetRuntimeDecision = "ignored_action_event"
)

// BotFleetRuntimeResult 包含决策、诊断和是否需要拉取完整快照。
type BotFleetRuntimeResult struct {
	Decision         BotFleetRuntimeDecision
	BotUUID          string
	Diagnostic       string
	SnapshotRequired bool
}

// BotFleetRuntimeRepository 封装 Bot runtime 身份读取与带世代条件的原子更新。
type BotFleetRuntimeRepository interface {
	FindBotRuntime(ctx context.Context, botUUID string) (*model.Bot, error)
	ApplyBotRuntime(ctx context.Context, bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, status model.BotStatus, observedAt time.Time) (bool, error)
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

func (r *gormBotFleetRuntimeRepository) ApplyBotRuntime(ctx context.Context, bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot, status model.BotStatus, observedAt time.Time) (bool, error) {
	updates := runtimeUpdateValues(snapshot, status, observedAt)
	query := r.runtimeUpdateQuery(ctx, bot, executorNodeID, snapshot)
	result := query.Updates(updates)
	if result.Error != nil {
		return false, fmt.Errorf("更新 Bot Fleet runtime 失败: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (r *gormBotFleetRuntimeRepository) runtimeUpdateQuery(ctx context.Context, bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot) *gorm.DB {
	query := r.db.WithContext(ctx).Model(&model.Bot{}).Where("id = ?", bot.ID).
		Where("desired_state_generation = ?", snapshot.Generation).
		Where("config_hash = ?", snapshot.ConfigHash).
		Where("(worker_epoch_generation < ?) OR (worker_epoch_generation = ? AND last_event_seq < ?)", snapshot.WorkerEpochGeneration, snapshot.WorkerEpochGeneration, snapshot.EventSeq)
	query = runtimeSessionCondition(query, bot)
	return runtimeExecutorCondition(query, bot, executorNodeID)
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
	subquery := query.Session(&gorm.Session{NewDB: true}).Model(&model.Instance{}).Select("id").Where("node_id = ?", executorNodeID)
	return query.Where("executor_node_id IS NULL").Where("instance_id IN (?)", subquery)
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

// Ingest 校验来源与三层世代后，以数据库条件更新保证并发消息不会倒退。
func (s *BotFleetRuntimeService) Ingest(ctx context.Context, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot) (BotFleetRuntimeResult, error) {
	if snapshot == nil {
		return runtimeResult(BotFleetRuntimeIgnoredBotMissing, "", "runtime snapshot 为空"), nil
	}
	bot, err := s.repository.FindBotRuntime(ctx, snapshot.BotUuid)
	if err != nil {
		return s.findErrorResult(snapshot.BotUuid, err)
	}
	if result, accepted := classifyRuntimeSnapshot(bot, executorNodeID, snapshot); !accepted {
		return result, nil
	}
	status, ok := runtimeStatus(snapshot.Status)
	if !ok {
		return runtimeResult(BotFleetRuntimeIgnoredInvalidStatus, snapshot.BotUuid, "runtime status 不在冻结枚举内"), nil
	}
	applied, err := s.repository.ApplyBotRuntime(ctx, bot, executorNodeID, snapshot, status, s.observedAt(snapshot))
	if err != nil || applied {
		return appliedRuntimeResult(snapshot.BotUuid, applied), err
	}
	return s.classifyConcurrentResult(ctx, executorNodeID, snapshot)
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

func (s *BotFleetRuntimeService) classifyConcurrentResult(ctx context.Context, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot) (BotFleetRuntimeResult, error) {
	latest, err := s.repository.FindBotRuntime(ctx, snapshot.BotUuid)
	if err != nil {
		return s.findErrorResult(snapshot.BotUuid, err)
	}
	if result, accepted := classifyRuntimeSnapshot(latest, executorNodeID, snapshot); !accepted {
		return result, nil
	}
	return runtimeResult(BotFleetRuntimeIgnoredConcurrentUpdate, snapshot.BotUuid, "数据库条件更新未命中，消息已被并发状态取代"), nil
}

func classifyRuntimeSnapshot(bot *model.Bot, executorNodeID uint, snapshot *workerpb.BotRuntimeSnapshot) (BotFleetRuntimeResult, bool) {
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
	return classifyRuntimeEpoch(bot, snapshot)
}

func classifyRuntimeEpoch(bot *model.Bot, snapshot *workerpb.BotRuntimeSnapshot) (BotFleetRuntimeResult, bool) {
	if bot.ConfigHash != snapshot.ConfigHash {
		return snapshotRuntimeResult(BotFleetRuntimeIgnoredConfigMismatch, snapshot.BotUuid, "runtime configHash 与 desired 配置不匹配"), false
	}
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
	ingester     *BotFleetRuntimeService
	client       BotFleetRuntimeClient
	capacitySink BotFleetCapacityGenerationSink
	reconciler   BotFleetSnapshotReconciler

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

// NewGRPCBotFleetRuntimeCoordinator 使用既有隧道优先连接池装配协调器。
func NewGRPCBotFleetRuntimeCoordinator(db *gorm.DB, pool *cpgrpc.ClientPool, capacitySink BotFleetCapacityGenerationSink, clock BotLoadClock) *BotFleetRuntimeCoordinator {
	ingester := NewBotFleetRuntimeService(db, clock)
	return NewBotFleetRuntimeCoordinator(ingester, poolBotFleetRuntimeClient{pool: pool}, capacitySink)
}

// HandleEvent 只消费 runtime_snapshot；action_event 留给后续动作结果服务。
func (c *BotFleetRuntimeCoordinator) HandleEvent(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, event *workerpb.BotFleetEvent) (BotFleetRuntimeResult, error) {
	if event == nil || event.GetRuntimeSnapshot() == nil {
		return ignoredFleetEventResult(event), nil
	}
	result, err := c.ingester.Ingest(ctx, nodeID, event.GetRuntimeSnapshot())
	if err != nil || !result.SnapshotRequired {
		return result, err
	}
	generation := event.GetRuntimeSnapshot().Generation
	return result, c.requestSnapshotOnce(ctx, nodeID, nodeUUID, sessionUUID, generation)
}

func ignoredFleetEventResult(event *workerpb.BotFleetEvent) BotFleetRuntimeResult {
	botUUID := ""
	if event != nil && event.GetActionEvent() != nil {
		botUUID = event.GetActionEvent().BotUuid
	}
	return runtimeResult(BotFleetRuntimeIgnoredActionEvent, botUUID, "action_event 已安全转交后续 FR，不更新 Bot runtime")
}

// RefreshSnapshot 拉取完整快照并让每个 Bot 复用同一 ingest 规则。
func (c *BotFleetRuntimeCoordinator) RefreshSnapshot(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string) error {
	if c.client == nil {
		return fmt.Errorf("Bot Fleet runtime client 未装配")
	}
	snapshot, err := c.client.GetBotFleetSnapshot(ctx, nodeUUID, sessionUUID)
	if err != nil {
		return fmt.Errorf("拉取 Bot FleetSnapshot 失败: %w", err)
	}
	if c.reconciler != nil {
		if err := c.reconciler.ReconcileBotFleetSnapshot(ctx, nodeID, nodeUUID, sessionUUID, snapshot); err != nil {
			return fmt.Errorf("协调 Bot FleetSnapshot desired 差异失败: %w", err)
		}
	}
	return c.applySnapshot(ctx, nodeID, snapshot)
}

func (c *BotFleetRuntimeCoordinator) applySnapshot(ctx context.Context, nodeID uint, snapshot *workerpb.GetBotFleetSnapshotResponse) error {
	if snapshot == nil {
		return nil
	}
	if c.capacitySink != nil {
		c.capacitySink.ObserveBotFleetCapacityGeneration(nodeID, snapshot.CapacityGeneration)
	}
	for _, bot := range snapshot.Bots {
		if _, err := c.ingester.Ingest(ctx, nodeID, bot); err != nil {
			return err
		}
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

func (c *BotFleetRuntimeCoordinator) requestSnapshotOnce(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, generation int64) error {
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
	call.err = c.RefreshSnapshot(ctx, nodeID, nodeUUID, sessionUUID)
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
