package mcp

import (
	"context"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-396 节点域工具：详情/指标/Docker/排空/归档列表与清理。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "node_get",
				Description: "获取指定节点详情（须在 scope 内且具备 node.read）",
				InputSchema: idSchema("节点 ID"),
			},
			Action: service.AgentActionNodeGet,
			Exec:   execNodeGet,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "node_get_metrics",
				Description: "获取指定节点实时指标（须在 scope 内且具备 observability.read）",
				InputSchema: idSchema("节点 ID"),
			},
			Action: service.AgentActionNodeGetMetrics,
			Exec:   execNodeGetMetrics,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "node_check_docker",
				Description: "探测节点 Docker 守护进程可用性（须在 scope 内且具备 node.read）",
				InputSchema: idSchema("节点 ID"),
			},
			Action: service.AgentActionNodeCheckDocker,
			Exec:   execNodeCheckDocker,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "node_drain",
				Description: "排空节点：停止其上全部实例并进入维护（须具备 node.operate）",
				InputSchema: idSchema("节点 ID"),
			},
			Action: service.AgentActionNodeDrain,
			Exec:   execNodeDrain,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "node_list_archived",
				Description: "列出 Token scope 内的已归档（下线）节点",
				InputSchema: emptySchema(),
			},
			Action: service.AgentActionNodeListArchived,
			Exec:   execNodeListArchived,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "node_purge_archived",
				Description: "彻底清理已归档节点（须 node.destructive + 精确确认 confirmNodeName）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "number", "description": "节点 ID"},
						"confirmNodeName": map[string]any{
							"type": "string", "description": "与节点当前名称精确一致的确认字符串（区分大小写）",
						},
						"force": map[string]any{
							"type": "boolean", "description": "有残留实例记录时是否级联硬删（默认 false）",
						},
					},
					"required": []string{"id", "confirmNodeName"},
				},
			},
			Action:       service.AgentActionNodePurgeArchived,
			ConfirmField: "confirmNodeName",
			Exec:         execNodePurgeArchived,
		},
	)
}

func execNodeGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	n, err := deps.Node.GetByID(id)
	if err != nil {
		return toolErr("查询节点失败: " + err.Error())
	}
	return toolOK(n)
}

func execNodeGetMetrics(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	m, err := deps.Node.GetMetrics(id)
	if err != nil {
		return toolErr("查询节点指标失败: " + err.Error())
	}
	return toolOK(m)
}

func execNodeCheckDocker(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.Docker == nil {
		return toolErr("Docker 服务不可用")
	}
	r, err := deps.Docker.CheckDocker(id)
	if err != nil {
		return toolErr("探测 Docker 失败: " + err.Error())
	}
	return toolOK(r)
}

func execNodeDrain(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	r, err := deps.Node.Drain(id)
	if err != nil {
		return toolErr("排空失败: " + err.Error())
	}
	return toolOK(r)
}

func execNodeListArchived(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, _ map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Node == nil {
		return toolErr("节点服务不可用")
	}
	all, err := deps.Node.ListArchived()
	if err != nil {
		return toolErr("查询归档节点失败: " + err.Error())
	}
	// 过滤到 scope 内节点。
	out := make([]service.ArchivedNode, 0, len(all))
	for _, n := range all {
		if service.PrincipalCanAccessNode(p, n.ID) {
			out = append(out, n)
		}
	}
	return toolOK(out)
}

func execNodePurgeArchived(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	force, _ := args["force"].(bool)
	r, err := deps.Node.Purge(id, force)
	if err != nil {
		return toolErr("清理归档节点失败: " + err.Error())
	}
	return toolOK(r)
}
