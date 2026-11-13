package service

/*
 * 确认决定之后 run 必须离开 waiting_confirmation（P0 回归）。
 *
 * 【为什么这组用例是必需的】
 * 修复前：用户点过批准/拒绝之后，run 永久停在 waiting_confirmation。
 * 而这个状态在 `AssistantRunActiveStatuses()` 里，占着 `ux_assistant_runs_active`
 * 部分唯一索引——后果是同一会话再发消息永远 409 CONVERSATION_BUSY，
 * 只能重启 CP 才能恢复。当时**零测试覆盖**，所以门禁全绿也发现不了。
 *
 * 这里不启动真实 executor（那要模型上游），而是直接断言「状态推进」与
 * 「工具消息写入」这两件确定性事实：它们才是解开会话锁的关键。
 */

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// seedWaitingRun 造一个停在确认态的会话 + run。
func seedWaitingRun(t *testing.T, svc *AssistantChatService) (*model.AssistantConversation, *model.AssistantRun) {
	t.Helper()
	conv := &model.AssistantConversation{OwnerUserID: 1, Title: "t"}
	require.NoError(t, svc.db.Create(conv).Error)
	run := &model.AssistantRun{
		OwnerUserID:    1,
		ConversationID: conv.ID,
		Status:         model.AssistantRunStatusWaitingConfirmation,
		RequestDigest:  strPtr("d"),
	}
	require.NoError(t, svc.db.Create(run).Error)
	require.NoError(t, svc.db.Model(conv).Update("active_run_id", run.ID).Error)
	return conv, run
}

// TestSweepStaleConfirmations_UnlocksConversation 过期票据被收口后，会话不再被占。
//
// 这是「用户关页面不处理确认」这条最常见的真实路径。
func TestSweepStaleConfirmations_UnlocksConversation(t *testing.T) {
	db := newConfirmTestDB(t)
	svc := NewAssistantChatService(db, nil)

	conv, run := seedWaitingRun(t, svc)
	tk := seedPending(t, db, run.ID, "call-1")
	// 把它改成已过期。
	require.NoError(t, db.Model(tk).Update("expires_at", time.Now().Add(-time.Minute)).Error)

	expired, _, err := svc.SweepStaleConfirmations(time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, expired, "应有一张票据被收口为过期")

	var got model.AssistantConfirmation
	require.NoError(t, db.First(&got, tk.ID).Error)
	require.Equal(t, model.AssistantConfirmStatusExpired, got.Status)

	// 关键：run 不再处于活跃态，会话能被重新使用。
	active, err := svc.findActiveRun(conv.ID)
	require.NoError(t, err)
	require.Nil(t, active, "过期收口后不得再有活跃 run 占着会话槽")
}

// TestSweepStaleConfirmations_Idempotent 重复巡检不重复写工具消息。
func TestSweepStaleConfirmations_Idempotent(t *testing.T) {
	db := newConfirmTestDB(t)
	svc := NewAssistantChatService(db, nil)

	_, run := seedWaitingRun(t, svc)
	tk := seedPending(t, db, run.ID, "call-1")
	require.NoError(t, db.Model(tk).Update("expires_at", time.Now().Add(-time.Minute)).Error)

	first, _, err := svc.SweepStaleConfirmations(time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, first)

	second, _, err := svc.SweepStaleConfirmations(time.Now())
	require.NoError(t, err)
	require.Equal(t, 0, second, "已终态的票据不应被再次选中")

	var toolMsgs int64
	require.NoError(t, db.Model(&model.AssistantMessage{}).
		Where("run_id = ? AND tool_call_id = ?", run.ID, "call-1").Count(&toolMsgs).Error)
	require.Equal(t, int64(1), toolMsgs, "同一张票据只能补一条 tool 消息")
}

// TestSweepStaleConfirmations_SkipsFreshTickets 未过期的票据不应被收口。
func TestSweepStaleConfirmations_SkipsFreshTickets(t *testing.T) {
	db := newConfirmTestDB(t)
	svc := NewAssistantChatService(db, nil)

	_, run := seedWaitingRun(t, svc)
	tk := seedPending(t, db, run.ID, "call-1") // 默认 expires_at = now+1m

	expired, _, err := svc.SweepStaleConfirmations(time.Now())
	require.NoError(t, err)
	require.Equal(t, 0, expired)

	var got model.AssistantConfirmation
	require.NoError(t, db.First(&got, tk.ID).Error)
	require.Equal(t, model.AssistantConfirmStatusPending, got.Status, "未过期不得被收口")

	// run 也不应被动过（它还在等人做决定）。
	var r model.AssistantRun
	require.NoError(t, db.First(&r, run.ID).Error)
	require.Equal(t, model.AssistantRunStatusWaitingConfirmation, r.Status)
}

// TestRecordConfirmationToolMessage_BuildHistoryKeepsRound 补的 tool 消息能让整轮不被丢弃。
//
// 这条直接盯着 `buildHistory` 的配对规则：若写入的 ToolCallID / RunID 不对，
// 它会把这轮当「不成对」丢掉——模型下一轮就看不到这次调用的结果。
func TestRecordConfirmationToolMessage_BuildHistoryKeepsRound(t *testing.T) {
	db := newConfirmTestDB(t)
	svc := NewAssistantChatService(db, nil)
	conv, run := seedWaitingRun(t, svc)

	// 一轮完整的形状：user → assistant(带 tool_call) → tool(结果)。
	require.NoError(t, db.Create(&model.AssistantMessage{
		ConversationID: conv.ID, Role: model.AssistantMessageRoleUser, Content: "停实例 1", Turn: 1,
		State: model.AssistantMessageStateComplete,
	}).Error)
	runID := run.ID
	asMsg := &model.AssistantMessage{
		ConversationID: conv.ID, RunID: &runID, Role: model.AssistantMessageRoleAssistant,
		Content: "", Turn: 1, State: model.AssistantMessageStateComplete,
	}
	require.NoError(t, db.Create(asMsg).Error)
	callID := "call-1"
	sink := newAssistantEventSink(db, run, svc.limits)
	require.NoError(t, sink.append(model.AssistantEventTypeToolCall, map[string]any{
		"callId": callID, "name": "instance_stop", "argsJson": `{"id":1}`,
	}, &asMsg.ID, &callID))

	// 决策后补上 tool 消息。
	svc.recordConfirmationToolMessage(&model.AssistantConfirmation{
		RunID: run.ID, ConversationID: conv.ID, CallID: callID, ID: 1,
	}, "用户已确认，操作已执行成功。")

	history, err := svc.buildHistory(t.Context(), conv.ID, sink, svc.limits)
	require.NoError(t, err)

	// 期望 user + assistant(tool_calls) + tool 三条都在。
	require.Len(t, history, 3, "补了 tool 消息后整轮不应被丢弃: %+v", history)
	require.Equal(t, "tool", history[2].Role)
	require.Equal(t, callID, history[2].ToolCallID)
}

// TestRecordConfirmationToolMessage_NoCallIDSkips 没有 callId 的票据不写消息（不凭空造一条）。
func TestRecordConfirmationToolMessage_NoCallIDSkips(t *testing.T) {
	db := newConfirmTestDB(t)
	svc := NewAssistantChatService(db, nil)
	conv, run := seedWaitingRun(t, svc)

	svc.recordConfirmationToolMessage(&model.AssistantConfirmation{
		RunID: run.ID, ConversationID: conv.ID, CallID: "", ID: 1,
	}, "x")

	var n int64
	require.NoError(t, db.Model(&model.AssistantMessage{}).Where("conversation_id = ?", conv.ID).Count(&n).Error)
	require.Equal(t, int64(0), n)
}
