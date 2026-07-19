package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// WaitingActionFinder 提供强关联的运行中动作查询。
type WaitingActionFinder interface {
	FindWaitingAction(ctx context.Context, runID, botUUID, actionRunID, correlationToken string) (*WaitingAction, error)
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

// Route 保持输入顺序返回逐项结果，并对合法项按执行节点分组。
func (r *ActionSignalRouter) Route(ctx context.Context, inputs []ActionSignalInput) ActionSignalReport {
	report := ActionSignalReport{Items: make([]ActionSignalReceipt, len(inputs))}
	groups := make(map[string][]routedActionSignal)
	for index, input := range inputs {
		report.Items[index] = ActionSignalReceipt{Input: input, SignalID: stableActionSignalID(input)}
		waiting, diagnostic, retriable := r.resolve(ctx, input)
		if diagnostic != "" {
			if retriable {
				markSignalFailure(&report.Items[index], "LOOKUP_FAILED", diagnostic)
			} else {
				report.Items[index].Status, report.Items[index].Error = ActionSignalRejected, diagnostic
			}
			continue
		}
		signal := actionSignalProto(input, waiting, report.Items[index].SignalID, r.clock.Now().UnixMilli())
		groups[waiting.ExecutorNodeUUID] = append(groups[waiting.ExecutorNodeUUID], routedActionSignal{index: index, signal: signal})
	}
	for nodeUUID, signals := range groups {
		r.routeGroup(ctx, nodeUUID, signals, &report)
	}
	return report
}

type routedActionSignal struct {
	index  int
	signal *workerpb.BotActionSignal
}

func (r *ActionSignalRouter) resolve(ctx context.Context, input ActionSignalInput) (*WaitingAction, string, bool) {
	if r.finder == nil || r.client == nil {
		return nil, "动作信号路由器未完整装配", true
	}
	if strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.BotUUID) == "" || strings.TrimSpace(input.ActionRunID) == "" || strings.TrimSpace(input.CorrelationToken) == "" || strings.TrimSpace(input.Type) == "" {
		return nil, "动作信号关联字段不完整", false
	}
	if len(input.Payload) == 0 || !json.Valid(input.Payload) {
		return nil, "动作信号 payload 不是有效 JSON", false
	}
	waiting, err := r.finder.FindWaitingAction(ctx, input.RunID, input.BotUUID, input.ActionRunID, input.CorrelationToken)
	if err != nil {
		return nil, err.Error(), true
	}
	if waiting == nil || waiting.Generation <= 0 || waiting.ExecutorNodeUUID == "" {
		return nil, "未找到完整关联的等待动作", false
	}
	if waiting.Bot.DesiredStateGeneration != waiting.Generation || waiting.SessionUUID != input.RunID {
		return nil, "等待动作 generation 或运行关联已变化", false
	}
	return waiting, "", false
}

func (r *ActionSignalRouter) routeGroup(ctx context.Context, nodeUUID string, routed []routedActionSignal, report *ActionSignalReport) {
	signals := make([]*workerpb.BotActionSignal, 0, len(routed))
	for _, item := range routed {
		signals = append(signals, item.signal)
	}
	response, err := r.client.SignalBotActions(ctx, nodeUUID, &workerpb.SignalBotActionsRequest{Signals: signals})
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
