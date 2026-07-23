package grpc

import (
	"context"
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
		Evt:  "command-schedule-result",
		BotID: "00000000-0000-0000-0000-000000000001",
		Data: []byte(`{"runUuid":"00000000-0000-0000-0000-000000000099","botUuid":"00000000-0000-0000-0000-000000000001","generation":1,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"a","occurrence":0,"attempt":1,"durationMs":5,"observedAtUnixMs":100,"status":"sent","plannedAtUnixMs":95,"sentAtUnixMs":100,"attemptErrors":[]}`),
	}
	fleet := botWorkerEventToFleetProto(event, "")
	require.Len(t, fleet, 1)
	action := fleet[0].GetActionEvent()
	require.Equal(t, "succeeded", action.Status)
	require.Empty(t, action.ErrorCode)
	require.Equal(t, "00000000-0000-0000-0000-0000000000bb", action.ActionRunId)
	require.Contains(t, action.ResultJson, `"status":"sent"`)
}

func TestCommandScheduleResultMapping_TimedOutMappedToDeadlineExceeded(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:  "command-schedule-result",
		BotID: "00000000-0000-0000-0000-000000000001",
		Data: []byte(`{"runUuid":"00000000-0000-0000-0000-000000000099","botUuid":"00000000-0000-0000-0000-000000000001","generation":1,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"a","occurrence":0,"attempt":1,"durationMs":0,"observedAtUnixMs":100,"status":"timed_out","plannedAtUnixMs":95,"sentAtUnixMs":null,"errorCode":"COMMAND_DEADLINE_EXCEEDED","message":"deadline","attemptErrors":[]}`),
	}
	fleet := botWorkerEventToFleetProto(event, "")
	require.Equal(t, "timed_out", fleet[0].GetActionEvent().Status)
	require.Equal(t, "COMMAND_DEADLINE_EXCEEDED", fleet[0].GetActionEvent().ErrorCode)
}

func TestCommandScheduleResultMapping_CancelledToCancelled(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:  "command-schedule-result",
		BotID: "00000000-0000-0000-0000-000000000001",
		Data: []byte(`{"runUuid":"00000000-0000-0000-0000-000000000099","botUuid":"00000000-0000-0000-0000-000000000001","generation":1,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"a","occurrence":0,"attempt":1,"durationMs":0,"observedAtUnixMs":100,"status":"cancelled","plannedAtUnixMs":95,"sentAtUnixMs":null,"errorCode":"ACTION_CANCELLED","message":"x","attemptErrors":[]}`),
	}
	fleet := botWorkerEventToFleetProto(event, "")
	require.Equal(t, "cancelled", fleet[0].GetActionEvent().Status)
	require.Equal(t, "ACTION_CANCELLED", fleet[0].GetActionEvent().ErrorCode)
}

func TestCommandScheduleResultMapping_SessionFilterApplies(t *testing.T) {
	event := &bot.BotWorkerEvent{
		Evt:  "command-schedule-result",
		BotID: "00000000-0000-0000-0000-000000000001",
		Data: []byte(`{"runUuid":"filtered","botUuid":"00000000-0000-0000-0000-000000000001","generation":1,"stepId":"command-schedule","scheduleRunId":"00000000-0000-0000-0000-0000000000aa","actionRunId":"00000000-0000-0000-0000-0000000000bb","correlationToken":"00000000-0000-0000-0000-0000000000cc","commandId":"a","occurrence":0,"attempt":1,"durationMs":0,"observedAtUnixMs":100,"status":"sent","plannedAtUnixMs":95,"sentAtUnixMs":100,"attemptErrors":[]}`),
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
		RunUuid:          "11111111-2222-3333-4444-555555555555",
		BotUuid:          "00000000-0000-0000-0000-000000000001",
		Generation:       1,
		StepId:           "command-schedule",
		ScheduleRunId:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		BarrierKey:       "main",
		ReleaseAtUnixMs:  1_500,
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