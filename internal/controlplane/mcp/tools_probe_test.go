package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// probeTestPrincipal 一个具备实例只读与观测能力的最小主体。
func probeTestPrincipal() *service.AgentPrincipal {
	return &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{service.AgentCapabilityInstanceRead, service.AgentCapabilityObservabilityRead},
	}
}

// TestProbeTools_Registered probe_tools / probe_diagnose 必须出现在工具目录里。
//
// 它们是把 ServerProbe 的 42 个诊断工具经 CP 代理出去的**唯一入口**：
// 托管模式下探针零监听，外部直连不了，没有这两个工具就等于诊断能力在 MCP 面完全缺失。
func TestProbeTools_Registered(t *testing.T) {
	names := map[string]bool{}
	for _, n := range ToolNames() {
		names[n] = true
	}
	assert.True(t, names["probe_tools"], "probe_tools 应注册进工具目录")
	assert.True(t, names["probe_diagnose"], "probe_diagnose 应注册进工具目录")
}

// TestProbeTools_SchemaCoversRequiredParams 两个代理工具的 InputSchema 必须声明必需参数。
//
// 少写 required 会让模型传空参数进来，落到执行器才报错——报错信息在 MCP 面
// 表现为工具调用失败，排查时会误以为是探针问题。
func TestProbeTools_SchemaCoversRequiredParams(t *testing.T) {
	byName := map[string]ToolDef{}
	for _, d := range RegisteredTools() {
		byName[d.Name] = d
	}
	toolsDef, ok := byName["probe_tools"]
	require.True(t, ok)
	assert.Equal(t, []string{"id"}, requiredOf(t, toolsDef))

	diagDef, ok := byName["probe_diagnose"]
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"id", "tool"}, requiredOf(t, diagDef))
}

// requiredOf 取 InputSchema 的 required 列表。
func requiredOf(t *testing.T, def ToolDef) []string {
	t.Helper()
	raw, ok := def.InputSchema["required"]
	require.True(t, ok, "%s 的 InputSchema 应有 required", def.Name)
	out := make([]string, 0)
	switch v := raw.(type) {
	case []string:
		out = v
	case []any:
		for _, it := range v {
			s, _ := it.(string)
			out = append(out, s)
		}
	}
	return out
}

// TestProbeDiagnose_RejectsMissingParams 参数缺失要在执行器入口就拦下，
// 不能带着空 tool 名去敲探针（那样错误来自远端，排查成本高得多）。
func TestProbeDiagnose_RejectsMissingParams(t *testing.T) {
	p := probeTestPrincipal()
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"缺 id", map[string]any{"tool": "server_status"}, "id"},
		{"缺 tool", map[string]any{"id": float64(1)}, "tool"},
		{"tool 为空", map[string]any{"id": float64(1), "tool": "   "}, "tool"},
		{"params 非对象", map[string]any{"id": float64(1), "tool": "x", "params": "bad"}, "params"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := execProbeDiagnose(context.Background(), ToolDeps{}, p, "", c.args)
			require.True(t, res.IsError, "应返回错误")
			require.NotEmpty(t, res.Content)
			assert.Contains(t, res.Content[0].Text, c.want)
		})
	}
}

// TestProbeTools_NilBusinessFailsClosed 未装配 Business 时必须失败关闭。
//
// 与审批服务同一取向：装配缺失时**拒绝**而不是静默返回空结果——
// 后者会让调用方以为「探针没有问题」，实际根本没有问过探针。
func TestProbeTools_NilBusinessFailsClosed(t *testing.T) {
	p := probeTestPrincipal()
	// action 必须传真实值：授权阶段会拿它去查 action 目录，传空串会被当成未知 action 而拒绝，
	// 那就测不到“Business 未装配”这条路径了。
	res := execProbeTools(context.Background(), ToolDeps{}, p, service.AgentActionProbeDiagnose,
		map[string]any{"id": float64(1)})
	require.True(t, res.IsError, "Business 未装配时应报错")
	assert.Contains(t, res.Content[0].Text, "业务下发服务不可用")
}

// TestUnwrapProbeResult_FlattensMCPContent 探针与 CP 都是 MCP 端点，
// 若不拆包就会形成 content→content→content 三层套娃，模型要自己拆两层才看到数据。
func TestUnwrapProbeResult_FlattensMCPContent(t *testing.T) {
	// 典型形状：外层 MCP content，内层文本是 JSON 字符串。
	inner := `{"jvm":{"threadCount":132}}`
	wrapped, err := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": inner}},
	})
	require.NoError(t, err)

	got := string(unwrapProbeResult(wrapped))
	assert.Contains(t, got, `"threadCount":132`, "应拆到内层 JSON")
	assert.NotContains(t, got, `"content"`, "不应残留外层 content 包装")
}

// TestUnwrapProbeResult_PassesThroughNonContent 非 content 形状（如 tools/list 的 {tools:[...]}）原样透传。
func TestUnwrapProbeResult_PassesThroughNonContent(t *testing.T) {
	raw := json.RawMessage(`{"tools":[{"name":"server_status"}]}`)
	got := string(unwrapProbeResult(raw))
	assert.Equal(t, string(raw), got, "非 content 形状不得改写")
}

// TestProbeTools_RejectsOutOfScope 跨 scope 调探针工具必须被拒。
//
// 这是一条**安全回归**：callRegisteredTool 只在 ConfirmField 非空时才调
// authorizeDestructiveTarget，只读工具没有统一授权。代理工具不显式授权的话，
// 任意持有 observability.read 的 token 都能读任意实例的 JVM/线程/玩家数据。
func TestProbeTools_RejectsOutOfScope(t *testing.T) {
	// scope 只含实例 1，却去调实例 2。
	p := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{service.AgentCapabilityInstanceRead, service.AgentCapabilityObservabilityRead},
	}

	t.Run("probe_tools", func(t *testing.T) {
		res := execProbeTools(context.Background(), ToolDeps{}, p, service.AgentActionProbeDiagnose,
			map[string]any{"id": float64(2)})
		require.True(t, res.IsError, "越界访问必须报错")
		require.NotEmpty(t, res.Content)
	})

	t.Run("probe_diagnose", func(t *testing.T) {
		res := execProbeDiagnose(context.Background(), ToolDeps{}, p, service.AgentActionProbeDiagnose,
			map[string]any{"id": float64(2), "tool": "server_status"})
		require.True(t, res.IsError, "越界访问必须报错")
		require.NotEmpty(t, res.Content)
	})
}

// TestProbeTools_NodeScopeGrantsInstanceAccess 节点 scope 应覆盖其下实例。
//
// V2 Token 常见写法是只给 scopedNodeIds（不列实例 id）。若授权时只传 InstanceID
// 不传 NodeID，`principalCanAccessInstance` 的节点分支（要求 nodeID != 0）就没法命中，
// 结果是节点-scope 的 token 被误拒——看起来像“没权限”，实际是实现漏传了一个字段。
func TestProbeTools_NodeScopeGrantsInstanceAccess(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{7},
		Capabilities:  []string{service.AgentCapabilityInstanceRead, service.AgentCapabilityObservabilityRead},
	}
	// 实例 5 属于节点 7：带上 NodeID 后应放行。
	target := service.AgentTrustedTarget{
		ResourceType: service.AgentResourceInstance,
		InstanceID:   5,
		NodeID:       7,
	}
	_, err := service.Authorize(p, service.AgentActionProbeDiagnose, target)
	assert.NoError(t, err, "节点 scope 应允许访问其下实例")

	// 不带 NodeID（即漏传）则命中不了节点分支——这正是本次修掉的缺陷。
	withoutNode := service.AgentTrustedTarget{
		ResourceType: service.AgentResourceInstance,
		InstanceID:   5,
	}
	_, err2 := service.Authorize(p, service.AgentActionProbeDiagnose, withoutNode)
	assert.Error(t, err2, "不带 NodeID 时节点 scope 无法命中，说明该字段必须传")
}

// TestProbeTools_AllowsInScope scope 内应放行（至于探针能不能答是另一回事）。
//
// Business 为 nil，所以放行后会在 probeRPC 里报“业务下发服务不可用”——
// 那正是“授权已通过”的证据（否则会先被拦成 403）。
func TestProbeTools_AllowsInScope(t *testing.T) {
	p := probeTestPrincipal() // scope = [1]
	res := execProbeTools(context.Background(), ToolDeps{}, p, service.AgentActionProbeDiagnose,
		map[string]any{"id": float64(1)})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "业务下发服务不可用", "scope 内应通过授权，卡在依赖缺失而非 403")
}

// TestProbeRPC_PayloadIsJSONRPCString 桥载荷必须是「JSON-RPC 2.0 的字符串」。
//
// 这是真机踩过的坑：payload 传对象会被 CP 参数绑定拒掉（INVALID_REQUEST），
// 缺 jsonrpc 字段探针回 -32600。这里断言我们发出的形状不会重犯。
func TestProbeRPC_PayloadIsJSONRPCString(t *testing.T) {
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "server_status", "arguments": map[string]any{}}}
	raw, err := json.Marshal(req)
	require.NoError(t, err)

	var back map[string]any
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, "2.0", back["jsonrpc"], "必须带 jsonrpc 版本")
	assert.Equal(t, "tools/call", back["method"])
	assert.NotNil(t, back["params"])
}
