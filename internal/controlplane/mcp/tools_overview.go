package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 助手概览类只读工具（补回）。
//
// ## 为什么它们在 MCP 而不在助手侧
//
// 这三个能力（集群概览 / 任务列表 / 告警列表）原本是助手侧的「受限只读」实现：
// 只回聚合值、按调用者权限收敛，不暴露逐台明细。把工具真源统一到 MCP 后，
// 它们必须以 MCP 工具的形式存在——否则助手就失去了「看整体状况」的能力
// （MCP 那 108 个工具里没有等价项：instance_search 是逐台明细，不是聚合）。
//
// ## 与其它 MCP 工具的差异：按用户而非按 Token scope 收敛
//
// 其它 MCP 工具按 `AgentPrincipal.ScopedInstanceIDs` 收窄；这三个要按**调用者的
// RBAC** 收窄。助手调用时 principal 由 `AssistantPrincipalForOwner` 派生，
// owner 编码在 Name（`assistant:<id>`）里——这里解出来复用同一套 AuthzService 判定。
// 解不出 owner 时**拒绝**而不是放开：那意味着调用方不是助手派生身份，
// 此时按用户收敛的前提不成立。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "cluster_overview",
				Description: "查看集群概览：节点在线数、实例状态分布。只返回聚合值，不返回逐台明细；资源水位请用 node_get_metrics。",
				InputSchema: emptySchema(),
			},
			Action: service.AgentActionClusterOverview,
			Exec:   execClusterOverview,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "list_tasks",
				Description: "列出当前用户可见的最近任务（部署、备份、批量操作等）。按创建者收敛，看不到他人任务。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit": map[string]any{"type": "number", "description": "返回条数上限，默认 10，最大 50"},
						"state": map[string]any{"type": "string", "description": "按状态过滤，如 running / succeeded / failed；留空表示不限"},
					},
				},
			},
			Action: service.AgentActionListTasksBrief,
			Exec:   execListTasksBrief,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "list_alerts",
				Description: "列出当前用户可见的未解决告警（按时间倒序），可按级别过滤。",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit": map[string]any{"type": "number", "description": "返回条数上限，默认 10，最大 50"},
						"level": map[string]any{"type": "string", "description": "按级别过滤：info / warning / critical；留空表示不限"},
					},
				},
			},
			Action: service.AgentActionListAlertsBrief,
			Exec:   execListAlertsBrief,
		},
	)
}

// assistantOwnerOf 从 principal 解出「代表其权限的用户 id」。
//
// 约定见 AssistantPrincipalForOwner：Name = "assistant:<userId>"。
// 解不出就返回 0，调用方据此拒绝——按用户收敛的前提是知道是谁。
func assistantOwnerOf(p *service.AgentPrincipal) uint {
	if p == nil {
		return 0
	}
	raw := strings.TrimPrefix(p.Name, "assistant:")
	if raw == p.Name {
		return 0 // 没有前缀：不是助手派生身份
	}
	id, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}

// execClusterOverview 集群概览（聚合值，不含逐台明细）。
func execClusterOverview(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, _ map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.AssistantOverview == nil {
		return toolErr("概览查询服务不可用")
	}
	owner := assistantOwnerOf(p)
	if owner == 0 {
		return toolForbidden(fmt.Errorf("无法确定调用者身份"))
	}
	out, err := deps.AssistantOverview.ClusterOverview(owner)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(out)
}

// execListTasksBrief 任务摘要列表（按创建者/可见性收敛）。
func execListTasksBrief(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.AssistantOverview == nil {
		return toolErr("概览查询服务不可用")
	}
	owner := assistantOwnerOf(p)
	if owner == 0 {
		return toolForbidden(fmt.Errorf("无法确定调用者身份"))
	}
	limit := intArgClamped(args, "limit", 10, 50)
	out, err := deps.AssistantOverview.ListTasks(owner, limit, strings.TrimSpace(stringArg(args, "state")))
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(out)
}

// execListAlertsBrief 未解决告警摘要列表。
func execListAlertsBrief(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.AssistantOverview == nil {
		return toolErr("概览查询服务不可用")
	}
	owner := assistantOwnerOf(p)
	if owner == 0 {
		return toolForbidden(fmt.Errorf("无法确定调用者身份"))
	}
	limit := intArgClamped(args, "limit", 10, 50)
	out, err := deps.AssistantOverview.ListAlerts(owner, limit, strings.TrimSpace(stringArg(args, "level")))
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(out)
}

// intArgClamped 读整数参数并夹到 [1, max]；缺省用 dflt。
//
// 夹上界是必须的：模型可能给 10000，而那会让一次工具调用把整表拉进上下文。
func intArgClamped(args map[string]any, key string, dflt, max int) int {
	raw, ok := args[key]
	if !ok || raw == nil {
		return dflt
	}
	var v int
	switch n := raw.(type) {
	case float64:
		v = int(n)
	case int:
		v = n
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			v = parsed
		} else {
			return dflt
		}
	default:
		return dflt
	}
	if v <= 0 {
		return dflt
	}
	if v > max {
		return max
	}
	return v
}

// 编译期断言：这三个工具不依赖 Agent Token 的 scope（ResourceType=none）。
var _ = model.NodeStatusOnline
