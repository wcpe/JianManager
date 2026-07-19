package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const (
	actionSignalBatchSize             = 100
	actionSignalWaitingQueryBatchSize = 500
	actionSignalNodeConcurrency       = 16
	actionSignalRPCTimeout            = 3 * time.Second
)

// WaitingActionFinder 批量提供强关联的运行中动作查询。
type WaitingActionFinder interface {
	FindWaitingActions(ctx context.Context, inputs []ActionSignalInput) ([]*WaitingAction, error)
}

// ActionSignalClient 隔离 Worker 连接池，便于验证分组和部分失败。
type ActionSignalClient interface {
	SignalBotActions(ctx context.Context, nodeUUID string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error)
}

// ActionSignalInput 是 FR-353/355 可复用的内部动作信号输入。
type ActionSignalInput struct {
	RunID            string
	BotUUID          string
	ActionRunID      string
	CorrelationToken string
	Type             string
	Payload          json.RawMessage
}

type waitingActionBatchRepository interface {
	FindWaitingActions(ctx context.Context, inputs []ActionSignalInput) ([]*WaitingAction, error)
}

type waitingActionRow struct {
	StressSessionID  uint   `gorm:"column:stress_session_id"`
	BotID            uint   `gorm:"column:bot_id"`
	CohortKey        string `gorm:"column:cohort_key"`
	StepID           string `gorm:"column:step_id"`
	ActionRunID      string `gorm:"column:action_run_id"`
	Attempt          int    `gorm:"column:attempt"`
	CorrelationToken string `gorm:"column:correlation_token"`
	BotUUID          string `gorm:"column:bot_uuid"`
	SessionUUID      string `gorm:"column:session_uuid"`
	InstanceID       uint   `gorm:"column:instance_id"`
	ExecutorNodeID   uint   `gorm:"column:executor_node_id"`
	ExecutorNodeUUID string `gorm:"column:executor_node_uuid"`
	Generation       int64  `gorm:"column:generation"`
}

// FindWaitingActions 批量读取动作账本与当前执行节点，避免逐 Bot preload。
func (s *ActionResultService) FindWaitingActions(ctx context.Context, inputs []ActionSignalInput) ([]*WaitingAction, error) {
	if repository, ok := s.repository.(waitingActionBatchRepository); ok {
		return repository.FindWaitingActions(ctx, inputs)
	}
	waiting := make([]*WaitingAction, len(inputs))
	for index, input := range inputs {
		item, err := s.FindWaitingAction(ctx, input.RunID, input.BotUUID, input.ActionRunID, input.CorrelationToken)
		if err != nil {
			return nil, err
		}
		waiting[index] = item
	}
	return waiting, nil
}

func (r *gormActionResultRepository) FindWaitingActions(ctx context.Context, inputs []ActionSignalInput) ([]*WaitingAction, error) {
	rows, err := r.findWaitingActionRows(ctx, inputs)
	if err != nil {
		return nil, err
	}
	byActionRunID := make(map[string]waitingActionRow, len(rows))
	for _, row := range rows {
		byActionRunID[row.ActionRunID] = row
	}
	waiting := make([]*WaitingAction, len(inputs))
	for index, input := range inputs {
		row, ok := byActionRunID[input.ActionRunID]
		if ok && row.SessionUUID == input.RunID && row.BotUUID == input.BotUUID && row.CorrelationToken == input.CorrelationToken {
			waiting[index] = waitingActionFromRow(row)
		}
	}
	return waiting, nil
}

func (r *gormActionResultRepository) findWaitingActionRows(ctx context.Context, inputs []ActionSignalInput) ([]waitingActionRow, error) {
	rows := make([]waitingActionRow, 0, len(inputs))
	for start := 0; start < len(inputs); start += actionSignalWaitingQueryBatchSize {
		end := min(start+actionSignalWaitingQueryBatchSize, len(inputs))
		actionRunIDs := uniqueActionRunIDs(inputs[start:end])
		var batch []waitingActionRow
		err := r.db.WithContext(ctx).Table("bot_load_action_results AS action_results").
			Select(`action_results.stress_session_id, action_results.bot_id, action_results.cohort_key,
				action_results.step_id, action_results.action_run_id, action_results.attempt,
				action_results.correlation_token, bots.uuid AS bot_uuid, sessions.uuid AS session_uuid,
				bots.instance_id, COALESCE(executor_nodes.id, target_nodes.id) AS executor_node_id,
				COALESCE(executor_nodes.uuid, target_nodes.uuid) AS executor_node_uuid,
				bots.desired_state_generation AS generation`).
			Joins("JOIN bots ON bots.id = action_results.bot_id AND bots.deleted_at IS NULL").
			Joins("JOIN bot_stress_sessions AS sessions ON sessions.id = action_results.stress_session_id AND sessions.deleted_at IS NULL").
			Joins("JOIN instances ON instances.id = bots.instance_id AND instances.deleted_at IS NULL").
			Joins("JOIN nodes AS target_nodes ON target_nodes.id = instances.node_id AND target_nodes.deleted_at IS NULL").
			Joins("LEFT JOIN nodes AS executor_nodes ON executor_nodes.id = bots.executor_node_id AND executor_nodes.deleted_at IS NULL").
			Where("action_results.action_run_id IN ? AND action_results.status = ?", actionRunIDs, model.BotLoadActionRunning).
			Scan(&batch).Error
		if err != nil {
			return nil, fmt.Errorf("批量查询等待动作失败: %w", err)
		}
		rows = append(rows, batch...)
	}
	return rows, nil
}

func uniqueActionRunIDs(inputs []ActionSignalInput) []string {
	seen := make(map[string]struct{}, len(inputs))
	ids := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if _, ok := seen[input.ActionRunID]; ok {
			continue
		}
		seen[input.ActionRunID] = struct{}{}
		ids = append(ids, input.ActionRunID)
	}
	return ids
}

func waitingActionFromRow(row waitingActionRow) *WaitingAction {
	stressSessionID, executorNodeID := row.StressSessionID, row.ExecutorNodeID
	return &WaitingAction{
		Result: model.BotLoadActionResult{
			StressSessionID: row.StressSessionID, BotID: row.BotID, CohortKey: row.CohortKey,
			StepID: row.StepID, ActionRunID: row.ActionRunID, Attempt: row.Attempt,
			Status: model.BotLoadActionRunning, CorrelationToken: row.CorrelationToken,
		},
		Bot: model.Bot{
			ID: row.BotID, UUID: row.BotUUID, InstanceID: row.InstanceID,
			StressSessionID: &stressSessionID, ExecutorNodeID: &executorNodeID,
			DesiredStateGeneration: row.Generation,
		},
		SessionUUID: row.SessionUUID, ExecutorNodeID: row.ExecutorNodeID,
		ExecutorNodeUUID: row.ExecutorNodeUUID, Generation: row.Generation,
	}
}

// ActionSignalStatus 是单项路由结论。
type ActionSignalStatus string

const (
	ActionSignalAccepted ActionSignalStatus = "accepted"
	ActionSignalSkipped  ActionSignalStatus = "skipped"
	ActionSignalRejected ActionSignalStatus = "rejected"
	ActionSignalFailed   ActionSignalStatus = "failed"
)

// ActionSignalReceipt 保留 Worker 逐项回执和可重试输入。
type ActionSignalReceipt struct {
	Input     ActionSignalInput
	SignalID  string
	Status    ActionSignalStatus
	Accepted  bool
	Skipped   bool
	Retriable bool
	ErrorCode string
	Error     string
}

// ActionSignalReport 汇总部分成功且不掩盖失败。
type ActionSignalReport struct{ Items []ActionSignalReceipt }

// RetryableInputs 返回原始失败项，稳定 signalId 保证重试幂等。
func (r ActionSignalReport) RetryableInputs() []ActionSignalInput {
	inputs := make([]ActionSignalInput, 0)
	for _, item := range r.Items {
		if item.Retriable {
			inputs = append(inputs, item.Input)
		}
	}
	return inputs
}

type poolActionSignalClient struct{ pool *cpgrpc.ClientPool }

func (c poolActionSignalClient) SignalBotActions(ctx context.Context, nodeUUID string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
	if c.pool == nil {
		return nil, errBotLoadWorkerMissing
	}
	client, ok := c.pool.Get(nodeUUID)
	if !ok || client.Worker == nil {
		return nil, errBotLoadWorkerMissing
	}
	return client.Worker.SignalBotActions(ctx, request)
}

// ActionSignalRouter 强校验动作关联并按 Bot 当前执行节点批量投递。
type ActionSignalRouter struct {
	finder WaitingActionFinder
	client ActionSignalClient
	clock  BotLoadClock
}

// NewActionSignalRouter 创建可注入查询器与 Worker client 的内部路由器。
func NewActionSignalRouter(finder WaitingActionFinder, client ActionSignalClient, clock BotLoadClock) *ActionSignalRouter {
	return &ActionSignalRouter{finder: finder, client: client, clock: normalizeBotLoadClock(clock)}
}

// NewGRPCActionSignalRouter 使用现有隧道优先连接池创建路由器。
func NewGRPCActionSignalRouter(finder WaitingActionFinder, pool *cpgrpc.ClientPool, clock BotLoadClock) *ActionSignalRouter {
	return NewActionSignalRouter(finder, poolActionSignalClient{pool: pool}, clock)
}

// Route 保持输入顺序返回逐项结果，并对合法项批量查询后按节点并发投递。
func (r *ActionSignalRouter) Route(ctx context.Context, inputs []ActionSignalInput) ActionSignalReport {
	report, indexes, valid := prepareSignalReport(inputs)
	if len(valid) == 0 {
		return report
	}
	if r.finder == nil || r.client == nil {
		markLookupFailure(indexes, &report, "动作信号路由器未完整装配")
		return report
	}
	waiting, err := r.finder.FindWaitingActions(ctx, valid)
	if err != nil {
		markLookupFailure(indexes, &report, err.Error())
		return report
	}
	groups := r.groupResolvedSignals(valid, indexes, waiting, &report)
	r.routeGroups(ctx, groups, &report)
	return report
}

func prepareSignalReport(inputs []ActionSignalInput) (ActionSignalReport, []int, []ActionSignalInput) {
	report := ActionSignalReport{Items: make([]ActionSignalReceipt, len(inputs))}
	indexes := make([]int, 0, len(inputs))
	valid := make([]ActionSignalInput, 0, len(inputs))
	for index, input := range inputs {
		report.Items[index] = ActionSignalReceipt{Input: input, SignalID: stableActionSignalID(input)}
		if diagnostic := validateActionSignalInput(input); diagnostic != "" {
			report.Items[index].Status, report.Items[index].Error = ActionSignalRejected, diagnostic
			continue
		}
		indexes, valid = append(indexes, index), append(valid, input)
	}
	return report, indexes, valid
}

func validateActionSignalInput(input ActionSignalInput) string {
	if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.BotUUID) == "" || strings.TrimSpace(input.ActionRunID) == "" || strings.TrimSpace(input.CorrelationToken) == "" || strings.TrimSpace(input.Type) == "" {
		return "动作信号关联字段不完整"
	}
	if len(input.Payload) == 0 || !json.Valid(input.Payload) {
		return "动作信号 payload 不是有效 JSON"
	}
	return ""
}

func markLookupFailure(indexes []int, report *ActionSignalReport, diagnostic string) {
	for _, index := range indexes {
		markSignalFailure(&report.Items[index], "LOOKUP_FAILED", diagnostic)
	}
}

func (r *ActionSignalRouter) groupResolvedSignals(inputs []ActionSignalInput, indexes []int, waiting []*WaitingAction, report *ActionSignalReport) map[string][]routedActionSignal {
	groups := make(map[string][]routedActionSignal)
	observedAt := r.clock.Now().UnixMilli()
	for offset, input := range inputs {
		index := indexes[offset]
		var resolved *WaitingAction
		if offset < len(waiting) {
			resolved = waiting[offset]
		}
		if resolved == nil && isBarrierDecisionSignal(input.Type) {
			report.Items[index].Status = ActionSignalSkipped
			report.Items[index].Skipped = true
			continue
		}
		if diagnostic := validateWaitingAction(input, resolved); diagnostic != "" {
			report.Items[index].Status, report.Items[index].Error = ActionSignalRejected, diagnostic
			continue
		}
		signal := actionSignalProto(input, resolved, report.Items[index].SignalID, observedAt)
		groups[resolved.ExecutorNodeUUID] = append(groups[resolved.ExecutorNodeUUID], routedActionSignal{index: index, signal: signal})
	}
	return groups
}

func isBarrierDecisionSignal(signalType string) bool {
	return signalType == "barrier-release" || signalType == "barrier-fail"
}

func validateWaitingAction(input ActionSignalInput, waiting *WaitingAction) string {
	if waiting == nil || waiting.Generation <= 0 || waiting.ExecutorNodeUUID == "" {
		return "未找到完整关联的等待动作"
	}
	if waiting.Bot.DesiredStateGeneration != waiting.Generation || waiting.SessionUUID != input.RunID {
		return "等待动作 generation 或运行关联已变化"
	}
	return ""
}

type routedActionSignal struct {
	index  int
	signal *workerpb.BotActionSignal
}

func (r *ActionSignalRouter) routeGroups(ctx context.Context, groups map[string][]routedActionSignal, report *ActionSignalReport) {
	nodeUUIDs := make([]string, 0, len(groups))
	for nodeUUID := range groups {
		nodeUUIDs = append(nodeUUIDs, nodeUUID)
	}
	sort.Strings(nodeUUIDs)
	jobs := make(chan string, len(nodeUUIDs))
	for _, nodeUUID := range nodeUUIDs {
		jobs <- nodeUUID
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(actionSignalNodeConcurrency, len(nodeUUIDs)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for nodeUUID := range jobs {
				r.routeNode(ctx, nodeUUID, groups[nodeUUID], report)
			}
		}()
	}
	workers.Wait()
}

func (r *ActionSignalRouter) routeNode(ctx context.Context, nodeUUID string, signals []routedActionSignal, report *ActionSignalReport) {
	for start := 0; start < len(signals); start += actionSignalBatchSize {
		end := min(start+actionSignalBatchSize, len(signals))
		r.routeGroup(ctx, nodeUUID, signals[start:end], report)
	}
}

func (r *ActionSignalRouter) routeGroup(ctx context.Context, nodeUUID string, routed []routedActionSignal, report *ActionSignalReport) {
	signals := make([]*workerpb.BotActionSignal, 0, len(routed))
	for _, item := range routed {
		signals = append(signals, item.signal)
	}
	rpcCtx, cancel := context.WithTimeout(ctx, actionSignalRPCTimeout)
	defer cancel()
	response, err := r.client.SignalBotActions(rpcCtx, nodeUUID, &workerpb.SignalBotActionsRequest{Signals: signals})
	if err != nil {
		for _, item := range routed {
			markSignalFailure(&report.Items[item.index], "WORKER_UNAVAILABLE", err.Error())
		}
		return
	}
	results := make(map[string]*workerpb.SignalBotActionItemResult)
	if response != nil {
		for _, result := range response.Results {
			if result != nil {
				results[result.SignalId] = result
			}
		}
	}
	for _, item := range routed {
		applySignalReceipt(&report.Items[item.index], results[item.signal.SignalId])
	}
}

func applySignalReceipt(receipt *ActionSignalReceipt, result *workerpb.SignalBotActionItemResult) {
	if result == nil {
		markSignalFailure(receipt, "MISSING_RECEIPT", "Worker 未返回逐项回执")
		return
	}
	receipt.Accepted, receipt.Skipped = result.Accepted, result.Skipped
	receipt.ErrorCode, receipt.Error = result.ErrorCode, result.Error
	switch {
	case result.Accepted:
		receipt.Status = ActionSignalAccepted
	case result.Skipped && result.ErrorCode == "":
		receipt.Status = ActionSignalSkipped
	default:
		receipt.Status, receipt.Retriable = ActionSignalFailed, true
	}
}

func markSignalFailure(receipt *ActionSignalReceipt, code, message string) {
	receipt.Status, receipt.Retriable = ActionSignalFailed, true
	receipt.ErrorCode, receipt.Error = code, message
}

func actionSignalProto(input ActionSignalInput, waiting *WaitingAction, signalID string, observedAt int64) *workerpb.BotActionSignal {
	return &workerpb.BotActionSignal{
		SignalId: signalID, BotUuid: input.BotUUID, SessionUuid: input.RunID,
		Generation: waiting.Generation, ActionRunId: input.ActionRunID, StepId: waiting.Result.StepID,
		Type: input.Type, CorrelationToken: input.CorrelationToken, PayloadJson: string(input.Payload), ObservedAtUnixMs: observedAt,
	}
}

func stableActionSignalID(input ActionSignalInput) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", input.RunID, input.BotUUID, input.ActionRunID, input.CorrelationToken, input.Type, input.Payload)))
	return hex.EncodeToString(digest[:16])
}
