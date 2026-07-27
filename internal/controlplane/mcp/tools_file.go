package mcp

import (
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// fileToolSpecs 文件域工具（FR-397）：复用 FileService / FileVersionService，不复制策略。
var fileToolSpecs = []toolSpec{
	{
		Def: ToolDef{
			Name:        "file_list",
			Description: "列出实例工作目录下的文件与元数据（须具备 instance.read 能力）",
			InputSchema: instancePathSchema("相对工作目录的目录路径，留空为根目录", false, nil),
		},
		Action: service.AgentActionFileList,
	},
	{
		Def: ToolDef{
			Name:        "file_check_access",
			Description: "探测实例内某路径是否存在、可读、可写（写前预检）",
			InputSchema: instancePathSchema("相对工作目录的路径", true, nil),
		},
		Action: service.AgentActionFileCheckAccess,
	},
	{
		Def: ToolDef{
			Name:        "file_read_text",
			Description: "读取实例内文本文件（上限 512KiB；二进制或超限须改用传输票据下载）",
			InputSchema: instancePathSchema("相对工作目录的文件路径", true, nil),
		},
		Action: service.AgentActionFileReadText,
	},
	{
		Def: ToolDef{
			Name:        "file_write_text",
			Description: "写入实例内文本文件（上限 512KiB；覆盖已存在文件前自动改前快照）",
			InputSchema: instancePathSchema("相对工作目录的文件路径", true,
				map[string]any{"content": stringProp("文件文本内容，上限 512KiB")}, "content"),
		},
		Action: service.AgentActionFileWriteText,
	},
	{
		Def: ToolDef{
			Name:        "file_rename",
			Description: "重命名或移动实例内文件/目录",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      numberProp("实例 ID"),
					"oldPath": stringProp("原路径（相对工作目录）"),
					"newPath": stringProp("新路径（相对工作目录）"),
				},
				"required": []string{"id", "oldPath", "newPath"},
			},
		},
		Action: service.AgentActionFileRename,
	},
	{
		Def: ToolDef{
			Name:        "file_chmod",
			Description: "修改实例内单个路径的权限（非递归）",
			InputSchema: instancePathSchema("相对工作目录的路径", true,
				map[string]any{"mode": stringProp("八进制权限如 644；留空表示保证属主可读写")}),
		},
		Action: service.AgentActionFileChmod,
	},
	{
		Def: ToolDef{
			Name:        "file_delete",
			Description: "删除实例内文件（破坏性；必须提供与 path 完全一致的 confirmPath）",
			InputSchema: instancePathSchema("要删除的路径（相对工作目录）", true,
				map[string]any{"confirmPath": stringProp("再次输入与 path 完全一致的路径以确认删除")}, "confirmPath"),
		},
		Action: service.AgentActionFileDelete,
	},
	{
		Def: ToolDef{
			Name:        "file_versions",
			Description: "列出实例内某文件的历史版本（改前快照与回滚记录）",
			InputSchema: instancePathSchema("相对工作目录的文件路径", true, nil),
		},
		Action: service.AgentActionFileVersions,
	},
	{
		Def: ToolDef{
			Name:        "file_diff",
			Description: "比较文件两个历史版本；to 省略或为 0 表示与当前文件比较",
			InputSchema: instancePathSchema("相对工作目录的文件路径", true, map[string]any{
				"from": numberProp("源版本 ID"),
				"to":   numberProp("目标版本 ID；省略或 0 表示与当前文件比较"),
			}, "from"),
		},
		Action: service.AgentActionFileDiff,
	},
	{
		Def: ToolDef{
			Name:        "file_rollback",
			Description: "把实例内文件回滚到指定历史版本（回滚前自动快照当前内容）",
			InputSchema: instancePathSchema("相对工作目录的文件路径", true,
				map[string]any{"versionId": numberProp("要回滚到的版本 ID")}, "versionId"),
		},
		Action: service.AgentActionFileRollback,
	},
	{
		Def: ToolDef{
			Name:        "file_issue_transfer_ticket",
			Description: "签发 5 分钟单用途流式传输票据，用于大文件上传/下载（不经 MCP 传字节）",
			InputSchema: instancePathSchema("传输目标路径（相对工作目录）", true,
				map[string]any{"direction": stringProp("传输方向：upload 或 download")}, "direction"),
		},
		Action: service.AgentActionFileIssueTransferTicket,
	},
}

// callFileTool 分发文件域工具；未命中返回 false 交由上层继续匹配。
func callFileTool(deps ToolDeps, p *service.AgentPrincipal, name, action string, args map[string]any) (ToolResult, bool) {
	switch name {
	case "file_list":
		return callFileList(deps, p, action, args), true
	case "file_check_access":
		return callFileCheckAccess(deps, p, action, args), true
	case "file_read_text":
		return callFileReadText(deps, p, action, args), true
	case "file_write_text":
		return callFileWriteText(deps, p, action, args), true
	case "file_rename":
		return callFileRename(deps, p, action, args), true
	case "file_chmod":
		return callFileChmod(deps, p, action, args), true
	case "file_delete":
		return callFileDelete(deps, p, action, args), true
	case "file_versions":
		return callFileVersions(deps, p, action, args), true
	case "file_diff":
		return callFileDiff(deps, p, action, args), true
	case "file_rollback":
		return callFileRollback(deps, p, action, args), true
	case "file_issue_transfer_ticket":
		return callFileIssueTransferTicket(deps, p, action, args), true
	default:
		return ToolResult{}, false
	}
}

// resolveInstancePath 完成「授权 → 取路径 → 路径校验」三步，是文件域工具的公共前置。
func resolveInstancePath(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any, pathRequired bool) (uint, string, *ToolResult) {
	id, denied := authorizeInstanceTool(deps, p, action, args)
	if denied != nil {
		return 0, "", denied
	}
	var path string
	var err error
	if pathRequired {
		path, err = requireStringArg(args, "path")
	} else {
		path, err = optionalStringArg(args, "path")
	}
	if err != nil {
		res := toolErr(err.Error())
		return 0, "", &res
	}
	if pathRequired {
		if verr := service.ValidateNonRootInstancePath(path); verr != nil {
			res := toolErr(verr.Error())
			return 0, "", &res
		}
	} else if path != "" {
		if verr := service.ValidateInstancePath(path); verr != nil {
			res := toolErr(verr.Error())
			return 0, "", &res
		}
	}
	return id, path, nil
}

func callFileList(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, false)
	if denied != nil {
		return *denied
	}
	if deps.File == nil {
		return toolErr("文件服务不可用")
	}
	files, err := deps.File.ListFiles(id, path)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(files)
}

func callFileCheckAccess(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	if deps.File == nil {
		return toolErr("文件服务不可用")
	}
	access, err := deps.File.CheckPathAccess(id, path)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(access)
}

func callFileReadText(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	if deps.File == nil {
		return toolErr("文件服务不可用")
	}
	content, err := deps.File.ReadFile(id, path)
	if err != nil {
		return toolErr(err.Error())
	}
	return buildReadTextResult(path, content)
}

func callFileWriteText(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.File == nil {
		return toolErr("文件服务不可用")
	}
	// FR-051 语义：覆盖已存在文件前落改前快照（文件不存在自动跳过）。
	if deps.FileVersion != nil {
		if serr := deps.FileVersion.SnapshotBeforeWrite(id, path, 0); serr != nil {
			return toolErr(serr.Error())
		}
	}
	if werr := deps.File.WriteFile(id, path, []byte(content)); werr != nil {
		return toolErr(werr.Error())
	}
	return toolOK(map[string]any{"ok": true, "path": path, "size": len(content)})
}

func callFileRename(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, denied := authorizeInstanceTool(deps, p, action, args)
	if denied != nil {
		return *denied
	}
	oldPath, err := requireStringArg(args, "oldPath")
	if err != nil {
		return toolErr(err.Error())
	}
	newPath, err := requireStringArg(args, "newPath")
	if err != nil {
		return toolErr(err.Error())
	}
	for _, path := range []string{oldPath, newPath} {
		if verr := service.ValidateNonRootInstancePath(path); verr != nil {
			return toolErr(verr.Error())
		}
	}
	if deps.File == nil {
		return toolErr("文件服务不可用")
	}
	if rerr := deps.File.RenameFile(id, oldPath, newPath); rerr != nil {
		return toolErr(rerr.Error())
	}
	return toolOK(map[string]any{"ok": true, "oldPath": oldPath, "newPath": newPath})
}

func callFileChmod(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	mode, err := optionalStringArg(args, "mode")
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.File == nil {
		return toolErr("文件服务不可用")
	}
	modeOctal, cerr := deps.File.ChmodPath(id, path, mode)
	if cerr != nil {
		return toolErr(cerr.Error())
	}
	return toolOK(map[string]any{"ok": true, "path": path, "modeOctal": modeOctal})
}

func callFileDelete(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	confirmPath, err := optionalStringArg(args, "confirmPath")
	if err != nil {
		return toolErr(err.Error())
	}
	if cerr := requireExactConfirm("confirmPath", path, confirmPath); cerr != nil {
		return toolErr(cerr.Error())
	}
	if deps.File == nil {
		return toolErr("文件服务不可用")
	}
	if derr := deps.File.DeleteFile(id, path); derr != nil {
		return toolErr(derr.Error())
	}
	return toolOK(map[string]any{"ok": true, "path": path})
}

func callFileVersions(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	if deps.FileVersion == nil {
		return toolErr("文件版本服务不可用")
	}
	versions, err := deps.FileVersion.Versions(id, path)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolOK(versions)
}

func callFileDiff(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.FileVersion == nil {
		return toolErr("文件版本服务不可用")
	}
	diff, derr := deps.FileVersion.Diff(id, path, fromID, toID)
	if derr != nil {
		return toolErr(derr.Error())
	}
	return toolOK(diff)
}

func callFileRollback(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.FileVersion == nil {
		return toolErr("文件版本服务不可用")
	}
	newVersionID, rerr := deps.FileVersion.Rollback(id, path, versionID, 0)
	if rerr != nil {
		return toolErr(rerr.Error())
	}
	return toolOK(map[string]any{"ok": true, "path": path, "versionId": newVersionID})
}

func callFileIssueTransferTicket(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, path, denied := resolveInstancePath(deps, p, action, args, true)
	if denied != nil {
		return *denied
	}
	direction, err := requireStringArg(args, "direction")
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Transfer == nil {
		return toolErr("传输票据功能不可用（服务端未配置票据密钥）")
	}
	ticket, expiresAt, ierr := deps.Transfer.Issue(p, id, direction, path)
	if ierr != nil {
		return toolForbidden(ierr)
	}
	return toolOK(map[string]any{
		"ticket":           ticket,
		"direction":        direction,
		"path":             path,
		"expiresAt":        expiresAt,
		"uploadEndpoint":   "PUT /api/v1/agent-transfer/upload?ticket=<ticket>",
		"downloadEndpoint": "GET /api/v1/agent-transfer/download?ticket=<ticket>",
	})
}
