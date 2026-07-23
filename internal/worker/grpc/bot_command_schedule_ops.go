package grpc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-369 命令编排错误码（Worker 侧同步回执）。
const (
	CommandErrorIPCFailed          = "COMMAND_IPC_FAILED"
	CommandErrorArgumentInvalid    = "COMMAND_ARGUMENT_INVALID"
	CommandErrorScheduleRejected   = "COMMAND_SCHEDULE_REJECTED"
	CommandErrorDeadlineExceeded   = "COMMAND_DEADLINE_EXCEEDED"
	CommandErrorRuntimeUnavailable = "COMMAND_RUNTIME_UNAVAILABLE"
	CommandErrorRouteFailed        = "COMMAND_ROUTE_FAILED"
	CommandErrorSendFailed         = "COMMAND_SEND_FAILED"
)

// 命令编排批量 RPC 限值（与规格保持一致）。
const (
	commandScheduleBatchLimit        = 100
	commandScheduleIPCPrepareTimeout = 5 * time.Second
	commandScheduleIPCReleaseTimeout = 5 * time.Second
	commandScheduleIPCCancelTimeout  = 5 * time.Second
)

// commandScheduleOperation 标识批量 RPC 类型，供派生稳定 IPC requestId 使用。
type commandScheduleOperation string

const (
	commandScheduleOpApply   commandScheduleOperation = "apply"
	commandScheduleOpRelease commandScheduleOperation = "release"
	commandScheduleOpCancel  commandScheduleOperation = "cancel"
)

// ApplyBotCommandSchedules 接收 CP 下发的 occurrence plan，写入 Bot Worker scheduler。
func (s *Server) ApplyBotCommandSchedules(ctx context.Context, req *workerpb.ApplyBotCommandSchedulesRequest) (*workerpb.ApplyBotCommandSchedulesResponse, error) {
	if err := validateApplyBotCommandSchedules(req); err != nil {
		return nil, err
	}
	if err := s.prepareBotFleet(ctx); err != nil {
		return nil, status.Errorf(codes.Unavailable, "Bot 集群未就绪: %v", err)
	}
	results := make([]*workerpb.ApplyBotCommandScheduleItemResult, 0, len(req.Items))
	for _, item := range req.Items {
		results = append(results, s.applyCommandScheduleOnce(ctx, req.RequestId, item))
	}
	return &workerpb.ApplyBotCommandSchedulesResponse{RequestId: req.RequestId, Results: results}, nil
}

func (s *Server) applyCommandScheduleOnce(ctx context.Context, batchRequestID string, item *workerpb.ApplyBotCommandScheduleItem) *workerpb.ApplyBotCommandScheduleItemResult {
	if item == nil {
		return rejectedApplyResult("", "", CommandErrorArgumentInvalid, "命令计划项不能为空")
	}
	if !commandScheduleApplyShapeValid(item) {
		return rejectedApplyResult(item.GetBotUuid(), item.GetScheduleRunId(), CommandErrorArgumentInvalid, "Apply 字段不完整或非法")
	}
	ipcRequestID, err := commandScheduleIPCRequestID(batchRequestID, commandScheduleOpApply, item.GetRunUuid(), item.GetBotUuid(), item.GetGeneration(), item.GetScheduleRunId())
	if err != nil {
		return rejectedApplyResult(item.GetBotUuid(), item.GetScheduleRunId(), CommandErrorArgumentInvalid, err.Error())
	}
	ipcCmd := bot.CommandScheduleCommand{
		Cmd:                   "command-schedule",
		RequestID:             ipcRequestID,
		RunID:                 strconv.FormatInt(item.GetRunId(), 10),
		RunUUID:               item.GetRunUuid(),
		BotUUID:               item.GetBotUuid(),
		Generation:            item.GetGeneration(),
		StepID:                item.GetStepId(),
		ScheduleRunID:         item.GetScheduleRunId(),
		CorrelationToken:      item.GetCorrelationToken(),
		StartMode:             strings.ToLower(item.GetStartMode()),
		ScheduleStartAtUnixMs: item.GetScheduleStartAtUnixMs(),
		BarrierKey:            item.GetBarrierKey(),
		RunDeadlineUnixMs:     item.GetRunDeadlineUnixMs(),
		JitterSeed:            item.GetJitterSeed(),
		Plan:                  commandSchedulePlanFromProto(item.GetPlan()),
		SkipOccurrences:       commandScheduleSkipFromProto(item.GetSkipOccurrences()),
	}
	deadline := time.Duration(item.GetRunDeadlineUnixMs()-time.Now().UnixMilli()) * time.Millisecond
	if deadline <= 0 || deadline > commandScheduleIPCPrepareTimeout {
		deadline = commandScheduleIPCPrepareTimeout
	}
	event, err := s.botFleet.ApplyCommandSchedule(ctx, ipcRequestID, ipcCmd, deadline)
	if err != nil {
		return &workerpb.ApplyBotCommandScheduleItemResult{
			BotUuid:       item.GetBotUuid(),
			ScheduleRunId: item.GetScheduleRunId(),
			Disposition:   "unknown",
			ErrorCode:     CommandErrorIPCFailed,
			Error:         err.Error(),
		}
	}
	return commandScheduleApplyResultFromEvent(item.GetBotUuid(), item.GetScheduleRunId(), event)
}

// ReleaseBotCommandSchedules 启动已 prepared 的 barrier 命令计划。
func (s *Server) ReleaseBotCommandSchedules(ctx context.Context, req *workerpb.ReleaseBotCommandSchedulesRequest) (*workerpb.ReleaseBotCommandSchedulesResponse, error) {
	if err := validateReleaseBotCommandSchedules(req); err != nil {
		return nil, err
	}
	if err := s.prepareBotFleet(ctx); err != nil {
		return nil, status.Errorf(codes.Unavailable, "Bot 集群未就绪: %v", err)
	}
	results := make([]*workerpb.ReleaseBotCommandScheduleItemResult, 0, len(req.Items))
	for _, item := range req.Items {
		results = append(results, s.releaseCommandScheduleOnce(ctx, req.RequestId, item))
	}
	return &workerpb.ReleaseBotCommandSchedulesResponse{RequestId: req.RequestId, Results: results}, nil
}

func (s *Server) releaseCommandScheduleOnce(ctx context.Context, batchRequestID string, item *workerpb.ReleaseBotCommandScheduleItem) *workerpb.ReleaseBotCommandScheduleItemResult {
	if item == nil {
		return rejectedReleaseResult("", "", CommandErrorArgumentInvalid, "Release 项不能为空")
	}
	if item.GetBarrierKey() == "" || item.GetReleaseAtUnixMs() <= 0 {
		return rejectedReleaseResult(item.GetBotUuid(), item.GetScheduleRunId(), CommandErrorArgumentInvalid, "Release 字段不完整")
	}
	ipcRequestID, err := commandScheduleIPCRequestID(batchRequestID, commandScheduleOpRelease, item.GetRunUuid(), item.GetBotUuid(), item.GetGeneration(), item.GetScheduleRunId())
	if err != nil {
		return rejectedReleaseResult(item.GetBotUuid(), item.GetScheduleRunId(), CommandErrorArgumentInvalid, err.Error())
	}
	ipcCmd := bot.CommandScheduleReleaseCommand{
		Cmd:             "command-schedule-release",
		RequestID:       ipcRequestID,
		RunUUID:         item.GetRunUuid(),
		BotUUID:         item.GetBotUuid(),
		Generation:      item.GetGeneration(),
		StepID:          item.GetStepId(),
		ScheduleRunID:   item.GetScheduleRunId(),
		BarrierKey:      item.GetBarrierKey(),
		ReleaseAtUnixMs: item.GetReleaseAtUnixMs(),
	}
	deadline := time.Duration(item.GetReleaseAtUnixMs()-time.Now().UnixMilli()) * time.Millisecond
	if deadline <= 0 || deadline > commandScheduleIPCReleaseTimeout {
		deadline = commandScheduleIPCReleaseTimeout
	}
	event, err := s.botFleet.ReleaseCommandSchedule(ctx, ipcRequestID, ipcCmd, deadline)
	if err != nil {
		return &workerpb.ReleaseBotCommandScheduleItemResult{
			BotUuid:       item.GetBotUuid(),
			ScheduleRunId: item.GetScheduleRunId(),
			Disposition:   "unknown",
			ErrorCode:     CommandErrorIPCFailed,
			Error:         err.Error(),
		}
	}
	return commandScheduleReleaseResultFromEvent(item.GetBotUuid(), item.GetScheduleRunId(), event)
}

// CancelBotCommandSchedules 下发 CP cancel intent。
func (s *Server) CancelBotCommandSchedules(ctx context.Context, req *workerpb.CancelBotCommandSchedulesRequest) (*workerpb.CancelBotCommandSchedulesResponse, error) {
	if err := validateCancelBotCommandSchedules(req); err != nil {
		return nil, err
	}
	if err := s.prepareBotFleet(ctx); err != nil {
		return nil, status.Errorf(codes.Unavailable, "Bot 集群未就绪: %v", err)
	}
	results := make([]*workerpb.CancelBotCommandScheduleItemResult, 0, len(req.Items))
	for _, item := range req.Items {
		results = append(results, s.cancelCommandScheduleOnce(ctx, req.RequestId, item))
	}
	return &workerpb.CancelBotCommandSchedulesResponse{RequestId: req.RequestId, Results: results}, nil
}

func (s *Server) cancelCommandScheduleOnce(ctx context.Context, batchRequestID string, item *workerpb.CancelBotCommandScheduleItem) *workerpb.CancelBotCommandScheduleItemResult {
	if item == nil {
		return rejectedCancelResult("", "", CommandErrorArgumentInvalid, "Cancel 项不能为空")
	}
	if item.GetScheduleRunId() == "" || item.GetBotUuid() == "" {
		return rejectedCancelResult(item.GetBotUuid(), item.GetScheduleRunId(), CommandErrorArgumentInvalid, "Cancel 字段不完整")
	}
	ipcRequestID, err := commandScheduleIPCRequestID(batchRequestID, commandScheduleOpCancel, item.GetRunUuid(), item.GetBotUuid(), item.GetGeneration(), item.GetScheduleRunId())
	if err != nil {
		return rejectedCancelResult(item.GetBotUuid(), item.GetScheduleRunId(), CommandErrorArgumentInvalid, err.Error())
	}
	ipcCmd := bot.CommandScheduleCancelCommand{
		Cmd:              "command-schedule-cancel",
		RequestID:        ipcRequestID,
		RunUUID:          item.GetRunUuid(),
		BotUUID:          item.GetBotUuid(),
		Generation:       item.GetGeneration(),
		StepID:           item.GetStepId(),
		ScheduleRunID:    item.GetScheduleRunId(),
		Reason:           item.GetReason(),
		CorrelationToken: item.GetCorrelationToken(),
	}
	event, err := s.botFleet.CancelCommandSchedule(ctx, ipcRequestID, ipcCmd, commandScheduleIPCCancelTimeout)
	if err != nil {
		return &workerpb.CancelBotCommandScheduleItemResult{
			BotUuid:       item.GetBotUuid(),
			ScheduleRunId: item.GetScheduleRunId(),
			Disposition:   "unknown",
			ErrorCode:     CommandErrorIPCFailed,
			Error:         err.Error(),
		}
	}
	return commandScheduleCancelResultFromEvent(item.GetBotUuid(), item.GetScheduleRunId(), event)
}

func validateApplyBotCommandSchedules(req *workerpb.ApplyBotCommandSchedulesRequest) error {
	if req == nil {
		return status.Error(codes.InvalidArgument, "Apply 请求不能为空")
	}
	if _, err := parseUUID(req.RequestId); err != nil {
		return status.Error(codes.InvalidArgument, "requestId 必须是合法 UUID")
	}
	if len(req.Items) == 0 || len(req.Items) > commandScheduleBatchLimit {
		return status.Errorf(codes.InvalidArgument, "单批命令计划不能超过 %d 个", commandScheduleBatchLimit)
	}
	for _, item := range req.Items {
		if !commandScheduleApplyShapeValid(item) {
			return status.Error(codes.InvalidArgument, "Apply 项字段不完整")
		}
	}
	return nil
}

func commandScheduleApplyShapeValid(item *workerpb.ApplyBotCommandScheduleItem) bool {
	if item == nil {
		return false
	}
	if _, err := parseUUID(item.GetRunUuid()); err != nil {
		return false
	}
	if _, err := parseUUID(item.GetBotUuid()); err != nil {
		return false
	}
	if _, err := parseUUID(item.GetScheduleRunId()); err != nil {
		return false
	}
	if item.GetGeneration() <= 0 || item.GetRunDeadlineUnixMs() <= 0 || item.GetJitterSeed() == "" {
		return false
	}
	if item.GetStepId() == "" || item.GetCorrelationToken() == "" {
		return false
	}
	if item.GetPlan() == nil || len(item.GetPlan().GetOccurrences()) == 0 {
		return false
	}
	switch strings.ToLower(item.GetStartMode()) {
	case "absolute":
		return item.GetScheduleStartAtUnixMs() > 0 && item.GetBarrierKey() == ""
	case "barrier":
		return item.GetBarrierKey() != "" && item.GetScheduleStartAtUnixMs() == 0
	default:
		return false
	}
}

func validateReleaseBotCommandSchedules(req *workerpb.ReleaseBotCommandSchedulesRequest) error {
	if req == nil {
		return status.Error(codes.InvalidArgument, "Release 请求不能为空")
	}
	if _, err := parseUUID(req.RequestId); err != nil {
		return status.Error(codes.InvalidArgument, "requestId 必须是合法 UUID")
	}
	if len(req.Items) == 0 || len(req.Items) > commandScheduleBatchLimit {
		return status.Errorf(codes.InvalidArgument, "单批 Release 不能超过 %d 个", commandScheduleBatchLimit)
	}
	for _, item := range req.Items {
		if _, err := parseUUID(item.GetRunUuid()); err != nil {
			return status.Error(codes.InvalidArgument, "Release runUuid 非法")
		}
		if _, err := parseUUID(item.GetBotUuid()); err != nil {
			return status.Error(codes.InvalidArgument, "Release botUuid 非法")
		}
		if _, err := parseUUID(item.GetScheduleRunId()); err != nil {
			return status.Error(codes.InvalidArgument, "Release scheduleRunId 非法")
		}
		if item.GetGeneration() <= 0 || item.GetReleaseAtUnixMs() <= 0 || item.GetBarrierKey() == "" {
			return status.Error(codes.InvalidArgument, "Release 字段不完整")
		}
	}
	return nil
}

func validateCancelBotCommandSchedules(req *workerpb.CancelBotCommandSchedulesRequest) error {
	if req == nil {
		return status.Error(codes.InvalidArgument, "Cancel 请求不能为空")
	}
	if _, err := parseUUID(req.RequestId); err != nil {
		return status.Error(codes.InvalidArgument, "requestId 必须是合法 UUID")
	}
	if len(req.Items) == 0 || len(req.Items) > commandScheduleBatchLimit {
		return status.Errorf(codes.InvalidArgument, "单批 Cancel 不能超过 %d 个", commandScheduleBatchLimit)
	}
	for _, item := range req.Items {
		if _, err := parseUUID(item.GetRunUuid()); err != nil {
			return status.Error(codes.InvalidArgument, "Cancel runUuid 非法")
		}
		if _, err := parseUUID(item.GetBotUuid()); err != nil {
			return status.Error(codes.InvalidArgument, "Cancel botUuid 非法")
		}
		if _, err := parseUUID(item.GetScheduleRunId()); err != nil {
			return status.Error(codes.InvalidArgument, "Cancel scheduleRunId 非法")
		}
		if item.GetGeneration() <= 0 {
			return status.Error(codes.InvalidArgument, "Cancel generation 非法")
		}
	}
	return nil
}

func rejectedApplyResult(botUUID, scheduleRunID, code, msg string) *workerpb.ApplyBotCommandScheduleItemResult {
	return &workerpb.ApplyBotCommandScheduleItemResult{
		BotUuid:       botUUID,
		ScheduleRunId: scheduleRunID,
		Disposition:   "rejected",
		ErrorCode:     code,
		Error:         msg,
	}
}

func rejectedReleaseResult(botUUID, scheduleRunID, code, msg string) *workerpb.ReleaseBotCommandScheduleItemResult {
	return &workerpb.ReleaseBotCommandScheduleItemResult{
		BotUuid:       botUUID,
		ScheduleRunId: scheduleRunID,
		Disposition:   "rejected",
		ErrorCode:     code,
		Error:         msg,
	}
}

func rejectedCancelResult(botUUID, scheduleRunID, code, msg string) *workerpb.CancelBotCommandScheduleItemResult {
	return &workerpb.CancelBotCommandScheduleItemResult{
		BotUuid:       botUUID,
		ScheduleRunId: scheduleRunID,
		Disposition:   "rejected",
		ErrorCode:     code,
		Error:         msg,
	}
}

// commandScheduleIPCRequestID 为同一幂等项派生稳定 IPC requestId（UUIDv5）。
func commandScheduleIPCRequestID(batchRequestID string, op commandScheduleOperation, runUUID, botUUID string, generation int64, scheduleRunID string) (string, error) {
	namespace, err := parseUUID(batchRequestID)
	if err != nil {
		return "", fmt.Errorf("批次 requestId 非法: %w", err)
	}
	_ = runUUID
	name := strings.Join([]string{
		string(op), botUUID, strconv.FormatInt(generation, 10), scheduleRunID,
	}, "\u0000")
	return uuidV5(namespace, name)
}

func commandSchedulePlanFromProto(plan *workerpb.AppliedCommandOccurrencePlan) bot.CommandSchedulePlan {
	if plan == nil {
		return bot.CommandSchedulePlan{}
	}
	out := bot.CommandSchedulePlan{
		DurationMS: plan.GetDurationMs(),
		JitterMS:   plan.GetJitterMs(),
	}
	for _, occ := range plan.GetOccurrences() {
		out.Occurrences = append(out.Occurrences, bot.CommandScheduleOccurrence{
			CommandID:             occ.GetCommandId(),
			Occurrence:            int(occ.GetOccurrence()),
			CommandDeclarationIdx: int(occ.GetCommandDeclarationIndex()),
			BaseAtMS:              occ.GetBaseAtMs(),
			JitterOffsetMS:        occ.GetJitterOffsetMs(),
			ActionRunID:           occ.GetActionRunId(),
			Command:               occ.GetCommand(),
		})
	}
	return out
}

func commandScheduleSkipFromProto(keys []*workerpb.CommandOccurrenceKey) []bot.CommandOccurrenceKey {
	if len(keys) == 0 {
		return nil
	}
	out := make([]bot.CommandOccurrenceKey, 0, len(keys))
	for _, k := range keys {
		if k == nil {
			continue
		}
		out = append(out, bot.CommandOccurrenceKey{
			CommandID:  k.GetCommandId(),
			Occurrence: int(k.GetOccurrence()),
		})
	}
	return out
}

// Bot Worker 同步回执为扁平字段：accepted / alreadyReleased / alreadyCancelled / errorCode / error。
func commandScheduleApplyResultFromEvent(botUUID, scheduleRunID string, event *bot.BotWorkerEvent) *workerpb.ApplyBotCommandScheduleItemResult {
	if event == nil {
		return &workerpb.ApplyBotCommandScheduleItemResult{BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "unknown"}
	}
	if event.Evt != "command-schedule-accepted" {
		return &workerpb.ApplyBotCommandScheduleItemResult{
			BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "unknown",
			ErrorCode: CommandErrorIPCFailed, Error: "Bot Worker 回执类型非法: " + event.Evt,
		}
	}
	if event.Accepted {
		return &workerpb.ApplyBotCommandScheduleItemResult{
			BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "accepted",
		}
	}
	code := event.ErrorCode
	if code == "" {
		code = CommandErrorScheduleRejected
	}
	return &workerpb.ApplyBotCommandScheduleItemResult{
		BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "rejected",
		ErrorCode: code, Error: event.Error,
	}
}

func commandScheduleReleaseResultFromEvent(botUUID, scheduleRunID string, event *bot.BotWorkerEvent) *workerpb.ReleaseBotCommandScheduleItemResult {
	if event == nil {
		return &workerpb.ReleaseBotCommandScheduleItemResult{BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "unknown"}
	}
	if event.Evt != "command-schedule-release-result" {
		return &workerpb.ReleaseBotCommandScheduleItemResult{
			BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "unknown",
			ErrorCode: CommandErrorIPCFailed, Error: "Bot Worker 回执类型非法: " + event.Evt,
		}
	}
	if event.Accepted {
		return &workerpb.ReleaseBotCommandScheduleItemResult{
			BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "accepted",
			AlreadyReleased: event.AlreadyReleased,
		}
	}
	code := event.ErrorCode
	if code == "" {
		code = CommandErrorScheduleRejected
	}
	return &workerpb.ReleaseBotCommandScheduleItemResult{
		BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "rejected",
		ErrorCode: code, Error: event.Error,
	}
}

func commandScheduleCancelResultFromEvent(botUUID, scheduleRunID string, event *bot.BotWorkerEvent) *workerpb.CancelBotCommandScheduleItemResult {
	if event == nil {
		return &workerpb.CancelBotCommandScheduleItemResult{BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "unknown"}
	}
	if event.Evt != "command-schedule-cancel-result" {
		return &workerpb.CancelBotCommandScheduleItemResult{
			BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "unknown",
			ErrorCode: CommandErrorIPCFailed, Error: "Bot Worker 回执类型非法: " + event.Evt,
		}
	}
	if event.Accepted {
		return &workerpb.CancelBotCommandScheduleItemResult{
			BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "accepted",
			AlreadyCancelled: event.AlreadyCancelled,
		}
	}
	code := event.ErrorCode
	if code == "" {
		code = CommandErrorScheduleRejected
	}
	return &workerpb.CancelBotCommandScheduleItemResult{
		BotUuid: botUUID, ScheduleRunId: scheduleRunID, Disposition: "rejected",
		ErrorCode: code, Error: event.Error,
	}
}
