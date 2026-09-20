package mcp

import (
	"context"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 群组服 Network 软标签与代理注册（FR-032 / FR-335）的 MCP 工具：
// Networks 是非独占软标签，仅供分组/筛选/批量运维，不驱动真实路由（路由由 server_registrations 驱动）；
// 代理注册描述「哪个 proxy 聚合哪些 backend」，是群组拓扑图的边。
// 权限与实例分组同面：读走 instance.read、写走 instance.write。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "network_list",
				Description: "列出全部群组（Network 软标签）及其成员计数与健康分布（须 instance.read）",
				InputSchema: emptySchema(),
			},
			Action: service.AgentActionNetworkList,
			Exec:   execNetworkList,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "network_get",
				Description: "获取群组详情（含成员实例列表）（须 instance.read）",
				InputSchema: idSchema("群组 ID"),
			},
			Action: service.AgentActionNetworkRead,
			Exec:   execNetworkGet,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "network_create",
				Description: "创建群组（Network 软标签）（须 instance.write）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        map[string]any{"type": "string", "description": "群组名（未软删记录间唯一）"},
						"description": map[string]any{"type": "string", "description": "可选：描述"},
					},
					"required": []string{"name"},
				},
			},
			Action: service.AgentActionNetworkCreate,
			Exec:   execNetworkCreate,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "network_update",
				Description: "改群组名或描述（须 instance.write；字段缺省表示不变）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":          map[string]any{"type": "number", "description": "群组 ID"},
						"name":        map[string]any{"type": "string", "description": "可选：新群组名"},
						"description": map[string]any{"type": "string", "description": "可选：新描述"},
					},
					"required": []string{"id"},
				},
			},
			Action: service.AgentActionNetworkUpdate,
			Exec:   execNetworkUpdate,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "network_delete",
				Description: "删除群组（危险操作）；仅删除群组及其成员关系，不触及成员实例与代理注册（须 instance.write + 精确确认 confirmName）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":          map[string]any{"type": "number", "description": "群组 ID"},
						"confirmName": map[string]any{"type": "string", "description": "精确确认：必须等于目标群组名称"},
					},
					"required": []string{"id", "confirmName"},
				},
			},
			Action: service.AgentActionNetworkDelete,
			Exec:   execNetworkDelete,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "network_add_members",
				Description: "把实例加入群组（软标签，不改变实例归属或路由）（须 instance.write）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":          map[string]any{"type": "number", "description": "群组 ID"},
						"instanceIds": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "待加入的实例 ID 列表"},
					},
					"required": []string{"id", "instanceIds"},
				},
			},
			Action: service.AgentActionNetworkAddMembers,
			Exec:   execNetworkAddMembers,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "network_remove_member",
				Description: "把实例移出群组（须 instance.write）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":         map[string]any{"type": "number", "description": "群组 ID"},
						"instanceId": map[string]any{"type": "number", "description": "待移出的实例 ID"},
					},
					"required": []string{"id", "instanceId"},
				},
			},
			Action: service.AgentActionNetworkRemoveMember,
			Exec:   execNetworkRemoveMember,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "topology_get",
				Description: "读取全量群组拓扑：所有代理实例及其后端注册 + 各群组成员归属（一次聚合，供拓扑图渲染）（须 instance.read）",
				InputSchema: emptySchema(),
			},
			Action: service.AgentActionTopologyGet,
			Exec:   execTopologyGet,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "registration_list",
				Description: "列出某代理实例的后端注册关系（须 instance.read）",
				InputSchema: idSchema("代理实例 ID"),
			},
			Action: service.AgentActionRegistrationList,
			Exec:   execRegistrationList,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "registration_create",
				Description: "把一个后端实例注册进代理（代理须 role=proxy、后端须 role=backend）；注册后代理才可启动（须 instance.write）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":         map[string]any{"type": "number", "description": "代理实例 ID（role=proxy）"},
						"backendId":  map[string]any{"type": "number", "description": "后端实例 ID（role=backend）"},
						"alias":      map[string]any{"type": "string", "description": "可选：代理内别名（缺省用后端名）"},
						"priority":   map[string]any{"type": "number", "description": "可选：优先级（越小越优先，缺省 0）"},
						"forcedHost": map[string]any{"type": "string", "description": "可选：强制主机名"},
						"restricted": map[string]any{"type": "boolean", "description": "可选：是否受限（须显式连接才可进入）"},
					},
					"required": []string{"id", "backendId"},
				},
			},
			Action: service.AgentActionRegistrationCreate,
			Exec:   execRegistrationCreate,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "registration_delete",
				Description: "删除一条代理后端注册关系（须 instance.write）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":             map[string]any{"type": "number", "description": "代理实例 ID"},
						"registrationId": map[string]any{"type": "number", "description": "注册关系 ID"},
					},
					"required": []string{"id", "registrationId"},
				},
			},
			Action: service.AgentActionRegistrationDelete,
			Exec:   execRegistrationDelete,
		},
	)
}

func execNetworkList(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, _ map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Network == nil {
		return toolErr("群组服务不可用")
	}
	list, err := deps.Network.List()
	if err != nil {
		return toolErr("查询群组列表失败: " + err.Error())
	}
	return toolOK(list)
}

func execNetworkGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Network == nil {
		return toolErr("群组服务不可用")
	}
	detail, err := deps.Network.Get(id)
	if err != nil {
		return toolErr("查询群组详情失败: " + err.Error())
	}
	return toolOK(detail)
}

func execNetworkCreate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	name := stringArg(args, "name")
	if name == "" {
		return toolErr("缺少必填参数 name")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Network == nil {
		return toolErr("群组服务不可用")
	}
	net, err := deps.Network.Create(name, stringArg(args, "description"))
	if err != nil {
		return toolErr("创建群组失败: " + err.Error())
	}
	return toolOK(net)
}

func execNetworkUpdate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Network == nil {
		return toolErr("群组服务不可用")
	}
	var name, desc *string
	// 仅当调用方显式传了该字段才更新（缺省=不变）；空串也是有效的新值。
	if _, ok := args["name"]; ok {
		s := stringArg(args, "name")
		name = &s
	}
	if _, ok := args["description"]; ok {
		s := stringArg(args, "description")
		desc = &s
	}
	detail, err := deps.Network.Update(id, name, desc)
	if err != nil {
		return toolErr("更新群组失败: " + err.Error())
	}
	return toolOK(detail)
}

func execNetworkDelete(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	confirm := stringArg(args, "confirmName")
	if confirm == "" {
		return toolErr("缺少必填参数 confirmName（须精确等于目标群组名称）")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Network == nil {
		return toolErr("群组服务不可用")
	}
	// 先读回名称做精确确认，避免误删（与实例删除同风格的二次确认语义）。
	detail, err := deps.Network.Get(id)
	if err != nil {
		return toolErr("查询群组失败: " + err.Error())
	}
	if detail.Name != confirm {
		return toolErr("确认名称与目标群组不符：期望 " + detail.Name)
	}
	if err := deps.Network.Delete(id); err != nil {
		return toolErr("删除群组失败: " + err.Error())
	}
	return toolOK(map[string]any{"deleted": true, "id": id, "name": detail.Name})
}

func execNetworkAddMembers(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	ids, e := uintSliceArg(args, "instanceIds")
	if e != nil {
		return toolErr("参数 instanceIds 无效: " + e.Error())
	}
	if len(ids) == 0 {
		return toolErr("缺少必填参数 instanceIds")
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Network == nil {
		return toolErr("群组服务不可用")
	}
	added, detail, err := deps.Network.AddMembers(id, ids)
	if err != nil {
		return toolErr("添加群组成员失败: " + err.Error())
	}
	return toolOK(map[string]any{"added": added, "network": detail})
}

func execNetworkRemoveMember(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	instanceID, e := toUint(args["instanceId"])
	if e != nil {
		return toolErr("参数 instanceId 无效: " + e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Network == nil {
		return toolErr("群组服务不可用")
	}
	if err := deps.Network.RemoveMember(id, instanceID); err != nil {
		return toolErr("移出群组成员失败: " + err.Error())
	}
	return toolOK(map[string]any{"removed": true, "networkId": id, "instanceId": instanceID})
}

func execTopologyGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, _ map[string]any) ToolResult {
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Registration == nil || deps.Network == nil {
		return toolErr("拓扑服务不可用")
	}
	proxies, existing, err := deps.Registration.Topology()
	if err != nil {
		return toolErr("查询代理拓扑失败: " + err.Error())
	}
	networks, err := deps.Network.TopoBriefs(existing)
	if err != nil {
		return toolErr("查询群组归属失败: " + err.Error())
	}
	return toolOK(map[string]any{"proxies": proxies, "networks": networks})
}

func execRegistrationList(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Registration == nil {
		return toolErr("代理注册服务不可用")
	}
	list, err := deps.Registration.List(id)
	if err != nil {
		return toolErr("查询代理注册失败: " + err.Error())
	}
	return toolOK(list)
}

func execRegistrationCreate(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	backendID, e := toUint(args["backendId"])
	if e != nil {
		return toolErr("参数 backendId 无效: " + e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Registration == nil {
		return toolErr("代理注册服务不可用")
	}
	req := service.CreateRegistrationRequest{
		BackendID:  backendID,
		Alias:      stringArg(args, "alias"),
		ForcedHost: stringArg(args, "forcedHost"),
	}
	if v, ok := args["priority"]; ok {
		if n, e := toUint(v); e == nil {
			p := int(n)
			req.Priority = &p
		}
	}
	if v, ok := args["restricted"]; ok {
		if b, ok := v.(bool); ok {
			req.Restricted = &b
		}
	}
	view, err := deps.Registration.Create(id, req)
	if err != nil {
		return toolErr("创建代理注册失败: " + err.Error())
	}
	return toolOK(view)
}

func execRegistrationDelete(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	regID, e := toUint(args["registrationId"])
	if e != nil {
		return toolErr("参数 registrationId 无效: " + e.Error())
	}
	if _, err := service.CanDiscover(p, action); err != nil {
		return toolForbidden(err)
	}
	if deps.Registration == nil {
		return toolErr("代理注册服务不可用")
	}
	if err := deps.Registration.Delete(id, regID); err != nil {
		return toolErr("删除代理注册失败: " + err.Error())
	}
	return toolOK(map[string]any{"deleted": true, "proxyId": id, "registrationId": regID})
}
