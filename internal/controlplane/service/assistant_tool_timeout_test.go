package service

// 写调用超时的回执语义（FR-516 §6.2 冻结项）。
//
// 「超时」在写操作里是一件**语义特殊**的事：请求已经发出去了，上游可能已经执行，
// 只是我们没等到回复。因此正确处置既不是「当成功」（可能虚报），
// 也不是「当失败重试」（可能把一次写入做成两次）。
// 规范要求的中间态是 `unverified`：结果待核实，且**绝不自动重试**。
//
// 本用例断言三件事：
//  1. 回执 outcome 落成 `unverified`、error_code 落成 `TOOL_TIMEOUT`；
//  2. 执行器**只被调用一次**（自动重试会让计数变成 2+）；
//  3. 返回的错误是「已执行但失败」语义（AssistantToolExecError），
//     而不是「票据无效」——因为票据确实被消费了。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestApproveAndRun_TimeoutMarksUnverifiedAndDoesNotRetry 超时必须标 unverified 且不重试。
func TestApproveAndRun_TimeoutMarksUnverifiedAndDoesNotRetry(t *testing.T) {
	db := newAssistantChatTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.AssistantConfirmation{}, &model.AssistantReceipt{},
		&model.User{}, &model.UserRoleBinding{}, &model.RoleTemplate{}, &model.UserPermissionOverride{},
	))
	require.NoError(t, db.Create(&model.User{
		ID: 1, Username: "timeout-approver", Role: model.RolePlatformAdmin, Status: model.UserStatusActive,
	}).Error)

	// 造一张已批准的票据，工具名用一个真实存在的内置只读工具
	// （这里只需要「能走到执行器」，业务语义不重要）。
	// 工具目录在服务层是由 mcp 包注入的，测试环境里必须显式装一个，
	// 否则 findAssistantTool 查不到任何工具、用例根本走不到执行器。
	injectTestMCPCatalog(t)
	toolName := "agent_get_instance"
	require.False(t, isAssistantExternalTool(toolName),
		"前提不成立：本用例要走内置工具分支")
	require.NotNil(t, findAssistantTool(toolName), "前提不成立：目录里应能查到该工具")

	pending := model.AssistantConfirmation{
		OwnerUserID:       1,
		ConversationID:    1,
		RunID:             1,
		CallID:            "call-timeout",
		ToolName:          toolName,
		SchemaFingerprint: "fp1",
		ArgsFrozen:        `{}`,
		ArgsHash:          hashToolArgs(map[string]any{}),
		// 状态必须是 pending：approveAndRun 内部会做「pending → consumed」的 CAS，
		// 预置成 approved/consumed 会让 CAS 命中 0 行、直接返回「已被执行过」。
		Status:            model.AssistantConfirmStatusPending,
		TargetFingerprint: "instance:1",
		ExpiresAt:         time.Now().Add(10 * time.Minute),
	}
	require.NoError(t, db.Create(&pending).Error)

	// 注入执行器：**立刻**返回 DeadlineExceeded 模拟写调用超时，
	// 并统计调用次数（用于证明「没有自动重试」）。
	calls := 0
	prev := assistantToolExecutor()
	SetAssistantToolExecutor(func(ctx context.Context, owner uint, name string, args map[string]any) (string, error) {
		calls++
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() { SetAssistantToolExecutor(prev) })

	svc := NewAssistantChatService(db, nil)
	_, err := svc.approveAndRun(context.Background(), 1, &pending)
	require.Error(t, err, "超时必须以错误返回，不能假装成功")

	// ① 错误语义：「已执行但失败」，不是「票据无效」。
	var execErr *AssistantToolExecError
	require.True(t, errors.As(err, &execErr),
		"超时应返回 AssistantToolExecError（已执行但失败），实际 %T: %v", err, err)

	// ② 只调用一次：任何自动重试都会把计数推到 2+。
	// 写操作重试可能把一次写入做成两次，这是本节最核心的约束。
	require.Equal(t, 1, calls, "写调用超时后**不得自动重试**（会把一次写入做成两次）")

	// ③ 回执落成 unverified + TOOL_TIMEOUT。
	var rec model.AssistantReceipt
	require.NoError(t, db.Where("confirmation_id = ?", pending.ID).First(&rec).Error)
	require.Equal(t, model.AssistantReceiptUnverified, rec.Outcome,
		"超时必须标 unverified（结果待核实），实际 %q", rec.Outcome)
	require.Equal(t, "TOOL_TIMEOUT", rec.ErrorCode,
		"错误码应能区分「超时」与一般失败，实际 %q", rec.ErrorCode)
}

// TestApproveAndRun_NonTimeoutFailureMarksFailed 非超时失败应标 failed 而非 unverified。
//
// 对照组：两者必须可区分。若所有失败都标 unverified，运维就无法判断
// 「这次到底执行了没有」——而这两者的处置完全不同（前者要人工核实副作用，
// 后者可以直接重试）。
func TestApproveAndRun_NonTimeoutFailureMarksFailed(t *testing.T) {
	db := newAssistantChatTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.AssistantConfirmation{}, &model.AssistantReceipt{},
		&model.User{}, &model.UserRoleBinding{}, &model.RoleTemplate{}, &model.UserPermissionOverride{},
	))
	require.NoError(t, db.Create(&model.User{
		ID: 1, Username: "fail-approver", Role: model.RolePlatformAdmin, Status: model.UserStatusActive,
	}).Error)

	injectTestMCPCatalog(t)
	toolName := "agent_get_instance"
	pending := model.AssistantConfirmation{
		OwnerUserID: 1, ConversationID: 1, RunID: 1, CallID: "call-fail", ToolName: toolName,
		SchemaFingerprint: "fp1", ArgsFrozen: `{}`,
		ArgsHash:          hashToolArgs(map[string]any{}),
		Status:            model.AssistantConfirmStatusPending,
		TargetFingerprint: "instance:1",
		ExpiresAt:         time.Now().Add(10 * time.Minute),
	}
	require.NoError(t, db.Create(&pending).Error)

	prev := assistantToolExecutor()
	SetAssistantToolExecutor(func(ctx context.Context, owner uint, name string, args map[string]any) (string, error) {
		// 明确的业务失败（不是超时）：此时可以确定「没有产生副作用」。
		return "", errors.New("实例不存在")
	})
	t.Cleanup(func() { SetAssistantToolExecutor(prev) })

	svc := NewAssistantChatService(db, nil)
	_, err := svc.approveAndRun(context.Background(), 1, &pending)
	require.Error(t, err)

	var rec model.AssistantReceipt
	require.NoError(t, db.Where("confirmation_id = ?", pending.ID).First(&rec).Error)
	require.Equal(t, model.AssistantReceiptFailed, rec.Outcome,
		"非超时失败应标 failed（可确定无副作用），实际 %q", rec.Outcome)
	require.NotEqual(t, model.AssistantReceiptUnverified, rec.Outcome,
		"不能把所有失败都标 unverified——那会让「是否已执行」变得不可判断")
}
