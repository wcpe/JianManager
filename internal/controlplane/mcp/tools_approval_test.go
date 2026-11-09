package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 审批票据自查工具（spec §8.4）。
//
// 这组用例守住的核心不是「能查」，而是**只能查到自己的**：
// 按 TokenID 严格限域，同一用户的两把 Token 互不可见，
// 无 Token 的助手委托链直接拒绝（而不是退化成“查到所有人的票”）。

func approvalTestDeps(t *testing.T) (ToolDeps, *model.ApprovalTicket) {
	t.Helper()
	db := newStatelessTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ApprovalTicket{}, &model.ApprovalReceipt{}))

	// 一张属于 Token 9 的待审票（用服务层出票，确保字段与真实路径一致）。
	svc := service.NewApprovalService(db)
	ticket, _, err := svc.SubmitApproval(service.SubmitApprovalParams{
		ApplicantUserID:  1,
		ApplicantKind:    model.ApprovalApplicantAgent,
		TokenID:          "9",
		Action:           "mcp.instance_stop",
		TargetType:       "instance",
		TargetID:         "7",
		Payload:          map[string]any{"id": 7},
		Evidence:         map[string]any{"status": "RUNNING"},
		Reason:           "停掉测试实例",
		RequireApplicant: true,
	})
	require.NoError(t, err)
	require.NotNil(t, ticket)
	return ToolDeps{Approval: svc}, ticket
}

func tokenPrincipal(id uint) *service.AgentPrincipal {
	return &service.AgentPrincipal{
		TokenID:       id,
		PolicyVersion: service.AgentPolicyVersionV2,
		Capabilities:  []string{service.AgentCapabilityInstanceRead},
	}
}

func TestApprovalTools_ListScopedToOwnToken(t *testing.T) {
	deps, ticket := approvalTestDeps(t)

	mine := CallTool(t.Context(), deps, tokenPrincipal(9), "approval_list", map[string]any{})
	require.False(t, mine.IsError, firstText(mine))
	require.Contains(t, firstText(mine), "instance_stop", "自己的票应能看到")
	require.Contains(t, firstText(mine), ticket.UUID, "应带 requestId 便于与拦截应答对账")

	// 另一把 Token 看不到它：这是本工具的核心约束，不是可选行为。
	other := CallTool(t.Context(), deps, tokenPrincipal(10), "approval_list", map[string]any{})
	require.False(t, other.IsError, firstText(other))
	require.NotContains(t, firstText(other), ticket.UUID, "别人的 Token 不得看到本票")
}

func TestApprovalTools_GetRejectsForeignTicket(t *testing.T) {
	deps, ticket := approvalTestDeps(t)

	ok := CallTool(t.Context(), deps, tokenPrincipal(9), "approval_get", map[string]any{"id": float64(ticket.ID)})
	require.False(t, ok.IsError, firstText(ok))
	require.Contains(t, firstText(ok), "pending")
	// 详情回冻结参数，供 Agent 核对自己当时提交了什么。
	require.Contains(t, firstText(ok), "payload")

	// 越权与不存在共用同一文案：分开回 404/403 等于给出探测接口。
	bad := CallTool(t.Context(), deps, tokenPrincipal(10), "approval_get", map[string]any{"id": float64(ticket.ID)})
	require.True(t, bad.IsError)
	require.Contains(t, firstText(bad), "不存在")
}

func TestApprovalTools_WithdrawScopedToOwnToken(t *testing.T) {
	deps, ticket := approvalTestDeps(t)

	// 别人的 Token 不能撤回我的票。
	bad := CallTool(t.Context(), deps, tokenPrincipal(10), "approval_withdraw", map[string]any{"id": float64(ticket.ID)})
	require.True(t, bad.IsError, "跨 Token 撤回必须被拒")

	ok := CallTool(t.Context(), deps, tokenPrincipal(9), "approval_withdraw", map[string]any{"id": float64(ticket.ID)})
	require.False(t, ok.IsError, firstText(ok))
	require.Contains(t, firstText(ok), "withdrawn")
}

func TestApprovalTools_RejectPrincipalWithoutToken(t *testing.T) {
	deps, _ := approvalTestDeps(t)
	// 助手委托链没有自己的 Token：它的票 token_id 为空，按空串查会把所有人的
	// 无 Token 票混在一起——所以必须直接拒，而不是“宽松处理”。
	noToken := &service.AgentPrincipal{PolicyVersion: service.AgentPolicyVersionV2, ActAsUserID: 1,
		Capabilities: []string{service.AgentCapabilityInstanceRead}}

	for _, name := range []string{"approval_list", "approval_get", "approval_withdraw"} {
		res := CallTool(t.Context(), deps, noToken, name, map[string]any{"id": float64(1)})
		require.True(t, res.IsError, "%s 应在无 Token 时拒绝", name)
		require.Contains(t, firstText(res), "Agent Token", "%s 的拒因应指向缺少 Token", name)
	}
}

func TestApprovalTools_RegisteredInCatalog(t *testing.T) {
	names := ToolNames()
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, want := range []string{"approval_list", "approval_get", "approval_withdraw"} {
		require.True(t, have[want], "工具 %s 应已注册进 allToolSpecs", want)
	}
}

func TestApprovalTools_ServiceUnavailable(t *testing.T) {
	// Approval 为 nil（未装配）时必须是明确错误，不是零值票或 panic。
	deps := ToolDeps{}
	res := CallTool(t.Context(), deps, tokenPrincipal(9), "approval_list", map[string]any{})
	require.True(t, res.IsError)
	require.Contains(t, firstText(res), "不可用")
}
