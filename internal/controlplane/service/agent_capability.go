package service

import (
	"fmt"
	"sort"
)

// Agent 策略版本与 V2 能力常量（FR-395 / ADR-080）。
const (
	AgentPolicyVersionV1 = 1
	AgentPolicyVersionV2 = 2

	AgentCapabilityNodeRead            = "node.read"
	AgentCapabilityNodeOperate         = "node.operate"
	AgentCapabilityNodeDestructive     = "node.destructive"
	AgentCapabilityInstanceRead        = "instance.read"
	AgentCapabilityInstanceLife        = "instance.life"
	AgentCapabilityInstanceCommand     = "instance.command"
	AgentCapabilityInstanceProvision   = "instance.provision"
	AgentCapabilityInstanceConfigure   = "instance.configure"
	AgentCapabilityInstanceContent     = "instance.content"
	AgentCapabilityInstanceDestructive = "instance.destructive"
	AgentCapabilityBotRead             = "bot.read"
	AgentCapabilityBotManage           = "bot.manage"
	AgentCapabilityBotLoad             = "bot.load"
	AgentCapabilityObservabilityRead   = "observability.read"

	// V1 流水兼容标签（非授权来源）。
	AgentLegacyCapabilityRead            = "legacy.read"
	AgentLegacyCapabilityInstanceLife    = "legacy.instance.life"
	AgentLegacyCapabilityNodeMaintenance = "legacy.node.maintenance"

	AgentResourceNone     = "none"
	AgentResourceNode     = "node"
	AgentResourceInstance = "instance"

	AgentOperationRead        = "read"
	AgentOperationWrite       = "write"
	AgentOperationDestructive = "destructive"
)

// AgentActionDescriptor 统一 action 目录条目。
type AgentActionDescriptor struct {
	Action          string
	V2Capability    string
	ResourceType    string
	Operation       string
	V1Allowed       bool
	V1WriteAllow    string
	HTTPMethod      string
	HTTPPath        string
	HTTPInContract  bool
	RequiresConfirm bool // FR-396：destructive 操作须服务端精确确认参数
}

// AgentAuthorization 授权结果；Capability 供调用流水记录。
type AgentAuthorization struct {
	Capability string
}

// AgentTrustedTarget 由 CP 可信数据解析后的目标；客户端不得伪造归属。
type AgentTrustedTarget struct {
	ResourceType string
	InstanceID   uint
	NodeID       uint
}

var agentKnownCapabilities = map[string]struct{}{
	AgentCapabilityNodeRead:            {},
	AgentCapabilityNodeOperate:         {},
	AgentCapabilityNodeDestructive:     {},
	AgentCapabilityInstanceRead:        {},
	AgentCapabilityInstanceLife:        {},
	AgentCapabilityInstanceCommand:     {},
	AgentCapabilityInstanceProvision:   {},
	AgentCapabilityInstanceConfigure:   {},
	AgentCapabilityInstanceContent:     {},
	AgentCapabilityInstanceDestructive: {},
	AgentCapabilityBotRead:             {},
	AgentCapabilityBotManage:           {},
	AgentCapabilityBotLoad:             {},
	AgentCapabilityObservabilityRead:   {},
}

// agentActionCatalog 是 action→capability→scope 的唯一真源。
var agentActionCatalog = map[string]AgentActionDescriptor{
	AgentActionWhoami: {
		Action: AgentActionWhoami, ResourceType: AgentResourceNone, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/whoami", HTTPInContract: true,
	},
	AgentActionListNodes: {
		Action: AgentActionListNodes, V2Capability: AgentCapabilityNodeRead,
		ResourceType: AgentResourceNode, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/nodes", HTTPInContract: true,
	},
	AgentActionListInstances: {
		Action: AgentActionListInstances, V2Capability: AgentCapabilityInstanceRead,
		ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/instances", HTTPInContract: true,
	},
	AgentActionGetInstance: {
		Action: AgentActionGetInstance, V2Capability: AgentCapabilityInstanceRead,
		ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/instances/:id", HTTPInContract: true,
	},
	AgentActionGetInstanceMetrics: {
		Action: AgentActionGetInstanceMetrics, V2Capability: AgentCapabilityObservabilityRead,
		ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/instances/:id/metrics", HTTPInContract: true,
	},
	AgentActionGetInstanceLogs: {
		Action: AgentActionGetInstanceLogs, V2Capability: AgentCapabilityObservabilityRead,
		ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		V1Allowed: true, // MCP 专属，不进 HTTP 契约投影
	},
	AgentActionInstanceStart: {
		Action: AgentActionInstanceStart, V2Capability: AgentCapabilityInstanceLife,
		ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteInstanceLife,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/instances/:id/start", HTTPInContract: true,
	},
	AgentActionInstanceStop: {
		Action: AgentActionInstanceStop, V2Capability: AgentCapabilityInstanceLife,
		ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteInstanceLife,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/instances/:id/stop", HTTPInContract: true,
	},
	AgentActionInstanceRestart: {
		Action: AgentActionInstanceRestart, V2Capability: AgentCapabilityInstanceLife,
		ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteInstanceLife,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/instances/:id/restart", HTTPInContract: true,
	},
	AgentActionNodeMaintenanceEnter: {
		Action: AgentActionNodeMaintenanceEnter, V2Capability: AgentCapabilityNodeOperate,
		ResourceType: AgentResourceNode, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteNodeMaintenance,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/nodes/:id/maintenance/enter", HTTPInContract: true,
	},
	AgentActionNodeMaintenanceLeave: {
		Action: AgentActionNodeMaintenanceLeave, V2Capability: AgentCapabilityNodeOperate,
		ResourceType: AgentResourceNode, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteNodeMaintenance,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/nodes/:id/maintenance/leave", HTTPInContract: true,
	},
}

// fr396DomainActions 是 FR-396 节点/实例扩展 action，经 init 注入 catalog，
// 保持 map 字面量本体不被并行分支争用（与 FR-397/398 的追加风格一致）。
var fr396DomainActions = []AgentActionDescriptor{
	{Action: AgentActionNodeGet, V2Capability: AgentCapabilityNodeRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead},
	{Action: AgentActionNodeGetMetrics, V2Capability: AgentCapabilityObservabilityRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead},
	{Action: AgentActionNodeCheckDocker, V2Capability: AgentCapabilityNodeRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead},
	{Action: AgentActionNodeDrain, V2Capability: AgentCapabilityNodeOperate, ResourceType: AgentResourceNode, Operation: AgentOperationWrite},
	{Action: AgentActionNodeListArchived, V2Capability: AgentCapabilityNodeRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead},
	{Action: AgentActionNodePurgeArchived, V2Capability: AgentCapabilityNodeDestructive, ResourceType: AgentResourceNode, Operation: AgentOperationDestructive, RequiresConfirm: true},
	{Action: AgentActionInstanceSearch, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	{Action: AgentActionInstanceGetEnv, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	{Action: AgentActionInstanceListCrashSnapshots, V2Capability: AgentCapabilityObservabilityRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	// 创建类：目标资源类型按节点授权（创建前尚无实例），clone/rebuild 源实例仍走 instance。
	{Action: AgentActionInstanceCreate, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceNode, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceProvisionServer, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceNode, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceImportInspect, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceNode, Operation: AgentOperationRead},
	{Action: AgentActionInstanceImport, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceNode, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceClone, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceRebuild, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceUpdateConfig, V2Capability: AgentCapabilityInstanceConfigure, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	// task_get 无固定资源类型：先按 task 关联实例归属重验，再走 instance.read 能力。
	{Action: AgentActionTaskGet, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceNone, Operation: AgentOperationRead},
	{Action: AgentActionInstanceSendCommand, V2Capability: AgentCapabilityInstanceCommand, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceBatch, V2Capability: AgentCapabilityInstanceLife, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceKill, V2Capability: AgentCapabilityInstanceDestructive, ResourceType: AgentResourceInstance, Operation: AgentOperationDestructive, RequiresConfirm: true},
	{Action: AgentActionInstanceDelete, V2Capability: AgentCapabilityInstanceDestructive, ResourceType: AgentResourceInstance, Operation: AgentOperationDestructive, RequiresConfirm: true},
}

func init() {
	for _, d := range fr396DomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// AgentKnownCapabilities 返回排序后的已知 V2 能力列表。
func AgentKnownCapabilities() []string {
	out := make([]string, 0, len(agentKnownCapabilities))
	for k := range agentKnownCapabilities {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsAgentKnownCapability 判断 capability 是否在固定集合内。
func IsAgentKnownCapability(cap string) bool {
	_, ok := agentKnownCapabilities[cap]
	return ok
}

// DescribeAgentAction 返回 action 描述；未知 action 返回 false。
func DescribeAgentAction(action string) (AgentActionDescriptor, bool) {
	d, ok := agentActionCatalog[action]
	return d, ok
}

// AgentActionCatalog 返回全部已登记 action 描述（无序）。
func AgentActionCatalog() []AgentActionDescriptor {
	out := make([]AgentActionDescriptor, 0, len(agentActionCatalog))
	for _, d := range agentActionCatalog {
		out = append(out, d)
	}
	return out
}

// ValidateAgentCapabilities 校验 V2 能力数组：去空、禁未知、禁重复。
func ValidateAgentCapabilities(caps []string) error {
	if caps == nil {
		return fmt.Errorf("capabilities 必须显式提交（可为空数组）")
	}
	seen := make(map[string]struct{}, len(caps))
	for _, c := range caps {
		if c == "" {
			return fmt.Errorf("capabilities 含空值")
		}
		if !IsAgentKnownCapability(c) {
			return fmt.Errorf("未知 capability: %s", c)
		}
		if _, ok := seen[c]; ok {
			return fmt.Errorf("重复 capability: %s", c)
		}
		seen[c] = struct{}{}
	}
	return nil
}

func resolveCapabilityLabel(p *AgentPrincipal, d AgentActionDescriptor) string {
	if p == nil {
		return ""
	}
	if p.PolicyVersion == AgentPolicyVersionV2 {
		return d.V2Capability
	}
	if d.Operation == AgentOperationWrite || d.Operation == AgentOperationDestructive {
		if d.V1WriteAllow == AgentWriteInstanceLife {
			return AgentLegacyCapabilityInstanceLife
		}
		if d.V1WriteAllow == AgentWriteNodeMaintenance {
			return AgentLegacyCapabilityNodeMaintenance
		}
	}
	if d.Action == AgentActionWhoami {
		return ""
	}
	return AgentLegacyCapabilityRead
}

// CapabilityForCallLog 返回流水应记录的 capability 标签；未知 action 返回空。
func CapabilityForCallLog(p *AgentPrincipal, action string) string {
	d, ok := DescribeAgentAction(action)
	if !ok {
		return ""
	}
	return resolveCapabilityLabel(p, d)
}

func principalHasCapability(p *AgentPrincipal, cap string) bool {
	if p == nil || cap == "" {
		return false
	}
	return containsStr(p.Capabilities, cap)
}

func principalHasPotentialScope(p *AgentPrincipal, resourceType string) bool {
	if p == nil {
		return false
	}
	switch resourceType {
	case AgentResourceNone:
		return true
	case AgentResourceNode:
		return len(p.ScopedNodeIDs) > 0
	case AgentResourceInstance:
		if len(p.ScopedInstanceIDs) > 0 {
			return true
		}
		// 仅 V2 节点 scope 可继承实例。
		return p.PolicyVersion == AgentPolicyVersionV2 && len(p.ScopedNodeIDs) > 0
	default:
		return false
	}
}

func principalCanAccessInstance(p *AgentPrincipal, instanceID, nodeID uint) bool {
	if p == nil || instanceID == 0 {
		return false
	}
	if agentContainsUint(p.ScopedInstanceIDs, instanceID) {
		return true
	}
	if p.PolicyVersion == AgentPolicyVersionV2 && nodeID != 0 && agentContainsUint(p.ScopedNodeIDs, nodeID) {
		return true
	}
	return false
}

func principalCanAccessNode(p *AgentPrincipal, nodeID uint) bool {
	if p == nil || nodeID == 0 {
		return false
	}
	return agentContainsUint(p.ScopedNodeIDs, nodeID)
}

// PrincipalCanAccessNode 导出节点 scope 判断，供 MCP 列表过滤等只读投影使用。
func PrincipalCanAccessNode(p *AgentPrincipal, nodeID uint) bool {
	return principalCanAccessNode(p, nodeID)
}

// CanDiscover 判断 Token 是否可在 tools/list / 契约枚举中看到该 action。
func CanDiscover(p *AgentPrincipal, action string) (AgentAuthorization, error) {
	if p == nil {
		return AgentAuthorization{}, ErrAgentForbidden
	}
	d, ok := DescribeAgentAction(action)
	if !ok {
		return AgentAuthorization{}, ErrAgentForbidden
	}
	if err := authorizeCapability(p, d); err != nil {
		return AgentAuthorization{}, err
	}
	if !principalHasPotentialScope(p, d.ResourceType) {
		return AgentAuthorization{}, ErrAgentForbidden
	}
	return AgentAuthorization{Capability: resolveCapabilityLabel(p, d)}, nil
}

// Authorize 对可信目标执行最终授权。
func Authorize(p *AgentPrincipal, action string, target AgentTrustedTarget) (AgentAuthorization, error) {
	if p == nil {
		return AgentAuthorization{}, ErrAgentForbidden
	}
	d, ok := DescribeAgentAction(action)
	if !ok {
		return AgentAuthorization{}, ErrAgentForbidden
	}
	if err := authorizeCapability(p, d); err != nil {
		return AgentAuthorization{}, err
	}
	switch d.ResourceType {
	case AgentResourceNone:
		// whoami 等
	case AgentResourceNode:
		nodeID := target.NodeID
		if nodeID == 0 {
			return AgentAuthorization{}, ErrAgentForbidden
		}
		if !principalCanAccessNode(p, nodeID) {
			return AgentAuthorization{}, ErrAgentForbidden
		}
	case AgentResourceInstance:
		if target.InstanceID == 0 {
			return AgentAuthorization{}, ErrAgentForbidden
		}
		if !principalCanAccessInstance(p, target.InstanceID, target.NodeID) {
			return AgentAuthorization{}, ErrAgentForbidden
		}
	default:
		return AgentAuthorization{}, ErrAgentForbidden
	}
	return AgentAuthorization{Capability: resolveCapabilityLabel(p, d)}, nil
}

func authorizeCapability(p *AgentPrincipal, d AgentActionDescriptor) error {
	if p.PolicyVersion == AgentPolicyVersionV2 {
		if d.Action == AgentActionWhoami {
			return nil
		}
		if d.V2Capability == "" || !principalHasCapability(p, d.V2Capability) {
			return ErrAgentForbidden
		}
		return nil
	}
	// V1
	if !d.V1Allowed {
		return ErrAgentForbidden
	}
	if d.Operation == AgentOperationWrite || d.Operation == AgentOperationDestructive {
		if d.V1WriteAllow == "" || !containsStr(p.WriteAllowlist, d.V1WriteAllow) {
			return ErrAgentForbidden
		}
	}
	return nil
}
