package service

// PermissionDomain 权限树的一个能力域。
type PermissionDomain struct {
	Domain string        `json:"domain"`
	Label  string        `json:"label"`
	Nodes  []PermNodeDef `json:"nodes"`
}

// PermNodeDef 目录中的权限节点定义（区别于 authz.PermissionNode 字符串常量）。
type PermNodeDef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Risk 高危能力（如启动规格、业务高危写）。
	Risk bool `json:"risk,omitempty"`
}

// PermissionCatalog 静态权限目录（FR-432）。API 与导航共用；实例级隔离仍由用户组决定。
var PermissionCatalog = []PermissionDomain{
	{
		Domain: "platform", Label: "平台",
		Nodes: []PermNodeDef{
			{ID: "user.read", Label: "用户读取"},
			{ID: "user.manage", Label: "用户管理"},
			{ID: "group.read", Label: "用户组读取"},
			{ID: "group.manage", Label: "用户组管理"},
			{ID: "group.member.write", Label: "组成员写入"},
			{ID: "group.quota.write", Label: "配额写入"},
			{ID: "node.read", Label: "节点读取"},
			{ID: "node.manage", Label: "节点管理"},
			{ID: "settings.read", Label: "设置读取"},
			{ID: "settings.write", Label: "设置写入"},
			{ID: "system.update", Label: "系统更新"},
			{ID: "system.db.browse", Label: "数据库浏览"},
			{ID: "license.read", Label: "开源许可"},
			{ID: "audit.read", Label: "审计读取"},
			{ID: "rbac.read", Label: "权限配置读取"},
			{ID: "rbac.manage", Label: "权限配置管理"},
		},
	},
	{
		Domain: "runtime", Label: "运行时",
		Nodes: []PermNodeDef{
			{ID: "instance.read", Label: "实例读取"},
			{ID: "instance.create", Label: "创建实例"},
			{ID: "instance.write", Label: "实例写入"},
			{ID: "instance.operate", Label: "实例启停"},
			{ID: "instance.delete", Label: "删除实例"},
			{ID: "instance.launchspec.write", Label: "启动规格 / 环境变量", Risk: true},
			{ID: "instance.business.write", Label: "业务高危写", Risk: true},
			{ID: "file.read", Label: "文件读取"},
			{ID: "file.write", Label: "文件写入"},
			{ID: "terminal.access", Label: "终端访问"},
			{ID: "bot.read", Label: "Bot 读取"},
			{ID: "bot.manage", Label: "Bot 管理"},
			{ID: "backup.read", Label: "备份读取"},
			{ID: "backup.write", Label: "备份写入"},
			{ID: "schedule.read", Label: "定时读取"},
			{ID: "schedule.write", Label: "定时写入"},
			{ID: "task.read", Label: "任务中心"},
			{ID: "task.manage", Label: "任务管理"},
		},
	},
	{
		Domain: "observability", Label: "观测",
		Nodes: []PermNodeDef{
			{ID: "monitor.read", Label: "监控"},
			{ID: "log.read", Label: "日志"},
			{ID: "stats.read", Label: "统计"},
			{ID: "alert.read", Label: "告警读取"},
			{ID: "alert.manage", Label: "告警管理"},
			{ID: "notification.read", Label: "通知中心"},
		},
	},
	{
		Domain: "distribution", Label: "客户端分发",
		Nodes: []PermNodeDef{
			{ID: "template.read", Label: "模板读取"},
			{ID: "template.manage", Label: "模板管理"},
			{ID: "channel.read", Label: "分发频道"},
			{ID: "channel.write", Label: "频道写入"},
			{ID: "dist.publish", Label: "版本发布"},
			{ID: "dist.ops.read", Label: "分发运维读取"},
			{ID: "dist.ops.write", Label: "分发安全处置"},
		},
	},
	{
		Domain: "agent", Label: "Agent",
		Nodes: []PermNodeDef{
			{ID: "agent.token.read", Label: "Token 读取"},
			{ID: "agent.token.manage", Label: "Token 管理"},
			{ID: "agent.mcp.read", Label: "MCP 会话"},
			{ID: "agent.calllog.read", Label: "调用流水"},
		},
	},
	{
		Domain: "workspace", Label: "工作区",
		Nodes: []PermNodeDef{
			{ID: "network.read", Label: "网络读取"},
			{ID: "network.manage", Label: "网络管理"},
			{ID: "player.read", Label: "玩家读取"},
			{ID: "player.manage", Label: "玩家管理"},
			{ID: "super.read", Label: "超级工作台"},
			{ID: "director.read", Label: "导播台"},
		},
	},
}

// AllPermissionNodes 目录中全部节点 id。
func AllPermissionNodes() []string {
	out := make([]string, 0, 64)
	for _, d := range PermissionCatalog {
		for _, n := range d.Nodes {
			out = append(out, n.ID)
		}
	}
	return out
}

// IsValidPermissionNode 判断节点是否在目录中。
func IsValidPermissionNode(id string) bool {
	for _, d := range PermissionCatalog {
		for _, n := range d.Nodes {
			if n.ID == id {
				return true
			}
		}
	}
	return false
}

// systemRoleNodes 预置角色模板授予的节点（FR-432 种子）。
var systemRoleNodes = map[string][]string{
	"platform_admin": AllPermissionNodes(),
	"group_admin": filterNodes(func(id string) bool {
		return hasPrefixAny(id,
			"instance.", "file.", "terminal.", "bot.", "backup.", "schedule.", "task.",
			"monitor.", "log.", "stats.", "alert.", "notification.", "player.", "network.",
		) || id == "super.read" || id == "director.read" || id == "group.read" ||
			id == "group.member.write" || id == "instance.launchspec.write"
	}),
	"group_operator": []string{
		"instance.read", "instance.write", "instance.operate", "instance.create", "instance.delete",
		"file.read", "file.write", "terminal.access", "bot.read", "bot.manage",
		"backup.read", "backup.write", "schedule.read", "schedule.write", "task.read", "task.manage",
		"monitor.read", "log.read", "stats.read", "alert.read", "notification.read",
		"player.read", "network.read", "super.read", "director.read", "group.read",
	},
	"group_viewer": []string{
		"instance.read", "file.read", "bot.read", "backup.read", "schedule.read", "task.read",
		"monitor.read", "log.read", "stats.read", "alert.read", "notification.read",
		"player.read", "network.read", "group.read",
	},
	"member": []string{
		"instance.read", "instance.write", "instance.operate",
		"file.read", "file.write", "terminal.access", "bot.read", "bot.manage",
		"backup.read", "schedule.read", "task.read",
		"monitor.read", "log.read", "notification.read", "player.read", "group.read",
	},
}

var systemRoleMeta = map[string]struct{ Name, Desc string }{
	"platform_admin": {Name: "平台管理员", Desc: "平台全量能力"},
	"group_admin":    {Name: "组管理员", Desc: "组内全权 + 管理成员"},
	"group_operator": {Name: "组运维", Desc: "实例运维操作，不改成员与高危启动规格"},
	"group_viewer":   {Name: "组只读分析", Desc: "只读实例/指标/日志"},
	"member":         {Name: "组成员", Desc: "兼容历史成员：组内实例操作"},
}

func SystemRoleSeed() map[string]struct {
	Name, Desc string
	Nodes      []string
} {
	out := make(map[string]struct {
		Name, Desc string
		Nodes      []string
	}, len(systemRoleMeta))
	for k, meta := range systemRoleMeta {
		nodes := append([]string(nil), systemRoleNodes[k]...)
		out[k] = struct {
			Name, Desc string
			Nodes      []string
		}{Name: meta.Name, Desc: meta.Desc, Nodes: nodes}
	}
	return out
}

func hasPrefixAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

func filterNodes(fn func(string) bool) []string {
	var out []string
	for _, id := range AllPermissionNodes() {
		if fn(id) {
			out = append(out, id)
		}
	}
	return out
}

// PermOverride 用户覆盖项。
type PermOverride struct {
	Node   string
	Effect string
}

// EffectivePermissionNodes 计算有效权限集合。
// 平台管理员（legacy role 10 或模板 platform_admin）短路全开；
// 否则 base=角色模板节点，再叠用户覆盖；deny 永胜。
func EffectivePermissionNodes(roleKey string, roleNodes []string, overrides []PermOverride, isPlatformAdmin bool) map[string]struct{} {
	if isPlatformAdmin || roleKey == "platform_admin" {
		all := make(map[string]struct{}, len(PermissionCatalog)*8)
		for _, id := range AllPermissionNodes() {
			all[id] = struct{}{}
		}
		return all
	}
	set := make(map[string]struct{}, len(roleNodes)+len(overrides))
	for _, n := range roleNodes {
		set[n] = struct{}{}
	}
	for _, o := range overrides {
		if o.Effect == "allow" {
			set[o.Node] = struct{}{}
		}
	}
	for _, o := range overrides {
		if o.Effect == "deny" {
			delete(set, o.Node)
		}
	}
	return set
}
