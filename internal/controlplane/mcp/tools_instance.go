package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-396 实例生命周期扩展：搜索/环境/崩溃快照/命令/批量/强杀/删除。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "instance_search",
				Description: "在 Token scope 内搜索实例；支持 q/status 过滤与分页（pageSize≤100）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"q":        map[string]any{"type": "string", "description": "可选：按名称模糊搜索"},
						"status":   map[string]any{"type": "string", "description": "可选：按状态过滤（running/stopped/...）"},
						"nodeId":   map[string]any{"type": "number", "description": "可选：仅返回该节点上的实例"},
						"page":     map[string]any{"type": "number", "description": "页码，默认 1"},
						"pageSize": map[string]any{"type": "number", "description": "每页条数，默认 20，最大 100"},
					},
				},
			},
			Action: service.AgentActionInstanceSearch,
			Exec:   execInstanceSearch,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_get_env",
				Description: "获取实例环境变量视图（须 instance.read）",
				InputSchema: idSchema("实例 ID"),
			},
			Action: service.AgentActionInstanceGetEnv,
			Exec:   execInstanceGetEnv,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_list_crash_snapshots",
				Description: "列出实例崩溃快照元数据（须 observability.read；不含正文）",
				InputSchema: idSchema("实例 ID"),
			},
			Action: service.AgentActionInstanceListCrashSnapshots,
			Exec:   execInstanceListCrashSnapshots,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_send_command",
				Description: "向运行中实例下发控制台命令（须 instance.command）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "number", "description": "实例 ID"},
						"command": map[string]any{"type": "string", "description": "控制台命令文本"},
					},
					"required": []string{"id", "command"},
				},
			},
			Action: service.AgentActionInstanceSendCommand,
			Exec:   execInstanceSendCommand,
		},
		toolSpec{
			Def: ToolDef{
				Name: "instance_batch",
				Description: "批量 start/stop/restart（不含 kill/delete）。" +
					"目标列表逐一过 scope，任一越界整体拒绝，不部分执行",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"op": map[string]any{
							"type": "string", "description": "操作：start / stop / restart",
						},
						"ids": map[string]any{
							"type": "array", "items": map[string]any{"type": "number"},
							"description": "目标实例 ID 列表",
						},
					},
					"required": []string{"op", "ids"},
				},
			},
			Action: service.AgentActionInstanceBatch,
			Exec:   execInstanceBatch,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_kill",
				Description: "强制终止实例（须 instance.destructive + 精确确认 confirmInstanceName）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "number", "description": "实例 ID"},
						"confirmInstanceName": map[string]any{
							"type": "string", "description": "与实例当前名称精确一致的确认字符串（区分大小写）",
						},
					},
					"required": []string{"id", "confirmInstanceName"},
				},
			},
			Action:       service.AgentActionInstanceKill,
			ConfirmField: "confirmInstanceName",
			Exec:         execInstanceKill,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_delete",
				Description: "删除实例及工作目录（须 instance.destructive + 精确确认 confirmInstanceName）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "number", "description": "实例 ID"},
						"confirmInstanceName": map[string]any{
							"type": "string", "description": "与实例当前名称精确一致的确认字符串（区分大小写）",
						},
					},
					"required": []string{"id", "confirmInstanceName"},
				},
			},
			Action:       service.AgentActionInstanceDelete,
			ConfirmField: "confirmInstanceName",
			Exec:         execInstanceDelete,
		},
	)
}

func execInstanceSearch(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	var nodeFilter *uint
	if v, ok := args["nodeId"]; ok {
		nid, err := toUint(v)
		if err != nil {
			return toolErr("参数 nodeId 无效: " + err.Error())
		}
		nodeFilter = &nid
	}
	list, err := deps.Agent.ListAccessibleInstances(p, nodeFilter)
	if err != nil {
		return toolForbidden(err)
	}
	q := strings.TrimSpace(stringArg(args, "q"))
	status := strings.TrimSpace(stringArg(args, "status"))
	filtered := make([]model.Instance, 0, len(list))
	for _, inst := range list {
		if q != "" && !strings.Contains(strings.ToLower(inst.Name), strings.ToLower(q)) {
			continue
		}
		if status != "" && !strings.EqualFold(string(inst.Status), status) {
			continue
		}
		filtered = append(filtered, inst)
	}
	page, pageSize := normalizePage(args)
	total := len(filtered)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return toolOK(map[string]any{
		"items": filtered[start:end], "total": total, "page": page, "pageSize": pageSize,
	})
}

func execInstanceGetEnv(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	env, err := deps.Instance.GetInstanceEnv(id)
	if err != nil {
		return toolErr("查询环境变量失败: " + err.Error())
	}
	return toolOK(env)
}

func execInstanceListCrashSnapshots(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.Crash == nil {
		return toolErr("崩溃快照服务不可用")
	}
	list, err := deps.Crash.ListByInstance(id)
	if err != nil {
		return toolErr("查询崩溃快照失败: " + err.Error())
	}
	return toolOK(list)
}

func execInstanceSendCommand(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	command := strings.TrimSpace(stringArg(args, "command"))
	if command == "" {
		return toolErr("缺少必填参数 command")
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	_, inst, err := deps.Agent.AuthorizeInstanceAction(p, action, id)
	if err != nil {
		return toolForbidden(err)
	}
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	if err := deps.Instance.SendCommandForExpectedNode(id, command, inst.NodeID); err != nil {
		return toolErr(err.Error())
	}
	return toolOK(map[string]any{"ok": true})
}

func execInstanceBatch(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	op := strings.ToLower(strings.TrimSpace(stringArg(args, "op")))
	switch op {
	case "start", "stop", "restart":
	default:
		return toolErr("op 仅支持 start / stop / restart（破坏性操作不进批量）")
	}
	ids, err := toUintSlice(args["ids"])
	if err != nil || len(ids) == 0 {
		return toolErr("参数 ids 无效或为空")
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if deps.Batch == nil {
		return toolErr("批量服务不可用")
	}
	// 逐一过 scope：任一越界整体拒绝。
	for _, id := range ids {
		if _, _, e := deps.Agent.AuthorizeInstanceAction(p, action, id); e != nil {
			return toolForbidden(fmt.Errorf("目标实例 %d 不在 scope 内，批量整体拒绝", id))
		}
	}
	// 以可访问实例集合作为 scope 传入，防止 service 层扩大范围。
	accessible, err := deps.Agent.ListAccessibleInstances(p, nil)
	if err != nil {
		return toolForbidden(err)
	}
	scopeIDs := make([]uint, 0, len(accessible))
	for _, inst := range accessible {
		scopeIDs = append(scopeIDs, inst.ID)
	}
	result, err := deps.Batch.Batch(service.InstanceBatchRequest{
		Action: service.InstanceBatchAction(op),
		IDs:    ids,
	}, scopeIDs, true)
	if err != nil {
		return toolErr("批量操作失败: " + err.Error())
	}
	return toolOK(result)
}

func execInstanceKill(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	_, inst, err := deps.Agent.AuthorizeInstanceAction(p, action, id)
	if err != nil {
		return toolForbidden(err)
	}
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	if err := deps.Instance.KillForExpectedNode(id, inst.NodeID); err != nil {
		return toolErr(err.Error())
	}
	return toolOK(map[string]any{"ok": true})
}

func execInstanceDelete(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	_, inst, err := deps.Agent.AuthorizeInstanceAction(p, action, id)
	if err != nil {
		return toolForbidden(err)
	}
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	if err := deps.Instance.DeleteForExpectedNode(id, inst.NodeID); err != nil {
		return toolErr(err.Error())
	}
	return toolOK(map[string]any{"ok": true})
}

func normalizePage(args map[string]any) (page, pageSize int) {
	page, pageSize = 1, 20
	if v, ok := args["page"]; ok {
		if n, err := toUint(v); err == nil && n > 0 {
			page = int(n)
		}
	}
	if v, ok := args["pageSize"]; ok {
		if n, err := toUint(v); err == nil && n > 0 {
			pageSize = int(n)
		}
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func toUintSlice(v any) ([]uint, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("期望数组")
	}
	out := make([]uint, 0, len(arr))
	for _, item := range arr {
		n, err := toUint(item)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}
