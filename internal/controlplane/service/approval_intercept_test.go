package service

/* *
 *审批拦截的守门用例。
 *
 * 核心断言是「**被拦截时必须零副作用**」：只落票据、不发执行。
 * 只看返回了 requestId 不足以证明这一点——实现可能「先执行再出票」，
 * 那样 Agent 拿到的仍是一句「需要审批」，而实际动作已经发生。
 * 因此这里断言的是：票据已落库 + 执行器没有被调用。
 */

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newInterceptFixture(t *testing.T) (*ApprovalService, *ApprovalExecutor, func() int) {
	t.Helper()
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	ex := NewApprovalExecutor(db, svc, nil)

	// 给高危 action 装一个会「真的执行」的 adapter，用来观测是否被调用。
	calls := 0
	for _, action := range ApprovalRequiredActionNames() {
		ex.RegisterAdapter(action, func(context.Context, *model.ApprovalTicket, map[string]any) error {
			calls++
			return nil
		})
	}
	return svc, ex, func() int { return calls }
}

// TestIntercept_HighRiskWritesTicketAndDoesNotExecute 高危操作只落票、不执行。
func TestIntercept_HighRiskWritesTicketAndDoesNotExecute(t *testing.T) {
	svc, ex, callCount := newInterceptFixture(t)

	res := svc.InterceptForApproval(ApprovalInterceptParams{
		ToolName: "instance_stop", Action: "agent.instance_stop",
		Operation:  AgentOperationWrite,
		TargetType: "instance", TargetID: "7",
		Payload:         map[string]any{"id": float64(7)},
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent, TokenID: "tok-1",
	})
	require.True(t, res.Blocked, "高危操作必须被拦截")
	require.NoError(t, res.Err)
	require.NotEmpty(t, res.RequestID, "必须给 Agent 一个可轮询的 requestId")
	require.Contains(t, res.Message, "未执行")

	// 关键：执行器一次都没被调用（零副作用）。
	require.Equal(t, 0, callCount(), "拦截后绝不能执行")

	// 票据确实落库且是 pending。
	var tk model.ApprovalTicket
	require.NoError(t, svc.db.First(&tk, res.TicketID).Error)
	require.Equal(t, model.ApprovalStatusPending, tk.Status)
	require.Equal(t, "instance_stop", tk.Action)
	require.NotEmpty(t, tk.Reason, "理由必须有（哪怕是系统生成的）")

	_ = ex
}

// TestIntercept_ExemptActionPassesThrough 免审操作直接放行，不出票。
func TestIntercept_ExemptActionPassesThrough(t *testing.T) {
	svc, _, _ := newInterceptFixture(t)

	for _, action := range []string{"instance_start", "instance_restart"} {
		res := svc.InterceptForApproval(ApprovalInterceptParams{
			ToolName: action, Operation: AgentOperationWrite,
			TargetType: "instance", TargetID: "7",
			Payload:         map[string]any{"id": float64(7)},
			ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent,
		})
		require.False(t, res.Blocked, "%s 应免审放行", action)
		require.Zero(t, res.TicketID)
	}

	// 反向校验：确实没有产生票据（否则「放行」只是看起来放行）。
	var count int64
	require.NoError(t, svc.db.Model(&model.ApprovalTicket{}).Count(&count).Error)
	require.Equal(t, int64(0), count, "免审操作不该产生审批票据")
}

// TestIntercept_ReadActionPassesThrough 读操作直接放行。
func TestIntercept_ReadActionPassesThrough(t *testing.T) {
	svc, _, _ := newInterceptFixture(t)
	res := svc.InterceptForApproval(ApprovalInterceptParams{
		ToolName: "agent_list_instances", Operation: AgentOperationRead,
	})
	require.False(t, res.Blocked)
}

// TestIntercept_UnlistedWriteKeepsExistingSemantics 名单外的写操作沿用既有确认语义。
//
// 这是刻意的取舍（登记表文件头有记录）：若所有写操作都要审批，
// 批准人只会闭眼点同意，审批退化成橡皮图章。
// 「漏登记」由 mcp 包的 `TestRegistryCoversAllDangerousTools` 在测试层兜住——
// 那条用例会把「名字含 delete/rollback/write 等词根的写工具」与审批名单全量比对。
func TestIntercept_UnlistedWriteKeepsExistingSemantics(t *testing.T) {
	svc, _, _ := newInterceptFixture(t)
	res := svc.InterceptForApproval(ApprovalInterceptParams{
		ToolName: "brand_new_unlisted_tool", Operation: AgentOperationWrite,
		TargetType: "instance", TargetID: "7",
		Payload:         map[string]any{},
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent,
	})
	require.False(t, res.Blocked, "名单外不拦截，交给既有确认语义")

	var count int64
	require.NoError(t, svc.db.Model(&model.ApprovalTicket{}).Count(&count).Error)
	require.Equal(t, int64(0), count, "名单外不该产生票据")
}

// TestIntercept_IdempotentAcrossRetries Agent 重试同一调用返回同一张票。
//
// 这条对 Agent 语义特别重要：Agent 常常「没收到响应就重试」，
// 若每次都出新票，批准人会看到一堆重复申请，且批多张就会执行多次。
func TestIntercept_IdempotentAcrossRetries(t *testing.T) {
	svc, _, _ := newInterceptFixture(t)
	p := ApprovalInterceptParams{
		ToolName: "instance_stop", Operation: AgentOperationWrite,
		TargetType: "instance", TargetID: "7",
		Payload:         map[string]any{"id": float64(7)},
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent, TokenID: "tok-1",
	}
	first := svc.InterceptForApproval(p)
	second := svc.InterceptForApproval(p)

	require.True(t, first.Blocked)
	require.True(t, second.Blocked)
	require.Equal(t, first.RequestID, second.RequestID, "重试必须复用同一张票")
	require.Equal(t, first.TicketID, second.TicketID)

	var count int64
	require.NoError(t, svc.db.Model(&model.ApprovalTicket{}).Count(&count).Error)
	require.Equal(t, int64(1), count, "重试不得产生第二张票")
}

// TestIntercept_DestructiveTreatedAsWrite destructive 同样需审。
func TestIntercept_DestructiveTreatedAsWrite(t *testing.T) {
	svc, _, _ := newInterceptFixture(t)
	res := svc.InterceptForApproval(ApprovalInterceptParams{
		ToolName: "instance_snapshot_rollback", Operation: AgentOperationDestructive,
		TargetType: "instance", TargetID: "7",
		Payload:         map[string]any{"id": float64(7)},
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent,
	})
	require.True(t, res.Blocked, "destructive 必须与 write 同等对待")
}

// TestActionOperationOf_UnknownDefaultsToWrite 未知工具按 write 保守处理。
//
// 两种误判的代价不对称：把读误判成写 = 多要一次审批（麻烦）；
// 把写误判成读 = 直接放行（事故）。因此默认取保守侧。
func TestActionOperationOf_UnknownDefaultsToWrite(t *testing.T) {
	// 未注入解析器时一律无法判定 → 必须按 write。
	SetApprovalToolActionResolver(nil)
	require.Equal(t, AgentOperationWrite, ActionOperationOf("anything_unknown"),
		"无法判定时必须按 write 保守处理")
}
