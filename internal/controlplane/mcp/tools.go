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
	Log      *service.LogService // 可选；nil 时 agent_get_instance_logs 返回中文错误
}

// RegisteredTools 返回本网关注册的全部工具（硬拒绝面永不出现）。
// 与 jm-agent / Agent Ops 对齐；含 agent_get_instance_logs（spec FR-389）。
func RegisteredTools() []ToolDef {
	return []ToolDef{
		{
			Name:        "agent_whoami",
			Description: "查询当前 Agent Token 身份与 scope/写白名单",
			InputSchema: emptySchema(),
		},
		{
			Name:        "agent_list_nodes",
			Description: "列出 Token scope 内的节点",
			InputSchema: emptySchema(),
		},
		{
			Name:        "agent_list_instances",
			Description: "列出 Token scope 内的实例；可选按 nodeId 过滤",
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
		{
			Name:        "agent_get_instance",
			Description: "获取指定实例详情（须在 scope 内）",
			InputSchema: idSchema("实例 ID"),
		},
		{
			Name:        "agent_get_instance_metrics",
			Description: "获取指定实例运行指标（须在 scope 内）",
			InputSchema: idSchema("实例 ID"),
		},
		{
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
		{
			Name:        "instance_start",
			Description: "启动实例（需写白名单 instance.life + 实例 scope）",
			InputSchema: idSchema("实例 ID"),
		},
		{
			Name:        "instance_stop",
			Description: "停止实例（需写白名单 instance.life + 实例 scope）",
			InputSchema: idSchema("实例 ID"),
		},
		{
			Name:        "instance_restart",
			Description: "重启实例（需写白名单 instance.life + 实例 scope）",
			InputSchema: idSchema("实例 ID"),
		},
		{
			Name:        "node_maintenance_enter",
			Description: "节点进入维护模式（需写白名单 node.maintenance + 节点 scope）",
			InputSchema: idSchema("节点 ID"),
		},
		{
			Name:        "node_maintenance_leave",
			Description: "节点离开维护模式（需写白名单 node.maintenance + 节点 scope）",
			InputSchema: idSchema("节点 ID"),
		},
	}
}

// ToolNames 返回注册工具名称列表（单测用）。
func ToolNames() []string {
	tools := RegisteredTools()
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

// CallTool 按名称分发工具调用；策略一律走 ResolveAction。
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

	switch name {
	case "agent_whoami":
		if err := service.ResolveAction(p, service.AgentActionWhoami, 0, 0); err != nil {
			return toolForbidden(err)
		}
		return toolOK(map[string]any{
			"kind":              "agent",
			"name":              p.Name,
			"tokenId":           p.TokenID,
			"tokenPrefix":       p.TokenPrefix,
			"scopedInstanceIds": p.ScopedInstanceIDs,
			"scopedNodeIds":     p.ScopedNodeIDs,
			"writeAllowlist":    p.WriteAllowlist,
		})

	case "agent_list_nodes":
		if err := service.ResolveAction(p, service.AgentActionListNodes, 0, 0); err != nil {
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
		if err := service.ResolveAction(p, service.AgentActionListInstances, 0, 0); err != nil {
			return toolForbidden(err)
		}
		if deps.Instance == nil {
			return toolErr("实例服务不可用")
		}
		var nodeFilter uint
		if v, ok := args["nodeId"]; ok {
			nid, err := toUint(v)
			if err != nil {
				return toolErr("参数 nodeId 无效: " + err.Error())
			}
			nodeFilter = nid
		}
		var out []model.Instance
		for _, id := range p.ScopedInstanceIDs {
			inst, err := deps.Instance.GetByID(id)
			if err != nil {
				continue
			}
			if nodeFilter != 0 && inst.NodeID != nodeFilter {
				continue
			}
			out = append(out, *inst)
		}
		return toolOK(out)

	case "agent_get_instance":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		if err := service.ResolveAction(p, service.AgentActionGetInstance, id, 0); err != nil {
			return toolForbidden(err)
		}
		if deps.Instance == nil {
			return toolErr("实例服务不可用")
		}
		inst, err := deps.Instance.GetByID(id)
		if err != nil {
			return toolErr("实例不存在")
		}
		return toolOK(inst)

	case "agent_get_instance_metrics":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		if err := service.ResolveAction(p, service.AgentActionGetInstanceMetrics, id, 0); err != nil {
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
		if err := service.ResolveAction(p, service.AgentActionGetInstanceLogs, id, 0); err != nil {
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

	case "instance_start":
		return callLifecycle(ctx, deps, p, service.AgentActionInstanceStart, args, func(id uint) error {
			return deps.Instance.Start(id)
		})
	case "instance_stop":
		return callLifecycle(ctx, deps, p, service.AgentActionInstanceStop, args, func(id uint) error {
			return deps.Instance.Stop(id)
		})
	case "instance_restart":
		return callLifecycle(ctx, deps, p, service.AgentActionInstanceRestart, args, func(id uint) error {
			return deps.Instance.Restart(id)
		})

	case "node_maintenance_enter":
		return callMaintenance(deps, p, service.AgentActionNodeMaintenanceEnter, args, true)
	case "node_maintenance_leave":
		return callMaintenance(deps, p, service.AgentActionNodeMaintenanceLeave, args, false)

	default:
		return toolErr("未知工具: " + name + "（硬拒绝面操作不会注册为 tool）")
	}
}

func callLifecycle(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any, fn func(uint) error) ToolResult {
	_ = ctx
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if err := service.ResolveAction(p, action, id, 0); err != nil {
		return toolForbidden(err)
	}
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	if err := fn(id); err != nil {
		return toolErr(err.Error())
	}
	return toolOK(map[string]any{"ok": true})
}

func callMaintenance(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any, enabled bool) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if err := service.ResolveAction(p, action, 0, id); err != nil {
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
	msg := "操作被拒绝"
	if err != nil && !errors.Is(err, service.ErrAgentForbidden) {
		msg = err.Error()
	} else if errors.Is(err, service.ErrAgentForbidden) {
		msg = "写白名单/scope 不足或硬拒绝"
	}
	return toolErr(msg)
}
