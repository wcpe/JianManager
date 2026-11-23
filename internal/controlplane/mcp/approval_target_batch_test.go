package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

/*
 * instance_batch 的审批目标解析 + **逐台 scope**。
 *
 * ## 两个缺陷（真机 2026-11-12 暴露）
 *
 * ① **解析不出目标**：`approvalTargetID` 只认标量 `id`。它的兜底分支找「首个 *id 结尾
 *    的数字参数」，而 `strings.HasSuffix("ids", "id")` 为 false（"ids" 以 "ds" 结尾），
 *    于是数组 `ids` 被整条跳过 ⇒ 恒判「缺少有效的目标 id 参数」⇒
 *    instance_batch 在 MCP 审批路径上 100% 无法提交，只能拆成 N 张单实例票。
 *
 * ② **只校验首个 id 是越权漏洞**（比①严重得多）。审批拦截层对
 *    `service.Authorize(p, action, targetForApproval(deps, action, id))` 只传**代表 id**，
 *    而票据里存的是完整 args（含全部 ids）。执行时 `approval_adapters.go` 逐台调
 *    `Instance.Start/Stop(id)` 并**不再校验 scope**。于是：
 *
 *        Agent 的 scope = {3}，提交 ids=[3, 999]
 *          → 拦截层只校验 3，通过，出票
 *          → 批准人看到「停一个实例」并批准
 *          → 执行器停掉 3 **和 999**
 *
 *    这正好违反 approval_intercept.go 自己的设计注释：
 *    「若先出票，一个越权的 Agent 就能通过提交申请，让某位管理员在批准的瞬间
 *      替它完成了越权——批准人不该被安排成提权的最后一步。」
 *
 * 所以修复必须**两件一起做**：能解析目标（否则能力不可用）+ 逐台校验（否则开了越权口）。
 * 只做①比不改更危险：把一个越权漏洞变成可利用的。
 */

// approvalTargetID 现在能取到批量目标（否则票据根本出不来）。
func TestApprovalTargetID_BatchIdsResolvable(t *testing.T) {
	id, ok := approvalTargetID("instance_batch", map[string]any{
		"op":  "stop",
		"ids": []any{float64(3), float64(7), float64(9)},
	})
	require.True(t, ok, "instance_batch 的 ids 数组应能被解析出授权目标，否则它永远无法提交审批")
	require.Equal(t, uint(3), id, "应取数组首个 id 作为 scope 判定代表")
}

// 同一函数也服务于流水记录的 targetType/targetID（tools.go toolTargetByName）。
func TestToolTargetByName_BatchIdsNotDropped(t *testing.T) {
	targetType, targetID := toolTargetByName("instance_batch", map[string]any{
		"op":  "start",
		"ids": []any{float64(5)},
	})
	require.NotEmpty(t, targetID, "批量操作的票据/流水目标不应为空")
	require.Equal(t, "5", targetID)
	require.NotEmpty(t, targetType)
}

// 回归护栏：原有标量 `id` 与「首个 *Id 数字参数」兼容路径不得被本次修复破坏。
func TestApprovalTargetID_ScalarAndSuffixStillWork(t *testing.T) {
	id, ok := approvalTargetID("instance_stop", map[string]any{"id": float64(42)})
	require.True(t, ok)
	require.Equal(t, uint(42), id, "标量 id 必须仍然优先")

	id, ok = approvalTargetID("instance_snapshot_rollback", map[string]any{"snapshotId": float64(8)})
	require.True(t, ok, "*Id 后缀的兼容分支不应被本次修复破坏")
	require.Equal(t, uint(8), id)
}

// 边界：空数组 / 全为 0 / 非法元素都不算解析成功。
//
// 注意「首个元素为 0」不是错误输入：它只是不是一个**有效目标**，跳过它继续找。
// 这与 toUint 的语义一致（它不拒绝 0，只拒绝负数/非整数）——0 在领域上
// 等价于「未设置」，所以“跳过”比“整体拒绝”更贴合调用方意图。
func TestApprovalTargetID_BatchEmptyOrInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"空数组", map[string]any{"op": "stop", "ids": []any{}}},
		{"全部元素为 0", map[string]any{"op": "stop", "ids": []any{float64(0), float64(0)}}},
		{"元素非法", map[string]any{"op": "stop", "ids": []any{"abc"}}},
		{"非数组", map[string]any{"op": "stop", "ids": "3"}},
		{"nil args", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := approvalTargetID("instance_batch", tc.args)
			require.False(t, ok, "不应解析出有效目标")
		})
	}
}

// 批量目标数组中出现 0 或重复 id 时整体视为非法，不能静默修正后出票。
func TestApprovalTargetID_RejectsZeroAndDuplicateElements(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []any
	}{
		{"零元素", []any{float64(0), float64(3)}},
		{"重复元素", []any{float64(3), float64(3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := approvalTargetID("instance_batch", map[string]any{"op": "stop", "ids": tc.ids})
			require.False(t, ok)
		})
	}
}

// batchPrincipal 构造一个**走 V2 策略**的 principal。
//
// 为何必须显式设 PolicyVersion/Capabilities：`instance_batch` 的 descriptor
// （agent_capability.go）只给了 V2Capability=instance.life，**没有 V1Allowed**。
// 留空 PolicyVersion 会落回 V1 分支，那条路对 batch 恒拒——于是正向用例会
// 拿「V1 不允许」的拒绝当成「越界拦截」，形成**假阴性**（测试看似在守越权，
// 实际只是能力没给对）。
func batchPrincipal(scope ...uint) *service.AgentPrincipal {
	return &service.AgentPrincipal{
		TokenID:           1,
		PolicyVersion:     service.AgentPolicyVersionV2,
		Capabilities:      []string{service.AgentCapabilityInstanceLife},
		ScopedInstanceIDs: scope,
	}
}

// ★ 核心越权用例：ids 里只要有一个越界，就必须**整体拒绝、不出票**。
//
// 这是①修好后唯一能挡住「拿首个 id 蒙混过关」的闸。
func TestApprovalIntercept_BatchRejectsOutOfScopeID(t *testing.T) {
	deps := newApprovalTestDeps(t)
	// scope 只覆盖实例 3；999 不在其中。
	p := batchPrincipal(3)

	res := CallTool(t.Context(), deps, p, "instance_batch", map[string]any{
		"op":  "stop",
		"ids": []any{float64(3), float64(999)},
	})

	require.True(t, res.IsError, "含越界 id 的批量必须被拒绝，而不是出票：%s", firstText(res))
	require.NotContains(t, firstText(res), "awaiting_approval",
		"绝不能为含越界 id 的批量出票——批准人会以为只停 scope 内的实例")
	require.NotContains(t, firstText(res), "ticketId")

	// 并确认真的没落票。
	list := CallTool(t.Context(), deps, p, "approval_list", map[string]any{})
	require.NotContains(t, firstText(list), "instance_batch",
		"越界的批量申请不应产生任何票据")
}

// 正路：ids 全部在 scope 内 → 正常出票（能力确实修好了，不是“一概拒绝”）。
func TestApprovalIntercept_BatchAllInScopeProducesTicket(t *testing.T) {
	deps := newApprovalTestDeps(t)
	p := batchPrincipal(3, 7)

	res := CallTool(t.Context(), deps, p, "instance_batch", map[string]any{
		"op":  "stop",
		"ids": []any{float64(3), float64(7)},
	})
	require.False(t, res.IsError, "全在 scope 内的批量应正常受理：%s", firstText(res))
	require.Contains(t, firstText(res), "awaiting_approval")

	// 票据上要能看出目标，且 payload 里的 ids 未被塔缩成首个。
	list := CallTool(t.Context(), deps, p, "approval_list", map[string]any{})
	require.Contains(t, firstText(list), "instance_batch")
}

// 单元素批量与多元素必须走同一条路径（防止只修了多元素分支）。
func TestApprovalIntercept_BatchSingleIDInScope(t *testing.T) {
	deps := newApprovalTestDeps(t)
	p := batchPrincipal(5)
	res := CallTool(t.Context(), deps, p, "instance_batch", map[string]any{
		"op":  "restart",
		"ids": []any{float64(5)},
	})
	require.False(t, res.IsError, "%s", firstText(res))
	require.Contains(t, firstText(res), "awaiting_approval")
}

// 审批执行器侧的教训（用断言式注释钉住，不只靠代码）：
// approval_adapters.go 的 instance_batch adapter 执行时逐台调 Instance.Start/Stop，
// 并且**不再校验 scope**。所以越界一律拦在拦截层，不能指望执行器兜底——
// 这就是上面 TestApprovalIntercept_BatchRejectsOutOfScopeID 存在的理由。

// 批量工具只认 ids：额外的 id 既不能掩盖越权目标，也不能替代缺失或非法目标。
func TestApprovalIntercept_BatchRejectsInvalidTargetsWithScalarID(t *testing.T) {
	cases := []struct {
		name string
		ids  any
	}{
		{"后续目标越权", []any{float64(3), float64(999)}},
		{"全部目标越权", []any{float64(999)}},
		{"目标缺失", nil},
		{"目标为空", []any{}},
		{"目标不是数组", "3"},
		{"包含非法字符串", []any{float64(3), "invalid"}},
		{"包含负数", []any{float64(3), float64(-1)}},
		{"包含小数", []any{float64(3), float64(7.5)}},
		{"包含零目标", []any{float64(0), float64(3)}},
		{"重复目标", []any{float64(3), float64(3)}},
	}
	for _, op := range []string{"stop", "restart"} {
		for _, tc := range cases {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				db := newStatelessTestDB(t)
				require.NoError(t, db.AutoMigrate(&model.ApprovalTicket{}, &model.ApprovalReceipt{}))
				deps := ToolDeps{Approval: service.NewApprovalService(db)}
				res := CallTool(t.Context(), deps, batchPrincipal(3), "instance_batch", map[string]any{
					"op": op, "id": float64(3), "ids": tc.ids,
				})
				require.True(t, res.IsError, "非法批量目标必须在出票前拒绝：%s", firstText(res))
				require.NotContains(t, firstText(res), "awaiting_approval")
				var tickets int64
				require.NoError(t, db.Model(&model.ApprovalTicket{}).Count(&tickets).Error)
				require.Zero(t, tickets, "非法目标不能留下可被批准执行的票据")
			})
		}
	}
}

// 即使附加标量 id 越权，合法批量仍按 ids 授权，票据目标也须来自同一列表。
func TestApprovalIntercept_BatchIgnoresUnrelatedScalarID(t *testing.T) {
	db := newStatelessTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ApprovalTicket{}, &model.ApprovalReceipt{}))
	deps := ToolDeps{Approval: service.NewApprovalService(db)}
	res := CallTool(t.Context(), deps, batchPrincipal(3, 7), "instance_batch", map[string]any{
		"op": "stop", "id": float64(999), "ids": []any{float64(3), float64(7)},
	})
	require.False(t, res.IsError, firstText(res))
	require.Contains(t, firstText(res), "awaiting_approval")
	var ticket model.ApprovalTicket
	require.NoError(t, db.First(&ticket).Error)
	require.Equal(t, "3", ticket.TargetID)
	require.JSONEq(t, `{"op":"stop","id":999,"ids":[3,7]}`, ticket.PayloadFrozen)
}

// 端到端 CallTool 结果已经证明审批拦截在执行器之前返回；不替换全局 catalog，避免测试间共享状态。
