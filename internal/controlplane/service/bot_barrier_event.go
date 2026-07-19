package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/proto/workerpb"
)

const barrierArrivedEventType = "barrier-arrived"

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

// ScenarioActionEventService 将动作结果持久化与屏障协调串在同一 Fleet 事件入口。
type ScenarioActionEventService struct {
	results  *ActionResultService
	barriers *BarrierCoordinator
	router   *ActionSignalRouter
	expected BarrierExpectedBotProvider
}

// NewScenarioActionEventService 创建 FR-352 场景动作事件处理链。
func NewScenarioActionEventService(results *ActionResultService, barriers *BarrierCoordinator, router *ActionSignalRouter, expected BarrierExpectedBotProvider) *ScenarioActionEventService {
	return &ScenarioActionEventService{results: results, barriers: barriers, router: router, expected: expected}
}

// NewGRPCScenarioActionEventService 使用数据库与既有 Worker 连接池依赖创建事件处理链。
func NewGRPCScenarioActionEventService(db *gorm.DB, results *ActionResultService, barriers *BarrierCoordinator, router *ActionSignalRouter) *ScenarioActionEventService {
	return NewScenarioActionEventService(results, barriers, router, gormBarrierExpectedBotProvider{db: db})
}

// Ingest 先拒绝非法屏障载荷，再写动作账本并协调释放。
func (s *ScenarioActionEventService) Ingest(ctx context.Context, executorNodeID uint, expectedSessionUUID string, event *workerpb.BotActionEvent) (ActionResultIngestResult, error) {
	payload, isBarrier, err := parseBarrierArrived(event)
	if err != nil {
		return actionIngestResult(ActionResultIgnoredInvalid, event, err.Error()), nil
	}
	if !isBarrier {
		return s.results.Ingest(ctx, executorNodeID, expectedSessionUUID, event)
	}
	expected, err := s.expected.ExpectedBots(ctx, event.SessionUuid, payload.CohortKey)
	if err != nil {
		return ActionResultIngestResult{}, fmt.Errorf("查询屏障期望 Bot 集合失败: %w", err)
	}
	scope := BarrierScope{RunID: event.SessionUuid, StageIndex: payload.StageIndex, CohortKey: payload.CohortKey, BarrierKey: payload.BarrierKey, Round: payload.Round}
	definition := BarrierDefinition{
		Scope: scope, ExpectedBots: expected, Release: payload.Release,
		TimeoutPolicy: payload.TimeoutPolicy, Deadline: time.UnixMilli(payload.DeadlineUnixMS).UTC(),
	}
	if err := validateBarrierDefinition(definition); err != nil {
		return actionIngestResult(ActionResultIgnoredInvalid, event, err.Error()), nil
	}
	if expected[event.BotUuid] != event.Generation {
		return actionIngestResult(ActionResultIgnoredIdentity, event, "barrier-arrived Bot 或 generation 不在冻结候选集合内"), nil
	}
	result, err := s.results.Ingest(ctx, executorNodeID, expectedSessionUUID, event)
	if err != nil || result.Decision == ActionResultIgnoredInvalid || result.Decision == ActionResultIgnoredIdentity {
		return result, err
	}
	if err := s.barriers.Ensure(definition); err != nil {
		return ActionResultIngestResult{}, err
	}
	barrierResult := s.barriers.Arrive(BarrierArrival{
		Scope: scope, BotUUID: event.BotUuid, Generation: event.Generation,
		ActionRunID: event.ActionRunId, CorrelationToken: event.CorrelationToken,
	})
	if barrierResult.Release != nil {
		report := s.routeRelease(ctx, scope, barrierResult.Release)
		result.Diagnostic = fmt.Sprintf("%s；屏障=%s，信号成功=%d，待重试=%d", result.Diagnostic, barrierResult.Decision, deliveredSignalCount(report), len(report.RetryableInputs()))
	}
	return result, nil
}

// RetryBarrierRelease 重投尚未逐项确认的 release，不把 RPC 失败视为已送达。
func (s *ScenarioActionEventService) RetryBarrierRelease(ctx context.Context, scope BarrierScope) ActionSignalReport {
	release := s.barriers.PendingRelease(scope)
	if release == nil {
		return ActionSignalReport{}
	}
	return s.routeRelease(ctx, scope, release)
}

// StopRun 清理屏障内存生命周期，不改变运行状态或指标。
func (s *ScenarioActionEventService) StopRun(runID string) { s.barriers.StopRun(runID) }

func (s *ScenarioActionEventService) routeRelease(ctx context.Context, scope BarrierScope, release *BarrierRelease) ActionSignalReport {
	payload, _ := json.Marshal(map[string]any{"round": release.Round, "releaseAtUnixMs": release.ReleaseAtUnixMS})
	inputs := make([]ActionSignalInput, 0, len(release.Pending))
	for _, participant := range release.Pending {
		inputs = append(inputs, ActionSignalInput{
			RunID: scope.RunID, BotUUID: participant.BotUUID, ActionRunID: participant.ActionRunID,
			CorrelationToken: participant.CorrelationToken, Type: "barrier-release", Payload: payload,
		})
	}
	report := s.router.Route(ctx, inputs)
	delivered := make([]string, 0)
	for _, item := range report.Items {
		if item.Accepted || (item.Skipped && item.ErrorCode == "") {
			delivered = append(delivered, item.Input.BotUUID)
		}
	}
	s.barriers.MarkDelivered(scope, delivered)
	return report
}

func parseBarrierArrived(event *workerpb.BotActionEvent) (barrierArrivedPayload, bool, error) {
	if event == nil || event.ResultJson == "" {
		return barrierArrivedPayload{}, false, nil
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(event.ResultJson), &discriminator); err != nil || discriminator.Type != barrierArrivedEventType {
		return barrierArrivedPayload{}, false, nil
	}
	if event.Status != string(modelBotActionRunning()) {
		return barrierArrivedPayload{}, true, fmt.Errorf("barrier-arrived 必须是 running 动作事件")
	}
	var payload barrierArrivedPayload
	if err := json.Unmarshal([]byte(event.ResultJson), &payload); err != nil {
		return payload, true, fmt.Errorf("barrier-arrived payload 非法")
	}
	if payload.StageIndex < 0 || payload.CohortKey == "" || payload.BarrierKey == "" || payload.Round <= 0 || payload.DeadlineUnixMS <= 0 {
		return payload, true, fmt.Errorf("barrier-arrived payload 关联字段不完整")
	}
	return payload, true, nil
}

func deliveredSignalCount(report ActionSignalReport) int {
	count := 0
	for _, item := range report.Items {
		if item.Accepted || item.Skipped {
			count++
		}
	}
	return count
}

func modelBotActionRunning() string { return "running" }
