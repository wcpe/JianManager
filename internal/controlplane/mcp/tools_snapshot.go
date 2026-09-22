package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-466 实例整机快照：列表 / 创建 / 一键回滚。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "instance_snapshot_list",
				Description: "列出实例整机快照（时间点可回滚点；须 instance.read）",
				InputSchema: idSchema("实例 ID"),
			},
			Action: service.AgentActionInstanceSnapshotList,
			Exec:   execInstanceSnapshotList,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_snapshot_create",
				Description: "为实例创建整机快照（全量备份工作目录 + 采集二进制指纹；须 instance.write，异步任务）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":   map[string]any{"type": "number", "description": "实例 ID"},
						"name": map[string]any{"type": "string", "description": "可选：快照名称"},
					},
					"required": []string{"id"},
				},
			},
			Action: service.AgentActionInstanceSnapshotCreate,
			Exec:   execInstanceSnapshotCreate,
		},
		toolSpec{
			Def: ToolDef{
				Name: "instance_snapshot_rollback",
				Description: "一键回滚实例到指定快照（须 instance.write；会强制先建「回滚前」快照，运行中实例先停服，" +
					"回滚后停在 STOPPED 不自动拉起）。破坏性操作：必须显式传 confirmSnapshotId 或 confirmName 之一，值须与目标快照一致",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"snapshotId":        map[string]any{"type": "number", "description": "目标快照 ID"},
						"confirmSnapshotId": map[string]any{"type": "number", "description": "确认要素：须与 snapshotId 相同"},
						"confirmName":       map[string]any{"type": "string", "description": "确认要素：须与目标快照名完全相同"},
					},
					"required": []string{"snapshotId"},
				},
			},
			Action: service.AgentActionInstanceSnapshotRollback,
			Exec:   execInstanceSnapshotRollback,
		},
	)
}

func execInstanceSnapshotList(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.Snapshot == nil {
		return toolErr("快照服务不可用")
	}
	list, err := deps.Snapshot.ListByInstance(id)
	if err != nil {
		return toolErr("查询快照失败: " + err.Error())
	}
	return toolOK(list)
}

func execInstanceSnapshotCreate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
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
	if deps.Snapshot == nil {
		return toolErr("快照服务不可用")
	}
	name, _ := args["name"].(string)
	snap, err := deps.Snapshot.Create(id, name, service.SnapshotCreateOptions{
		Kind:        model.SnapshotKindManual,
		TriggeredBy: p.TokenID,
	})
	if err != nil {
		return toolErr("创建快照失败: " + err.Error())
	}
	return toolOK(snap)
}

func execInstanceSnapshotRollback(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	sid, e := requireUintArg(args, "snapshotId")
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if deps.Snapshot == nil {
		return toolErr("快照服务不可用")
	}
	target, err := deps.Snapshot.GetByID(sid)
	if err != nil {
		return toolErr("快照不存在: " + err.Error())
	}
	// 回滚是实例级动作：授权按快照归属实例判定（与 HTTP 侧同口径）。
	if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, target.InstanceID); err != nil {
		return toolForbidden(err)
	}
	// m-1：破坏性回滚要求显式确认要素，与 HTTP 侧 requireSnapshotRollbackConfirm 同口径，
	// 避免「脚本循环变量写错」直接覆盖生产实例数据。
	if err := confirmSnapshotRollbackArgs(args, target); err != nil {
		return toolErr(err.Error())
	}
	res, err := deps.Snapshot.Rollback(ctx, sid, p.TokenID)
	if err != nil {
		return toolErr("回滚失败: " + err.Error())
	}
	return toolOK(res)
}

// confirmSnapshotRollbackArgs 校验回滚的确认要素（m-1）。
func confirmSnapshotRollbackArgs(args map[string]any, target *model.InstanceSnapshot) error {
	confirmID, hasID := uintArg(args, "confirmSnapshotId")
	confirmName := strings.TrimSpace(stringArg(args, "confirmName"))
	if !hasID && confirmName == "" {
		return fmt.Errorf("回滚为破坏性操作：请显式传 confirmSnapshotId=%d 或 confirmName=%q 以确认目标快照",
			target.ID, target.Name)
	}
	if hasID && confirmID != target.ID {
		return fmt.Errorf("确认要素不匹配：confirmSnapshotId=%d 与目标快照 #%d 不一致", confirmID, target.ID)
	}
	if confirmName != "" && confirmName != target.Name {
		return fmt.Errorf("确认要素不匹配：confirmName 与目标快照名不一致（目标为 %q）", target.Name)
	}
	return nil
}

// uintArg 读取可选的无符号整数参数，返回 (值, 是否存在且合法)。
func uintArg(args map[string]any, key string) (uint, bool) {
	v, err := requireUintArg(args, key)
	if err != nil {
		return 0, false
	}
	return v, true
}

// requireUintArg 解析必填的无符号整数参数。
func requireUintArg(args map[string]any, key string) (uint, error) {
	raw, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("缺少必填参数 %s", key)
	}
	v, err := toUint(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %v", key, err)
	}
	if v == 0 {
		return 0, fmt.Errorf("%s 须为正整数", key)
	}
	return v, nil
}
