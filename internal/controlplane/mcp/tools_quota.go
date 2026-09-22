package mcp

import (
	"context"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-467 运行期配额：读配额与实时用量（含强制状态）。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "instance_quota_status",
				Description: "读取实例运行期配额与实时用量（限额来源 + CPU/RSS/磁盘用量 + 强制状态；须 observability.read）",
				InputSchema: idSchema("实例 ID"),
			},
			Action: service.AgentActionInstanceQuotaStatus,
			Exec:   execInstanceQuotaStatus,
		},
	)
}

func execInstanceQuotaStatus(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.Quota == nil {
		return toolErr("配额服务不可用")
	}
	status, err := deps.Quota.Status(id)
	if err != nil {
		return toolErr("查询配额失败: " + err.Error())
	}
	return toolOK(status)
}
