package service

/* *
 *§9「审计中能按 conversationId / runId / callId 串起一次完整调用」的验收用例。
 *
 * 这组用例守的是**可查性**，而不只是「有没有写审计行」：
 * 三者必须是独立列 + 可过滤，否则「串起来」只能靠 LIKE 全表扫描，
 * 而审计表正是最容易变大的那张表。
 */

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newAuditTestDB 建审计表。
func newAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newAssistantChatTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.AuditLog{}, &model.User{}))
	return db
}

// TestAuditToolExecution_CorrelationIDsPersisted 写操作审计必须带三个串联 ID。
func TestAuditToolExecution_CorrelationIDsPersisted(t *testing.T) {
	db := newAuditTestDB(t)
	svc := NewAssistantChatService(db, nil)

	ticket := &model.AssistantConfirmation{
		ID:                11,
		OwnerUserID:       42,
		ConversationID:    7,
		RunID:             99,
		CallID:            "call-xyz",
		ToolName:          "system.agent.instance_start",
		Policy:            model.AssistantPolicySelfConfirm,
		ArgsHash:          "hash123",
		TargetFingerprint: "instance:5",
	}
	svc.auditToolExecution(ticket, true, "")

	var logs []model.AuditLog
	require.NoError(t, db.Find(&logs).Error)
	require.Len(t, logs, 1, "应写入一条审计")

	got := logs[0]
	require.Equal(t, uint(42), got.UserID)
	require.Equal(t, "assistant.tool.system.agent.instance_start", got.Action,
		"action 需带 assistant.tool. 前缀，才能与用户直接操作区分开")
	require.Equal(t, "instance", got.TargetType)
	require.Equal(t, "5", got.TargetID)
	require.False(t, got.Failed)

	// 三列必须真的落库（而不是只塞进 Detail）。
	require.NotNil(t, got.ConversationID)
	require.Equal(t, uint(7), *got.ConversationID)
	require.NotNil(t, got.RunID)
	require.Equal(t, uint(99), *got.RunID)
	require.Equal(t, "call-xyz", got.CallID)

	// 参数原文不得进审计详情：审计可见面更广，只留摘要。
	require.NotContains(t, got.Detail, "argsFrozen")
	require.Contains(t, got.Detail, "hash123", "详情应含参数摘要以便比对")
}

// TestAuditToolExecution_FailureAlsoRecorded 失败的写操作也要留审计。
//
// 只记成功的写，会让「模型反复尝试但一直被拒」这类行为在审计里完全隐形——
// 而这恰恰是安全审查最想看到的东西。
func TestAuditToolExecution_FailureAlsoRecorded(t *testing.T) {
	db := newAuditTestDB(t)
	svc := NewAssistantChatService(db, nil)

	svc.auditToolExecution(&model.AssistantConfirmation{
		ID: 12, OwnerUserID: 42, ConversationID: 7, RunID: 100, CallID: "call-fail",
		ToolName: "system.agent.node_maintenance_enter", Policy: model.AssistantPolicySelfConfirm,
		ArgsHash: "h", TargetFingerprint: "node:3",
	}, false, "节点未连接")

	var got model.AuditLog
	require.NoError(t, db.First(&got).Error)
	require.True(t, got.Failed, "失败操作必须标 failed")
	require.Contains(t, got.Error, "节点未连接")
	require.Equal(t, "node", got.TargetType)
	require.NotNil(t, got.RunID)
	require.Equal(t, uint(100), *got.RunID)
}

// TestAuditFilter_QueryByRunID 审计必须能按 runId 精确查出这一轮的全部助手写入。
//
// 这是「串起一次完整调用」的实际用法：拿一个 runId，查出它改了什么。
func TestAuditFilter_QueryByRunID(t *testing.T) {
	db := newAuditTestDB(t)
	svc := NewAssistantChatService(db, nil)
	for _, callID := range []string{"c1", "c2", "c3"} {
		svc.auditToolExecution(&model.AssistantConfirmation{
			ID: 1, OwnerUserID: 1, ConversationID: 7, RunID: 555, CallID: callID,
			ToolName: "system.agent.instance_start", Policy: model.AssistantPolicySelfConfirm,
			ArgsHash: "h", TargetFingerprint: "instance:1",
		}, true, "")
	}
	// 另一个 run 的行，用于验证过滤不会误取。
	svc.auditToolExecution(&model.AssistantConfirmation{
		ID: 2, OwnerUserID: 1, ConversationID: 7, RunID: 556, CallID: "other",
		ToolName: "system.agent.instance_stop", Policy: model.AssistantPolicySelfConfirm,
		ArgsHash: "h", TargetFingerprint: "instance:1",
	}, true, "")

	auditSvc := NewAuditService(db)
	runID := uint(555)
	rows, err := auditSvc.List(AuditFilter{RunID: &runID})
	require.NoError(t, err)
	require.Len(t, rows, 3, "按 runId 应恰好查出该轮 3 条，不多不少")

	// 按 callId 单条定位。
	callID := "c2"
	one, err := auditSvc.List(AuditFilter{CallID: &callID})
	require.NoError(t, err)
	require.Len(t, one, 1)
	require.Equal(t, "c2", one[0].CallID)

	// 按 conversationId 查出该会话跨 run 的全部记录。
	convID := uint(7)
	all, err := auditSvc.List(AuditFilter{ConversationID: &convID})
	require.NoError(t, err)
	require.Len(t, all, 4, "该会话共 4 条（3 + 1）")
}

// TestAuditLog_NonAssistantRowsUnaffected 非助手产生的审计行不受影响。
//
// 新加三列可空，既有写入路径（RecordResultSafe）不该被迫提供它们——
// 否则要么改所有调用点，要么塞假值污染审计。
func TestAuditLog_NonAssistantRowsUnaffected(t *testing.T) {
	db := newAuditTestDB(t)
	auditSvc := NewAuditService(db)

	auditSvc.RecordResultSafe(1, "instance.start", "instance", "9", `{"x":1}`, "10.0.0.9", true, "")

	var got model.AuditLog
	require.NoError(t, db.First(&got).Error)
	require.Nil(t, got.ConversationID, "非助手行不该有会话 ID")
	require.Nil(t, got.RunID, "非助手行不该有 run ID")
	require.Equal(t, "", got.CallID)
	require.Equal(t, "10.0.0.9", got.IP, "既有 IP 字段语义不变")

	// 按 runId 过滤时不该把这些行带出来。
	runID := uint(1)
	rows, err := auditSvc.List(AuditFilter{RunID: &runID})
	require.NoError(t, err)
	require.Empty(t, rows, "没有 runId 的行不应被 runId 过滤命中")
}

// TestAuditToolExecution_DetailHasNoRawArgs 审计详情不得含参数原文。
//
// 审计页面对所有有审计读权限的人可见，而工具参数可能含路径、命令等敏感值。
// 原文保留在票据 args_frozen 里，按 confirmationId 取。
func TestAuditToolExecution_DetailHasNoRawArgs(t *testing.T) {
	db := newAuditTestDB(t)
	svc := NewAssistantChatService(db, nil)
	svc.auditToolExecution(&model.AssistantConfirmation{
		ID: 3, OwnerUserID: 1, ConversationID: 1, RunID: 1, CallID: "c",
		ToolName: "system.agent.instance_start", Policy: model.AssistantPolicySelfConfirm,
		ArgsHash:          "abc",
		ArgsFrozen:        `{"startCommand":"rm -rf /sensitive/path"}`,
		TargetFingerprint: "instance:1",
	}, true, "")

	var got model.AuditLog
	require.NoError(t, db.First(&got).Error)
	require.False(t, strings.Contains(got.Detail, "sensitive"), "参数原文泄漏进审计详情")
	require.Contains(t, got.Detail, "abc")
}
