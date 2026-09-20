package mcp

import (
	"context"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 实例分组（FR-165 / ADR-033）的 MCP 工具：只读树/成员查询 + 建组/改组/删组/加成员/移成员。
// 分组仅承载运维归类，不承载权限或配额；写操作须 instance.write。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "instance_group_tree",
				Description: "读取实例分组树（含各级成员），用于按大区/小区等结构批量运维（须 instance.read）",
				InputSchema: emptySchema(),
			},
			Action: service.AgentActionInstanceGroupList,
			Exec:   execInstanceGroupTree,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_group_members",
				Description: "读取指定分组的成员实例 ID 列表（含子树；须 instance.read）",
				InputSchema: idSchema("分组 ID"),
			},
			Action: service.AgentActionInstanceGroupRead,
			Exec:   execInstanceGroupMembers,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_group_create",
				Description: "创建实例分组，可选挂到父分组下（须 instance.write）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":     map[string]any{"type": "string", "description": "分组名"},
						"parentId": map[string]any{"type": "number", "description": "可选：父分组 ID；缺省为根分组"},
					},
					"required": []string{"name"},
				},
			},
			Action: service.AgentActionInstanceGroupCreate,
			Exec:   execInstanceGroupCreate,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_group_update",
				Description: "改分组名或移动分组到新父级（须 instance.write；parentId 传 0 表示移到根）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":       map[string]any{"type": "number", "description": "分组 ID"},
						"name":     map[string]any{"type": "string", "description": "可选：新名称"},
						"parentId": map[string]any{"type": "number", "description": "可选：新父分组 ID；0 表示移到根"},
					},
					"required": []string{"id"},
				},
			},
			Action: service.AgentActionInstanceGroupUpdate,
			Exec:   execInstanceGroupUpdate,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_group_delete",
				Description: "删除分组；非空分组默认拒绝（须 instance.write + 精确确认 confirmGroupName）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":               map[string]any{"type": "number", "description": "分组 ID"},
						"confirmGroupName": map[string]any{"type": "string", "description": "与分组当前名称精确一致的确认字符串"},
					},
					"required": []string{"id", "confirmGroupName"},
				},
			},
			Action:       service.AgentActionInstanceGroupDelete,
			ConfirmField: "confirmGroupName",
			Exec:         execInstanceGroupDelete,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_group_add_members",
				Description: "把实例加入分组（须 instance.write；目标实例须在 scope 内）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":          map[string]any{"type": "number", "description": "分组 ID"},
						"instanceIds": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "待加入的实例 ID 列表"},
					},
					"required": []string{"id", "instanceIds"},
				},
			},
			Action: service.AgentActionInstanceGroupAddMembers,
			Exec:   execInstanceGroupAddMembers,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_group_remove_members",
				Description: "把实例移出分组（须 instance.write）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":          map[string]any{"type": "number", "description": "分组 ID"},
						"instanceIds": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "待移出的实例 ID 列表"},
					},
					"required": []string{"id", "instanceIds"},
				},
			},
			Action: service.AgentActionInstanceGroupRemoveMembers,
			Exec:   execInstanceGroupRemoveMembers,
		},
	)
}

func execInstanceGroupTree(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, _ map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.InstanceGroup == nil {
		return toolErr("实例分组服务不可用")
	}
	tree, err := deps.InstanceGroup.Tree()
	if err != nil {
		return toolErr("查询分组树失败: " + err.Error())
	}
	return toolOK(tree)
}

func execInstanceGroupMembers(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.InstanceGroup == nil {
		return toolErr("实例分组服务不可用")
	}
	ids, err := deps.InstanceGroup.SubtreeInstanceIDs(id)
	if err != nil {
		return toolErr("查询分组成员失败: " + err.Error())
	}
	return toolOK(map[string]any{"groupId": id, "instanceIds": ids})
}

func execInstanceGroupCreate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	name := stringArg(args, "name")
	if name == "" {
		return toolErr("缺少必填参数 name")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.InstanceGroup == nil {
		return toolErr("实例分组服务不可用")
	}
	var parentID *uint
	if v, ok := args["parentId"]; ok {
		if n, e := toUint(v); e == nil && n > 0 {
			parentID = &n
		}
	}
	node, err := deps.InstanceGroup.Create(name, parentID)
	if err != nil {
		return toolErr("创建分组失败: " + err.Error())
	}
	return toolOK(node)
}

func execInstanceGroupUpdate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.InstanceGroup == nil {
		return toolErr("实例分组服务不可用")
	}
	var name *string
	if v, ok := args["name"].(string); ok && v != "" {
		name = &v
	}
	// parentID 三态：缺省=不动；0=移到根（显式 nil）；N=挂到该父级。
	var parent **uint
	if v, ok := args["parentId"]; ok {
		n, e := toUint(v)
		if e != nil {
			return toolErr("参数 parentId 无效")
		}
		if n == 0 {
			var rootParent *uint
			parent = &rootParent
		} else {
			pid := n
			newParent := &pid
			parent = &newParent
		}
	}
	node, err := deps.InstanceGroup.Update(id, name, parent)
	if err != nil {
		return toolErr("更新分组失败: " + err.Error())
	}
	return toolOK(node)
}

func execInstanceGroupDelete(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.InstanceGroup == nil {
		return toolErr("实例分组服务不可用")
	}
	if err := deps.InstanceGroup.Delete(id); err != nil {
		return toolErr("删除分组失败: " + err.Error())
	}
	return toolOK(map[string]any{"ok": true})
}

func execInstanceGroupAddMembers(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, ids, res := groupMemberArgs(deps, p, action, args)
	if res != nil {
		return *res
	}
	added, members, err := deps.InstanceGroup.AddMembers(id, ids)
	if err != nil {
		return toolErr("加入分组失败: " + err.Error())
	}
	return toolOK(map[string]any{"added": added, "members": members})
}

func execInstanceGroupRemoveMembers(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, ids, res := groupMemberArgs(deps, p, action, args)
	if res != nil {
		return *res
	}
	if err := deps.InstanceGroup.RemoveMembers(id, ids); err != nil {
		return toolErr("移出分组失败: " + err.Error())
	}
	return toolOK(map[string]any{"ok": true})
}

// groupMemberArgs 解析并授权分组成员变更参数；返回非 nil ToolResult 表示已失败。
func groupMemberArgs(deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) (uint, []uint, *ToolResult) {
	id, e := requireID(args)
	if e != nil {
		r := toolErr(e.Error())
		return 0, nil, &r
	}
	ids, err := toUintSlice(args["instanceIds"])
	if err != nil || len(ids) == 0 {
		r := toolErr("参数 instanceIds 无效或为空")
		return 0, nil, &r
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		r := toolForbidden(err)
		return 0, nil, &r
	}
	if deps.InstanceGroup == nil {
		r := toolErr("实例分组服务不可用")
		return 0, nil, &r
	}
	// 目标实例须在 token scope 内，避免借分组接口越权触碰他组实例。
	if deps.Agent != nil {
		for _, iid := range ids {
			if _, _, err := deps.Agent.AuthorizeInstanceAction(p, service.AgentActionGetInstance, iid); err != nil {
				r := toolForbidden(err)
				return 0, nil, &r
			}
		}
	}
	return id, ids, nil
}
