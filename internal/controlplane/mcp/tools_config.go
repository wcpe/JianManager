package mcp

import (
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// configToolSpecs 配置域工具（FR-397）：复用 ConfigService，版本机制由 service 内部维护。
var configToolSpecs = []toolSpec{
	{
		Def: ToolDef{
			Name:        "config_discover",
			Description: "递归发现实例工作目录下的全部配置文件",
			InputSchema: idSchema("实例 ID"),
		},
		Action: service.AgentActionConfigDiscover,
	},
	{
		Def: ToolDef{
			Name:        "config_read",
			Description: "读取实例配置文件正文与结构化字段（正文上限 512KiB）",
			InputSchema: instancePathSchema("配置文件路径（相对工作目录）", true, nil),
		},
		Action: service.AgentActionConfigRead,
	},
	{
		Def: ToolDef{
			Name:        "config_write_text",
			Description: "以整份文本写入实例配置文件并生成新版本（上限 512KiB）",
			InputSchema: instancePathSchema("配置文件路径（相对工作目录）", true, map[string]any{
				"content": stringProp("配置文件完整文本，上限 512KiB"),
				"message": stringProp("可选：本次修改说明"),
			}, "content"),
		},
		Action: service.AgentActionConfigWriteText,
	},
	{
		Def: ToolDef{
			Name:        "config_write_fields",
			Description: "按字段补丁修改实例配置（保留注释与顺序）并生成新版本",
			InputSchema: instancePathSchema("配置文件路径（相对工作目录）", true, map[string]any{
				"updates": map[string]any{
					"type":                 "object",
					"description":          "键→新值映射；键为 properties 平铺键 / yaml 点路径 / toml 顶层键",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"message": stringProp("可选：本次修改说明"),
			}, "updates"),
		},
		Action: service.AgentActionConfigWriteFields,
	},
	{
		Def: ToolDef{
			Name:        "config_cross_check",
			Description: "对给定配置内容做跨实例一致性校验（同节点其它实例最新版本参与比对）",
			InputSchema: instancePathSchema("配置文件路径（相对工作目录）", true,
				map[string]any{"content": stringProp("待校验的配置文本，上限 512KiB")}, "content"),
		},
		Action: service.AgentActionConfigCrossCheck,
	},
	{
		Def: ToolDef{
			Name:        "config_versions",
			Description: "列出实例某配置文件的历史版本",
			InputSchema: instancePathSchema("配置文件路径（相对工作目录）", true, nil),
		},
		Action: service.AgentActionConfigVersions,
	},
	{
		Def: ToolDef{
			Name:        "config_diff",
			Description: "比较配置文件两个历史版本；to 省略或为 0 表示与当前文件比较",
			InputSchema: instancePathSchema("配置文件路径（相对工作目录）", true, map[string]any{
				"from": numberProp("源版本 ID"),
				"to":   numberProp("目标版本 ID；省略或 0 表示与当前文件比较"),
			}, "from"),
		},
		Action: service.AgentActionConfigDiff,
	},
	{
		Def: ToolDef{
			Name:        "config_rollback",
			Description: "把实例配置文件回滚到指定历史版本（回滚写入同样生成新版本）",
			InputSchema: instancePathSchema("配置文件路径（相对工作目录）", true, map[string]any{
				"versionId": numberProp("要回滚到的版本 ID"),
				"message":   stringProp("可选：回滚说明"),
			}, "versionId"),
		},
		Action: service.AgentActionConfigRollback,
	},
}

// callConfigTool 分发配置域工具；未命中返回 false。
func callConfigTool(deps ToolDeps, p *service.AgentPrincipal, name, action string, args map[string]any) (ToolResult, bool) {
	switch name {
	case "config_discover":
		return callConfigDiscover(deps, p, action, args), true
	case "config_read":
		return callConfigRead(deps, p, action, args), true
	case "config_write_text":
		return callConfigWriteText(deps, p, action, args), true
	case "config_write_fields":
		return callConfigWriteFields(deps, p, action, args), true
	case "config_cross_check":
		return callConfigCrossCheck(deps, p, action, args), true
	case "config_versions":
		return callConfigVersions(deps, p, action, args), true
	case "config_diff":
		return callConfigDiff(deps, p, action, args), true
	case "config_rollback":
		return callConfigRollback(deps, p, action, args), true
	default:
		return ToolResult{}, false
	}
}

func callConfigDiscover(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, denied := authorizeInstanceTool(deps, p, action, args)
	if denied != nil {
		return *denied
	}
	if deps.Config == nil {
		return toolErr("配置服务不可用")
	}
	files, truncated, err := deps.Config.Discover(id)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(map[string]any{"files": files, "truncated": truncated})
}

func callConfigRead(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	if deps.Config == nil {
		return toolErr("配置服务不可用")
	}
	res, err := deps.Config.Read(id, path)
	if err != nil {
		return toolErr(err.Error())
	}
	if lerr := requireTextWithinLimit("配置正文", res.Content); lerr != nil {
		return toolErr(lerr.Error())
	}
	return toolOK(res)
}

func callConfigWriteText(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	content, err := requireStringArg(args, "content")
	if err != nil {
		return toolErr(err.Error())
	}
	if lerr := requireTextWithinLimit("content", content); lerr != nil {
		return toolErr(lerr.Error())
	}
	message, err := optionalStringArg(args, "message")
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Config == nil {
		return toolErr("配置服务不可用")
	}
	versionID, validation, werr := deps.Config.Write(id, path, content, message, 0, nil)
	if werr != nil {
		return toolErr(werr.Error())
	}
	return toolOK(map[string]any{"ok": true, "versionId": versionID, "validation": validation})
}

func callConfigWriteFields(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	updates, err := stringMapArg(args, "updates")
	if err != nil {
		return toolErr(err.Error())
	}
	message, err := optionalStringArg(args, "message")
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Config == nil {
		return toolErr("配置服务不可用")
	}
	versionID, validation, werr := deps.Config.WriteFields(id, path, updates, message, 0)
	if werr != nil {
		return toolErr(werr.Error())
	}
	return toolOK(map[string]any{"ok": true, "versionId": versionID, "validation": validation})
}

func callConfigCrossCheck(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	content, err := requireStringArg(args, "content")
	if err != nil {
		return toolErr(err.Error())
	}
	if lerr := requireTextWithinLimit("content", content); lerr != nil {
		return toolErr(lerr.Error())
	}
	if deps.Config == nil {
		return toolErr("配置服务不可用")
	}
	issues, cerr := deps.Config.CheckCrossFile(id, path, content)
	if cerr != nil {
		return toolErr(cerr.Error())
	}
	return toolOK(map[string]any{"issues": issues})
}

func callConfigVersions(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	if deps.Config == nil {
		return toolErr("配置服务不可用")
	}
	versions, err := deps.Config.Versions(id, path)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(versions)
}

func callConfigDiff(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	fromID, err := optionalUintArg(args, "from")
	if err != nil {
		return toolErr(err.Error())
	}
	if fromID == 0 {
		return toolErr("缺少必填参数 from（源版本 ID）")
	}
	toID, err := optionalUintArg(args, "to")
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Config == nil {
		return toolErr("配置服务不可用")
	}
	diff, derr := deps.Config.Diff(id, path, fromID, toID)
	if derr != nil {
		return toolErr(derr.Error())
	}
	return toolOK(diff)
}

func callConfigRollback(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	versionID, err := optionalUintArg(args, "versionId")
	if err != nil {
		return toolErr(err.Error())
	}
	if versionID == 0 {
		return toolErr("缺少必填参数 versionId")
	}
	message, err := optionalStringArg(args, "message")
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Config == nil {
		return toolErr("配置服务不可用")
	}
	newVersionID, rerr := deps.Config.Rollback(id, path, versionID, message, 0)
	if rerr != nil {
		return toolErr(rerr.Error())
	}
	return toolOK(map[string]any{"ok": true, "versionId": newVersionID})
}
