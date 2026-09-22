package mcp

import (
	"context"
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-468 二进制/Beacon 版本管理：读当前版本 + 受控升级 + 一级回滚。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "instance_binary_version_get",
				Description: "读取实例二进制/Beacon 版本（当前版本 + 可升级候选 + 可回滚性 + 漂移提示；须 instance.read）",
				InputSchema: idSchema("实例 ID"),
			},
			Action: service.AgentActionInstanceBinaryVersionGet,
			Exec:   execInstanceBinaryVersionGet,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_binary_upgrade",
				Description: "把实例升级到指定制品版本（须 instance.write；实例须已停止。制品为版本真源，升级前自动记录回滚点）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "number", "description": "实例 ID"},
						"assetId": map[string]any{"type": "number", "description": "目标制品（Asset）ID"},
					},
					"required": []string{"id", "assetId"},
				},
			},
			Action: service.AgentActionInstanceBinaryUpgrade,
			Exec:   execInstanceBinaryUpgrade,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_binary_rollback",
				Description: "回滚实例二进制到上一版本（须 instance.write；实例须已停止。语义对称：回滚本身也可再回滚）",
				InputSchema: idSchema("实例 ID"),
			},
			Action: service.AgentActionInstanceBinaryRollback,
			Exec:   execInstanceBinaryRollback,
		},
	)
}

func execInstanceBinaryVersionGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.BinaryVersion == nil {
		return toolErr("二进制版本服务不可用")
	}
	view, err := deps.BinaryVersion.View(id)
	if err != nil {
		return toolErr("查询二进制版本失败: " + err.Error())
	}
	return toolOK(view)
}

func execInstanceBinaryUpgrade(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	assetID, e := requireAssetID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, id); err != nil {
		return toolForbidden(err)
	}
	if deps.BinaryVersion == nil {
		return toolErr("二进制版本服务不可用")
	}
	res, err := deps.BinaryVersion.Upgrade(ctx, id, assetID, p.TokenID)
	if err != nil {
		return toolErr("升级失败: " + err.Error())
	}
	return toolOK(res)
}

func execInstanceBinaryRollback(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.BinaryVersion == nil {
		return toolErr("二进制版本服务不可用")
	}
	res, err := deps.BinaryVersion.Rollback(ctx, id, p.TokenID)
	if err != nil {
		return toolErr("回滚失败: " + err.Error())
	}
	return toolOK(res)
}

// requireAssetID 解析必填的 assetId 参数。
func requireAssetID(args map[string]any) (uint, error) {
	raw, ok := args["assetId"]
	if !ok {
		return 0, fmt.Errorf("缺少必填参数 assetId")
	}
	id, err := toUint(raw)
	if err != nil {
		return 0, fmt.Errorf("assetId %v", err)
	}
	if id == 0 {
		return 0, fmt.Errorf("assetId 须为正整数")
	}
	return id, nil
}
