package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const actionResultJSONLimit = 16 * 1024

const (
	ActionErrorConnectTimeout        = "CONNECT_TIMEOUT"
	ActionErrorConnectEnded          = "CONNECT_ENDED"
	ActionErrorPathfinderUnavailable = "PATHFINDER_UNAVAILABLE"
	ActionErrorPathNotFound          = "PATH_NOT_FOUND"
	ActionErrorMoveTimeout           = "MOVE_TIMEOUT"
	ActionErrorTargetNotFound        = "TARGET_NOT_FOUND"
	ActionErrorAttackAssertionUnmet  = "ATTACK_ASSERTION_UNMET"
	ActionErrorProbeEventTimeout     = "PROBE_EVENT_TIMEOUT"
	ActionErrorBarrierTimeout        = "BARRIER_TIMEOUT"
	ActionErrorCancelled             = "ACTION_CANCELLED"
	ActionErrorInternal              = "ACTION_INTERNAL_ERROR"
)

var actionErrorCodes = map[string]struct{}{
	ActionErrorConnectTimeout: {}, ActionErrorConnectEnded: {},
	ActionErrorPathfinderUnavailable: {}, ActionErrorPathNotFound: {},
	ActionErrorMoveTimeout: {}, ActionErrorTargetNotFound: {}, ActionErrorAttackAssertionUnmet: {},
	ActionErrorProbeEventTimeout: {}, ActionErrorBarrierTimeout: {},
	ActionErrorCancelled: {}, ActionErrorInternal: {},
	// FR-369 通用命令编排新增的错误码。
	CommandErrorRouteFailed: {}, CommandErrorIPCFailed: {},
	CommandErrorArgumentInvalid: {}, CommandErrorRuntimeUnavailable: {},
	CommandErrorScheduleRejected: {}, CommandErrorDeadlineExceeded: {},
	CommandErrorSendFailed: {},
}

// ActionResultDecision 表示动作事件对持久化账本的处理结果。
type ActionResultDecision string

const (
	ActionResultApplied          ActionResultDecision = "applied"
	ActionResultIgnoredDuplicate ActionResultDecision = "ignored_duplicate"
	ActionResultIgnoredTerminal  ActionResultDecision = "ignored_terminal"
	ActionResultIgnoredInvalid   ActionResultDecision = "ignored_invalid"
	ActionResultIgnoredIdentity  ActionResultDecision = "ignored_identity"
)

// ActionResultIngestResult 包含动作事件的幂等处理结论与诊断。
type ActionResultIngestResult struct {
	Decision    ActionResultDecision
	ActionRunID string
	BotUUID     string
	Diagnostic  string
}

// WaitingAction 是外部信号路由所需的运行中动作及当前 Bot 路由真源。
type WaitingAction struct {
	Result           model.BotLoadActionResult
	Bot              model.Bot
	SessionUUID      string
	ExecutorNodeID   uint
	ExecutorNodeUUID string
	Generation       int64
}

type actionResultRepository interface {
	FindBot(ctx context.Context, botUUID string) (*model.Bot, error)
	Start(ctx context.Context, result *model.BotLoadActionResult) (ActionResultDecision, error)
	Finish(ctx context.Context, result *model.BotLoadActionResult) (ActionResultDecision, error)
	FindWaiting(ctx context.Context, runID, botUUID, actionRunID, correlationToken string) (*WaitingAction, error)
}

type gormActionResultRepository struct{ db *gorm.DB }

func (r *gormActionResultRepository) FindBot(ctx context.Context, botUUID string) (*model.Bot, error) {
	var bot model.Bot
	err := r.db.WithContext(ctx).Preload("Instance.Node").Preload("ExecutorNode").Preload("StressSession").Where("uuid = ?", botUUID).First(&bot).Error
	return &bot, err
}

func (r *gormActionResultRepository) Start(ctx context.Context, result *model.BotLoadActionResult) (ActionResultDecision, error) {
	created := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "action_run_id"}}, DoNothing: true,
	}).Create(result)
	if created.Error != nil {
		return "", created.Error
	}
	if created.RowsAffected == 1 {
		return ActionResultApplied, nil
	}
	return r.existingDecision(ctx, result)
}

func (r *gormActionResultRepository) Finish(ctx context.Context, result *model.BotLoadActionResult) (ActionResultDecision, error) {
	decision := ActionResultIgnoredTerminal
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		start := *result
		start.Status = model.BotLoadActionRunning
		start.ErrorCode, start.Message, start.ResultJSON = "", "", ""
		start.DurationMS, start.EndedAt = 0, nil
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "action_run_id"}}, DoNothing: true,
		}).Create(&start).Error; err != nil {
			return err
		}
		updated := actionResultIdentityQuery(tx.Model(&model.BotLoadActionResult{}), result).
			Where("status = ?", model.BotLoadActionRunning).
			Updates(map[string]any{
				"status": result.Status, "error_code": result.ErrorCode, "message": result.Message,
				"duration_ms": result.DurationMS, "correlation_token": result.CorrelationToken,
				"ended_at": result.EndedAt, "result_json": result.ResultJSON,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 1 {
			decision = ActionResultApplied
			return nil
		}
		var classifyErr error
		decision, classifyErr = existingActionResultDecision(tx, result)
		return classifyErr
	})
	return decision, err
}

func (r *gormActionResultRepository) existingDecision(ctx context.Context, result *model.BotLoadActionResult) (ActionResultDecision, error) {
	return existingActionResultDecision(r.db.WithContext(ctx), result)
}

func existingActionResultDecision(db *gorm.DB, expected *model.BotLoadActionResult) (ActionResultDecision, error) {
	var existing model.BotLoadActionResult
	if err := db.Where("action_run_id = ?", expected.ActionRunID).First(&existing).Error; err != nil {
		return "", err
	}
	if !sameActionResultIdentity(&existing, expected) {
		return ActionResultIgnoredIdentity, nil
	}
	if existing.Status == model.BotLoadActionRunning {
		return ActionResultIgnoredDuplicate, nil
	}
	return ActionResultIgnoredTerminal, nil
}

func actionResultIdentityQuery(db *gorm.DB, result *model.BotLoadActionResult) *gorm.DB {
	return db.Where(
		"action_run_id = ? AND stress_session_id = ? AND bot_id = ? AND cohort_key = ? AND step_id = ? AND attempt = ? AND correlation_token = ?",
		result.ActionRunID, result.StressSessionID, result.BotID, result.CohortKey, result.StepID, result.Attempt, result.CorrelationToken,
	)
}

func sameActionResultIdentity(left, right *model.BotLoadActionResult) bool {
	return left.ActionRunID == right.ActionRunID &&
		left.StressSessionID == right.StressSessionID && left.BotID == right.BotID &&
		left.CohortKey == right.CohortKey && left.StepID == right.StepID &&
		left.Attempt == right.Attempt && left.CorrelationToken == right.CorrelationToken
}

func (r *gormActionResultRepository) FindWaiting(ctx context.Context, runID, botUUID, actionRunID, correlationToken string) (*WaitingAction, error) {
	var result model.BotLoadActionResult
	sessionIDs := r.db.WithContext(ctx).Model(&model.BotStressSession{}).Select("id").Where("uuid = ?", runID)
	botIDs := r.db.WithContext(ctx).Model(&model.Bot{}).Select("id").Where("uuid = ?", botUUID)
	err := r.db.WithContext(ctx).
		Where("stress_session_id IN (?) AND bot_id IN (?)", sessionIDs, botIDs).
		Where("action_run_id = ? AND correlation_token = ? AND status = ?", actionRunID, correlationToken, model.BotLoadActionRunning).
		First(&result).Error
	if err != nil {
		return nil, err
	}
	bot, err := r.FindBot(ctx, botUUID)
	if err != nil {
		return nil, err
	}
	return waitingAction(result, bot), nil
}

func waitingAction(result model.BotLoadActionResult, bot *model.Bot) *WaitingAction {
	waiting := &WaitingAction{Result: result, Bot: *bot, Generation: bot.DesiredStateGeneration}
	if bot.StressSession != nil {
		waiting.SessionUUID = bot.StressSession.UUID
	}
	waiting.ExecutorNodeID = runtimeExecutorNodeID(bot)
	if bot.ExecutorNode != nil {
		waiting.ExecutorNodeUUID = bot.ExecutorNode.UUID
	} else {
		waiting.ExecutorNodeUUID = bot.Instance.Node.UUID
	}
	return waiting
}

// ActionResultService 校验并持久化 FR-351 Fleet action_event，保证首终态胜出。
type ActionResultService struct {
	repository actionResultRepository
	clock      BotLoadClock
	// db 可选：用于 FR-369 命令 checkpoint 回写；nil 时跳过。
	db *gorm.DB
}

// NewActionResultService 使用 GORM 账本创建动作结果服务。
func NewActionResultService(db *gorm.DB, clock BotLoadClock) *ActionResultService {
	return newActionResultService(&gormActionResultRepository{db: db}, clock, db)
}

func newActionResultService(repository actionResultRepository, clock BotLoadClock, db *gorm.DB) *ActionResultService {
	return &ActionResultService{repository: repository, clock: normalizeBotLoadClock(clock), db: db}
}

// Ingest 校验执行节点、运行、Bot 与 generation 后幂等写入动作开始或首个终态。
func (s *ActionResultService) Ingest(ctx context.Context, executorNodeID uint, expectedSessionUUID string, event *workerpb.BotActionEvent) (ActionResultIngestResult, error) {
	status, diagnostic := validateActionEvent(event)
	if diagnostic != "" {
		return actionIngestResult(ActionResultIgnoredInvalid, event, diagnostic), nil
	}
	bot, err := s.repository.FindBot(ctx, event.BotUuid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return actionIngestResult(ActionResultIgnoredIdentity, event, "Bot UUID 不存在"), nil
		}
		return ActionResultIngestResult{}, fmt.Errorf("查询动作事件 Bot 失败: %w", err)
	}
	if diagnostic = validateActionIdentity(bot, executorNodeID, expectedSessionUUID, event); diagnostic != "" {
		return actionIngestResult(ActionResultIgnoredIdentity, event, diagnostic), nil
	}
	result := s.actionResult(bot, event, status)
	var decision ActionResultDecision
	if status == model.BotLoadActionRunning {
		decision, err = s.repository.Start(ctx, result)
	} else {
		decision, err = s.repository.Finish(ctx, result)
	}
	if err != nil {
		return ActionResultIngestResult{}, fmt.Errorf("持久化动作结果失败: %w", err)
	}
	// FR-369：命令编排终态回写 checkpoint（仅 applied 的终态；duplicate/invalid 跳过）。
	if decision == ActionResultApplied && status != model.BotLoadActionRunning {
		s.syncCommandCheckpoint(ctx, bot, event, status)
	}
	return actionIngestResult(decision, event, "动作事件已幂等处理"), nil
}

// syncCommandCheckpoint 将 Fleet action 终态映射到 bot_load_command_checkpoints。
func (s *ActionResultService) syncCommandCheckpoint(ctx context.Context, bot *model.Bot, event *workerpb.BotActionEvent, status model.BotLoadActionResultStatus) {
	if s == nil || s.db == nil || bot == nil || event == nil {
		return
	}
	if event.StepId != commandScheduleDefaultStepID && !strings.HasPrefix(event.StepId, "command-schedule") {
		return
	}
	// 按 actionRunId 定位 occurrence 行
	var row model.BotLoadCommandCheckpoint
	err := s.db.WithContext(ctx).Where("action_run_id = ?", event.ActionRunId).First(&row).Error
	if err != nil {
		return
	}
	ck := NewBotLoadCommandCheckpointService(s.db)
	attempt := int(event.Attempt)
	if attempt <= 0 {
		attempt = row.Attempt
	}
	switch status {
	case model.BotLoadActionSucceeded:
		if err := ck.MarkSent(ctx, row.RunUUID, row.BotUUID, row.StepID, row.CommandID, row.Occurrence, attempt, event.ObservedAtUnixMs); err != nil {
			slog.Warn("同步命令检查点发送状态失败", "runUuid", row.RunUUID, "botUuid", row.BotUUID, "error", err)
		}
	case model.BotLoadActionFailed:
		if err := ck.MarkFailed(ctx, row.RunUUID, row.BotUUID, row.StepID, row.CommandID, row.Occurrence,
			model.BotLoadCommandCheckpointFailed, attempt, event.ErrorCode); err != nil {
			slog.Warn("同步命令检查点失败状态失败", "runUuid", row.RunUUID, "botUuid", row.BotUUID, "error", err)
		}
	case model.BotLoadActionTimedOut:
		if err := ck.MarkFailed(ctx, row.RunUUID, row.BotUUID, row.StepID, row.CommandID, row.Occurrence,
			model.BotLoadCommandCheckpointTimedOut, attempt, event.ErrorCode); err != nil {
			slog.Warn("同步命令检查点超时状态失败", "runUuid", row.RunUUID, "botUuid", row.BotUUID, "error", err)
		}
	case model.BotLoadActionCancelled:
		if err := ck.MarkFailed(ctx, row.RunUUID, row.BotUUID, row.StepID, row.CommandID, row.Occurrence,
			model.BotLoadCommandCheckpointCancelled, attempt, event.ErrorCode); err != nil {
			slog.Warn("同步命令检查点取消状态失败", "runUuid", row.RunUUID, "botUuid", row.BotUUID, "error", err)
		}
	}
}

// FindWaitingAction 强校验完整关联并返回当前执行节点和 generation。
func (s *ActionResultService) FindWaitingAction(ctx context.Context, runID, botUUID, actionRunID, correlationToken string) (*WaitingAction, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(botUUID) == "" || strings.TrimSpace(actionRunID) == "" || strings.TrimSpace(correlationToken) == "" {
		return nil, nil
	}
	waiting, err := s.repository.FindWaiting(ctx, runID, botUUID, actionRunID, correlationToken)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询等待动作失败: %w", err)
	}
	if waiting.SessionUUID != runID || waiting.Result.ActionRunID != actionRunID || waiting.Result.CorrelationToken != correlationToken || waiting.Bot.UUID != botUUID {
		return nil, nil
	}
	return waiting, nil
}

func (s *ActionResultService) actionResult(bot *model.Bot, event *workerpb.BotActionEvent, status model.BotLoadActionResultStatus) *model.BotLoadActionResult {
	observedAt := s.clock.Now().UTC()
	if event.ObservedAtUnixMs > 0 {
		observedAt = time.UnixMilli(event.ObservedAtUnixMs).UTC()
	}
	startedAt := observedAt
	if status != model.BotLoadActionRunning && event.DurationMs > 0 {
		startedAt = observedAt.Add(-time.Duration(event.DurationMs) * time.Millisecond)
	}
	result := &model.BotLoadActionResult{
		StressSessionID: *bot.StressSessionID, BotID: bot.ID, CohortKey: bot.CohortKey,
		StepID: event.StepId, ActionRunID: event.ActionRunId, Attempt: int(event.Attempt), Status: status,
		ErrorCode: event.ErrorCode, Message: event.Message, DurationMS: event.DurationMs,
		CorrelationToken: event.CorrelationToken, StartedAt: startedAt,
	}
	if status != model.BotLoadActionRunning {
		result.EndedAt = &observedAt
		result.ResultJSON = truncateActionResultJSON(event.ResultJson)
	}
	return result
}

func validateActionEvent(event *workerpb.BotActionEvent) (model.BotLoadActionResultStatus, string) {
	if event == nil {
		return "", "action_event 为空"
	}
	if strings.TrimSpace(event.BotUuid) == "" || strings.TrimSpace(event.SessionUuid) == "" || strings.TrimSpace(event.StepId) == "" {
		return "", "action_event 缺少 Bot、运行或步骤标识"
	}
	if _, err := uuid.Parse(event.ActionRunId); err != nil {
		return "", "actionRunId 不是有效 UUID"
	}
	if strings.TrimSpace(event.CorrelationToken) == "" {
		return "", "correlationToken 不能为空"
	}
	if _, err := uuid.Parse(event.CorrelationToken); err != nil {
		return "", "correlationToken 不是有效 UUID"
	}
	if event.Generation <= 0 || event.Attempt <= 0 || event.DurationMs < 0 {
		return "", "action_event generation、attempt 或 duration 非法"
	}
	status, ok := actionResultStatus(event.Status)
	if !ok {
		return "", "动作状态不在冻结枚举内"
	}
	if event.ErrorCode != "" {
		if _, ok := actionErrorCodes[event.ErrorCode]; !ok {
			return "", "动作错误码不在冻结枚举内"
		}
	}
	if status != model.BotLoadActionRunning && status != model.BotLoadActionSucceeded && event.ErrorCode == "" {
		return "", "失败终态必须携带冻结错误码"
	}
	if event.ResultJson != "" && !json.Valid([]byte(event.ResultJson)) {
		return "", "resultJson 不是有效 JSON"
	}
	return status, ""
}

func validateActionIdentity(bot *model.Bot, executorNodeID uint, expectedSessionUUID string, event *workerpb.BotActionEvent) string {
	if bot.StressSessionID == nil || bot.StressSession == nil {
		return "Bot 不属于压测运行"
	}
	if runtimeExecutorNodeID(bot) != executorNodeID {
		return "action_event 来源执行节点不匹配"
	}
	if event.SessionUuid != expectedSessionUUID || bot.StressSession.UUID != event.SessionUuid {
		return "action_event 运行标识不匹配"
	}
	if bot.DesiredStateGeneration != event.Generation {
		return "action_event generation 与当前 Bot 不匹配"
	}
	return ""
}

func actionResultStatus(status string) (model.BotLoadActionResultStatus, bool) {
	switch model.BotLoadActionResultStatus(strings.ToLower(strings.TrimSpace(status))) {
	case model.BotLoadActionRunning, model.BotLoadActionSucceeded, model.BotLoadActionFailed,
		model.BotLoadActionTimedOut, model.BotLoadActionCancelled:
		return model.BotLoadActionResultStatus(strings.ToLower(strings.TrimSpace(status))), true
	default:
		return "", false
	}
}

func actionIngestResult(decision ActionResultDecision, event *workerpb.BotActionEvent, diagnostic string) ActionResultIngestResult {
	result := ActionResultIngestResult{Decision: decision, Diagnostic: diagnostic}
	if event != nil {
		result.ActionRunID, result.BotUUID = event.ActionRunId, event.BotUuid
	}
	return result
}

func truncateActionResultJSON(raw string) string {
	if len([]byte(raw)) <= actionResultJSONLimit {
		return raw
	}
	preview := raw
	for len(preview) > 0 {
		preview = trimUTF8Bytes(preview, len([]byte(preview))-512)
		encoded, err := json.Marshal(map[string]any{
			"truncated": true, "originalBytes": len([]byte(raw)), "preview": preview,
		})
		if err != nil {
			slog.Error("序列化 Bot 操作结果截断摘要失败", "error", err)
			break
		}
		if len(encoded) <= actionResultJSONLimit {
			return string(encoded)
		}
	}
	encoded, err := json.Marshal(map[string]any{"truncated": true, "originalBytes": len([]byte(raw))})
	if err != nil {
		slog.Error("序列化 Bot 操作结果最小截断摘要失败", "error", err)
		return `{"truncated":true}`
	}
	return string(encoded)
}

func trimUTF8Bytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
