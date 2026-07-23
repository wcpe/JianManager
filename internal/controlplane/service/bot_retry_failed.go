package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// BotLoadRetryRequest 是 retry-failed 请求体（FR-365）。
type BotLoadRetryRequest struct {
	RequestID  string   `json:"requestId"`
	BotUUIDs   []string `json:"botUuids,omitempty"`
	ErrorCodes []string `json:"errorCodes,omitempty"`
	FromStepID string   `json:"fromStepId,omitempty"`
}

// BotLoadRetryResult 是 retry-failed 202 响应。
type BotLoadRetryResult struct {
	Requested int                      `json:"requested"`
	Accepted  int                      `json:"accepted"`
	Skipped   int                      `json:"skipped"`
	Errors    []BotLoadRetryItemError  `json:"errors"`
}

// BotLoadRetryItemError 描述单个 Bot 被跳过或失败的原因。
type BotLoadRetryItemError struct {
	BotUUID   string `json:"botUuid,omitempty"`
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
}

// ErrBotLoadRetryIdempotent 表示相同 requestId 已处理，应直接返回缓存结果。
var ErrBotLoadRetryIdempotent = errors.New("retry-failed 请求已处理")

const botLoadRetryAuditAction = "bot_load.run.retry_failed"

// RetryFailed 选择失败 Bot，事务内 DesiredStateGeneration+1 且 DesiredState=running，再按节点批量 Apply。
// requestId 经审计幂等：重复调用不重复 generation+1。
func (s *BotLoadExecutionService) RetryFailed(ctx context.Context, sessionID uint, req BotLoadRetryRequest) (*BotLoadRetryResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("Bot 负载执行服务未装配")
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return nil, &botLoadValidationError{message: "requestId 不能为空"}
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return nil, &botLoadValidationError{message: "requestId 必须是 UUID"}
	}

	// 审计幂等：同 requestId 已成功处理则直接返回上次结果。
	if cached, ok, err := s.loadRetryIdempotentResult(ctx, sessionID, requestID); err != nil {
		return nil, err
	} else if ok {
		return cached, nil
	}

	session, err := s.loadRetrySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// V1 仅 running；degraded 待 FR-370 run_state，当前用 status=running 等价。
	if session.Status != model.BotStressSessionRunning {
		return nil, ErrBotLoadInvalidState
	}
	if botLoadStopIntentRecorded(session.LastError) {
		return nil, ErrBotLoadInvalidState
	}

	candidates, err := s.selectRetryCandidates(ctx, session, req)
	if err != nil {
		return nil, err
	}
	result := &BotLoadRetryResult{Requested: len(candidates), Errors: []BotLoadRetryItemError{}}
	if len(candidates) == 0 {
		// 无失败 Bot 也记审计，保证幂等。
		_ = s.persistRetryAudit(ctx, sessionID, requestID, result, nil)
		return result, nil
	}

	accepted, skipped, itemErrors, err := s.applyRetryCandidates(ctx, session, candidates, strings.TrimSpace(req.FromStepID))
	if err != nil {
		return nil, err
	}
	result.Accepted = accepted
	result.Skipped = skipped
	result.Errors = itemErrors
	if err := s.persistRetryAudit(ctx, sessionID, requestID, result, nil); err != nil {
		return result, err
	}
	return result, nil
}

type botLoadValidationError struct{ message string }

func (e *botLoadValidationError) Error() string { return e.message }

func (s *BotLoadExecutionService) loadRetrySession(ctx context.Context, sessionID uint) (*model.BotStressSession, error) {
	var session model.BotStressSession
	if err := s.db.WithContext(ctx).Preload("Instance").First(&session, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, fmt.Errorf("查询压测会话失败: %w", err)
	}
	return &session, nil
}

func (s *BotLoadExecutionService) selectRetryCandidates(ctx context.Context, session *model.BotStressSession, req BotLoadRetryRequest) ([]model.Bot, error) {
	query := s.db.WithContext(ctx).Where("stress_session_id = ?", session.ID).
		Where("status IN ?", []model.BotStatus{model.BotStatusError, model.BotStatusDisconnected}).
		Where("desired_state = ? OR desired_state = ''", model.BotDesiredRunning)
	if len(req.BotUUIDs) > 0 {
		query = query.Where("uuid IN ?", req.BotUUIDs)
	}
	if len(req.ErrorCodes) > 0 {
		// last_error 可能是完整文案或错误码；按包含匹配。
		ors := make([]string, 0, len(req.ErrorCodes))
		args := make([]any, 0, len(req.ErrorCodes))
		for _, code := range req.ErrorCodes {
			code = strings.TrimSpace(code)
			if code == "" {
				continue
			}
			ors = append(ors, "last_error LIKE ?")
			args = append(args, "%"+code+"%")
		}
		if len(ors) > 0 {
			query = query.Where(strings.Join(ors, " OR "), args...)
		}
	}
	var bots []model.Bot
	if err := query.Order("id ASC").Find(&bots).Error; err != nil {
		return nil, fmt.Errorf("查询失败 Bot 失败: %w", err)
	}
	return bots, nil
}

func (s *BotLoadExecutionService) applyRetryCandidates(ctx context.Context, session *model.BotStressSession, candidates []model.Bot, fromStepID string) (accepted, skipped int, itemErrors []BotLoadRetryItemError, err error) {
	// 事务内 bump generation、清 last_error、desired=running。
	bumped := make([]model.Bot, 0, len(candidates))
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index := range candidates {
			bot := candidates[index]
			updates := map[string]any{
				"desired_state":            model.BotDesiredRunning,
				"desired_state_generation": gorm.Expr("desired_state_generation + 1"),
				"last_error":               "",
				"status":                   model.BotStatusPending,
			}
			result := tx.Model(&model.Bot{}).Where("id = ? AND stress_session_id = ?", bot.ID, session.ID).Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				skipped++
				itemErrors = append(itemErrors, BotLoadRetryItemError{
					BotUUID: bot.UUID, ErrorCode: "concurrent_update", Message: "Bot 已被并发更新，跳过",
				})
				continue
			}
			var reloaded model.Bot
			if err := tx.First(&reloaded, bot.ID).Error; err != nil {
				return err
			}
			bumped = append(bumped, reloaded)
		}
		return nil
	})
	if err != nil {
		return 0, 0, nil, fmt.Errorf("更新 retry-failed desired 失败: %w", err)
	}

	// 按执行节点分组 Apply。
	groups := groupBotsByExecutor(bumped)
	for nodeID, bots := range groups {
		nodeUUID, nodeErr := s.lookupNodeUUID(ctx, nodeID)
		if nodeErr != nil || nodeUUID == "" {
			// 首版：原节点不可用时尝试 resolver 重分配；失败则记 error。
			for _, bot := range bots {
				skipped++
				itemErrors = append(itemErrors, BotLoadRetryItemError{
					BotUUID: bot.UUID, ErrorCode: "executor_unavailable",
					Message: "原执行节点不可用，暂未重新分配",
				})
			}
			continue
		}
		items, applyErr := s.dispatchRetryBots(ctx, session, nodeUUID, bots, fromStepID)
		if applyErr != nil {
			for _, bot := range bots {
				skipped++
				itemErrors = append(itemErrors, BotLoadRetryItemError{
					BotUUID: bot.UUID, ErrorCode: "dispatch_failed", Message: applyErr.Error(),
				})
			}
			continue
		}
		for _, item := range items {
			if item.accepted {
				accepted++
			} else {
				skipped++
				itemErrors = append(itemErrors, BotLoadRetryItemError{
					BotUUID: item.bot.UUID, ErrorCode: "apply_rejected", Message: item.lastErr,
				})
			}
		}
	}
	return accepted, skipped, itemErrors, nil
}

func groupBotsByExecutor(bots []model.Bot) map[uint][]model.Bot {
	groups := make(map[uint][]model.Bot)
	for _, bot := range bots {
		var nodeID uint
		if bot.ExecutorNodeID != nil {
			nodeID = *bot.ExecutorNodeID
		}
		groups[nodeID] = append(groups[nodeID], bot)
	}
	return groups
}

func (s *BotLoadExecutionService) lookupNodeUUID(ctx context.Context, nodeID uint) (string, error) {
	if nodeID == 0 {
		return "", nil
	}
	var node model.Node
	if err := s.db.WithContext(ctx).Select("id", "uuid").First(&node, nodeID).Error; err != nil {
		return "", err
	}
	return node.UUID, nil
}

func (s *BotLoadExecutionService) dispatchRetryBots(ctx context.Context, session *model.BotStressSession, nodeUUID string, bots []model.Bot, fromStepID string) ([]botLoadDispatchItem, error) {
	assignments := make([]*workerpb.BotAssignment, 0, len(bots))
	for index := range bots {
		bot := &bots[index]
		assignment, err := s.rebuildRunningAssignment(ctx, session, bot)
		if err != nil {
			return nil, err
		}
		if fromStepID != "" {
			assignment.ResumeStepId = fromStepID
		}
		// retry 立即连接，不等待原 connectNotBefore。
		assignment.ConnectNotBeforeUnixMs = time.Now().UTC().UnixMilli()
		assignments = append(assignments, assignment)
	}
	raw, _ := json.Marshal(assignments)
	identity := fmt.Sprintf("retry|%s|%s|%s", session.UUID, nodeUUID, stableBotLoadDigest(string(raw)))
	request := &workerpb.ApplyBotBatchRequest{
		BatchId:        stableBotLoadUUID(identity),
		IdempotencyKey: "bot-load-retry-" + stableBotLoadDigest(identity),
		Assignments:    assignments,
	}
	response, rpcErr := s.applyBotLoadBatch(ctx, nodeUUID, request)
	return normalizeBotLoadDispatchItems(bots, request, response, rpcErr), nil
}

func (s *BotLoadExecutionService) loadRetryIdempotentResult(ctx context.Context, sessionID uint, requestID string) (*BotLoadRetryResult, bool, error) {
	var log model.AuditLog
	err := s.db.WithContext(ctx).
		Where("action = ? AND target_type = ? AND target_id = ? AND failed = ?",
			botLoadRetryAuditAction, "bot_load_run", fmt.Sprintf("%d", sessionID), false).
		Where("detail LIKE ?", "%\"requestId\":\""+requestID+"\"%").
		Order("id DESC").
		First(&log).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("查询 retry-failed 幂等审计失败: %w", err)
	}
	var full struct {
		RequestID string             `json:"requestId"`
		Result    BotLoadRetryResult `json:"result"`
	}
	if json.Unmarshal([]byte(log.Detail), &full) == nil {
		return &full.Result, true, nil
	}
	return &BotLoadRetryResult{}, true, nil
}

func (s *BotLoadExecutionService) persistRetryAudit(ctx context.Context, sessionID uint, requestID string, result *BotLoadRetryResult, opErr error) error {
	detail, _ := json.Marshal(map[string]any{
		"requestId": requestID,
		"result":    result,
	})
	errMessage := ""
	if opErr != nil {
		errMessage = opErr.Error()
	}
	log := model.AuditLog{
		UserID:     0,
		Action:     botLoadRetryAuditAction,
		TargetType: "bot_load_run",
		TargetID:   fmt.Sprintf("%d", sessionID),
		Detail:     string(detail),
		IP:         "",
		Failed:     opErr != nil,
		Error:      errMessage,
	}
	return s.db.WithContext(ctx).Create(&log).Error
}
