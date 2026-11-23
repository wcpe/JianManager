package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ServerProbe 诊断工具代理。
//
// ## 为什么必须由 CP 代理
// 托管模式下探针**零监听**：`config.yml` 里的 metrics/mcp 端口一个都不会真的开
// （真机核实过：9943/9945/29940–29945 全部未监听）。外部没有任何办法直连探针。
// 唯一可达路径是 Worker 上的反向 WS 桥——而 CP 正是那座桥的持有者。
//
// 所以把 42 个诊断工具经 CP 的 MCP 暴露出去是**唯一可行**的做法，
// 否则调用方要自己去连 Worker、自己说桥协议、自己处理 JSON-RPC 包装。
//
// ## 为什么不逐个注册 42 个工具
// 探针的工具集随版本演进（0.7.2 是 38 个，0.8.0 是 42 个），
// 在 CP 里硬编码一份清单会立刻变成会漂移的第二真源：
// 探针加了工具 CP 不知道，删了工具 CP 还在列。
// 这里只暴露**两个**稳定的入口工具：
//
//   - `probe_tools`     列探针当前实际支持哪些工具（现问现取，不缓存）
//   - `probe_diagnose`  调其中任意一个（名称原样透传）
//
// 调用方先 `probe_tools` 再 `probe_diagnose`，与探针版本自动保持一致。

// probeToolSpecs ServerProbe 诊断代理工具（两个稳定入口）。
var probeToolSpecs = []toolSpec{
	{
		Def: ToolDef{
			Name: "probe_tools",
			Description: "列出某实例 ServerProbe 探针当前支持的全部诊断工具（现问现取，随探针版本变化）。" +
				"返回的工具名可直接传给 probe_diagnose。托管模式下探针不监听任何本地端口，只能经此代理访问。",
			InputSchema: idSchema("实例 ID"),
		},
		Action: service.AgentActionProbeDiagnose,
		Exec:   execProbeTools,
	},
	{
		Def: ToolDef{
			Name: "probe_diagnose",
			Description: "调用实例 ServerProbe 的某个诊断工具。常用：server_status（JVM/内存/线程总览）、" +
				"thread_top（线程 CPU 排名）、thread_dump（线程栈）、gc_stats（GC 与内存池）、" +
				"plugin_list（插件清单）、plugin_cpu（插件 CPU 归因）、log_tail（日志尾部）、" +
				"player_lookup（玩家详情）、arthas_*（深度诊断）。先用 probe_tools 取最新工具名。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     numberProp("实例 ID"),
					"tool":   stringProp("探针工具名，如 server_status / thread_top / gc_stats"),
					"params": map[string]any{"type": "object", "description": "可选：传给该工具的参数对象"},
				},
				"required": []string{"id", "tool"},
			},
		},
		Action: service.AgentActionProbeDiagnose,
		Exec:   execProbeDiagnose,
	},
}

func init() {
	allToolSpecs = append(allToolSpecs, probeToolSpecs...)
}

// probeRPC 经桥向探针发一条 JSON-RPC 并取回 result。
//
// 桥协议细节（踩过的坑全在这里）：
//   - domain 恒为 "core"，action 恒为 "mcp"（探针侧 McpBridgeService.ACTION_MCP）
//   - payload 必须是**字符串**（CP 不解析，原样下发）；传对象会被 CP 的参数绑定拒掉
//   - 载荷必须是完整 JSON-RPC 2.0；缺 jsonrpc 字段探针回 -32600
func probeRPC(ctx context.Context, deps ToolDeps, instanceID uint, method string, params map[string]any) (json.RawMessage, error) {
	if deps.Business == nil {
		return nil, fmt.Errorf("业务下发服务不可用")
	}
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		req["params"] = params
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	res, err := deps.Business.Dispatch(instanceID, "core", "mcp", string(raw))
	if err != nil {
		return nil, err
	}
	if !res.Available {
		msg := res.Error
		if msg == "" {
			msg = "探针不可用（桥未连接或探针未部署）"
		}
		// 用 errors.New 而不是 fmt.Errorf("%s", msg)：msg 来自探针，可能含 % 等格式字符，
		// 拼进格式化串会变成不可预期的输出。
		return nil, errors.New(msg)
	}
	// res.Output 是探针回的整条 JSON-RPC 响应；取出 error 与 result。
	blob, err := json.Marshal(res.Output)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(blob, &resp); err != nil {
		return nil, fmt.Errorf("探针响应无法解析: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("探针返回错误 %d: %s", resp.Error.Code, resp.Error.Message)
	}
	// 探针的 tools/call 返回的是 **MCP 结果形状**（`{"content":[{"type":"text","text":"<真正数据>"}],"isError":bool}`），
	// 直接原样返回会让上层看到两层 content 嵌套（CP 的 ToolResult 再包一层）。
	// 这里拆到最内层：把 content[0].text 当正文。
	// 拆不了（如 tools/list 的 result 是普通对象）就原样返回。
	return unwrapProbeResult(resp.Result), nil
}

// unwrapProbeResult 把探针 tools/call 的 MCP 结果形状拆到内层文本。
//
// 探针与 CP 都是 MCP 端点，自然回的都是 `{content:[{type:text,text:...}]}`。
// 直接透传会形成 content→content→content 的套娃，调用方（模型）要自己拆两层才看到数据。
func unwrapProbeResult(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var wrapped struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil || len(wrapped.Content) == 0 {
		return raw
	}
	// 只在「整个 result 就是一层 MCP content」时拆；否则可能是别的形状。
	inner := wrapped.Content[0].Text
	if inner == "" {
		return raw
	}
	// 内层是 JSON 就规范化成 JSON，否则包成字符串。
	if json.Valid([]byte(inner)) {
		return json.RawMessage(inner)
	}
	b, err := json.Marshal(inner)
	if err != nil {
		return raw
	}
	return b
}

// execProbeTools 列探针支持的工具（tools/list）。
//
// 注意：tools/list 的 result 是 `{"tools":[...]}`（不是 content 形状），
// unwrapProbeResult 会原样透传；这正是预期行为。
func execProbeTools(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, ok := argUint(args, "id")
	if !ok {
		return toolErr("缺少参数 id（实例 ID）")
	}
	// 实例级授权必须显式做。
	//
	// callRegisteredTool 只在 ConfirmField 非空（destructive 工具）时才调
	// authorizeDestructiveTarget；只读工具没有任何统一授权。其他只读工具
	// （agent_get_instance 等）都是在各自 case 里显式调 CanDiscover。
	// 漏掉这一步 = 任意 token 都能读任意实例，跨 scope 越权。
	if res, ok := authorizeProbeTarget(deps, p, action, id); !ok {
		return res
	}
	// tools/list 不经过 unwrap：直接拿原结果，避免万一探针也把 tools 包进 content 时丢结构。
	res, err := probeRPC(ctx, deps, id, "tools/list", nil)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(res)
}

// authorizeProbeTarget 对探针代理工具做实例级授权。
//
// 返回 (拒绝结果, false) 表示应直接返回拒绝；(零值, true) 表示放行。
//
// 注意：只传 InstanceID 不够。`principalCanAccessInstance` 的节点分支要求
// `nodeID != 0` 才能命中——而 V2 Token 大量使用「节点 scope 覆盖其下全部实例」的写法。
// 因此必须把实例所属的 nodeID 一并带上，否则节点-scope 的 token 会被误拒。
func authorizeProbeTarget(deps ToolDeps, p *service.AgentPrincipal, action string, instanceID uint) (ToolResult, bool) {
	if p == nil {
		return toolErr("需要有效的 Agent Token"), false
	}
	target := service.AgentTrustedTarget{
		ResourceType: service.AgentResourceInstance,
		InstanceID:   instanceID,
	}
	// 查实例所属节点，供节点 scope 判定。
	//
	// 只在必要时查：实例 scope 直接命中就无需 DB（高频诊断调用下每次多一次查库是白费的）。
	// 而节点 scope 的 token 才需要拿到 nodeID 才能命中 principalCanAccessInstance 的节点分支。
	if !agentScopeHasInstance(p, instanceID) && deps.Instance != nil {
		if inst, err := deps.Instance.GetByID(instanceID); err == nil && inst != nil {
			target.NodeID = inst.NodeID
		}
	}
	if _, err := service.Authorize(p, action, target); err != nil {
		return toolForbidden(err), false
	}
	return ToolResult{}, true
}

// agentScopeHasInstance 报告 Token 的实例 scope 是否直接包含该 id。
func agentScopeHasInstance(p *service.AgentPrincipal, instanceID uint) bool {
	if p == nil {
		return false
	}
	for _, id := range p.ScopedInstanceIDs {
		if id == instanceID {
			return true
		}
	}
	return false
}

// execProbeDiagnose 调探针的某个工具（tools/call）。
//
// tool 名原样透传：CP 不维护工具白名单，也不解析参数语义。
// 这样做的好处是探针升级后新工具立刻可用、不需要改 CP；
// 代价是拼错的工具名要等探针侧报错——所以报错时把探针原始信息带回去。
func execProbeDiagnose(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, ok := argUint(args, "id")
	if !ok {
		return toolErr("缺少参数 id（实例 ID）")
	}
	tool, _ := args["tool"].(string)
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return toolErr("缺少参数 tool（探针工具名，可先用 probe_tools 获取）")
	}
	var toolArgs map[string]any
	switch v := args["params"].(type) {
	case map[string]any:
		toolArgs = v
	case nil:
		toolArgs = map[string]any{}
	default:
		return toolErr("params 必须是对象")
	}
	// 与 probe_tools 同一道实例级授权（见 authorizeProbeTarget 的说明）。
	if res, ok := authorizeProbeTarget(deps, p, action, id); !ok {
		return res
	}
	res, err := probeRPC(ctx, deps, id, "tools/call", map[string]any{
		"name":      tool,
		"arguments": toolArgs,
	})
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(res)
}

// argUint 从参数里取一个正整数（JSON 解出来是 float64）。
func argUint(args map[string]any, key string) (uint, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		if n <= 0 {
			return 0, false
		}
		return uint(n), true
	case int:
		if n <= 0 {
			return 0, false
		}
		return uint(n), true
	}
	return 0, false
}
