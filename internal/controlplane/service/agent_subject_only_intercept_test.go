package service

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 出票阶段的 Agent 专属拦截（主闸）。
//
// 修复前：人类提交会在**执行期**才失败（loadTicketPrincipal），
// 而此时票已经出了、也已经被批准了——批准人以为自己批了一件会生效的事。
// 把判定提前到出票前，人类提交立刻得到明确拒绝。

// interceptParams 构造一次针对传输票据的拦截请求。
func interceptParams(tokenID string, applicantKind string) ApprovalInterceptParams {
	return ApprovalInterceptParams{
		ToolName:         "file_issue_transfer_ticket",
		Action:           AgentActionFileIssueTransferTicket,
		Operation:        AgentOperationWrite,
		TargetType:       "instance",
		TargetID:         "7",
		Payload:          map[string]any{"id": 7, "direction": "upload", "path": "plugins/folia.jar"},
		Evidence:         map[string]any{"status": model.InstanceStatusStopped},
		ApplicantUserID:  3,
		ApplicantKind:    applicantKind,
		TokenID:          tokenID,
		RequireApplicant: true,
	}
}

// TestIntercept_HumanCannotApplyAgentOnlyAction 人类主体在出票阶段被拒，且**不落票**。
func TestIntercept_HumanCannotApplyAgentOnlyAction(t *testing.T) {
	db := setupApprovalResultDB(t)
	svc := NewApprovalService(db)

	res := svc.InterceptForApproval(interceptParams("", model.ApprovalApplicantHuman))
	require.True(t, res.Blocked, "人类申请 Agent 专属动作必须被拦截")
	require.Error(t, res.Err, "必须返回错误而不是出票")
	require.Contains(t, res.Message, "Agent 专用", "错误信息要说清原因")

	// 关键：一张票都不该留下。留票意味着「拒绝了但仍然产生了一张待办」，
	// 批准人会在审批中心看到一件注定失败的事。
	var count int64
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Count(&count).Error)
	require.Zero(t, count, "拒绝时不得落票")
}

// TestIntercept_AgentCanApplyAgentOnlyAction Agent 主体正常出票（防止误伤）。
func TestIntercept_AgentCanApplyAgentOnlyAction(t *testing.T) {
	db := setupApprovalResultDB(t)
	svc := NewApprovalService(db)

	res := svc.InterceptForApproval(interceptParams("9", model.ApprovalApplicantAgent))
	require.True(t, res.Blocked, "该动作仍需审批，所以仍然返回 blocked")
	require.NoError(t, res.Err, "但不应报错——Agent 是合法主体")
	require.NotEmpty(t, res.RequestID, "应正常出票并返回 requestId")

	var count int64
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Count(&count).Error)
	require.EqualValues(t, 1, count, "Agent 申请应落一张票")
}

// TestIntercept_NonAgentOnlyActionStillAllowsHuman 对照组：非 Agent 专属动作不受影响。
//
// 这条用例防止这次改动把「人类不能申请」误扩成「人类什么都不能申请」。
func TestIntercept_NonAgentOnlyActionStillAllowsHuman(t *testing.T) {
	db := setupApprovalResultDB(t)
	svc := NewApprovalService(db)

	// instance_stop 在审批表内，但**不是** Agent 专属。
	res := svc.InterceptForApproval(ApprovalInterceptParams{
		ToolName:         "instance_stop",
		Action:           AgentActionInstanceStop,
		Operation:        AgentOperationWrite,
		TargetType:       "instance",
		TargetID:         "7",
		Payload:          map[string]any{"id": 7},
		Evidence:         map[string]any{"status": model.InstanceStatusRunning},
		ApplicantUserID:  3,
		ApplicantKind:    model.ApprovalApplicantHuman,
		RequireApplicant: true,
	})
	require.True(t, res.Blocked)
	require.NoError(t, res.Err, "instance_stop 对人类仍应可申请")
	require.NotEmpty(t, res.RequestID)
}

// TestExecute_HumanTicketStillFailsClosed 执行期兜底：历史遗留或绕过拦截层的人类票仍被拒。
func TestExecute_HumanTicketStillFailsClosed(t *testing.T) {
	db := setupApprovalResultDB(t)
	agentSvc, _, inst := seedTransferPrincipal(t, db)
	tickets, terr := NewAgentTransferTicketService(
		DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, terr)
	svc := NewApprovalService(db)
	ex := NewApprovalExecutor(db, svc, nil)
	RegisterApprovalAdapters(ex, ApprovalAdapterDeps{Transfer: tickets, Agent: agentSvc})

	// 直接构造一张无 tokenId 的票，模拟「本字段上线前的历史票」。
	ticket, _, err := svc.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID:  1,
		ApplicantKind:    model.ApprovalApplicantHuman,
		Action:           "file_issue_transfer_ticket",
		TargetType:       "instance",
		TargetID:         strconv.FormatUint(uint64(inst.ID), 10),
		Payload:          map[string]any{"id": inst.ID, "direction": "upload", "path": "x.jar"},
		Evidence:         map[string]any{"status": model.InstanceStatusStopped},
		Reason:           "遗留票",
		RequireApplicant: true,
	})
	require.NoError(t, err)
	_, derr := svc.Decide(t.Context(), DecideApprovalParams{
		TicketID: ticket.ID, ActorUserID: 1, Approve: true,
	})
	require.NoError(t, derr)
	// 未装配 TaskService 时 Execute 同步执行并返回适配器错误——
	// 这里正是要看到那句「该动作仅限 Agent Token 主体」。
	execErr := ex.Execute(t.Context(), ticket.ID)
	require.Error(t, execErr, "无 tokenId 的票执行必须失败关闭")
	require.Contains(t, execErr.Error(), "仅限 Agent Token 主体",
		"错误信息应指向真正的原因，而不是笼统的失败")

	var got model.ApprovalTicket
	require.NoError(t, db.First(&got, ticket.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, got.Status, "无 tokenId 的票执行必须失败关闭")
	require.Empty(t, got.ResultJSON, "失败时不得留下结果")
}
