package service

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 助手调用 MCP 工具的身份派生。
//
// ## 安全前提：绝不造「超级身份」
//
// MCP 的 `Authorize()` 只认 `AgentPrincipal`（capabilities + scope）。
// 助手面对的是人类用户，若为了「让助手能用全部工具」而造一个全能力、全 scope 的
// principal，就等于给任何能聊天的用户开了一条提权通道——他能通过助手执行
// 他自己本没有权限做的操作，而审批链路只看到「一个合法 principal」。
//
// 因此这里的做法是**降权派生**：把用户的 RBAC 权限节点翻译成 agent 能力，
// 把用户可见的实例/节点翻译成 scope。助手做不了用户本人做不了的事。
//
// ## 为什么用白名单映射而不是「有任意权限就给全部能力」
//
// 能力名与权限节点名并不一一对应（`node.manage` vs `node.operate`、
// `instance.operate` vs `instance.life`）。映射写松了会放开越权，
// 写松一处就是一个洞；因此**只列已知对应关系，未列出的一律不给**。

// agentCapabilitiesForPermissions 把 RBAC 权限节点映射为 agent 能力。
//
// 映射依据「该权限节点允许用户通过面板做这件事」，因此对应能力也必须给——
// 否则会出现「用户在面板能停实例、在助手里却不行」的不一致。
//
// 未列出的权限节点**不产生能力**（默认拒绝）。新增权限节点时若忘了加映射，
// 表现是「助手用不了某个工具」，属安全的一侧；反过来则会静默放开，那不可接受。
var agentCapabilitiesForPermissions = map[string][]string{
	// 实例
	"instance.read":             {AgentCapabilityInstanceRead, AgentCapabilityObservabilityRead},
	"instance.write":            {AgentCapabilityInstanceRead, AgentCapabilityInstanceWrite, AgentCapabilityInstanceConfigure},
	"instance.operate":          {AgentCapabilityInstanceRead, AgentCapabilityInstanceLife, AgentCapabilityInstanceCommand},
	"instance.create":           {AgentCapabilityInstanceProvision},
	"instance.delete":           {AgentCapabilityInstanceDestructive},
	"instance.launchspec.write": {AgentCapabilityInstanceConfigure},
	"instance.business.write":   {AgentCapabilityInstanceContent},

	// 节点
	"node.read":   {AgentCapabilityNodeRead, AgentCapabilityObservabilityRead},
	"node.manage": {AgentCapabilityNodeRead, AgentCapabilityNodeOperate},

	// 观测
	"log.read":     {AgentCapabilityObservabilityRead},
	"monitor.read": {AgentCapabilityObservabilityRead},
	"stats.read":   {AgentCapabilityObservabilityRead},

	// 内容运维（file.* 在 agent 能力目录里没有独立能力项：
	// 文件工具的 action 走 instance.content，故此处映射到它，不造不存在的能力名）
	"file.read":  {AgentCapabilityInstanceRead},
	"file.write": {AgentCapabilityInstanceContent},

	// Bot 与压测
	"bot.read":   {AgentCapabilityBotRead},
	"bot.manage": {AgentCapabilityBotRead, AgentCapabilityBotManage, AgentCapabilityBotLoad},

	// 拓扑与注册（agent 能力目录同样没有 network.*：按实例读/写收敛）
	"network.read":   {AgentCapabilityInstanceRead},
	"network.manage": {AgentCapabilityInstanceWrite},
}

// AssistantPrincipalForOwner 由用户 ID 派生 MCP 调用身份。
//
// 返回 nil 表示该用户不应获得任何工具能力（调用方据此不下发工具、也拒绝执行）。
//
// 平台管理员：能力全开、scope 覆盖全部实例与节点。
// 注意不能靠「capabilities 全开 + 空 scope」——空 scope 会让实例类工具
// **不可发现**（principalHasPotentialScope），表现为「管理员没有工具可看」，
// 那是配置错觉而非安全收益。因此这里显式枚举全部 id。
func AssistantPrincipalForOwner(authz *AuthzService, owner uint) (*AgentPrincipal, error) {
	if authz == nil || owner == 0 {
		return nil, nil
	}
	access, err := authz.LoadUserAccess(owner)
	if err != nil {
		// 装不出权限就等于没有权限：不能凭猜测放行。
		return nil, fmt.Errorf("加载用户权限失败: %w", err)
	}
	if access == nil {
		return nil, nil
	}

	caps := map[string]struct{}{}
	if access.IsPlatformAdmin {
		for _, list := range agentCapabilitiesForPermissions {
			for _, c := range list {
				caps[c] = struct{}{}
			}
		}
	} else {
		// UserAccess 只暴露 HasNode（不暴露节点集合）：逐项探测已知映射节点。
		// 这样映射表就是唯一真源——新增节点必须显式加进表里才会被授予能力。
		for node, grants := range agentCapabilitiesForPermissions {
			if !access.HasNode(node) {
				continue
			}
			for _, c := range grants {
				caps[c] = struct{}{}
			}
		}
	}
	if len(caps) == 0 {
		return nil, nil
	}
	capList := make([]string, 0, len(caps))
	for c := range caps {
		capList = append(capList, c)
	}

	// scope：实例按可访问组收敛；节点按用户可管理的节点收敛。
	//
	// 平台管理员：全部实例 + 全部节点（显式枚举，理由见函数注释）。
	instanceIDs := []uint{}
	nodeIDs := []uint{}

	if access.IsPlatformAdmin {
		var ids []uint
		if err := authz.db.Model(&model.Instance{}).Pluck("id", &ids).Error; err != nil {
			return nil, fmt.Errorf("查询实例列表失败: %w", err)
		}
		instanceIDs = ids
		var nids []uint
		if err := authz.db.Model(&model.Node{}).Pluck("id", &nids).Error; err != nil {
			return nil, fmt.Errorf("查询节点列表失败: %w", err)
		}
		nodeIDs = nids
	} else {
		ids, _, ierr := authz.AccessibleInstanceIDs(access)
		if ierr != nil {
			return nil, ierr
		}
		instanceIDs = ids
		// 非管理员的节点可见性来自 UserAccess.Nodes。
		// 该集合的键是**节点标识**（uuid 或名称，取决于授权来源），不是数字 id，
		// 因此必须回表解析——把字符串直接当 id 会得到 0，scope 静默变空。
		if len(access.Nodes) > 0 {
			keys := make([]string, 0, len(access.Nodes))
			for k := range access.Nodes {
				keys = append(keys, k)
			}
			var ids []uint
			if err := authz.db.Model(&model.Node{}).
				Where("uuid IN ? OR name IN ?", keys, keys).
				Pluck("id", &ids).Error; err != nil {
				return nil, fmt.Errorf("查询可访问节点失败: %w", err)
			}
			nodeIDs = ids
		}
		nodeIDs = compactNonZero(nodeIDs)
	}

	return &AgentPrincipal{
		Name: "assistant:" + fmt.Sprint(owner),
		// PolicyVersion 用当前策略版本：助手身份不走 Token 策略演进，
		// 但确认票据会记版本，写一个真实值以便区分「策略变更前的旧确认」。
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: instanceIDs,
		ScopedNodeIDs:     nodeIDs,
		Capabilities:      capList,
	}, nil
}

// compactNonZero 去掉 0 与重复，保持输入顺序稳定（便于断言与排障）。
func compactNonZero(in []uint) []uint {
	seen := make(map[uint]struct{}, len(in))
	out := in[:0]
	for _, v := range in {
		if v == 0 {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
