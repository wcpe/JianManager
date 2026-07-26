package mcp

import (
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// pluginToolSpecs 插件域工具（FR-397）：只认既有制品 assetId，不接受任何文件字节。
var pluginToolSpecs = []toolSpec{
	{
		Def: ToolDef{
			Name:        "plugin_list",
			Description: "列出实例受控目录（plugins/mods/resourcepacks/datapacks）下的制品与启用状态",
			InputSchema: idSchema("实例 ID"),
		},
		Action: service.AgentActionPluginList,
	},
	{
		Def: ToolDef{
			Name:        "plugin_deploy_from_asset",
			Description: "把制品库中已存在的插件 jar（assetId）部署到实例；不接受文件字节，大文件请走传输票据上传后入库",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":        numberProp("实例 ID"),
					"assetId":   numberProp("制品库中的插件制品 ID"),
					"dir":       stringProp("可选：目标目录 plugins/mods/resourcepacks/datapacks，默认 plugins"),
					"overwrite": map[string]any{"type": "boolean", "description": "可选：是否覆盖同名文件，默认 false"},
				},
				"required": []string{"id", "assetId"},
			},
		},
		Action: service.AgentActionPluginDeployFromAsset,
	},
	{
		Def: ToolDef{
			Name:        "plugin_toggle",
			Description: "启用/禁用实例内某制品（经重命名 .disabled 后缀实现，不删除文件）",
			InputSchema: pluginNameSchema(nil),
		},
		Action: service.AgentActionPluginToggle,
	},
	{
		Def: ToolDef{
			Name:        "plugin_delete",
			Description: "删除实例内某制品（破坏性；必须提供与 name 完全一致的 confirmName）",
			InputSchema: pluginNameSchema(map[string]any{
				"confirmName": stringProp("再次输入与 name 完全一致的制品名以确认删除"),
			}, "confirmName"),
		},
		Action: service.AgentActionPluginDelete,
	},
}

// pluginNameSchema 生成「实例 ID + 制品名 + 目录」类工具的 InputSchema。
func pluginNameSchema(extra map[string]any, required ...string) map[string]any {
	props := map[string]any{
		"id":   numberProp("实例 ID"),
		"name": stringProp("制品展示名（含 .jar/.zip 后缀，不含 .disabled）"),
		"dir":  stringProp("可选：所在目录 plugins/mods/resourcepacks/datapacks，默认 plugins"),
	}
	for k, v := range extra {
		props[k] = v
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   append([]string{"id", "name"}, required...),
	}
}

// callPluginTool 分发插件域工具；未命中返回 false。
func callPluginTool(deps ToolDeps, p *service.AgentPrincipal, name, action string, args map[string]any) (ToolResult, bool) {
	switch name {
	case "plugin_list":
		return callPluginList(deps, p, action, args), true
	case "plugin_deploy_from_asset":
		return callPluginDeployFromAsset(deps, p, action, args), true
	case "plugin_toggle":
		return callPluginToggle(deps, p, action, args), true
	case "plugin_delete":
		return callPluginDelete(deps, p, action, args), true
	default:
		return ToolResult{}, false
	}
}

func callPluginList(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, denied := authorizeInstanceTool(deps, p, action, args)
	if denied != nil {
		return *denied
	}
	if deps.Plugin == nil {
		return toolErr("插件服务不可用")
	}
	list, err := deps.Plugin.List(id)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(list)
}

func callPluginDeployFromAsset(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, denied := authorizeInstanceTool(deps, p, action, args)
	if denied != nil {
		return *denied
	}
	assetID, err := optionalUintArg(args, "assetId")
	if err != nil {
		return toolErr(err.Error())
	}
	if assetID == 0 {
		return toolErr("缺少必填参数 assetId")
	}
	dir, err := optionalStringArg(args, "dir")
	if err != nil {
		return toolErr(err.Error())
	}
	overwrite, err := optionalBoolArg(args, "overwrite")
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Plugin == nil {
		return toolErr("插件服务不可用")
	}
	// 单实例单制品形态：scope 收敛到已授权的该实例，杜绝扇出到 scope 外目标。
	result, derr := deps.Plugin.BatchDeploy(service.PluginBatchDeployRequest{
		AssetIDs:    []uint{assetID},
		Target:      service.PluginBatchTarget{IDs: []uint{id}},
		Destination: dir,
		Overwrite:   overwrite,
	}, []uint{id}, true)
	if derr != nil {
		return toolErr(derr.Error())
	}
	if result.Failed > 0 {
		return toolErr("部署插件失败: " + firstDeployError(result))
	}
	if result.Succeeded == 0 {
		return toolErr("部署插件失败: 目标实例不可用或制品不存在")
	}
	return toolOK(map[string]any{"ok": true, "instanceId": id, "assetId": assetID})
}

// firstDeployError 取第一条失败原因，供收敛后的中文错误使用。
func firstDeployError(result *service.PluginBatchDeployResult) string {
	for _, item := range result.Results {
		if !item.OK && item.Error != "" {
			return item.Error
		}
	}
	return "未知原因"
}

func callPluginToggle(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, name, dir, denied := resolvePluginTarget(deps, p, action, args)
	if denied != nil {
		return *denied
	}
	if deps.Plugin == nil {
		return toolErr("插件服务不可用")
	}
	enabled, err := deps.Plugin.Toggle(id, dir, name)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(map[string]any{"ok": true, "name": name, "enabled": enabled})
}

func callPluginDelete(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, name, dir, denied := resolvePluginTarget(deps, p, action, args)
	if denied != nil {
		return *denied
	}
	confirmName, err := optionalStringArg(args, "confirmName")
	if err != nil {
		return toolErr(err.Error())
	}
	if cerr := requireExactConfirm("confirmName", name, confirmName); cerr != nil {
		return toolErr(cerr.Error())
	}
	if deps.Plugin == nil {
		return toolErr("插件服务不可用")
	}
	if derr := deps.Plugin.Delete(id, dir, name); derr != nil {
		return toolErr(derr.Error())
	}
	return toolOK(map[string]any{"ok": true, "name": name})
}

// resolvePluginTarget 完成「授权 → 取制品名与目录」，是插件域工具的公共前置。
func resolvePluginTarget(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) (uint, string, string, *ToolResult) {
	id, denied := authorizeInstanceTool(deps, p, action, args)
	if denied != nil {
		return 0, "", "", denied
	}
	name, err := requireStringArg(args, "name")
	if err != nil {
		res := toolErr(err.Error())
		return 0, "", "", &res
	}
	dir, err := optionalStringArg(args, "dir")
	if err != nil {
		res := toolErr(err.Error())
		return 0, "", "", &res
	}
	return id, name, dir, nil
}
