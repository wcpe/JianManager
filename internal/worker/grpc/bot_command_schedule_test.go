package grpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestApplyBotCommandSchedules_AcceptedOnAcceptedEvent(t *testing.T) {
	fake := &fakeBotFleetManager{apply: &bot.BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: true}}
	srv := newBotFleetTestServer(fake)
	resp, err := srv.ApplyBotCommandSchedules(context.Background(), &workerpb.ApplyBotCommandSchedulesRequest{
		RequestId: "11111111-2222-3333-4444-555555555555",
		Items: []*workerpb.ApplyBotCommandScheduleItem{
			makeApplyItem(),
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "accepted", resp.Results[0].Disposition)
	require.Equal(t, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", resp.Results[0].ScheduleRunId)
}

func TestApplyBotCommandSchedules_RejectedCarriesErrorCode(t *testing.T) {
	fake := &fakeBotFleetManager{apply: &bot.BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: false, ErrorCode: "COMMAND_ARGUMENT_INVALID", Error: "bad"}}
	srv := newBotFleetTestServer(fake)
	resp, err := srv.ApplyBotCommandSchedules(context.Background(), &workerpb.ApplyBotCommandSchedulesRequest{
		RequestId: "11111111-2222-3333-4444-555555555555",
		Items:     []*workerpb.ApplyBotCommandScheduleItem{makeApplyItem()},
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", resp.Results[0].Disposition)
	require.Equal(t, "COMMAND_ARGUMENT_INVALID", resp.Results[0].ErrorCode)
}

func TestApplyBotCommandSchedules_TransportFailureIsUnknown(t *testing.T) {
	fake := &fakeBotFleetManager{applyErr: context.DeadlineExceeded}
	srv := newBotFleetTestServer(fake)
	resp, err := srv.ApplyBotCommandSchedules(context.Background(), &workerpb.ApplyBotCommandSchedulesRequest{
		RequestId: "11111111-2222-3333-4444-555555555555",
		Items:     []*workerpb.ApplyBotCommandScheduleItem{makeApplyItem()},
	})
	require.NoError(t, err)
	require.Equal(t, "unknown", resp.Results[0].Disposition)
	require.Equal(t, "COMMAND_IPC_FAILED", resp.Results[0].ErrorCode)
}

func TestApplyBotCommandSchedules_RejectsInvalidRequestID(t *testing.T) {
	srv := newBotFleetTestServer(&fakeBotFleetManager{})
	_, err := srv.ApplyBotCommandSchedules(context.Background(), &workerpb.ApplyBotCommandSchedulesRequest{
		RequestId: "not-a-uuid",
		Items:     []*workerpb.ApplyBotCommandScheduleItem{makeApplyItem()},
	})
	require.Error(t, err)
}

func TestApplyBotCommandSchedules_RejectsBadStartMode(t *testing.T) {
	srv := newBotFleetTestServer(&fakeBotFleetManager{})
	item := makeApplyItem()
	item.StartMode = "unknown"
	item.BarrierKey = ""
	item.ScheduleStartAtUnixMs = 0
	_, err := srv.ApplyBotCommandSchedules(context.Background(), &workerpb.ApplyBotCommandSchedulesRequest{
		RequestId: "11111111-2222-3333-4444-555555555555",
		Items:     []*workerpb.ApplyBotCommandScheduleItem{item},
	})
	require.Error(t, err)
}

func TestReleaseBotCommandSchedules_Accepted(t *testing.T) {
	fake := &fakeBotFleetManager{release: &bot.BotWorkerEvent{Evt: "command-schedule-release-result", Accepted: true, AlreadyReleased: false}}
	srv := newBotFleetTestServer(fake)
	resp, err := srv.ReleaseBotCommandSchedules(context.Background(), &workerpb.ReleaseBotCommandSchedulesRequest{
		RequestId: "11111111-2222-3333-4444-555555555555",
		Items:     []*workerpb.ReleaseBotCommandScheduleItem{makeReleaseItem()},
	})
	require.NoError(t, err)
	require.Equal(t, "accepted", resp.Results[0].Disposition)
	require.False(t, resp.Results[0].AlreadyReleased)
}

func TestReleaseBotCommandSchedules_AlreadyReleased(t *testing.T) {
	fake := &fakeBotFleetManager{release: &bot.BotWorkerEvent{Evt: "command-schedule-release-result", Accepted: true, AlreadyReleased: true}}
	srv := newBotFleetTestServer(fake)
	resp, err := srv.ReleaseBotCommandSchedules(context.Background(), &workerpb.ReleaseBotCommandSchedulesRequest{
		RequestId: "11111111-2222-3333-4444-555555555555",
		Items:     []*workerpb.ReleaseBotCommandScheduleItem{makeReleaseItem()},
	})
	require.NoError(t, err)
	require.True(t, resp.Results[0].AlreadyReleased)
}

func TestCancelBotCommandSchedules_AlreadyCancelled(t *testing.T) {
	fake := &fakeBotFleetManager{cancel: &bot.BotWorkerEvent{Evt: "command-schedule-cancel-result", Accepted: true, AlreadyCancelled: true}}
	srv := newBotFleetTestServer(fake)
	resp, err := srv.CancelBotCommandSchedules(context.Background(), &workerpb.CancelBotCommandSchedulesRequest{
		RequestId: "11111111-2222-3333-4444-555555555555",
		Items:     []*workerpb.CancelBotCommandScheduleItem{makeCancelItem()},
	})
	require.NoError(t, err)
	require.Equal(t, "accepted", resp.Results[0].Disposition)
	require.True(t, resp.Results[0].AlreadyCancelled)
}

func TestCommandScheduleResultMapping_SentToSucceeded(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:   "command-schedule-result",
		BotID: "00000000-0000-0000-0000-000000000001",
		Data:  []byte(`{"runUuid":"00000000-0000-0000-0000-000000000099","botUuid":"00000000-0000-0000-0000-000000000001","generation":1,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"a","occurrence":0,"attempt":1,"durationMs":5,"observedAtUnixMs":100,"status":"sent","plannedAtUnixMs":95,"sentAtUnixMs":100,"attemptErrors":[]}`),
	}
	fleet := botWorkerEventToFleetProto(event, "")
	require.Len(t, fleet, 1)
	action := fleet[0].GetActionEvent()
	require.Equal(t, "succeeded", action.Status)
	require.Empty(t, action.ErrorCode)
	require.Equal(t, "00000000-0000-0000-0000-0000000000bb", action.ActionRunId)
	require.Contains(t, action.ResultJson, `"status":"sent"`)
}

// TestCommandScheduleResult_FlatWireFormatFromTS TS Bot Worker 实际输出的是**扁平** JSON
// （apps/bot-worker/src/scheduler/command-schedule.ts emitResult → process.stdout.write）：
// runUuid/botUuid/status/actionRunId 全在顶层，既没有 data 也没有 commandScheduleResult 包裹。
//
// 这条从「真实线上 JSON 文本」出发做解码，而不是像此前那样手工把字段塞进 Data——
// 正是手工 fixture 掩盖了协议不兼容：旧实现在 len(Data)==0 时直接返回 0 件事件，
// 命令终态在 Fleet 侧全部丢失，而单元测试却全绿。
func TestCommandScheduleResult_FlatWireFormatFromTS(t *testing.T) {
	// 直接从 TS 端 emitResult 会写出的形状取样本（字段名与顺序照抄 interface）。
	raw := `{"evt":"command-schedule-result","runId":"run-1","runUuid":"00000000-0000-0000-0000-000000000099","botUuid":"00000000-0000-0000-0000-000000000001","generation":3,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"cmd-a","occurrence":2,"attempt":1,"durationMs":42,"observedAtUnixMs":1700000000000,"status":"sent","plannedAtUnixMs":1699999999000,"sentAtUnixMs":1700000000000,"attemptErrors":[]}`

	var event bot.BotWorkerEvent
	require.NoError(t, json.Unmarshal([]byte(raw), &event), "TS 扁平 JSON 必须能被 BotWorkerEvent 解码")
	require.Empty(t, event.Data, "扁平形态没有 data 字段——这正是旧实现丢掉事件的原因")

	fleet := botWorkerEventToFleetProto(&event, "")
	require.Len(t, fleet, 1, "扁平 command-schedule-result 必须产出 1 个 Fleet 事件")

	action := fleet[0].GetActionEvent()
	require.NotNil(t, action)
	require.Equal(t, "succeeded", action.Status)
	require.Equal(t, "00000000-0000-0000-0000-0000000000bb", action.ActionRunId)
	require.Equal(t, "00000000-0000-0000-0000-000000000001", action.BotUuid)
	require.Equal(t, "00000000-0000-0000-0000-000000000099", action.SessionUuid)
	require.Equal(t, int64(3), action.Generation)
	require.Equal(t, "00000000-0000-0000-0000-0000000000cc", action.CorrelationToken)
	require.Equal(t, int64(42), action.DurationMs)
	// ResultJson 里应保留原始 wire 字段（供上层按 occurrence 归并）。
	require.Contains(t, action.ResultJson, `"status":"sent"`)
	require.Contains(t, action.ResultJson, `"commandId":"cmd-a"`)
}

// TestCommandScheduleResult_FlatFailuresKeepErrorCode 扁平形态的失败/超时/取消也必须带出错误码。
func TestCommandScheduleResult_FlatFailuresKeepErrorCode(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantStat string
		wantCode string
	}{
		{
			name:     "timed_out",
			raw:      `{"evt":"command-schedule-result","runUuid":"s-1","botUuid":"b-1","generation":1,"actionRunId":"a-1","status":"timed_out","errorCode":"COMMAND_DEADLINE_EXCEEDED","message":"deadline","durationMs":0,"observedAtUnixMs":100,"attemptErrors":[]}`,
			wantStat: "timed_out", wantCode: "COMMAND_DEADLINE_EXCEEDED",
		},
		{
			name:     "cancelled",
			raw:      `{"evt":"command-schedule-result","runUuid":"s-1","botUuid":"b-1","generation":1,"actionRunId":"a-1","status":"cancelled","errorCode":"ACTION_CANCELLED","message":"x","durationMs":0,"observedAtUnixMs":100,"attemptErrors":[]}`,
			wantStat: "cancelled", wantCode: "ACTION_CANCELLED",
		},
		{
			name:     "failed_without_code_defaults",
			raw:      `{"evt":"command-schedule-result","runUuid":"s-1","botUuid":"b-1","generation":1,"actionRunId":"a-1","status":"failed","message":"boom","durationMs":0,"observedAtUnixMs":100,"attemptErrors":[]}`,
			wantStat: "failed", wantCode: "COMMAND_SEND_FAILED",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var event bot.BotWorkerEvent
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &event))
			fleet := botWorkerEventToFleetProto(&event, "")
			require.Len(t, fleet, 1)
			action := fleet[0].GetActionEvent()
			require.Equal(t, tc.wantStat, action.Status)
			require.Equal(t, tc.wantCode, action.ErrorCode)
		})
	}
}

// TestCommandScheduleResult_StillAcceptsEmbeddedData 内嵌 data 形态（历史/手工构造）不能被改坏。
func TestCommandScheduleResult_StillAcceptsEmbeddedData(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:  "command-schedule-result",
		Data: []byte(`{"runUuid":"s-1","botUuid":"b-1","generation":1,"actionRunId":"a-1","status":"sent","durationMs":1,"observedAtUnixMs":100,"attemptErrors":[]}`),
	}
	fleet := botWorkerEventToFleetProto(event, "")
	require.Len(t, fleet, 1)
	require.Equal(t, "succeeded", fleet[0].GetActionEvent().Status)
	require.Equal(t, "a-1", fleet[0].GetActionEvent().ActionRunId)
}

func TestCommandScheduleResultMapping_TimedOutMappedToDeadlineExceeded(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:   "command-schedule-result",
		BotID: "00000000-0000-0000-0000-000000000001",
		Data:  []byte(`{"runUuid":"00000000-0000-0000-0000-000000000099","botUuid":"00000000-0000-0000-0000-000000000001","generation":1,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"a","occurrence":0,"attempt":1,"durationMs":0,"observedAtUnixMs":100,"status":"timed_out","plannedAtUnixMs":95,"sentAtUnixMs":null,"errorCode":"COMMAND_DEADLINE_EXCEEDED","message":"deadline","attemptErrors":[]}`),
	}
	fleet := botWorkerEventToFleetProto(event, "")
	require.Equal(t, "timed_out", fleet[0].GetActionEvent().Status)
	require.Equal(t, "COMMAND_DEADLINE_EXCEEDED", fleet[0].GetActionEvent().ErrorCode)
}

func TestCommandScheduleResultMapping_CancelledToCancelled(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:   "command-schedule-result",
		BotID: "00000000-0000-0000-0000-000000000001",
		Data:  []byte(`{"runUuid":"00000000-0000-0000-0000-000000000099","botUuid":"00000000-0000-0000-0000-000000000001","generation":1,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"a","occurrence":0,"attempt":1,"durationMs":0,"observedAtUnixMs":100,"status":"cancelled","plannedAtUnixMs":95,"sentAtUnixMs":null,"errorCode":"ACTION_CANCELLED","message":"x","attemptErrors":[]}`),
	}
	fleet := botWorkerEventToFleetProto(event, "")
	require.Equal(t, "cancelled", fleet[0].GetActionEvent().Status)
	require.Equal(t, "ACTION_CANCELLED", fleet[0].GetActionEvent().ErrorCode)
}

func TestCommandScheduleResultMapping_SessionFilterApplies(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:   "command-schedule-result",
		BotID: "00000000-0000-0000-0000-000000000001",
		Data:  []byte(`{"runUuid":"filtered","botUuid":"00000000-0000-0000-0000-000000000001","generation":1,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"a","occurrence":0,"attempt":1,"durationMs":0,"observedAtUnixMs":100,"status":"sent","plannedAtUnixMs":95,"sentAtUnixMs":100,"attemptErrors":[]}`),
	}
	require.Nil(t, botWorkerEventToFleetProto(event, "other-session"))
}

func makeApplyItem() *workerpb.ApplyBotCommandScheduleItem {
	return &workerpb.ApplyBotCommandScheduleItem{
		RunId:                 1,
		RunUuid:               "11111111-2222-3333-4444-555555555555",
		BotUuid:               "00000000-0000-0000-0000-000000000001",
		Generation:            1,
		StepId:                "command-schedule",
		ScheduleRunId:         "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		CorrelationToken:      "11111111-2222-3333-4444-666666666666",
		StartMode:             "absolute",
		ScheduleStartAtUnixMs: 1_000,
		RunDeadlineUnixMs:     10_000,
		JitterSeed:            "20260720",
		Plan: &workerpb.AppliedCommandOccurrencePlan{
			DurationMs: 5_000,
			JitterMs:   20,
			Occurrences: []*workerpb.AppliedCommandOccurrence{{
				CommandId: "a", Occurrence: 0, CommandDeclarationIndex: 0, BaseAtMs: 0, JitterOffsetMs: 0,
				ActionRunId: "cccccccc-cccc-cccc-cccc-cccccccccccc",
				Command:     "/say ready",
			}},
		},
	}
}

func makeReleaseItem() *workerpb.ReleaseBotCommandScheduleItem {
	return &workerpb.ReleaseBotCommandScheduleItem{
		RunUuid:         "11111111-2222-3333-4444-555555555555",
		BotUuid:         "00000000-0000-0000-0000-000000000001",
		Generation:      1,
		StepId:          "command-schedule",
		ScheduleRunId:   "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		BarrierKey:      "main",
		ReleaseAtUnixMs: 1_500,
	}
}

func makeCancelItem() *workerpb.CancelBotCommandScheduleItem {
	return &workerpb.CancelBotCommandScheduleItem{
		RunUuid:          "11111111-2222-3333-4444-555555555555",
		BotUuid:          "00000000-0000-0000-0000-000000000001",
		Generation:       1,
		StepId:           "command-schedule",
		ScheduleRunId:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		CorrelationToken: "11111111-2222-3333-4444-666666666666",
		Reason:           "manual",
		UnresolvedOccurrences: []*workerpb.CommandOccurrenceRef{
			{CommandId: "a", Occurrence: 0, ActionRunId: "cccccccc-cccc-cccc-cccc-cccccccccccc", PlannedAtUnixMs: 1_000},
		},
	}
}

// 防 unused import 警告。
var _ = strings.TrimSpace
