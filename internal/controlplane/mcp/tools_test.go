package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestRegisteredTools_Set(t *testing.T) {
	tools := RegisteredTools()
	names := make(map[string]bool, len(tools))
	for _, tl := range tools {
		names[tl.Name] = true
		assert.NotEmpty(t, tl.Description)
		assert.NotNil(t, tl.InputSchema)
	}
	want := []string{
		"agent_whoami",
		"agent_list_nodes",
		"agent_list_instances",
		"agent_get_instance",
		"agent_get_instance_metrics",
		"agent_get_instance_logs",
		"instance_start",
		"instance_stop",
		"instance_restart",
		"node_maintenance_enter",
		"node_maintenance_leave",
	}
	// 397/398 起工具集加性扩展：只断言既有 11 个仍在，不强制总数。
	// 各扩展域另有专门契约测试。
	assert.GreaterOrEqual(t, len(tools), len(want))
	for _, n := range want {
		assert.True(t, names[n], "应注册 %s", n)
	}
	// 硬拒绝面不得出现
	hardDenied := []string{
		"user_create", "delete_instance", "kill_instance",
		"db_browse", "self_update", "audit_delete",
	}
	for _, n := range hardDenied {
		assert.False(t, names[n], "硬拒绝工具不得注册: %s", n)
	}
}

// TestToolSpecsMatchCatalog 断言每个工具的 Action 都在 catalog 中登记（ADR-103）。
//
// catalog 是 action→capability→scope 的唯一真源（ADR-080 决策 4）。
// 工具若引用未登记 action，则该工具不可被授权，属于死配置。
func TestToolSpecsMatchCatalog(t *testing.T) {
	for _, s := range allToolSpecs {
		require.NotEmpty(t, s.Action, "工具 %s 缺 Action 关联", s.Def.Name)
		_, ok := service.DescribeAgentAction(s.Action)
		assert.True(t, ok, "工具 %s 引用了未登记的 action %s", s.Def.Name, s.Action)
	}
}

// TestToolSpecsAnnotations 校验标注按 catalog 操作等级派生（ADR-103）。
func TestToolSpecsAnnotations(t *testing.T) {
	for _, def := range RegisteredTools() {
		action, ok := toolActionByName(def.Name)
		require.True(t, ok, "工具 %s 找不到 action", def.Name)
		d, ok := service.DescribeAgentAction(action)
		require.True(t, ok, "工具 %s 的 action %s 未登记", def.Name, action)

		require.NotNil(t, def.Annotations, "工具 %s 缺 annotations", def.Name)
		assert.Equal(t, d.IsReadOnly(), def.Annotations["readOnlyHint"],
			"工具 %s 的 readOnlyHint 与 catalog 不一致", def.Name)
		assert.Equal(t, d.IsDestructive(), def.Annotations["destructiveHint"],
			"工具 %s 的 destructiveHint 与 catalog 不一致", def.Name)
	}
}

// TestDestructiveToolsAreAnnotated 断言破坏性工具一定被标注，
// 且需要确认的工具把确认字段透出给客户端。
func TestDestructiveToolsAreAnnotated(t *testing.T) {
	var destructiveCount int
	for _, s := range allToolSpecs {
		d, ok := service.DescribeAgentAction(s.Action)
		if !ok || !d.IsDestructive() {
			continue
		}
		destructiveCount++
		def := withAnnotations(s)
		assert.Equal(t, true, def.Annotations["destructiveHint"],
			"破坏性工具 %s 必须带 destructiveHint", def.Name)
		assert.Equal(t, false, def.Annotations["readOnlyHint"],
			"破坏性工具 %s 不得标为只读", def.Name)
		if d.RequiresConfirm && s.ConfirmField != "" {
			// 确认字段名由各工具自定义（confirmInstanceName / confirmNodeName / confirmName…），
			// 声明的必须原样透出；未声明 ConfirmField 的工具自行在 InputSchema 内校验，此处不强制。
			assert.Equal(t, s.ConfirmField, def.Annotations["confirmField"],
				"工具 %s 的确认字段未透出", def.Name)
		}
	}
	assert.Positive(t, destructiveCount, "应存在破坏性工具以覆盖标注路径")
}

func TestToolsForPrincipal_EmptyV2OnlyWhoami(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		Capabilities:  []string{},
	}
	tools := ToolsForPrincipal(p)
	require.Len(t, tools, 1)
	assert.Equal(t, "agent_whoami", tools[0].Name)
}

func TestToolsForPrincipal_V2InstanceReadAndLife(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{service.AgentCapabilityInstanceRead, service.AgentCapabilityInstanceLife},
	}
	tools := ToolsForPrincipal(p)
	names := make(map[string]bool, len(tools))
	for _, t2 := range tools {
		names[t2.Name] = true
	}
	assert.True(t, names["agent_whoami"])
	assert.True(t, names["agent_list_instances"])
	assert.True(t, names["agent_get_instance"])
	assert.True(t, names["instance_start"])
	assert.True(t, names["instance_stop"])
	assert.True(t, names["instance_restart"])
	// 无 node.read → 节点相关工具不可见
	assert.False(t, names["agent_list_nodes"])
	assert.False(t, names["node_maintenance_enter"])
}

func TestToolsForPrincipal_V2NodeRead(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{1},
		Capabilities:  []string{service.AgentCapabilityNodeRead},
	}
	tools := ToolsForPrincipal(p)
	names := make(map[string]bool, len(tools))
	for _, t2 := range tools {
		names[t2.Name] = true
	}
	assert.True(t, names["agent_list_nodes"])
	assert.False(t, names["instance_start"])
	assert.False(t, names["node_maintenance_enter"]) // 需 node.operate
}

func TestToolsForPrincipal_V1FullWriteShowsAllLegacyTools(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV1,
		ScopedInstanceIDs: []uint{1},
		ScopedNodeIDs:     []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife, service.AgentWriteNodeMaintenance},
	}
	// V1 只能看到兼容解释器允许的 action（V1Allowed=true）；
	// 397/398 起新增工具一律 V1Allowed=false，V1 Token 不得因工具集扩容而扩权。
	want := 0
	for _, spec := range allToolSpecs {
		d, ok := service.DescribeAgentAction(spec.Action)
		if ok && d.V1Allowed {
			want++
		}
	}
	tools := ToolsForPrincipal(p)
	assert.Len(t, tools, want, "V1 全白名单应显示全部兼容工具，且不含新增工具")
	for _, tool := range tools {
		action, ok := toolActionByName(tool.Name)
		assert.True(t, ok)
		d, ok := service.DescribeAgentAction(action)
		assert.True(t, ok)
		assert.True(t, d.V1Allowed, "V1 不应看到工具: "+tool.Name)
	}
}

func TestToolsForPrincipal_V1EmptyWriteNoLifecycle(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV1,
		ScopedInstanceIDs: []uint{1},
		ScopedNodeIDs:     []uint{1},
		WriteAllowlist:    []string{},
	}
	tools := ToolsForPrincipal(p)
	names := make(map[string]bool, len(tools))
	for _, t2 := range tools {
		names[t2.Name] = true
	}
	assert.False(t, names["instance_start"], "空写白名单不应显示生命周期工具")
	assert.False(t, names["node_maintenance_enter"])
	assert.True(t, names["agent_whoami"])
	assert.True(t, names["agent_get_instance"])
}

func TestCallTool_Whoami(t *testing.T) {
	p := &service.AgentPrincipal{
		TokenID:           1,
		Name:              "ci",
		TokenPrefix:       "jmat_xx",
		ScopedInstanceIDs: []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife},
	}
	res := CallTool(context.Background(), ToolDeps{}, p, "agent_whoami", nil)
	assert.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Contains(t, res.Content[0].Text, "ci")
	assert.Contains(t, res.Content[0].Text, "jmat_xx")
}

func TestCallTool_UnknownIsError(t *testing.T) {
	p := &service.AgentPrincipal{TokenID: 1, Name: "x"}
	res := CallTool(context.Background(), ToolDeps{}, p, "user_create", nil)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "未知工具")
}

func TestCallTool_ScopeDenied(t *testing.T) {
	p := &service.AgentPrincipal{
		TokenID:           1,
		Name:              "x",
		ScopedInstanceIDs: []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife},
	}
	// id=99 不在 scope
	res := CallTool(context.Background(), ToolDeps{}, p, "instance_start", map[string]any{"id": float64(99)})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "拒绝")
}

func TestCallTool_MissingID(t *testing.T) {
	p := &service.AgentPrincipal{
		TokenID:           1,
		ScopedInstanceIDs: []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife},
	}
	// instance_stop 现在**需审**，未装配审批服务会被失败关闭挡下。
	// 本用例要测的是「缺 id 参数」，因此必须装配真实审批服务，
	// 否则断言到的是审批失败而不是参数校验失败（两者都在最前面，顺序决定归属）。
	deps := newApprovalTestDeps(t)
	res := CallTool(context.Background(), deps, p, "instance_stop", map[string]any{})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "id")
}

// newApprovalTestDeps 装配一个含审批服务的 ToolDeps（内存库）。
//
// 存在的理由：之后**高危写工具一律走审批**，任何调用它们的用例
// 都必须装配审批服务，否则只会撞在「未装配（失败关闭）」上，
// 而测不到该用例原本想验的行为。
func newApprovalTestDeps(t *testing.T) ToolDeps {
	t.Helper()
	db := newStatelessTestDB(t)
	if err := db.AutoMigrate(&model.ApprovalTicket{}, &model.ApprovalReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	return ToolDeps{Approval: service.NewApprovalService(db)}
}

func TestCallTool_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &service.AgentPrincipal{TokenID: 1, Name: "x"}
	res := CallTool(ctx, ToolDeps{}, p, "agent_whoami", nil)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "已取消")
}
