package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const botLoadApplyTimeout = 15 * time.Second

var (
	// ErrBotLoadInvalidState 表示 V1 兼容状态不允许当前分布式操作。
	ErrBotLoadInvalidState = errors.New("Bot 负载运行状态不允许当前操作")
	// ErrBotLoadConfigInvalid 表示压测会话缺少 Worker assignment 所需连接配置。
	ErrBotLoadConfigInvalid = errors.New("Bot 负载连接配置无效")
)

// BotLoadCapacityRefresher 是 start 即时容量快检的最小依赖。
type BotLoadCapacityRefresher interface {
	Refresh(ctx context.Context, excludeRunID uint) (*BotLoadCapacitySnapshot, error)
}

// BotLoadBatchDispatcher 隔离连接池和 proto client，便于测试逐批编排。
type BotLoadBatchDispatcher interface {
	ApplyBotBatch(ctx context.Context, nodeUUID string, request *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error)
}

// BotLoadBackgroundRunner 提交有界后台任务；测试可注入同步或排队实现。
type BotLoadBackgroundRunner interface {
	Submit(task func()) error
}

// BotLoadGoroutineRunner 使用独立 goroutine 执行已提交事务后的 dispatch。
type BotLoadGoroutineRunner struct{}

// Submit 启动后台任务并立即返回。
func (BotLoadGoroutineRunner) Submit(task func()) error {
	go task()
	return nil
}

type poolBotLoadBatchDispatcher struct{ pool *cpgrpc.ClientPool }

func (d poolBotLoadBatchDispatcher) ApplyBotBatch(ctx context.Context, nodeUUID string, request *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
	if d.pool == nil {
		return nil, errBotLoadWorkerMissing
	}
	client, ok := d.pool.Get(nodeUUID)
	if !ok || client.Worker == nil {
		return nil, errBotLoadWorkerMissing
	}
	return client.Worker.ApplyBotBatch(ctx, request)
}

type botLoadConnectionConfig struct {
	Server   string `json:"server"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Version  string `json:"version"`
	Auth     string `json:"auth"`
}

type botLoadStartPreparation struct {
	session           *model.BotStressSession
	plan              *BotLoadAllocationPlan
	config            botLoadConnectionConfig
	hasBatch          bool
	cohortAssignments []string
	cohortJSON        map[string]string
}

type botLoadDispatchItem struct {
	bot      model.Bot
	accepted bool
	lastErr  string
}

type botLoadStopGroup struct {
	node model.Node
	bots []model.Bot
}

type botLoadReconcileMode string

const (
	botLoadReconcileStopped botLoadReconcileMode = "stopped"
	botLoadReconcileCleanup botLoadReconcileMode = "cleanup"
)

type botLoadReconcileItem struct {
	assignment *workerpb.BotAssignment
	bot        *model.Bot
	mode       botLoadReconcileMode
}

type botLoadSerialTask struct {
	identity string
	task     func()
}

type botLoadSerialQueue struct {
	pending    []botLoadSerialTask
	identities map[string]struct{}
}

// BotLoadExecutionService 实现 FR-351 的 start、后台 dispatch、批量 stop 与基础 snapshot 收敛。
type BotLoadExecutionService struct {
	db            *gorm.DB
	capacities    BotLoadCapacityRefresher
	reservations  *BotLoadReservationStore
	signer        *BotLoadPlanTokenSigner
	dispatcher    BotLoadBatchDispatcher
	runner        BotLoadBackgroundRunner
	clock         BotLoadClock
	resolver      *BotExecutorResolver
	subscriptions BotFleetSubscriptionController

	startMu     sync.Mutex
	taskMu      sync.Mutex
	tasks       map[string]struct{}
	serialTasks map[string]*botLoadSerialQueue
}

// NewBotLoadExecutionService 创建可注入容量、RPC、runner 和时钟的分布式运行核心。
func NewBotLoadExecutionService(db *gorm.DB, capacities BotLoadCapacityRefresher, reservations *BotLoadReservationStore, signer *BotLoadPlanTokenSigner, dispatcher BotLoadBatchDispatcher, runner BotLoadBackgroundRunner, clock BotLoadClock) *BotLoadExecutionService {
	if runner == nil {
		runner = BotLoadGoroutineRunner{}
	}
	return &BotLoadExecutionService{
		db: db, capacities: capacities, reservations: reservations, signer: signer,
		dispatcher: dispatcher, runner: runner, clock: normalizeBotLoadClock(clock),
		resolver: NewBotExecutorResolver(db), tasks: make(map[string]struct{}),
		serialTasks: make(map[string]*botLoadSerialQueue),
	}
}

// NewGRPCBotLoadExecutionService 使用 GORM、容量目录和既有连接池装配生产核心。
func NewGRPCBotLoadExecutionService(db *gorm.DB, capacities *BotLoadCapacityDirectory, reservations *BotLoadReservationStore, signer *BotLoadPlanTokenSigner, pool *cpgrpc.ClientPool, runner BotLoadBackgroundRunner, clock BotLoadClock) *BotLoadExecutionService {
	return NewBotLoadExecutionService(db, capacities, reservations, signer, poolBotLoadBatchDispatcher{pool: pool}, runner, clock)
}

// SetFleetSubscriptionManager 注入进程级共享的 Fleet 订阅生命周期管理器。
func (s *BotLoadExecutionService) SetFleetSubscriptionManager(subscriptions BotFleetSubscriptionController) {
	s.subscriptions = subscriptions
}

// FleetSubscriptionManager 返回当前注入的 Fleet 订阅管理器。
func (s *BotLoadExecutionService) FleetSubscriptionManager() BotFleetSubscriptionController {
	if s == nil {
		return nil
	}
	return s.subscriptions
}

// RecoverFleetSubscriptions 从持久化活动批次恢复已连接节点的 Fleet 订阅，不重派任务或重建 Bot。
func (s *BotLoadExecutionService) RecoverFleetSubscriptions(ctx context.Context, connectedNodeUUIDs []string) error {
	if s == nil || s.db == nil || s.subscriptions == nil || len(connectedNodeUUIDs) == 0 {
		return nil
	}
	nodeUUIDs := uniqueBotLoadNodeUUIDs(connectedNodeUUIDs)
	if len(nodeUUIDs) == 0 {
		return nil
	}
	targets, err := s.loadRecoverableFleetSubscriptions(ctx, nodeUUIDs)
	if err != nil {
		return err
	}
	s.subscriptions.Restore(targets)
	return nil
}

func (s *BotLoadExecutionService) loadRecoverableFleetSubscriptions(ctx context.Context, nodeUUIDs []string) ([]BotFleetSubscriptionTarget, error) {
	var targets []BotFleetSubscriptionTarget
	activeStates := []model.BotLoadBatchState{model.BotLoadBatchDispatching, model.BotLoadBatchRunning}
	waitingRuntime := `%"operation":"stop"%"state":"waiting_runtime"%`
	err := s.db.WithContext(ctx).Model(&model.BotLoadBatch{}).
		Select("DISTINCT bot_load_batches.executor_node_id AS node_id, nodes.uuid AS node_uuid, sessions.uuid AS session_uuid").
		Joins("JOIN bot_stress_sessions AS sessions ON sessions.id = bot_load_batches.stress_session_id AND sessions.deleted_at IS NULL").
		Joins("JOIN nodes ON nodes.id = bot_load_batches.executor_node_id AND nodes.deleted_at IS NULL").
		Where("nodes.uuid IN ?", nodeUUIDs).
		Where("sessions.status = ? AND (bot_load_batches.state IN ? OR sessions.last_error LIKE ?)", model.BotStressSessionRunning, activeStates, waitingRuntime).
		Order("sessions.uuid ASC, nodes.uuid ASC").
		Scan(&targets).Error
	if err != nil {
		return nil, fmt.Errorf("查询待恢复 Bot Fleet 订阅失败: %w", err)
	}
	return targets, nil
}

func uniqueBotLoadNodeUUIDs(nodeUUIDs []string) []string {
	seen := make(map[string]struct{}, len(nodeUUIDs))
	unique := make([]string, 0, len(nodeUUIDs))
	for _, nodeUUID := range nodeUUIDs {
		nodeUUID = strings.TrimSpace(nodeUUID)
		if nodeUUID == "" {
			continue
		}
		if _, exists := seen[nodeUUID]; exists {
			continue
		}
		seen[nodeUUID] = struct{}{}
		unique = append(unique, nodeUUID)
	}
	return unique
}

// Start 校验服务端计划和即时容量，在单事务物化批次/Bot 后提交后台 dispatch。
func (s *BotLoadExecutionService) Start(ctx context.Context, sessionID uint, planToken string) (*model.BotStressSession, error) {
	prepared, err := s.prepareStart(ctx, sessionID, planToken)
	if err != nil {
		return nil, err
	}
	s.startMu.Lock()
	hasPlanned, err := s.materializeStart(ctx, prepared)
	s.startMu.Unlock()
	if err != nil {
		return nil, err
	}
	if s.reservations != nil {
		s.reservations.Release(sessionID)
	}
	s.ensureFleetSubscriptions(prepared)
	if hasPlanned {
		if err := s.submitLifecycle(sessionID, "start", func() { s.runDispatch(prepared) }); err != nil {
			return nil, err
		}
	}
	return s.loadSession(ctx, sessionID)
}

func (s *BotLoadExecutionService) ensureFleetSubscriptions(prepared *botLoadStartPreparation) {
	if s.subscriptions == nil || prepared == nil || prepared.session == nil || prepared.plan == nil {
		return
	}
	seen := make(map[uint]struct{}, len(prepared.plan.Allocations))
	for _, allocation := range prepared.plan.Allocations {
		if _, exists := seen[allocation.ExecutorNodeID]; exists {
			continue
		}
		seen[allocation.ExecutorNodeID] = struct{}{}
		s.subscriptions.Ensure(BotFleetSubscriptionTarget{
			NodeID: allocation.ExecutorNodeID, NodeUUID: allocation.ExecutorNodeUUID, SessionUUID: prepared.session.UUID,
		})
	}
}

func (s *BotLoadExecutionService) prepareStart(ctx context.Context, sessionID uint, planToken string) (*botLoadStartPreparation, error) {
	if err := s.validateDependencies(); err != nil {
		return nil, err
	}
	session, err := s.loadSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := validateBotLoadStartState(session.Status); err != nil {
		return nil, err
	}
	plan, err := decodeStartAllocationPlan(session)
	if err != nil {
		return nil, err
	}
	config, err := parseBotLoadConnectionConfig(session.Config)
	if err != nil {
		return nil, err
	}
	cohortAssignments, cohortJSON, err := prepareBotLoadScenarioAssignments(session)
	if err != nil {
		return nil, err
	}
	hasBatch, err := s.sessionHasBatches(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.verifyStartCapacity(ctx, session, plan, planToken, hasBatch); err != nil {
		return nil, err
	}
	return &botLoadStartPreparation{
		session: session, plan: plan, config: config, hasBatch: hasBatch,
		cohortAssignments: cohortAssignments, cohortJSON: cohortJSON,
	}, nil
}

func (s *BotLoadExecutionService) validateDependencies() error {
	if s == nil || s.db == nil || s.capacities == nil || s.signer == nil || s.dispatcher == nil || s.runner == nil {
		return fmt.Errorf("Bot 负载执行核心未完整装配")
	}
	return nil
}

func validateBotLoadStartState(status model.BotStressSessionStatus) error {
	switch status {
	case model.BotStressSessionPending, model.BotStressSessionError, model.BotStressSessionRunning:
		return nil
	default:
		return fmt.Errorf("%w: 当前状态为 %s", ErrBotLoadInvalidState, status)
	}
}

func decodeStartAllocationPlan(session *model.BotStressSession) (*BotLoadAllocationPlan, error) {
	if session == nil || strings.TrimSpace(session.AllocationPlan) == "" {
		return nil, newBotLoadCapacityChanged("服务端分片计划不存在，请重新预检")
	}
	plan, err := DecodeBotLoadAllocationPlan(session.AllocationPlan)
	if err != nil {
		return nil, newBotLoadCapacityChanged("服务端分片计划无效，请重新预检")
	}
	if err := validateStartAllocationPlan(session, plan); err != nil {
		return nil, newBotLoadCapacityChanged(err.Error())
	}
	return plan, nil
}

func validateStartAllocationPlan(session *model.BotStressSession, plan *BotLoadAllocationPlan) error {
	if plan.RunID != session.ID || plan.RunUUID != session.UUID || plan.TargetBots != session.BotCount {
		return fmt.Errorf("服务端分片计划与当前运行不匹配")
	}
	total := 0
	for index, allocation := range plan.Allocations {
		if allocation.Ordinal != index+1 || allocation.PlannedCount < 1 || allocation.PlannedCount > maxBotLoadBatchSize {
			return fmt.Errorf("服务端分片计划批次不完整")
		}
		if allocation.ExecutorNodeID == 0 || allocation.BatchID == "" || allocation.IdempotencyKey == "" || allocation.ConnectStartAt.IsZero() {
			return fmt.Errorf("服务端分片计划批次字段缺失")
		}
		total += allocation.PlannedCount
	}
	if total != plan.TargetBots {
		return fmt.Errorf("服务端分片计划数量不一致")
	}
	return nil
}

func parseBotLoadConnectionConfig(raw string) (botLoadConnectionConfig, error) {
	var config botLoadConnectionConfig
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &config) != nil {
		return config, fmt.Errorf("%w: config 必须是有效 JSON", ErrBotLoadConfigInvalid)
	}
	if strings.TrimSpace(config.Server) == "" {
		config.Server = strings.TrimSpace(config.Host)
	}
	if config.Server == "" {
		return config, fmt.Errorf("%w: config.server 或 config.host 不能为空", ErrBotLoadConfigInvalid)
	}
	if config.Port < 1 || config.Port > 65535 {
		return config, fmt.Errorf("%w: config.port 必须为 1..65535", ErrBotLoadConfigInvalid)
	}
	return config, nil
}

func prepareBotLoadScenarioAssignments(session *model.BotStressSession) ([]string, map[string]string, error) {
	if session == nil || strings.TrimSpace(session.ScenarioSnapshot) == "" {
		return nil, nil, nil
	}
	scenario, err := ParseScenarioSnapshot(session.ScenarioSnapshot)
	if err != nil {
		return nil, nil, err
	}
	assignments, err := AssignScenarioCohorts(scenario.Seed, session.BotCount, scenario.Cohorts)
	if err != nil {
		return nil, nil, err
	}
	cohortJSON, err := ScenarioCohortJSONMap(scenario)
	if err != nil {
		return nil, nil, err
	}
	return assignments, cohortJSON, nil
}

func (s *BotLoadExecutionService) verifyStartCapacity(ctx context.Context, session *model.BotStressSession, plan *BotLoadAllocationPlan, token string, hasBatch bool) error {
	hash, err := BotLoadAllocationHash(plan.RunID, plan.TargetBots, plan.Allocations)
	if err != nil {
		return err
	}
	expected := BotLoadPlanTokenExpectation{RunID: plan.RunID, AllocationHash: hash, CapacityGenerations: plan.CapacityGenerations}
	if err := s.signer.Verify(token, expected); err != nil {
		return err
	}
	snapshot, err := s.capacities.Refresh(ctx, session.ID)
	if err != nil {
		return newBotLoadCapacityChanged(fmt.Sprintf("即时容量快检失败: %v", err))
	}
	expected.CapacityGenerations = currentBotLoadGenerations(plan, snapshot.NodeCapacities)
	if err := s.signer.Verify(token, expected); err != nil {
		return err
	}
	if err := validateBotLoadNodeReadiness(plan, snapshot.NodeCapacities); err != nil {
		return err
	}
	if !hasBatch {
		return validateImmediateBotLoadCapacity(plan, snapshot.NodeCapacities)
	}
	return nil
}

func currentBotLoadGenerations(plan *BotLoadAllocationPlan, capacities []BotLoadNodeCapacity) []BotLoadNodeGeneration {
	byNode := make(map[uint]int64, len(capacities))
	for _, capacity := range capacities {
		byNode[capacity.NodeID] = capacity.CapacityGeneration
	}
	out := make([]BotLoadNodeGeneration, 0, len(plan.CapacityGenerations))
	for _, frozen := range plan.CapacityGenerations {
		out = append(out, BotLoadNodeGeneration{NodeID: frozen.NodeID, CapacityGeneration: byNode[frozen.NodeID]})
	}
	return canonicalBotLoadGenerations(out)
}

func validateBotLoadNodeReadiness(plan *BotLoadAllocationPlan, capacities []BotLoadNodeCapacity) error {
	byNode := make(map[uint]BotLoadNodeCapacity, len(capacities))
	for _, capacity := range capacities {
		byNode[capacity.NodeID] = capacity
	}
	for nodeID := range allocationBotLoadCounts(plan.Allocations) {
		capacity, ok := byNode[nodeID]
		if !ok || !capacity.Online || !capacity.BotWorkerReady || capacity.Legacy || capacity.UnavailableReason != "" {
			return fmt.Errorf("%w: 发压节点 %d 当前不可用", ErrBotLoadNodeUnavailable, nodeID)
		}
	}
	return nil
}

func validateImmediateBotLoadCapacity(plan *BotLoadAllocationPlan, capacities []BotLoadNodeCapacity) error {
	byNode := make(map[uint]BotLoadNodeCapacity, len(capacities))
	for _, capacity := range capacities {
		byNode[capacity.NodeID] = capacity
	}
	for nodeID, count := range allocationBotLoadCounts(plan.Allocations) {
		if byNode[nodeID].AvailableBots < count {
			return fmt.Errorf("%w: 发压节点 %d 的即时可用容量不足", ErrBotLoadCapacityInsufficient, nodeID)
		}
	}
	return nil
}

func (s *BotLoadExecutionService) materializeStart(ctx context.Context, prepared *botLoadStartPreparation) (bool, error) {
	hasPlanned := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.claimBotLoadStartState(tx, prepared.session.ID); err != nil {
			return err
		}
		batches, err := materializeBotLoadBatches(tx, prepared.session.ID, prepared.plan)
		if err != nil {
			return err
		}
		bots, err := expectedBotLoadBots(prepared, batches)
		if err != nil {
			return err
		}
		if err := materializeBotLoadBots(tx, prepared.session.ID, bots); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.BotLoadBatch{}).
			Where("stress_session_id = ? AND state = ?", prepared.session.ID, model.BotLoadBatchPlanned).
			Count(&count).Error; err != nil {
			return fmt.Errorf("检查待派发 Bot 负载批次失败: %w", err)
		}
		hasPlanned = count > 0
		return nil
	})
	return hasPlanned, err
}

func (s *BotLoadExecutionService) claimBotLoadStartState(tx *gorm.DB, sessionID uint) error {
	allowed := []model.BotStressSessionStatus{
		model.BotStressSessionPending, model.BotStressSessionError, model.BotStressSessionRunning,
	}
	now := s.clock.Now().UTC()
	result := tx.Model(&model.BotStressSession{}).
		Where("id = ? AND status IN ? AND (last_error IS NULL OR last_error NOT LIKE ?)", sessionID, allowed, `%"operation":"stop"%`).
		Updates(map[string]any{
			"status": model.BotStressSessionRunning, "started_at": gorm.Expr("COALESCE(started_at, ?)", now), "ended_at": nil,
		})
	if result.Error != nil {
		return fmt.Errorf("锁定 Bot 负载启动状态失败: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var session model.BotStressSession
	if err := tx.Select("id", "status", "last_error").First(&session, sessionID).Error; err != nil {
		return err
	}
	if err := validateBotLoadStartState(session.Status); err != nil {
		return err
	}
	if botLoadStopIntentRecorded(session.LastError) {
		return fmt.Errorf("%w: 会话已收到停止意图", ErrBotLoadInvalidState)
	}
	return fmt.Errorf("%w: 会话状态已变化", ErrBotLoadInvalidState)
}

func materializeBotLoadBatches(tx *gorm.DB, sessionID uint, plan *BotLoadAllocationPlan) (map[int]model.BotLoadBatch, error) {
	for _, allocation := range plan.Allocations {
		batch := botLoadBatchFromAllocation(sessionID, allocation)
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch).Error; err != nil {
			return nil, fmt.Errorf("创建 Bot 负载批次失败: %w", err)
		}
	}
	var existing []model.BotLoadBatch
	if err := tx.Where("stress_session_id = ?", sessionID).Find(&existing).Error; err != nil {
		return nil, fmt.Errorf("查询 Bot 负载批次失败: %w", err)
	}
	if len(existing) != len(plan.Allocations) {
		return nil, fmt.Errorf("恢复 Bot 负载批次失败: 批次数量与服务端计划不一致")
	}
	byOrdinal := make(map[int]model.BotLoadBatch, len(existing))
	for _, batch := range existing {
		byOrdinal[batch.Ordinal] = batch
	}
	for _, allocation := range plan.Allocations {
		batch, ok := byOrdinal[allocation.Ordinal]
		if !ok {
			return nil, fmt.Errorf("恢复 Bot 负载批次失败: ordinal=%d 不存在", allocation.Ordinal)
		}
		if err := validateExistingBotLoadBatch(batch, allocation); err != nil {
			return nil, err
		}
	}
	return byOrdinal, nil
}

func botLoadBatchFromAllocation(sessionID uint, allocation BotLoadAllocation) model.BotLoadBatch {
	return model.BotLoadBatch{
		UUID: allocation.BatchID, StressSessionID: sessionID, ExecutorNodeID: allocation.ExecutorNodeID,
		Ordinal: allocation.Ordinal, PlannedCount: allocation.PlannedCount, State: model.BotLoadBatchPlanned,
		IdempotencyKey: allocation.IdempotencyKey, ConnectStartAt: allocation.ConnectStartAt.UTC(),
		ConnectIntervalMS: allocation.ConnectIntervalMS,
	}
}

func validateExistingBotLoadBatch(batch model.BotLoadBatch, allocation BotLoadAllocation) error {
	if batch.UUID != allocation.BatchID || batch.ExecutorNodeID != allocation.ExecutorNodeID || batch.PlannedCount != allocation.PlannedCount ||
		batch.IdempotencyKey != allocation.IdempotencyKey || !batch.ConnectStartAt.Equal(allocation.ConnectStartAt) || batch.ConnectIntervalMS != allocation.ConnectIntervalMS {
		return fmt.Errorf("恢复 Bot 负载批次失败: ordinal=%d 与服务端计划不一致", allocation.Ordinal)
	}
	return nil
}

func expectedBotLoadBots(prepared *botLoadStartPreparation, batches map[int]model.BotLoadBatch) ([]model.Bot, error) {
	bots := make([]model.Bot, 0, prepared.plan.TargetBots)
	for _, allocation := range prepared.plan.Allocations {
		batch, ok := batches[allocation.Ordinal]
		if !ok {
			return nil, fmt.Errorf("创建 Bot 失败: 批次 ordinal=%d 不存在", allocation.Ordinal)
		}
		firstOrdinal := botLoadAllocationFirstOrdinal(prepared.plan, allocation.Ordinal)
		for localIndex := 0; localIndex < allocation.PlannedCount; localIndex++ {
			botOrdinal := firstOrdinal + localIndex
			bots = append(bots, newPlannedBotLoadBot(prepared, batch, allocation, botOrdinal, localIndex))
		}
	}
	return bots, nil
}

func newPlannedBotLoadBot(prepared *botLoadStartPreparation, batch model.BotLoadBatch, allocation BotLoadAllocation, botOrdinal, localIndex int) model.Bot {
	executorNodeID, batchID, sessionID := allocation.ExecutorNodeID, batch.ID, prepared.session.ID
	cohortKey := ""
	if botOrdinal <= len(prepared.cohortAssignments) {
		cohortKey = prepared.cohortAssignments[botOrdinal-1]
	}
	bot := model.Bot{
		UUID:       stableBotLoadUUID(fmt.Sprintf("%s|bot|%d", prepared.session.UUID, botOrdinal)),
		InstanceID: prepared.session.InstanceID, StressSessionID: &sessionID, ExecutorNodeID: &executorNodeID,
		LoadBatchID: &batchID, Name: stableBotLoadBotName(prepared.session.NamePrefix, prepared.session.UUID, botOrdinal),
		Status: model.BotStatusPending, DesiredStateGeneration: 1, Config: prepared.session.Config,
		Behavior: prepared.session.Behavior, WorkerID: allocation.ExecutorNodeUUID, CohortKey: cohortKey,
	}
	assignment := buildRunningBotLoadAssignment(
		&bot, prepared.session, &prepared.session.Instance, prepared.config, allocation, localIndex, botOrdinal, prepared.cohortJSON[cohortKey],
	)
	bot.ConfigHash = botLoadAssignmentConfigHash(assignment)
	return bot
}

func stableBotLoadBotName(prefix, sessionUUID string, index int) string {
	digest := stableBotLoadDigest(sessionUUID)
	suffix := fmt.Sprintf("_%s_%03d", digest[:6], index)
	namePrefix := sanitizeMCUsername(prefix)
	maxPrefix := 16 - len(suffix)
	if len(namePrefix) > maxPrefix {
		namePrefix = namePrefix[:maxPrefix]
	}
	if namePrefix == "" {
		namePrefix = "b"
	}
	return namePrefix + suffix
}

func materializeBotLoadBots(tx *gorm.DB, sessionID uint, expected []model.Bot) error {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(expected, maxBotLoadBatchSize).Error; err != nil {
		return fmt.Errorf("创建 Bot 负载记录失败: %w", err)
	}
	var existing []model.Bot
	if err := tx.Where("stress_session_id = ?", sessionID).Find(&existing).Error; err != nil {
		return fmt.Errorf("查询 Bot 负载记录失败: %w", err)
	}
	if len(existing) != len(expected) {
		return fmt.Errorf("恢复 Bot 负载记录失败: Bot 数量与服务端计划不一致")
	}
	expectedByUUID := make(map[string]model.Bot, len(expected))
	for _, bot := range expected {
		expectedByUUID[bot.UUID] = bot
	}
	for _, bot := range existing {
		expectedBot, ok := expectedByUUID[bot.UUID]
		if !ok || !samePlannedBotLoadBot(bot, expectedBot) {
			return fmt.Errorf("恢复 Bot 负载记录失败: Bot %s 与服务端计划不一致", bot.UUID)
		}
	}
	return nil
}

func samePlannedBotLoadBot(existing, expected model.Bot) bool {
	return existing.InstanceID == expected.InstanceID && equalUintPointers(existing.StressSessionID, expected.StressSessionID) &&
		equalUintPointers(existing.ExecutorNodeID, expected.ExecutorNodeID) && equalUintPointers(existing.LoadBatchID, expected.LoadBatchID) &&
		existing.Name == expected.Name && existing.Config == expected.Config && existing.ConfigHash == expected.ConfigHash &&
		existing.DesiredStateGeneration == expected.DesiredStateGeneration && existing.CohortKey == expected.CohortKey
}

func equalUintPointers(left, right *uint) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func buildRunningBotLoadAssignment(bot *model.Bot, session *model.BotStressSession, instance *model.Instance, config botLoadConnectionConfig, allocation BotLoadAllocation, localIndex, botOrdinal int, scenarioJSON string) *workerpb.BotAssignment {
	username := config.Username
	if username == "" {
		username = sanitizeMCUsername(bot.Name)
	}
	scenarioJSON = scenarioRuntimeJSON(scenarioJSON, botOrdinal)
	return &workerpb.BotAssignment{
		BotUuid: bot.UUID, InstanceUuid: instance.UUID, SessionUuid: session.UUID,
		Generation: bot.DesiredStateGeneration, DesiredState: "running", ConfigHash: bot.ConfigHash,
		Name: bot.Name, Host: config.Server, Port: int32(config.Port), Username: username,
		Version: config.Version, Auth: config.Auth, CohortKey: bot.CohortKey, ScenarioJson: scenarioJSON,
		ConnectNotBeforeUnixMs: allocation.ConnectStartAt.Add(time.Duration(localIndex*allocation.ConnectIntervalMS) * time.Millisecond).UnixMilli(),
		CorrelationSeed:        stableBotLoadDigest(session.UUID + "|" + bot.UUID + "|correlation"),
	}
}

func scenarioRuntimeJSON(cohortJSON string, botOrdinal int) string {
	if strings.TrimSpace(cohortJSON) == "" {
		return cohortJSON
	}
	var envelope struct {
		Seed       int64           `json:"seed"`
		BotOrdinal int             `json:"botOrdinal"`
		Scenario   json.RawMessage `json:"scenario"`
	}
	if err := json.Unmarshal([]byte(cohortJSON), &envelope); err != nil || len(envelope.Scenario) == 0 {
		return cohortJSON
	}
	envelope.BotOrdinal = botOrdinal
	raw, err := json.Marshal(envelope)
	if err != nil {
		return cohortJSON
	}
	return string(raw)
}

func botLoadAssignmentConfigHash(assignment *workerpb.BotAssignment) string {
	canonical := struct {
		BotUUID, InstanceUUID, SessionUUID, Name, Host, Username, Version, Auth, CohortKey string
		Port, ConnectNotBefore                                                             int64
		ScenarioJSON, ResumeStepID, CorrelationSeed                                        string
	}{
		BotUUID: assignment.BotUuid, InstanceUUID: assignment.InstanceUuid, SessionUUID: assignment.SessionUuid,
		Name: assignment.Name, Host: assignment.Host, Port: int64(assignment.Port), Username: assignment.Username,
		Version: assignment.Version, Auth: assignment.Auth, CohortKey: assignment.CohortKey,
		ConnectNotBefore: assignment.ConnectNotBeforeUnixMs, ScenarioJSON: assignment.ScenarioJson,
		ResumeStepID: assignment.ResumeStepId, CorrelationSeed: assignment.CorrelationSeed,
	}
	raw, _ := json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Dispatch 派发所有仍为 planned 的批次；每批独立回写，失败不会回滚其他批次。
func (s *BotLoadExecutionService) Dispatch(ctx context.Context, sessionID uint) error {
	prepared, err := s.loadDispatchContext(ctx, sessionID)
	if err != nil {
		return err
	}
	return s.dispatchPrepared(ctx, prepared)
}

func (s *BotLoadExecutionService) dispatchPrepared(ctx context.Context, prepared *botLoadStartPreparation) error {
	var writeErrors []error
	for _, allocation := range prepared.plan.Allocations {
		stopped, err := s.stopIntentRecorded(ctx, prepared.session.ID)
		if err != nil {
			writeErrors = append(writeErrors, err)
			break
		}
		if stopped {
			break
		}
		if err := s.dispatchAllocation(ctx, prepared.session, prepared.plan, prepared.config, allocation); err != nil {
			writeErrors = append(writeErrors, err)
		}
	}
	if err := s.finishStartDispatch(ctx, prepared.session.ID); err != nil {
		writeErrors = append(writeErrors, err)
	}
	return errors.Join(writeErrors...)
}

func (s *BotLoadExecutionService) runDispatch(prepared *botLoadStartPreparation) {
	if err := s.dispatchPrepared(context.Background(), prepared); err != nil {
		slog.Error("Bot 负载后台派发存在数据库回写错误", "runId", prepared.session.ID, "error", err)
	}
}

func (s *BotLoadExecutionService) loadDispatchContext(ctx context.Context, sessionID uint) (*botLoadStartPreparation, error) {
	session, err := s.loadSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	plan, err := decodeStartAllocationPlan(session)
	if err != nil {
		return nil, err
	}
	config, err := parseBotLoadConnectionConfig(session.Config)
	if err != nil {
		return nil, err
	}
	return &botLoadStartPreparation{session: session, plan: plan, config: config}, nil
}

func (s *BotLoadExecutionService) dispatchAllocation(ctx context.Context, session *model.BotStressSession, plan *BotLoadAllocationPlan, config botLoadConnectionConfig, allocation BotLoadAllocation) error {
	batch, claimed, err := s.claimBotLoadBatch(ctx, session.ID, allocation.Ordinal)
	if err != nil || !claimed {
		return err
	}
	bots, err := s.loadBatchBots(ctx, batch.ID)
	if err != nil {
		lastErr := structuredBotLoadError("DB_LOAD_FAILED", "ephemeral_unavailable", err.Error())
		return s.failBotLoadBatchWithoutItems(ctx, batch, lastErr)
	}
	stopped, err := s.stopIntentRecorded(ctx, session.ID)
	if err != nil || stopped {
		return err
	}
	_, cohortJSON, err := prepareBotLoadScenarioAssignments(session)
	if err != nil {
		lastErr := structuredBotLoadError("SCENARIO_INVALID", "conflict", err.Error())
		return s.failBotLoadBatchWithoutItems(ctx, batch, lastErr)
	}
	request := buildStartBotLoadBatchRequest(session, plan, config, allocation, bots, cohortJSON)
	response, rpcErr := s.applyBotLoadBatch(ctx, allocation.ExecutorNodeUUID, request)
	items := normalizeBotLoadDispatchItems(bots, request, response, rpcErr)
	if err := s.persistStartBatchResult(ctx, batch, items); err != nil {
		slog.Error("Bot 负载 RPC 成功后数据库回写失败", "runId", session.ID, "batchId", batch.UUID, "error", err)
		return err
	}
	return nil
}

func (s *BotLoadExecutionService) claimBotLoadBatch(ctx context.Context, sessionID uint, ordinal int) (*model.BotLoadBatch, bool, error) {
	now := s.clock.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.BotLoadBatch{}).
		Where("stress_session_id = ? AND ordinal = ? AND state = ?", sessionID, ordinal, model.BotLoadBatchPlanned).
		Updates(map[string]any{"state": model.BotLoadBatchDispatching, "started_at": gorm.Expr("COALESCE(started_at, ?)", now), "last_error": ""})
	if result.Error != nil {
		return nil, false, fmt.Errorf("领取 Bot 负载批次失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	var batch model.BotLoadBatch
	if err := s.db.WithContext(ctx).Where("stress_session_id = ? AND ordinal = ?", sessionID, ordinal).First(&batch).Error; err != nil {
		return nil, false, fmt.Errorf("读取已领取 Bot 负载批次失败: %w", err)
	}
	return &batch, true, nil
}

func (s *BotLoadExecutionService) loadBatchBots(ctx context.Context, batchID uint) ([]model.Bot, error) {
	var bots []model.Bot
	if err := s.db.WithContext(ctx).Where("load_batch_id = ?", batchID).Order("id ASC").Find(&bots).Error; err != nil {
		return nil, fmt.Errorf("查询 Bot 负载批次成员失败: %w", err)
	}
	return bots, nil
}

func stableBotLoadOrdinals(sessionUUID string, targetBots int) map[string]int {
	ordinals := make(map[string]int, targetBots)
	for ordinal := 1; ordinal <= targetBots; ordinal++ {
		ordinals[stableBotLoadUUID(fmt.Sprintf("%s|bot|%d", sessionUUID, ordinal))] = ordinal
	}
	return ordinals
}

func botLoadOrdinalFromUUID(sessionUUID string, targetBots int, botUUID string) int {
	return stableBotLoadOrdinals(sessionUUID, targetBots)[botUUID]
}

func botLoadAllocationFirstOrdinal(plan *BotLoadAllocationPlan, allocationOrdinal int) int {
	firstOrdinal := 1
	for _, allocation := range plan.Allocations {
		if allocation.Ordinal < allocationOrdinal {
			firstOrdinal += allocation.PlannedCount
		}
	}
	return firstOrdinal
}

func botLoadAllocationLocalIndex(plan *BotLoadAllocationPlan, allocationOrdinal, botOrdinal int) int {
	return max(0, botOrdinal-botLoadAllocationFirstOrdinal(plan, allocationOrdinal))
}

func buildStartBotLoadBatchRequest(session *model.BotStressSession, plan *BotLoadAllocationPlan, config botLoadConnectionConfig, allocation BotLoadAllocation, bots []model.Bot, cohortJSON map[string]string) *workerpb.ApplyBotBatchRequest {
	assignments := make([]*workerpb.BotAssignment, 0, len(bots))
	ordinals := stableBotLoadOrdinals(session.UUID, plan.TargetBots)
	for index := range bots {
		botOrdinal := ordinals[bots[index].UUID]
		localIndex := botLoadAllocationLocalIndex(plan, allocation.Ordinal, botOrdinal)
		assignment := buildRunningBotLoadAssignment(
			&bots[index], session, &session.Instance, config, allocation, localIndex, botOrdinal, cohortJSON[bots[index].CohortKey],
		)
		assignment.ConfigHash = bots[index].ConfigHash
		assignments = append(assignments, assignment)
	}
	return &workerpb.ApplyBotBatchRequest{
		BatchId: allocation.BatchID, IdempotencyKey: allocation.IdempotencyKey,
		ExpectedCapacityGeneration: botLoadPlanGeneration(plan, allocation.ExecutorNodeID), Assignments: assignments,
	}
}

func botLoadPlanGeneration(plan *BotLoadAllocationPlan, nodeID uint) int64 {
	for _, generation := range plan.CapacityGenerations {
		if generation.NodeID == nodeID {
			return generation.CapacityGeneration
		}
	}
	return 0
}

func (s *BotLoadExecutionService) applyBotLoadBatch(ctx context.Context, nodeUUID string, request *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, botLoadApplyTimeout)
	defer cancel()
	return s.dispatcher.ApplyBotBatch(rpcCtx, nodeUUID, request)
}

func normalizeBotLoadDispatchItems(bots []model.Bot, request *workerpb.ApplyBotBatchRequest, response *workerpb.ApplyBotBatchResponse, rpcErr error) []botLoadDispatchItem {
	if rpcErr != nil {
		return failedBotLoadDispatchItems(bots, "RPC_FAILED", "ephemeral_unavailable", rpcErr.Error())
	}
	if response == nil || response.BatchId != request.BatchId || response.IdempotencyKey != request.IdempotencyKey {
		return failedBotLoadDispatchItems(bots, "RESPONSE_MISMATCH", "conflict", "Worker 批次回执与请求不匹配")
	}
	byUUID := make(map[string]*workerpb.ApplyBotBatchItemResult, len(response.Results))
	for _, result := range response.Results {
		if result != nil {
			byUUID[result.BotUuid] = result
		}
	}
	items := make([]botLoadDispatchItem, 0, len(bots))
	for _, bot := range bots {
		items = append(items, dispatchItemFromResult(bot, byUUID[bot.UUID]))
	}
	return items
}

func failedBotLoadDispatchItems(bots []model.Bot, code, status, message string) []botLoadDispatchItem {
	items := make([]botLoadDispatchItem, 0, len(bots))
	lastErr := structuredBotLoadError(code, status, message)
	for _, bot := range bots {
		items = append(items, botLoadDispatchItem{bot: bot, lastErr: lastErr})
	}
	return items
}

func dispatchItemFromResult(bot model.Bot, result *workerpb.ApplyBotBatchItemResult) botLoadDispatchItem {
	if result != nil && result.Accepted {
		return botLoadDispatchItem{bot: bot, accepted: true}
	}
	if result == nil {
		return botLoadDispatchItem{bot: bot, lastErr: structuredBotLoadError("MISSING_RESULT", "ephemeral_unavailable", "Worker 未返回逐项回执")}
	}
	code := result.ErrorCode
	if code == "" {
		code = strings.ToUpper(result.Status)
	}
	return botLoadDispatchItem{bot: bot, lastErr: structuredBotLoadError(code, result.Status, result.Error)}
}

func structuredBotLoadError(code, status, message string) string {
	value := struct {
		Code    string `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}{Code: code, Status: status, Message: message}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func (s *BotLoadExecutionService) persistStartBatchResult(ctx context.Context, batch *model.BotLoadBatch, items []botLoadDispatchItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session model.BotStressSession
		if err := tx.Select("id", "status", "last_error").First(&session, batch.StressSessionID).Error; err != nil {
			return fmt.Errorf("读取 Bot 负载会话派发状态失败: %w", err)
		}
		if session.Status == model.BotStressSessionStopped || botLoadStopIntentRecorded(session.LastError) {
			return nil
		}
		accepted, failed, lastErr := summarizeBotLoadItems(items)
		for _, item := range items {
			updates := map[string]any{"status": model.BotStatusError, "last_error": item.lastErr}
			if item.accepted {
				updates = map[string]any{"status": model.BotStatusConnecting, "last_error": ""}
			}
			if err := tx.Model(&model.Bot{}).
				Where("id = ? AND desired_state_generation = ?", item.bot.ID, item.bot.DesiredStateGeneration).
				Updates(updates).Error; err != nil {
				return fmt.Errorf("回写 Bot 逐项派发结果失败: %w", err)
			}
		}
		state := model.BotLoadBatchFailed
		if accepted > 0 {
			state = model.BotLoadBatchRunning
		}
		return tx.Model(batch).Updates(map[string]any{
			"accepted_count": accepted, "failed_count": failed, "state": state, "last_error": lastErr,
		}).Error
	})
}

func summarizeBotLoadItems(items []botLoadDispatchItem) (int, int, string) {
	accepted, failed, lastErr := 0, 0, ""
	for _, item := range items {
		if item.accepted {
			accepted++
			continue
		}
		failed++
		if lastErr == "" {
			lastErr = item.lastErr
		}
	}
	return accepted, failed, lastErr
}

func (s *BotLoadExecutionService) failBotLoadBatchWithoutItems(ctx context.Context, batch *model.BotLoadBatch, lastErr string) error {
	result := s.db.WithContext(ctx).Model(batch).Updates(map[string]any{
		"failed_count": batch.PlannedCount, "state": model.BotLoadBatchFailed, "last_error": lastErr,
	})
	if result.Error != nil {
		return fmt.Errorf("回写 Bot 负载批次读取失败状态失败: %w", result.Error)
	}
	return nil
}

func (s *BotLoadExecutionService) finishStartDispatch(ctx context.Context, sessionID uint) error {
	var summary struct {
		Accepted int
		Failed   int
		LastErr  string
	}
	if err := s.db.WithContext(ctx).Model(&model.BotLoadBatch{}).
		Select("COALESCE(SUM(accepted_count),0) AS accepted, COALESCE(SUM(failed_count),0) AS failed, COALESCE(MAX(NULLIF(last_error,'')),'') AS last_err").
		Where("stress_session_id = ?", sessionID).Scan(&summary).Error; err != nil {
		return fmt.Errorf("汇总 Bot 负载派发结果失败: %w", err)
	}
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Select("id", "status", "last_error").First(&session, sessionID).Error; err != nil {
		return fmt.Errorf("读取 Bot 负载会话最终派发状态失败: %w", err)
	}
	if session.Status == model.BotStressSessionStopped || botLoadStopIntentRecorded(session.LastError) {
		return nil
	}
	now := s.clock.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.BotStressSession{}).
		Where("id = ? AND status <> ? AND last_error = ?", sessionID, model.BotStressSessionStopped, session.LastError).
		Updates(map[string]any{
			"status": model.BotStressSessionRunning, "succeeded": summary.Accepted, "failed": summary.Failed,
			"last_error": summary.LastErr, "started_at": gorm.Expr("COALESCE(started_at, ?)", now), "ended_at": nil,
		})
	if result.Error != nil {
		return fmt.Errorf("更新 Bot 负载会话派发结果失败: %w", result.Error)
	}
	return nil
}

// Stop 持久化 stopped desired generation 后提交按执行节点分组的后台批量停止。
func (s *BotLoadExecutionService) Stop(ctx context.Context, sessionID uint, reasons ...string) (*model.BotStressSession, error) {
	if err := s.validateDependencies(); err != nil {
		return nil, err
	}
	session, err := s.loadSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.Status == model.BotStressSessionStopped {
		return session, nil
	}
	reason := ""
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	count, err := s.prepareStopIntent(ctx, sessionID, reason)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		if err := s.finishStopSession(ctx, sessionID); err != nil {
			return nil, err
		}
		return s.loadSession(ctx, sessionID)
	}
	session, err = s.loadSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if botLoadStopIntentState(session.LastError) == "waiting_runtime" {
		return session, nil
	}
	if err := s.submitLifecycle(sessionID, "stop", func() { s.runStopDispatch(sessionID) }); err != nil {
		return nil, err
	}
	return s.loadSession(ctx, sessionID)
}

func (s *BotLoadExecutionService) prepareStopIntent(ctx context.Context, sessionID uint, reason string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session model.BotStressSession
		if err := tx.Select("id", "last_error").First(&session, sessionID).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Bot{}).Where("stress_session_id = ? AND status <> ?", sessionID, model.BotStatusStopped).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 || botLoadStopIntentRecorded(session.LastError) {
			return nil
		}
		intent := botLoadStopSessionError("dispatching", "", reason)
		claimed := tx.Model(&model.BotStressSession{}).
			Where("id = ? AND last_error = ?", sessionID, session.LastError).
			Update("last_error", intent)
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected == 0 {
			return nil
		}
		return tx.Model(&model.Bot{}).Where("stress_session_id = ? AND status <> ?", sessionID, model.BotStatusStopped).
			Update("desired_state_generation", gorm.Expr("desired_state_generation + 1")).Error
	})
	if err != nil {
		return 0, fmt.Errorf("保存 Bot stopped desired intent 失败: %w", err)
	}
	return count, nil
}

func (s *BotLoadExecutionService) stopIntentRecorded(ctx context.Context, sessionID uint) (bool, error) {
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Select("id", "status", "last_error").First(&session, sessionID).Error; err != nil {
		return false, fmt.Errorf("检查 Bot 负载停止意图失败: %w", err)
	}
	return session.Status == model.BotStressSessionStopped || botLoadStopIntentRecorded(session.LastError), nil
}

func botLoadStopIntentRecorded(lastError string) bool {
	return botLoadStopIntentState(lastError) != ""
}

func botLoadStopIntentState(lastError string) string {
	var value struct {
		Operation string `json:"operation"`
		State     string `json:"state"`
	}
	if json.Unmarshal([]byte(lastError), &value) != nil || value.Operation != "stop" {
		return ""
	}
	return value.State
}

func botLoadStopSessionError(state, message string, reasons ...string) string {
	reason := ""
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	value := struct {
		Operation string `json:"operation"`
		State     string `json:"state"`
		Message   string `json:"message,omitempty"`
		Reason    string `json:"reason,omitempty"`
	}{Operation: "stop", State: state, Message: message, Reason: reason}
	raw, _ := json.Marshal(value)
	return string(raw)
}

// DispatchStop 按执行节点和 50 条上限下发停止；accepted 只清派发错误，runtime 状态等待 Fleet 归真。
func (s *BotLoadExecutionService) DispatchStop(ctx context.Context, sessionID uint) error {
	session, err := s.loadSession(ctx, sessionID)
	if err != nil {
		return err
	}
	groups, err := s.loadStopGroups(ctx, sessionID)
	if err != nil {
		return err
	}
	var writeErrors []error
	for _, group := range groups {
		for start := 0; start < len(group.bots); start += maxBotLoadBatchSize {
			end := min(start+maxBotLoadBatchSize, len(group.bots))
			if err := s.dispatchStopChunk(ctx, session, group.node, group.bots[start:end]); err != nil {
				writeErrors = append(writeErrors, err)
			}
		}
	}
	if err := s.reconcileStoppedBatches(ctx, sessionID); err != nil {
		writeErrors = append(writeErrors, err)
	}
	if err := s.finishStopSession(ctx, sessionID); err != nil {
		writeErrors = append(writeErrors, err)
	}
	return errors.Join(writeErrors...)
}

func (s *BotLoadExecutionService) runStopDispatch(sessionID uint) {
	if err := s.DispatchStop(context.Background(), sessionID); err != nil {
		slog.Error("Bot 负载后台停止存在数据库回写错误", "runId", sessionID, "error", err)
	}
}

func (s *BotLoadExecutionService) loadStopGroups(ctx context.Context, sessionID uint) ([]botLoadStopGroup, error) {
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Select("id", "last_error").First(&session, sessionID).Error; err != nil {
		return nil, fmt.Errorf("查询停止会话状态失败: %w", err)
	}
	var bots []model.Bot
	query := s.db.WithContext(ctx).Preload("Instance.Node").Preload("ExecutorNode").
		Where("stress_session_id = ? AND status <> ?", sessionID, model.BotStatusStopped)
	if botLoadStopIntentState(session.LastError) == "failed" {
		query = query.Where("last_error <> ''")
	}
	if err := query.Order("uuid ASC").Find(&bots).Error; err != nil {
		return nil, fmt.Errorf("查询待停止 Bot 失败: %w", err)
	}
	byNode := make(map[uint]*botLoadStopGroup)
	for index := range bots {
		node, _, err := s.resolver.Resolve(&bots[index])
		if err != nil {
			return nil, err
		}
		group := byNode[node.ID]
		if group == nil {
			group = &botLoadStopGroup{node: *node}
			byNode[node.ID] = group
		}
		group.bots = append(group.bots, bots[index])
	}
	return sortedBotLoadStopGroups(byNode), nil
}

func sortedBotLoadStopGroups(byNode map[uint]*botLoadStopGroup) []botLoadStopGroup {
	ids := make([]uint, 0, len(byNode))
	for nodeID := range byNode {
		ids = append(ids, nodeID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	groups := make([]botLoadStopGroup, 0, len(ids))
	for _, nodeID := range ids {
		groups = append(groups, *byNode[nodeID])
	}
	return groups
}

func (s *BotLoadExecutionService) dispatchStopChunk(ctx context.Context, session *model.BotStressSession, node model.Node, bots []model.Bot) error {
	request := buildStopBotLoadBatchRequest(session, node, bots)
	response, rpcErr := s.applyBotLoadBatch(ctx, node.UUID, request)
	items := normalizeBotLoadDispatchItems(bots, request, response, rpcErr)
	if err := s.persistStopBatchResult(ctx, items); err != nil {
		slog.Error("Bot 负载停止 RPC 后数据库回写失败", "runId", session.ID, "nodeId", node.ID, "error", err)
		return err
	}
	return nil
}

func buildStopBotLoadBatchRequest(session *model.BotStressSession, node model.Node, bots []model.Bot) *workerpb.ApplyBotBatchRequest {
	identity := botLoadStopIdentity(session.UUID, node.ID, bots)
	assignments := make([]*workerpb.BotAssignment, 0, len(bots))
	for _, bot := range bots {
		assignments = append(assignments, &workerpb.BotAssignment{
			BotUuid: bot.UUID, SessionUuid: session.UUID, Generation: bot.DesiredStateGeneration,
			DesiredState: "stopped", ConfigHash: bot.ConfigHash,
		})
	}
	return &workerpb.ApplyBotBatchRequest{
		BatchId: stableBotLoadUUID(identity), IdempotencyKey: "bot-load-stop-" + stableBotLoadDigest(identity), Assignments: assignments,
	}
}

func botLoadStopIdentity(sessionUUID string, nodeID uint, bots []model.Bot) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "stop|%s|%d", sessionUUID, nodeID)
	for _, bot := range bots {
		fmt.Fprintf(&builder, "|%s:%d", bot.UUID, bot.DesiredStateGeneration)
	}
	return builder.String()
}

func (s *BotLoadExecutionService) persistStopBatchResult(ctx context.Context, items []botLoadDispatchItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			updates := map[string]any{"last_error": item.lastErr}
			if item.accepted {
				updates = map[string]any{"last_error": ""}
			}
			if err := tx.Model(&model.Bot{}).Where("id = ?", item.bot.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("回写 Bot 停止逐项结果失败: %w", err)
			}
		}
		return nil
	})
}

func (s *BotLoadExecutionService) reconcileStoppedBatches(ctx context.Context, sessionID uint) error {
	var batches []model.BotLoadBatch
	if err := s.db.WithContext(ctx).Where("stress_session_id = ?", sessionID).Find(&batches).Error; err != nil {
		return fmt.Errorf("查询停止相关批次失败: %w", err)
	}
	for index := range batches {
		if err := s.reconcileStoppedBatch(ctx, &batches[index]); err != nil {
			return err
		}
	}
	return nil
}

func (s *BotLoadExecutionService) reconcileStoppedBatch(ctx context.Context, batch *model.BotLoadBatch) error {
	var bots []model.Bot
	if err := s.db.WithContext(ctx).Where("load_batch_id = ?", batch.ID).Find(&bots).Error; err != nil {
		return fmt.Errorf("查询停止批次成员失败: %w", err)
	}
	remaining, lastErr := 0, ""
	for _, bot := range bots {
		if bot.Status == model.BotStatusStopped {
			continue
		}
		remaining++
		if lastErr == "" && bot.LastError != "" {
			lastErr = bot.LastError
		}
	}
	updates := map[string]any{"state": model.BotLoadBatchStopped, "last_error": "", "ended_at": s.clock.Now().UTC()}
	if remaining > 0 && lastErr == "" {
		updates = map[string]any{"state": model.BotLoadBatchRunning, "last_error": "", "ended_at": nil}
	}
	if remaining > 0 && lastErr != "" {
		updates = map[string]any{"state": model.BotLoadBatchFailed, "last_error": lastErr, "failed_count": max(batch.FailedCount, remaining), "ended_at": nil}
	}
	if err := s.db.WithContext(ctx).Model(batch).Updates(updates).Error; err != nil {
		return fmt.Errorf("更新停止批次状态失败: %w", err)
	}
	return nil
}

func (s *BotLoadExecutionService) finishStopSession(ctx context.Context, sessionID uint) error {
	var bots []model.Bot
	if err := s.db.WithContext(ctx).Where("stress_session_id = ?", sessionID).Find(&bots).Error; err != nil {
		return fmt.Errorf("查询停止会话 Bot 状态失败: %w", err)
	}
	remaining, lastErr := 0, ""
	for _, bot := range bots {
		if bot.Status == model.BotStatusStopped {
			continue
		}
		remaining++
		if lastErr == "" && bot.LastError != "" {
			lastErr = bot.LastError
		}
	}
	updates := map[string]any{"status": model.BotStressSessionStopped, "last_error": "", "ended_at": s.clock.Now().UTC()}
	if remaining > 0 && lastErr == "" {
		updates = map[string]any{
			"status": model.BotStressSessionRunning, "last_error": botLoadStopSessionError("waiting_runtime", ""), "ended_at": nil,
		}
	}
	if remaining > 0 && lastErr != "" {
		updates = map[string]any{
			"status": model.BotStressSessionError, "last_error": botLoadStopSessionError("failed", lastErr), "ended_at": nil,
		}
	}
	if err := s.db.WithContext(ctx).Model(&model.BotStressSession{}).Where("id = ?", sessionID).Updates(updates).Error; err != nil {
		return fmt.Errorf("更新 Bot 停止会话状态失败: %w", err)
	}
	return nil
}

// ReconcileBotFleetRuntimeState 在可信 Fleet 更新后检查停止意图是否已经实际完成。
func (s *BotLoadExecutionService) ReconcileBotFleetRuntimeState(ctx context.Context, sessionUUID string) error {
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Where("uuid = ?", sessionUUID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("查询 Bot Fleet runtime 会话失败: %w", err)
	}
	if session.Status != model.BotStressSessionStopped && !botLoadStopIntentRecorded(session.LastError) {
		return nil
	}
	if session.Status != model.BotStressSessionStopped {
		if err := s.reconcileStoppedBatches(ctx, session.ID); err != nil {
			return err
		}
		if err := s.finishStopSession(ctx, session.ID); err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Select("status").First(&session, session.ID).Error; err != nil {
			return err
		}
	}
	if session.Status == model.BotStressSessionStopped && s.subscriptions != nil {
		s.subscriptions.StopSession(sessionUUID)
	}
	return nil
}

// ReconcileBotFleetSnapshot 以 CP desired 真源清理额外 runtime，不实现 FR-354 自动恢复。
func (s *BotLoadExecutionService) ReconcileBotFleetSnapshot(ctx context.Context, nodeID uint, nodeUUID, sessionUUID string, snapshot *workerpb.GetBotFleetSnapshotResponse) error {
	if snapshot == nil {
		return nil
	}
	key := "reconcile:" + stableBotLoadDigest(fmt.Sprintf("%d|%s|%s|%d|%d", nodeID, nodeUUID, sessionUUID, snapshot.CapacityGeneration, snapshot.ObservedAtUnixMs))
	if !s.beginTask(key) {
		return nil
	}
	defer s.finishTask(key)
	items, err := s.buildBotLoadReconcileItems(ctx, nodeID, sessionUUID, snapshot)
	if err != nil {
		return err
	}
	if err := s.convergeSnapshotBatches(ctx, sessionUUID, snapshot); err != nil {
		return err
	}
	return s.dispatchBotLoadReconcileItems(ctx, nodeUUID, sessionUUID, snapshot.CapacityGeneration, items)
}

func (s *BotLoadExecutionService) buildBotLoadReconcileItems(ctx context.Context, nodeID uint, sessionUUID string, snapshot *workerpb.GetBotFleetSnapshotResponse) ([]botLoadReconcileItem, error) {
	session, bots, err := s.loadReconcileDesired(ctx, nodeID, sessionUUID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	byUUID := make(map[string]*workerpb.BotRuntimeSnapshot, len(snapshot.Bots))
	for _, runtime := range snapshot.Bots {
		if runtime != nil {
			byUUID[runtime.BotUuid] = runtime
		}
	}
	items := desiredBotLoadReconcileItems(session, bots, byUUID)
	for _, runtime := range snapshot.Bots {
		if runtime != nil && !containsBotLoadBot(bots, runtime.BotUuid) {
			items = append(items, extraBotLoadReconcileItem(runtime))
		}
	}
	return items, nil
}

func (s *BotLoadExecutionService) loadReconcileDesired(ctx context.Context, nodeID uint, sessionUUID string) (*model.BotStressSession, []model.Bot, error) {
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Where("uuid = ?", sessionUUID).First(&session).Error; err != nil {
		return nil, nil, err
	}
	var bots []model.Bot
	query := s.db.WithContext(ctx).Where("stress_session_id = ?", session.ID).
		Where("executor_node_id = ? OR (executor_node_id IS NULL AND instance_id IN (?))", nodeID,
			s.db.Model(&model.Instance{}).Select("id").Where("node_id = ?", nodeID))
	if err := query.Order("name ASC").Find(&bots).Error; err != nil {
		return nil, nil, fmt.Errorf("查询 snapshot desired Bot 失败: %w", err)
	}
	return &session, bots, nil
}

func desiredBotLoadReconcileItems(session *model.BotStressSession, bots []model.Bot, runtime map[string]*workerpb.BotRuntimeSnapshot) []botLoadReconcileItem {
	if session == nil || (session.Status != model.BotStressSessionStopped && !botLoadStopIntentRecorded(session.LastError)) {
		return nil
	}
	items := make([]botLoadReconcileItem, 0, len(bots))
	for index := range bots {
		bot := &bots[index]
		if observed := runtime[bot.UUID]; observed != nil {
			items = append(items, stoppedBotLoadReconcileItem(bot, session.UUID, observed, botLoadReconcileStopped))
		}
	}
	return items
}

func stoppedBotLoadReconcileItem(bot *model.Bot, sessionUUID string, observed *workerpb.BotRuntimeSnapshot, mode botLoadReconcileMode) botLoadReconcileItem {
	generation := bot.DesiredStateGeneration
	if mode == botLoadReconcileCleanup && observed.Generation >= generation {
		generation = observed.Generation + 1
	}
	return botLoadReconcileItem{assignment: &workerpb.BotAssignment{
		BotUuid: bot.UUID, SessionUuid: sessionUUID, Generation: generation,
		DesiredState: "stopped", ConfigHash: bot.ConfigHash,
	}, bot: bot, mode: mode}
}

func extraBotLoadReconcileItem(runtime *workerpb.BotRuntimeSnapshot) botLoadReconcileItem {
	return botLoadReconcileItem{assignment: &workerpb.BotAssignment{
		BotUuid: runtime.BotUuid, SessionUuid: runtime.SessionUuid, Generation: runtime.Generation + 1,
		DesiredState: "stopped", ConfigHash: runtime.ConfigHash,
	}, mode: botLoadReconcileCleanup}
}

func containsBotLoadBot(bots []model.Bot, botUUID string) bool {
	for _, bot := range bots {
		if bot.UUID == botUUID {
			return true
		}
	}
	return false
}

func (s *BotLoadExecutionService) dispatchBotLoadReconcileItems(ctx context.Context, nodeUUID, sessionUUID string, generation int64, items []botLoadReconcileItem) error {
	var errs []error
	for start := 0; start < len(items); start += maxBotLoadBatchSize {
		end := min(start+maxBotLoadBatchSize, len(items))
		chunk := items[start:end]
		request := buildBotLoadReconcileRequest(sessionUUID, generation, chunk)
		response, rpcErr := s.applyBotLoadBatch(ctx, nodeUUID, request)
		if err := s.persistBotLoadReconcileResult(ctx, chunk, request, response, rpcErr); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func buildBotLoadReconcileRequest(sessionUUID string, generation int64, items []botLoadReconcileItem) *workerpb.ApplyBotBatchRequest {
	assignments := make([]*workerpb.BotAssignment, 0, len(items))
	for _, item := range items {
		assignments = append(assignments, item.assignment)
	}
	raw, _ := json.Marshal(assignments)
	identity := fmt.Sprintf("reconcile|%s|%d|%s", sessionUUID, generation, stableBotLoadDigest(string(raw)))
	expected := generation
	for _, item := range items {
		if item.assignment.DesiredState == "stopped" {
			expected = 0
			break
		}
	}
	return &workerpb.ApplyBotBatchRequest{
		BatchId: stableBotLoadUUID(identity), IdempotencyKey: "bot-load-reconcile-" + stableBotLoadDigest(identity),
		ExpectedCapacityGeneration: expected, Assignments: assignments,
	}
}

func (s *BotLoadExecutionService) persistBotLoadReconcileResult(ctx context.Context, items []botLoadReconcileItem, request *workerpb.ApplyBotBatchRequest, response *workerpb.ApplyBotBatchResponse, rpcErr error) error {
	bots := make([]model.Bot, 0, len(items))
	byUUID := make(map[string]botLoadReconcileItem, len(items))
	for _, item := range items {
		byUUID[item.assignment.BotUuid] = item
		if item.bot != nil {
			bots = append(bots, *item.bot)
		}
	}
	results := normalizeBotLoadDispatchItems(bots, request, response, rpcErr)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, result := range results {
			item := byUUID[result.bot.UUID]
			if err := persistBotLoadReconcileItem(tx, item, result); err != nil {
				return err
			}
		}
		return nil
	})
}

func persistBotLoadReconcileItem(tx *gorm.DB, item botLoadReconcileItem, result botLoadDispatchItem) error {
	if item.bot == nil {
		return nil
	}
	updates := map[string]any{}
	switch {
	case result.accepted:
		updates = map[string]any{"last_error": ""}
	case !result.accepted:
		updates = map[string]any{"last_error": result.lastErr}
	}
	if err := tx.Model(&model.Bot{}).Where("id = ?", item.bot.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("回写 snapshot reconcile 结果失败: %w", err)
	}
	return nil
}

func (s *BotLoadExecutionService) convergeSnapshotBatches(ctx context.Context, sessionUUID string, snapshot *workerpb.GetBotFleetSnapshotResponse) error {
	if len(snapshot.Bots) == 0 {
		return nil
	}
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Where("uuid = ?", sessionUUID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	matching := make(map[uint]int)
	for _, runtime := range snapshot.Bots {
		batchID, ok := s.matchSnapshotBotBatch(ctx, session.ID, runtime)
		if ok {
			matching[batchID]++
		}
	}
	for batchID, accepted := range matching {
		updates := map[string]any{"accepted_count": accepted, "state": model.BotLoadBatchRunning, "last_error": ""}
		if err := s.db.WithContext(ctx).Model(&model.BotLoadBatch{}).
			Where("id = ? AND state = ?", batchID, model.BotLoadBatchDispatching).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("按 FleetSnapshot 收敛批次回写失败: %w", err)
		}
	}
	if session.Status != model.BotStressSessionStopped && !botLoadStopIntentRecorded(session.LastError) {
		return s.finishStartDispatch(ctx, session.ID)
	}
	return nil
}

func (s *BotLoadExecutionService) matchSnapshotBotBatch(ctx context.Context, sessionID uint, runtime *workerpb.BotRuntimeSnapshot) (uint, bool) {
	if runtime == nil {
		return 0, false
	}
	var bot model.Bot
	if err := s.db.WithContext(ctx).Where("stress_session_id = ? AND uuid = ?", sessionID, runtime.BotUuid).First(&bot).Error; err != nil {
		return 0, false
	}
	if bot.LoadBatchID == nil || runtime.Generation != bot.DesiredStateGeneration || runtime.ConfigHash != bot.ConfigHash {
		return 0, false
	}
	return *bot.LoadBatchID, true
}

func (s *BotLoadExecutionService) loadSession(ctx context.Context, sessionID uint) (*model.BotStressSession, error) {
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Preload("Instance").First(&session, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, fmt.Errorf("查询 Bot 负载会话失败: %w", err)
	}
	return &session, nil
}

func (s *BotLoadExecutionService) sessionHasBatches(ctx context.Context, sessionID uint) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.BotLoadBatch{}).Where("stress_session_id = ?", sessionID).Count(&count).Error; err != nil {
		return false, fmt.Errorf("检查 Bot 负载批次失败: %w", err)
	}
	return count > 0, nil
}

func (s *BotLoadExecutionService) submitLifecycle(sessionID uint, identity string, task func()) error {
	key := fmt.Sprintf("lifecycle:%d", sessionID)
	s.taskMu.Lock()
	queue := s.serialTasks[key]
	if queue != nil {
		if _, exists := queue.identities[identity]; exists {
			s.taskMu.Unlock()
			return nil
		}
		queue.identities[identity] = struct{}{}
		queue.pending = append(queue.pending, botLoadSerialTask{identity: identity, task: task})
		s.taskMu.Unlock()
		return nil
	}
	queue = &botLoadSerialQueue{
		pending:    []botLoadSerialTask{{identity: identity, task: task}},
		identities: map[string]struct{}{identity: {}},
	}
	s.serialTasks[key] = queue
	s.taskMu.Unlock()
	if err := s.runner.Submit(func() { s.drainLifecycle(key) }); err != nil {
		s.taskMu.Lock()
		delete(s.serialTasks, key)
		s.taskMu.Unlock()
		return fmt.Errorf("提交 Bot 负载后台任务失败: %w", err)
	}
	return nil
}

func (s *BotLoadExecutionService) drainLifecycle(key string) {
	for {
		s.taskMu.Lock()
		queue := s.serialTasks[key]
		if queue == nil || len(queue.pending) == 0 {
			delete(s.serialTasks, key)
			s.taskMu.Unlock()
			return
		}
		current := queue.pending[0]
		queue.pending = queue.pending[1:]
		s.taskMu.Unlock()

		current.task()

		s.taskMu.Lock()
		queue = s.serialTasks[key]
		if queue != nil {
			delete(queue.identities, current.identity)
			if len(queue.pending) == 0 {
				delete(s.serialTasks, key)
				s.taskMu.Unlock()
				return
			}
		}
		s.taskMu.Unlock()
	}
}

func (s *BotLoadExecutionService) submitOnce(key string, task func()) error {
	if !s.beginTask(key) {
		return nil
	}
	err := s.runner.Submit(func() {
		defer s.finishTask(key)
		task()
	})
	if err != nil {
		s.finishTask(key)
		return fmt.Errorf("提交 Bot 负载后台任务失败: %w", err)
	}
	return nil
}

func (s *BotLoadExecutionService) beginTask(key string) bool {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if _, exists := s.tasks[key]; exists {
		return false
	}
	s.tasks[key] = struct{}{}
	return true
}

func (s *BotLoadExecutionService) finishTask(key string) {
	s.taskMu.Lock()
	delete(s.tasks, key)
	s.taskMu.Unlock()
}
