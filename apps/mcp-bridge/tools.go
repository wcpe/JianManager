package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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

// emptySchema 无参数工具的 inputSchema。
func emptySchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// idSchema 需要 id 参数的工具。
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

// RegisteredTools 返回本 bridge 注册的全部工具（硬拒绝面永不出现）。
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

// CallTool 按名称分发工具调用。
func CallTool(ctx context.Context, client *AgentClient, name string, args map[string]any) ToolResult {
	var (
		raw json.RawMessage
		err error
	)
	switch name {
	case "agent_whoami":
		raw, err = client.Whoami(ctx)
	case "agent_list_nodes":
		raw, err = client.ListNodes(ctx)
	case "agent_list_instances":
		var nodeID uint
		if v, ok := args["nodeId"]; ok {
			nodeID, err = toUint(v)
			if err != nil {
				return toolErr("参数 nodeId 无效: " + err.Error())
			}
		}
		raw, err = client.ListInstances(ctx, nodeID)
	case "agent_get_instance":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		raw, err = client.GetInstance(ctx, id)
	case "agent_get_instance_metrics":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		raw, err = client.GetInstanceMetrics(ctx, id)
	case "instance_start":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		raw, err = client.InstanceStart(ctx, id)
	case "instance_stop":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		raw, err = client.InstanceStop(ctx, id)
	case "instance_restart":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		raw, err = client.InstanceRestart(ctx, id)
	case "node_maintenance_enter":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		raw, err = client.NodeMaintenanceEnter(ctx, id)
	case "node_maintenance_leave":
		id, e := requireID(args)
		if e != nil {
			return toolErr(e.Error())
		}
		raw, err = client.NodeMaintenanceLeave(ctx, id)
	default:
		return toolErr("未知工具: " + name + "（硬拒绝面操作不会注册为 tool）")
	}

	if err != nil {
		return toolErrFromAPI(err)
	}
	return toolOK(string(raw))
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

func toolOK(text string) ToolResult {
	if text == "" {
		text = "{}"
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

// toolErrFromAPI 将 CP 错误映射为 MCP isError + 中文 message。
func toolErrFromAPI(err error) ToolResult {
	if ae, ok := err.(*APIError); ok {
		// 403 与其它业务错误均用中文 message；isError=true。
		msg := ae.Message
		if ae.Code != "" && ae.Message != "" {
			msg = ae.Message
		}
		if ae.IsForbidden() && msg == "" {
			msg = "操作被拒绝"
		}
		return toolErr(msg)
	}
	return toolErr(err.Error())
}
