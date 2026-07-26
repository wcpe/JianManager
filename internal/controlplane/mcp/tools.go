package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ToolDef MCP tools/list 中的工具描述。
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolResult tools/call 返回结构（MCP 子集）。
type ToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolContent 文本内容块。
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolDeps 工具调用依赖的 CP 服务（禁止在此复制策略）。
type ToolDeps struct {
	Instance *service.InstanceService
	Node     *service.NodeService
	Log      *service.LogService        // 可选；nil 时 agent_get_instance_logs 返回中文错误
	Agent    *service.AgentTokenService // FR-395：可信目标解析与 scope 查询
	// FR-396 扩展依赖；nil 时对应工具返回中文「服务不可用」。
	Provision *service.ProvisionService
	Import    *service.ImportServerService
	Clone     *service.CloneService
	Batch     *service.InstanceBatchService
	Docker    *service.DockerImageService
	Crash     *service.CrashSnapshotService
	Task      *service.TaskService
	// FR-397 内容运维依赖；nil 时对应工具返回中文「服务不可用」。
	File        *service.FileService
	FileVersion *service.FileVersionService
	Config      *service.ConfigService
	Plugin      *service.PluginService
	Transfer    *service.AgentTransferTicketService
	// FR-398：Bot 舰队与压测编排；任一为 nil 时对应工具返回中文「服务不可用」。
	Bot           *service.BotService
	LoadTemplate  *service.BotLoadTemplateService
	StressSession *service.BotStressSessionService
	Preflight     *service.BotLoadPreflightService
	Execution     BotLoadExecutor
	Projection    *service.BotLoadProjectionService
	Report        *service.BotLoadReportService
	Capacity      service.BotLoadCapacityProvider
	Metrics       *service.BotLoadMetricSampler
}

// toolExec 工具执行器：参数已校验过会话，action 已从 catalog 解析。
// 授权与确认由 CallTool 骨架统一处理，执行器只做参数解析 + 调 service。
type toolExec func(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult

// toolSpec 工具协议定义（静态注册）。
type toolSpec struct {
	Def          ToolDef
	Action       string
	Exec         toolExec // 可选；nil 时走 CallTool 内置 switch（兼容既有 11 工具）
	ConfirmField string   // FR-396：destructive 确认参数名（如 confirmInstanceName）
}

// allToolSpecs 全量工具目录（静态；永久禁区不在此处）。
// 工具可见性由 ToolsForPrincipal 按能力动态裁剪；此处只是声明与 InputSchema。
var allToolSpecs = []toolSpec{
	{
		Def: ToolDef{
			Name:        "agent_whoami",
			Description: "查询当前 Agent Token 身份与策略版本、能力与 scope",
			InputSchema: emptySchema(),
		},
		Action: service.AgentActionWhoami,
	},
	{
		Def: ToolDef{
			Name:        "agent_list_nodes",
			Description: "列出 Token scope 内的节点",
			InputSchema: emptySchema(),
		},
		Action: service.AgentActionListNodes,
	},
	{
		Def: ToolDef{
			Name:        "agent_list_instances",
			Description: "列出 Token scope 内的实例；V2 Token 节点 scope 自动覆盖当前实例；可选按 nodeId 过滤",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"nodeId": map[string]any{
						"type":        "number",
						"description": "可选：仅返回该节点上的实例",
					},
				},
			},
		},
		Action: service.AgentActionListInstances,
	},
	{
		Def: ToolDef{
			Name:        "agent_get_instance",
			Description: "获取指定实例详情（须在 scope 内）",
			InputSchema: idSchema("实例 ID"),
		},
		Action: service.AgentActionGetInstance,
	},
	{
		Def: ToolDef{
			Name:        "agent_get_instance_metrics",
			Description: "获取指定实例运行指标（须在 scope 内）",
			InputSchema: idSchema("实例 ID"),
		},
		Action: service.AgentActionGetInstanceMetrics,
	},
	{
		Def: ToolDef{
			Name:        "agent_get_instance_logs",
			Description: "获取指定实例最近日志（须在 scope 内）；可选 limit",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "number",
						"description": "实例 ID",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "返回条数上限，默认 50，最大 200",
					},
				},
				"required": []string{"id"},
			},
		},
		Action: service.AgentActionGetInstanceLogs,
	},
	{
		Def: ToolDef{
			Name:        "instance_start",
			Description: "启动实例（须具备 instance.life 能力或 V1 写白名单）",
			InputSchema: idSchema("实例 ID"),
		},
		Action: service.AgentActionInstanceStart,
	},
	{
		Def: ToolDef{
			Name:        "instance_stop",
			Description: "停止实例（须具备 instance.life 能力或 V1 写白名单）",
			InputSchema: idSchema("实例 ID"),
		},
		Action: service.AgentActionInstanceStop,
	},
	{
		Def: ToolDef{
			Name:        "instance_restart",
			Description: "重启实例（须具备 instance.life 能力或 V1 写白名单）",
			InputSchema: idSchema("实例 ID"),
		},
		Action: service.AgentActionInstanceRestart,
	},
	{
		Def: ToolDef{
			Name:        "node_maintenance_enter",
			Description: "节点进入维护模式（须具备 node.operate 能力或 V1 写白名单）",
			InputSchema: idSchema("节点 ID"),
		},
		Action: service.AgentActionNodeMaintenanceEnter,
	},
	{
		Def: ToolDef{
			Name:        "node_maintenance_leave",
			Description: "节点离开维护模式（须具备 node.operate 能力或 V1 写白名单）",
			InputSchema: idSchema("节点 ID"),
		},
		Action: service.AgentActionNodeMaintenanceLeave,
	},
}

// init 追加 FR-397 内容运维工具（文件/配置/插件），保持各域声明在各自文件内。
func init() {
	allToolSpecs = append(allToolSpecs, fileToolSpecs...)
	allToolSpecs = append(allToolSpecs, configToolSpecs...)
	allToolSpecs = append(allToolSpecs, pluginToolSpecs...)
}

// init 追加 FR-398 Bot 舰队与压测编排工具，保持 allToolSpecs 的追加式演进。
func init() {
	allToolSpecs = append(allToolSpecs, botToolSpecs()...)
	allToolSpecs = append(allToolSpecs, loadTestToolSpecs()...)
	allToolSpecs = append(allToolSpecs, loadTestQueryToolSpecs()...)
}

// RegisteredTools 返回全量工具目录（硬拒绝面与永久禁区永不出现）。
// 供全量测试用；生产 tools/list 调用 ToolsForPrincipal。
func RegisteredTools() []ToolDef {
	out := make([]ToolDef, 0, len(allToolSpecs))
	for _, s := range allToolSpecs {
		out = append(out, s.Def)
	}
	return out
}

// ToolNames 返回注册工具名称列表（单测用）。
func ToolNames() []string {
	out := make([]string, 0, len(allToolSpecs))
	for _, s := range allToolSpecs {
		out = append(out, s.Def.Name)
	}
	return out
}

// ToolsForPrincipal 按 principal 的能力与潜在 scope 动态裁剪工具列表（FR-395）。
// V1 仅显示兼容解释器确实可能允许的工具；V2 按 capability 与 scope 可用性过滤。
func ToolsForPrincipal(p *service.AgentPrincipal) []ToolDef {
	if p == nil {
		return nil
	}
	out := make([]ToolDef, 0, len(allToolSpecs))
	for _, s := range allToolSpecs {
		if _, err := service.CanDiscover(p, s.Action); err == nil {
			out = append(out, s.Def)
		}
	}
	return out
}

// toolActionByName 通过工具名快速查 action。
func toolActionByName(name string) (string, bool) {
	for _, s := range allToolSpecs {
		if s.Def.Name == name {
			return s.Action, true
		}
	}
	return "", false
}

// toolTargetByName 从参数提取 target 类型与 ID 字符串（供流水记录）。
func toolTargetByName(name string, args map[string]any) (targetType, targetID string) {
	if args == nil {
		return "", ""
	}
	action, ok := toolActionByName(name)
	if !ok {
		return "", ""
	}
	d, ok := service.DescribeAgentAction(action)
	if !ok || d.ResourceType == service.AgentResourceNone {
		return "", ""
	}
	id, err := toUint(args["id"])
	if err != nil {
		return "", ""
	}
	return d.ResourceType, strconv.FormatUint(uint64(id), 10)
}

// CallTool 按名称分发工具调用；策略走 action 目录，可信目标由 Agent service 解析。
func CallTool(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, name string, args map[string]any) ToolResult {
	if p == nil {
		return toolErr("需要有效的 Agent Token")
	}
	if args == nil {
		args = map[string]any{}
	}
	// 会话已踢线时 ctx 取消
	select {
	case <-ctx.Done():
		return toolErr("MCP 会话已关闭")
	default:
	}

	// 工具不在 catalog → 未知工具（永久禁区也不在 catalog）
	action, ok := toolActionByName(name)
	if !ok {
		return toolErr("未知工具: " + name + "（硬拒绝面操作不会注册为 tool）")
	}

	// FR-397 内容运维域（文件/配置/插件）在独立文件内分发。
	if res, handled := callContentTool(deps, p, name, action, args); handled {
		return res
	}

	switch name {
	case "agent_whoami":
		if _, err := service.CanDiscover(p, action); err != nil {
			return toolForbidden(err)
		}
		return toolOK(map[string]any{
			"kind":              "agent",
			"name":              p.Name,
			"tokenId":           p.TokenID,
			"tokenPrefix":       p.TokenPrefix,
			"policyVersion":     p.PolicyVersion,
			"scopedInstanceIds": p.ScopedInstanceIDs,
			"scopedNodeIds":     p.ScopedNodeIDs,
			"writeAllowlist":    p.WriteAllowlist,
			"capabilities":      p.Capabilities,
		})

	case "agent_list_nodes":
		if _, err := service.CanDiscover(p, action); err != nil {
			return toolForbidden(err)
		}
		if deps.Node == nil {
			return toolErr("节点服务不可用")
		}
		var out []model.Node
		for _, id := range p.ScopedNodeIDs {
			n, err := deps.Node.GetByID(id)
			if err != nil {
				continue
			}
			out = append(out, *n)
		}
		return toolOK(out)

	case "agent_list_instances":
		if deps.Agent == nil {
			return toolErr("策略服务不可用")
		}
		if deps.Instance == nil {
			return toolErr("实例服务不可用")
		}
		var nodeFilter *uint
		if v, ok := args["nodeId"]; ok {
			nid, err := toUint(v)
			if err != nil {
				return toolErr("参数 nodeId 无效: " + err.Error())
			}
			nodeFilter = &nid
		}
		list, err := deps.Agent.ListAccessibleInstances(p, nodeFilter)
		if err != nil {
			return toolForbidden(err)
		}
		return toolOK(list)

	case "agent_get_instance":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		if deps.Agent == nil {
			return toolErr("策略服务不可用")
		}
		_, inst, err := deps.Agent.AuthorizeInstanceAction(p, action, id)
		if err != nil {
			return toolForbidden(err)
		}
		return toolOK(inst)

	case "agent_get_instance_metrics":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		if deps.Agent == nil {
			return toolErr("策略服务不可用")
		}
		if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, id); err != nil {
			return toolForbidden(err)
		}
		if deps.Instance == nil {
			return toolErr("实例服务不可用")
		}
		m, err := deps.Instance.GetMetrics(id)
		if err != nil {
			return toolErr("查询指标失败: " + err.Error())
		}
		return toolOK(m)

	case "agent_get_instance_logs":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		if deps.Agent == nil {
			return toolErr("策略服务不可用")
		}
		if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, id); err != nil {
			return toolForbidden(err)
		}
		if deps.Log == nil {
			return toolErr("日志服务不可用")
		}
		limit := 50
		if v, ok := args["limit"]; ok {
			n, err := toUint(v)
			if err != nil || n == 0 {
				return toolErr("参数 limit 无效")
			}
			limit = int(n)
			if limit > 200 {
				limit = 200
			}
		}
		src := model.LogSourceInstance
		page, err := deps.Log.Query(service.LogFilter{
			InstanceID: &id,
			Source:     &src,
			Page:       1,
			PageSize:   limit,
		})
		if err != nil {
			return toolErr("查询日志失败: " + err.Error())
		}
		return toolOK(page)

	case "instance_start", "instance_stop", "instance_restart":
		return callLifecycle(ctx, deps, p, action, args)

	case "node_maintenance_enter":
		return callMaintenance(deps, p, action, args, true)
	case "node_maintenance_leave":
		return callMaintenance(deps, p, action, args, false)

	default:
		// FR-398：Bot 舰队与压测编排域由独立执行器分发。
		if exec, ok := botDomainExec(name); ok {
			return exec(ctx, deps, p, action, args)
		}
		// FR-396+：扩展工具走注册表 Exec，CallTool 骨架统一处理确认参数。
		return callRegisteredTool(ctx, deps, p, name, action, args)
	}
}

// callRegisteredTool 按工具名查找带 Exec 的 toolSpec 并执行。
// destructive 工具在授权通过后、执行器之前统一校验精确确认参数。
func callRegisteredTool(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, name, action string, args map[string]any) ToolResult {
	spec, ok := findToolSpec(name)
	if !ok || spec.Exec == nil {
		return toolErr("未知工具: " + name)
	}
	// 精确确认：与目标当前名称比对（区分大小写，不 trim）。
	if field := spec.ConfirmField; field != "" {
		if err := verifyDestructiveConfirm(deps, action, args, field); err != nil {
			return toolErr(err.Error())
		}
	}
	return spec.Exec(ctx, deps, p, action, args)
}

// findToolSpec 按工具名查找注册表条目。
func findToolSpec(name string) (toolSpec, bool) {
	for _, s := range allToolSpecs {
		if s.Def.Name == name {
			return s, true
		}
	}
	return toolSpec{}, false
}

// verifyDestructiveConfirm 从 args 取确认字段，与 CP 数据库中目标当前名称精确比对。
// 确认失败不进入 service 写路径；返回中文错误供 isError 投影。
func verifyDestructiveConfirm(deps ToolDeps, action string, args map[string]any, field string) error {
	raw, _ := args[field].(string)
	if raw == "" {
		return fmt.Errorf("缺少必填确认参数 %s", field)
	}
	id, err := requireID(args)
	if err != nil {
		return err
	}
	d, ok := service.DescribeAgentAction(action)
	if !ok {
		return fmt.Errorf("未知动作")
	}
	var actual string
	switch d.ResourceType {
	case service.AgentResourceInstance:
		if deps.Instance == nil {
			return fmt.Errorf("实例服务不可用")
		}
		inst, e := deps.Instance.GetByID(id)
		if e != nil {
			return fmt.Errorf("确认名称与目标不符")
		}
		actual = inst.Name
	case service.AgentResourceNode:
		if deps.Node == nil {
			return fmt.Errorf("节点服务不可用")
		}
		n, e := deps.Node.GetByID(id)
		if e != nil {
			return fmt.Errorf("确认名称与目标不符")
		}
		actual = n.Name
	default:
		return fmt.Errorf("确认参数仅适用于节点/实例目标")
	}
	if raw != actual {
		return fmt.Errorf("确认名称与目标不符")
	}
	return nil
}

// registerToolSpecs 追加工具到全局目录（FR-396+ 域文件 init 调用）。
func registerToolSpecs(specs ...toolSpec) {
	allToolSpecs = append(allToolSpecs, specs...)
}

// callLifecycle 实例生命周期工具调用：可信目标授权 + expected node 派发（FR-395）。
// 若 deps.Agent 为 nil（unit test 轻量场景），回退 ResolveAction 仅做主体内存判断。
func callLifecycle(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	_ = ctx
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	// 有 Agent service → 可信目标授权 + expected node（FR-395 生产路径）。
	if deps.Agent != nil {
		_, inst, err := deps.Agent.AuthorizeInstanceAction(p, action, id)
		if err != nil {
			return toolForbidden(err)
		}
		if deps.Instance == nil {
			return toolErr("实例服务不可用")
		}
		expectedNodeID := inst.NodeID
		var execErr error
		switch action {
		case service.AgentActionInstanceStart:
			execErr = deps.Instance.StartForExpectedNode(id, expectedNodeID)
		case service.AgentActionInstanceStop:
			execErr = deps.Instance.StopForExpectedNode(id, expectedNodeID)
		case service.AgentActionInstanceRestart:
			execErr = deps.Instance.RestartForExpectedNode(id, expectedNodeID)
		default:
			return toolErr("未知生命周期动作: " + action)
		}
		if execErr != nil {
			return toolErr(execErr.Error())
		}
		return toolOK(map[string]any{"ok": true})
	}
	// 无 Agent service → 先做主体内存策略判断，再检查服务可用性（unit test / legacy）。
	if err := service.ResolveAction(p, action, id, 0); err != nil {
		return toolForbidden(err)
	}
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	var execErr error
	switch action {
	case service.AgentActionInstanceStart:
		execErr = deps.Instance.Start(id)
	case service.AgentActionInstanceStop:
		execErr = deps.Instance.Stop(id)
	case service.AgentActionInstanceRestart:
		execErr = deps.Instance.Restart(id)
	default:
		return toolErr("未知生命周期动作: " + action)
	}
	if execErr != nil {
		return toolErr(execErr.Error())
	}
	return toolOK(map[string]any{"ok": true})
}

// callMaintenance 节点维护工具调用。
func callMaintenance(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any, enabled bool) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, err := deps.Agent.AuthorizeNodeAction(p, action, id); err != nil {
		return toolForbidden(err)
	}
	if deps.Node == nil {
		return toolErr("节点服务不可用")
	}
	if _, err := deps.Node.SetMaintenance(id, enabled); err != nil {
		return toolErr(err.Error())
	}
	return toolOK(map[string]any{"ok": true})
}

func emptySchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func idSchema(desc string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "number",
				"description": desc,
			},
		},
		"required": []string{"id"},
	}
}

func requireID(args map[string]any) (uint, error) {
	v, ok := args["id"]
	if !ok {
		return 0, fmt.Errorf("缺少必填参数 id")
	}
	return toUint(v)
}

func toUint(v any) (uint, error) {
	switch n := v.(type) {
	case float64:
		if n < 0 || n != float64(uint64(n)) {
			return 0, fmt.Errorf("须为正整数")
		}
		return uint(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil || i < 0 {
			return 0, fmt.Errorf("须为正整数")
		}
		return uint(i), nil
	case int:
		if n < 0 {
			return 0, fmt.Errorf("须为正整数")
		}
		return uint(n), nil
	case int64:
		if n < 0 {
			return 0, fmt.Errorf("须为正整数")
		}
		return uint(n), nil
	case string:
		i, err := strconv.ParseUint(n, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("须为正整数")
		}
		return uint(i), nil
	default:
		return 0, fmt.Errorf("类型无效")
	}
}

func toolOK(v any) ToolResult {
	text := "{}"
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return toolErr("序列化结果失败: " + err.Error())
		}
		text = string(b)
	}
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func toolErr(msg string) ToolResult {
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}

func toolForbidden(err error) ToolResult {
	msg := "能力/scope 不足或操作被拒绝"
	if err != nil && !errors.Is(err, service.ErrAgentForbidden) {
		msg = err.Error()
	}
	return toolErr(msg)
}
