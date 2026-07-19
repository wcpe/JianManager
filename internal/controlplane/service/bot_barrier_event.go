package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const (
	barrierArrivedEventType  = "barrier-arrived"
	barrierSchedulerInterval = 20 * time.Millisecond
)

type barrierArrivedPayload struct {
	Type           string                 `json:"type"`
	StageIndex     int                    `json:"stageIndex"`
	CohortKey      string                 `json:"cohortKey"`
	BarrierKey     string                 `json:"barrierKey"`
	Round          int64                  `json:"round"`
	Release        ScenarioBarrierRelease `json:"release"`
	TimeoutPolicy  string                 `json:"timeoutPolicy,omitempty"`
	DeadlineUnixMS int64                  `json:"deadlineUnixMs"`
}

// BarrierExpectedBotProvider 在首次到达时从当前运行分组冻结 Bot 与 generation。
type BarrierExpectedBotProvider interface {
	ExpectedBots(ctx context.Context, runID, cohortKey string) (map[string]int64, error)
}

type gormBarrierExpectedBotProvider struct{ db *gorm.DB }

func (p gormBarrierExpectedBotProvider) ExpectedBots(ctx context.Context, runID, cohortKey string) (map[string]int64, error) {
	type expectedBotRow struct {
		UUID       string
		Generation int64
	}
	var rows []expectedBotRow
	err := p.db.WithContext(ctx).Table("bots").
		Select("bots.uuid, bots.desired_state_generation AS generation").
		Joins("JOIN bot_stress_sessions ON bot_stress_sessions.id = bots.stress_session_id").
		Where("bot_stress_sessions.uuid = ? AND bots.cohort_key = ? AND bots.deleted_at IS NULL", runID, cohortKey).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	expected := make(map[string]int64, len(rows))
	for _, row := range rows {
		expected[row.UUID] = row.Generation
	}
	return expected, nil
}

// ScenarioActionEventService 将动作结果持久化与单一屏障调度器串在同一 Fleet 事件入口。
type ScenarioActionEventService struct {
	results  *ActionResultService
	barriers *BarrierCoordinator
	router   *ActionSignalRouter
	expected BarrierExpectedBotProvider

	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once

	runMu      sync.Mutex
	runContext map[string]context.Context
	runCancel  map[string]context.CancelFunc
}

// NewScenarioActionEventService 创建 FR-352 场景动作事件处理链。
func NewScenarioActionEventService(results *ActionResultService, barriers *BarrierCoordinator, router *ActionSignalRouter, expected BarrierExpectedBotProvider) *ScenarioActionEventService {
	ctx, cancel := context.WithCancel(context.Background())
	service := &ScenarioActionEventService{
		results: results, barriers: barriers, router: router, expected: expected,
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		runContext: make(map[string]context.Context), runCancel: make(map[string]context.CancelFunc),
	}
	service.wg.Add(1)
	go service.schedule()
	return service
}

// NewGRPCScenarioActionEventService 使用数据库与既有 Worker 连接池依赖创建事件处理链。
func NewGRPCScenarioActionEventService(db *gorm.DB, results *ActionResultService, barriers *BarrierCoordinator, router *ActionSignalRouter) *ScenarioActionEventService {
	return NewScenarioActionEventService(results, barriers, router, gormBarrierExpectedBotProvider{db: db})
}

// Ingest 对普通动作直接持久化；屏障到达先以场景快照校验权威定义，再冻结集合并交集中调度。
func (s *ScenarioActionEventService) Ingest(ctx context.Context, executorNodeID uint, expectedSessionUUID string, event *workerpb.BotActionEvent) (ActionResultIngestResult, error) {
	payload, isBarrier, diagnostic := parseBarrierArrived(event)
	if diagnostic != "" {
		return actionIngestResult(ActionResultIgnoredInvalid, event, diagnostic), nil
	}
	if !isBarrier {
		return s.results.Ingest(ctx, executorNodeID, expectedSessionUUID, event)
	}
	bot, scope, definition, diagnostic, err := s.authoritativeBarrier(ctx, executorNodeID, expectedSessionUUID, event, payload)
	if err != nil {
		return ActionResultIngestResult{}, err
	}
	if diagnostic != "" {
		return actionIngestResult(ActionResultIgnoredInvalid, event, diagnostic), nil
	}
	loader := func(loadCtx context.Context) (map[string]int64, error) {
		return s.expected.ExpectedBots(loadCtx, scope.RunID, scope.CohortKey)
	}
	if err := s.barriers.EnsureLazy(ctx, definition, loader); err != nil {
		return ActionResultIngestResult{}, fmt.Errorf("冻结屏障期望 Bot 集合失败: %w", err)
	}
	if !s.barriers.Accepts(scope, bot.UUID, event.Generation) {
		return actionIngestResult(ActionResultIgnoredIdentity, event, "barrier-arrived Bot 或 generation 不在冻结候选集合内"), nil
	}
	result, err := s.results.Ingest(ctx, executorNodeID, expectedSessionUUID, event)
	if err != nil || (result.Decision != ActionResultApplied && result.Decision != ActionResultIgnoredDuplicate) {
		return result, err
	}
	barrierResult := s.barriers.Arrive(BarrierArrival{
		Scope: scope, BotUUID: event.BotUuid, Generation: event.Generation,
		ActionRunID: event.ActionRunId, CorrelationToken: event.CorrelationToken,
	})
	result.Diagnostic = fmt.Sprintf("%s；屏障=%s", result.Diagnostic, barrierResult.Decision)
	s.runCtx(scope.RunID)
	s.notify()
	return result, nil
}

func parseBarrierArrived(event *workerpb.BotActionEvent) (barrierArrivedPayload, bool, string) {
	if event == nil || strings.TrimSpace(event.ResultJson) == "" {
		return barrierArrivedPayload{}, false, ""
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(event.ResultJson), &discriminator); err != nil || discriminator.Type != barrierArrivedEventType {
		return barrierArrivedPayload{}, false, ""
	}
	if event.Status != string(model.BotLoadActionRunning) {
		return barrierArrivedPayload{}, true, "barrier-arrived 必须是 running 动作事件"
	}
	var payload barrierArrivedPayload
	if err := json.Unmarshal([]byte(event.ResultJson), &payload); err != nil || payload.Round <= 0 {
		return payload, true, "barrier-arrived payload 非法"
	}
	return payload, true, ""
}

func (s *ScenarioActionEventService) authoritativeBarrier(ctx context.Context, executorNodeID uint, expectedSessionUUID string, event *workerpb.BotActionEvent, payload barrierArrivedPayload) (*model.Bot, BarrierScope, BarrierDefinition, string, error) {
	if s == nil || s.results == nil || s.barriers == nil || s.router == nil || s.expected == nil {
		return nil, BarrierScope{}, BarrierDefinition{}, "", errors.New("屏障协调器未完整装配")
	}
	status, diagnostic := validateActionEvent(event)
	if diagnostic != "" || status != model.BotLoadActionRunning {
		return nil, BarrierScope{}, BarrierDefinition{}, diagnostic, nil
	}
	bot, err := s.results.repository.FindBot(ctx, event.BotUuid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, BarrierScope{}, BarrierDefinition{}, "Bot UUID 不存在", nil
		}
		return nil, BarrierScope{}, BarrierDefinition{}, "", fmt.Errorf("查询屏障 Bot 失败: %w", err)
	}
	if diagnostic = validateActionIdentity(bot, executorNodeID, expectedSessionUUID, event); diagnostic != "" {
		return nil, BarrierScope{}, BarrierDefinition{}, diagnostic, nil
	}
	if bot.StressSession == nil || strings.TrimSpace(bot.StressSession.ScenarioSnapshot) == "" {
		return nil, BarrierScope{}, BarrierDefinition{}, "屏障运行缺少场景快照", nil
	}
	action, diagnostic := authoritativeBarrierAction(bot.StressSession.ScenarioSnapshot, bot.CohortKey, event.StepId)
	if diagnostic != "" {
		return nil, BarrierScope{}, BarrierDefinition{}, diagnostic, nil
	}
	policy := action.TimeoutPolicy
	if policy == "" {
		policy = "fail"
	}
	observedAt := s.barriers.clock.Now().UTC()
	if event.ObservedAtUnixMs > 0 {
		observedAt = time.UnixMilli(event.ObservedAtUnixMs).UTC()
	}
	deadline := observedAt.Add(time.Duration(*action.TimeoutMS) * time.Millisecond)
	if payload.TimeoutPolicy == "" {
		payload.TimeoutPolicy = policy
	}
	if !matchesAuthoritativeBarrierPayload(payload, bot.CohortKey, action, policy, deadline) {
		return nil, BarrierScope{}, BarrierDefinition{}, "barrier-arrived payload 与权威场景定义不一致", nil
	}
	scope := BarrierScope{
		RunID: event.SessionUuid, StageIndex: 0, CohortKey: bot.CohortKey,
		BarrierKey: action.Key, Round: payload.Round,
	}
	definition := BarrierDefinition{
		Scope: scope, Release: action.Release, TimeoutPolicy: policy, Deadline: deadline,
		TimeoutBudget: time.Duration(*action.TimeoutMS) * time.Millisecond,
	}
	return bot, scope, definition, "", nil
}

func matchesAuthoritativeBarrierPayload(payload barrierArrivedPayload, cohortKey string, action *BarrierAction, policy string, deadline time.Time) bool {
	return payload.Type == barrierArrivedEventType && payload.StageIndex == 0 &&
		payload.CohortKey == cohortKey && payload.BarrierKey == action.Key &&
		payload.Release == action.Release && payload.TimeoutPolicy == policy &&
		payload.DeadlineUnixMS == deadline.UnixMilli()
}

func authoritativeBarrierAction(snapshot, cohortKey, stepID string) (*BarrierAction, string) {
	scenario, err := ParseScenarioSnapshot(snapshot)
	if err != nil {
		return nil, "场景快照无法解析"
	}
	for _, cohort := range scenario.Cohorts {
		if cohort.Key != cohortKey {
			continue
		}
		for _, step := range cohort.Steps {
			if step.Base() != nil && step.Base().ID == stepID && step.Barrier != nil {
				return step.Barrier, ""
			}
		}
	}
	return nil, "场景中不存在对应 barrier 步骤"
}

func (s *ScenarioActionEventService) schedule() {
	defer s.wg.Done()
	ticker := time.NewTicker(barrierSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
		s.dispatchReady()
	}
}

func (s *ScenarioActionEventService) dispatchReady() {
	if s.barriers == nil || s.router == nil {
		return
	}
	for _, dispatch := range s.takeReadyDispatches() {
		report := s.router.Route(dispatch.ctx, barrierSignalInputs(dispatch.scope, dispatch.release))
		s.barriers.CompleteRelease(dispatch.scope, dispatch.release.SignalType, dispatch.release.ReleaseAtUnixMS, report, s.barriers.clock.Now().UTC())
	}
}

type barrierSignalDispatch struct {
	ctx     context.Context
	scope   BarrierScope
	release *BarrierRelease
}

func (s *ScenarioActionEventService) takeReadyDispatches() []barrierSignalDispatch {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	ready := s.barriers.TakeReady(s.barriers.clock.Now().UTC())
	dispatches := make([]barrierSignalDispatch, 0, len(ready))
	for scope, release := range ready {
		dispatches = append(dispatches, barrierSignalDispatch{ctx: s.runCtxLocked(scope.RunID), scope: scope, release: release})
	}
	return dispatches
}

func barrierSignalInputs(scope BarrierScope, release *BarrierRelease) []ActionSignalInput {
	payloadField := "releaseAtUnixMs"
	if release.SignalType == "barrier-fail" {
		payloadField = "failAtUnixMs"
	}
	payload, _ := json.Marshal(map[string]any{"round": scope.Round, payloadField: release.ReleaseAtUnixMS})
	inputs := make([]ActionSignalInput, 0, len(release.Pending))
	for _, participant := range release.Pending {
		inputs = append(inputs, ActionSignalInput{
			RunID: scope.RunID, BotUUID: participant.BotUUID, ActionRunID: participant.ActionRunID,
			CorrelationToken: participant.CorrelationToken, Type: release.SignalType, Payload: payload,
		})
	}
	return inputs
}

// RetryBarrierRelease 兼容显式触发，并与集中调度使用同一回执合并逻辑。
func (s *ScenarioActionEventService) RetryBarrierRelease(ctx context.Context, scope BarrierScope) ActionSignalReport {
	release := s.barriers.PendingRelease(scope)
	if release == nil || len(release.Pending) == 0 {
		return ActionSignalReport{}
	}
	report := s.router.Route(ctx, barrierSignalInputs(scope, release))
	s.barriers.CompleteRelease(scope, release.SignalType, release.ReleaseAtUnixMS, report, s.barriers.clock.Now().UTC())
	return report
}

func (s *ScenarioActionEventService) runCtx(runID string) context.Context {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.runCtxLocked(runID)
}

func (s *ScenarioActionEventService) runCtxLocked(runID string) context.Context {
	if ctx := s.runContext[runID]; ctx != nil {
		return ctx
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.runContext[runID], s.runCancel[runID] = ctx, cancel
	return ctx
}

func (s *ScenarioActionEventService) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// StopRun 取消对应运行的在途 RPC 与后续调度任务。
func (s *ScenarioActionEventService) StopRun(runID string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if cancel := s.runCancel[runID]; cancel != nil {
		cancel()
	}
	delete(s.runCancel, runID)
	delete(s.runContext, runID)
	if s.barriers != nil {
		s.barriers.StopRun(runID)
	}
}

// ScheduledTaskCount 返回当前仍需集中调度的屏障作用域数量。
func (s *ScenarioActionEventService) ScheduledTaskCount() int {
	if s.barriers == nil {
		return 0
	}
	return s.barriers.ScheduledCount()
}

// SchedulerWorkerCount 明确调度器只有一个共享 worker。
func (s *ScenarioActionEventService) SchedulerWorkerCount() int { return 1 }

// Close 停止唯一集中调度器并取消所有在途 RPC。
func (s *ScenarioActionEventService) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()
	})
}
