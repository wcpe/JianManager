package mcp

import (
	"context"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Beacon 拓扑拉取工具（FR-444，见 ADR-090）。
//
// 与 instance_group_* 系列同权限面（读 instance.read、写 instance.write）：
// 拉取改的是分组树与实例标签（归属类信息），不是实例内容。
//
// **可选协同，绝非依赖**：未配置 beacon.endpoint 时工具仍可见（保持工具集稳定），
// 但调用返回中文「未启用」提示而非静默成功——与 HTTP 端点 503 同口径。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "beacon_topology_status",
				Description: "查询 Beacon 拓扑拉取可用性（是否已配置协同端点、是否已开启拉取；须 instance.read）",
				InputSchema: emptySchema(),
			},
			Action: service.AgentActionBeaconTopologyStatus,
			Exec:   execBeaconTopologyStatus,
		},
		toolSpec{
			Def: ToolDef{
				Name: "beacon_topology_pull",
				Description: "手动触发从 Beacon 拉取区服结构树并映射为本地分组树（集群→大区→小区→实例），" +
					"同时补 region:/zone:/role: 标签（须 instance.write）。幂等：重复拉取不产生重复分组。" +
					"Beacon 不可达时返回错误且本地零改动",
				InputSchema: emptySchema(),
			},
			Action: service.AgentActionBeaconTopologyPull,
			Exec:   execBeaconTopologyPull,
		},
	)
}

func execBeaconTopologyStatus(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, _ map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.BeaconSync == nil {
		return toolOK(map[string]any{"configured": false, "enabled": false})
	}
	return toolOK(map[string]any{
		"configured": deps.BeaconSync.Configured(),
		"enabled":    deps.BeaconSync.Enabled(),
	})
}

func execBeaconTopologyPull(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, _ map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.BeaconSync == nil || !deps.BeaconSync.Enabled() {
		return toolErr("Beacon 拓扑拉取未启用（须配置 beacon.endpoint 并开启 beacon.pull-enabled）")
	}
	result, err := deps.BeaconSync.PullTopology(ctx, p.TokenID, "")
	if err != nil {
		return toolErr("Beacon 拓扑拉取失败: " + err.Error())
	}
	return toolOK(result)
}
