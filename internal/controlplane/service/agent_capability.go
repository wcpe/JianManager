package service

import (
	"fmt"
	"sort"
	"strings"
)

// Agent 策略版本与 V2 能力常量（FR-395 / ADR-080）。
const (
	AgentPolicyVersionV1 = 1
	AgentPolicyVersionV2 = 2

	AgentCapabilityNodeRead            = "node.read"
	AgentCapabilityNodeOperate         = "node.operate"
	AgentCapabilityNodeDestructive     = "node.destructive"
	AgentCapabilityInstanceRead        = "instance.read"
	AgentCapabilityInstanceWrite       = "instance.write"
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
	// FR-398：Bot 与压测运行资源；授权均回落到所属实例（botrun 另叠加 executor 节点校验）。
	AgentResourceBot    = "bot"
	AgentResourceBotRun = "botrun"

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

	// Summary 是一句话摘要，供 MCP tool Description 使用（ADR-111）。
	Summary string
}

// IsDestructive 报告该 action 是否为破坏性操作。
// destructive 操作在 MCP 层须带 destructiveHint 标注，客户端据此在调用前征求用户确认。
func (d AgentActionDescriptor) IsDestructive() bool {
	return d.Operation == AgentOperationDestructive
}

// IsReadOnly 报告该 action 是否只读。
func (d AgentActionDescriptor) IsReadOnly() bool {
	return d.Operation == AgentOperationRead
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
	AgentCapabilityInstanceWrite:       {},
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
		Summary: "显示当前 Agent Token 的身份、策略版本与能力",
	},
	AgentActionListNodes: {
		Action: AgentActionListNodes, V2Capability: AgentCapabilityNodeRead,
		ResourceType: AgentResourceNode, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/nodes", HTTPInContract: true,
		Summary: "列出 Token scope 内的节点",
	},
	AgentActionListInstances: {
		Action: AgentActionListInstances, V2Capability: AgentCapabilityInstanceRead,
		ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/instances", HTTPInContract: true,
		Summary: "列出 Token scope 内的实例",
	},
	AgentActionGetInstance: {
		Action: AgentActionGetInstance, V2Capability: AgentCapabilityInstanceRead,
		ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/instances/:id", HTTPInContract: true,
		Summary: "查看实例当前状态",
	},
	AgentActionGetInstanceMetrics: {
		Action: AgentActionGetInstanceMetrics, V2Capability: AgentCapabilityObservabilityRead,
		ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		V1Allowed: true, HTTPMethod: "GET", HTTPPath: "/api/v1/agent/instances/:id/metrics", HTTPInContract: true,
		Summary: "读取实例运行指标",
	},
	AgentActionGetInstanceLogs: {
		Action: AgentActionGetInstanceLogs, V2Capability: AgentCapabilityObservabilityRead,
		ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		V1Allowed: true, // MCP 专属，不进 HTTP 契约投影
		Summary:   "读取实例日志（仅 MCP）",
	},
	AgentActionInstanceStart: {
		Action: AgentActionInstanceStart, V2Capability: AgentCapabilityInstanceLife,
		ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteInstanceLife,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/instances/:id/start", HTTPInContract: true,
		Summary: "启动实例",
	},
	AgentActionInstanceStop: {
		Action: AgentActionInstanceStop, V2Capability: AgentCapabilityInstanceLife,
		ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteInstanceLife,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/instances/:id/stop", HTTPInContract: true,
		Summary: "优雅停止实例",
	},
	AgentActionInstanceRestart: {
		Action: AgentActionInstanceRestart, V2Capability: AgentCapabilityInstanceLife,
		ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteInstanceLife,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/instances/:id/restart", HTTPInContract: true,
		Summary: "重启实例",
	},
	AgentActionNodeMaintenanceEnter: {
		Action: AgentActionNodeMaintenanceEnter, V2Capability: AgentCapabilityNodeOperate,
		ResourceType: AgentResourceNode, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteNodeMaintenance,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/nodes/:id/maintenance/enter", HTTPInContract: true,
		Summary: "让节点进入维护模式",
	},
	AgentActionNodeMaintenanceLeave: {
		Action: AgentActionNodeMaintenanceLeave, V2Capability: AgentCapabilityNodeOperate,
		ResourceType: AgentResourceNode, Operation: AgentOperationWrite,
		V1Allowed: true, V1WriteAllow: AgentWriteNodeMaintenance,
		HTTPMethod: "POST", HTTPPath: "/api/v1/agent/nodes/:id/maintenance/leave", HTTPInContract: true,
		Summary: "让节点退出维护模式",
	},
}

// fr396DomainActions 是 FR-396 节点/实例扩展 action，经 init 注入 catalog，
// 保持 map 字面量本体不被并行分支争用（与 FR-397/398 的追加风格一致）。
var fr396DomainActions = []AgentActionDescriptor{
	{Action: AgentActionNodeGet, V2Capability: AgentCapabilityNodeRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead,
		Summary: "查看节点详情"},
	{Action: AgentActionNodeGetMetrics, V2Capability: AgentCapabilityObservabilityRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead,
		Summary: "读取节点运行指标"},
	{Action: AgentActionNodeCheckDocker, V2Capability: AgentCapabilityNodeRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead,
		Summary: "检查节点 Docker 环境"},
	{Action: AgentActionNodeDrain, V2Capability: AgentCapabilityNodeOperate, ResourceType: AgentResourceNode, Operation: AgentOperationWrite,
		Summary: "排空节点上的实例"},
	{Action: AgentActionNodeListArchived, V2Capability: AgentCapabilityNodeRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead,
		Summary: "列出已归档节点"},
	{Action: AgentActionNodePurgeArchived, V2Capability: AgentCapabilityNodeDestructive, ResourceType: AgentResourceNode, Operation: AgentOperationDestructive, RequiresConfirm: true,
		Summary: "永久清除已归档节点（不可恢复）"},
	{Action: AgentActionInstanceSearch, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		Summary: "在 Token scope 内搜索实例"},
	{Action: AgentActionInstanceGetEnv, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		Summary: "获取实例环境变量视图"},
	{Action: AgentActionInstanceListCrashSnapshots, V2Capability: AgentCapabilityObservabilityRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		Summary: "列出实例崩溃快照元数据"},
	// 创建类：目标资源类型按节点授权（创建前尚无实例），clone/rebuild 源实例仍走 instance。
	{Action: AgentActionInstanceCreate, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceNode, Operation: AgentOperationWrite,
		Summary: "在节点上创建新实例"},
	{Action: AgentActionInstanceProvisionServer, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceNode, Operation: AgentOperationWrite,
		Summary: "按服务端规格初始化实例"},
	{Action: AgentActionInstanceImportInspect, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceNode, Operation: AgentOperationRead,
		Summary: "导入前检视待导入实例"},
	{Action: AgentActionInstanceImport, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceNode, Operation: AgentOperationWrite,
		Summary: "导入既有实例"},
	{Action: AgentActionInstanceClone, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "克隆实例"},
	{Action: AgentActionInstanceRebuild, V2Capability: AgentCapabilityInstanceProvision, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "重建实例"},
	{Action: AgentActionInstanceUpdateConfig, V2Capability: AgentCapabilityInstanceConfigure, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "更新实例配置"},
	// 实例标签（FR-440）：权限面比 configure 更轻（instance.write），仅动 tags 字段。
	{Action: AgentActionInstanceUpdateTags, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "更新实例标签"},
	// task_get 无固定资源类型：先按 task 关联实例归属重验，再走 instance.read 能力。
	{Action: AgentActionTaskGet, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceNone, Operation: AgentOperationRead,
		Summary: "查询异步任务状态"},
	// 审批票据自查：无固定资源类型（按 TokenID 限域，见 ApprovalService 的按票过滤）。
	// list/get 是读；withdraw 是写但**不产生任何外部副作用**（只改自己的票状态），
	// 因此 V1WriteAllow 留空且 V1Allowed=false——V1 白名单里没有“审批”这一档，
	// 宁可让 V1 Token 用不了自查，也不给它新开口子。
	{Action: AgentActionApprovalList, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceNone, Operation: AgentOperationRead,
		Summary: "列出本 Token 名下的审批票据"},
	{Action: AgentActionApprovalGet, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceNone, Operation: AgentOperationRead,
		Summary: "查看审批票据详情"},
	{Action: AgentActionApprovalWithdraw, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceNone, Operation: AgentOperationRead,
		Summary: "撤回自己发起的审批票据"},
	// 实例分组：分组自身无独立资源类型，按节点/实例 scope 判定可发现性（复用 instance 分支）。
	{Action: AgentActionInstanceGroupList, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		Summary: "列出实例分组"},
	{Action: AgentActionInstanceGroupRead, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		Summary: "查看实例分组详情"},
	{Action: AgentActionInstanceGroupCreate, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "创建实例分组"},
	{Action: AgentActionInstanceGroupUpdate, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "更新实例分组"},
	{Action: AgentActionInstanceGroupDelete, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationDestructive, RequiresConfirm: true,
		Summary: "删除实例分组（不可恢复）"},
	{Action: AgentActionInstanceGroupAddMembers, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "向实例分组添加成员"},
	{Action: AgentActionInstanceGroupRemoveMembers, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "从实例分组移除成员"},
	{Action: AgentActionInstanceSendCommand, V2Capability: AgentCapabilityInstanceCommand, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "向运行中实例下发控制台命令"},
	{Action: AgentActionInstanceBatch, V2Capability: AgentCapabilityInstanceLife, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "批量下发实例生命周期操作"},
	{Action: AgentActionInstanceKill, V2Capability: AgentCapabilityInstanceDestructive, ResourceType: AgentResourceInstance, Operation: AgentOperationDestructive, RequiresConfirm: true,
		Summary: "强制终止实例（不保证优雅退出）"},
	{Action: AgentActionInstanceDelete, V2Capability: AgentCapabilityInstanceDestructive, ResourceType: AgentResourceInstance, Operation: AgentOperationDestructive, RequiresConfirm: true,
		Summary: "删除实例（不可恢复）"},
}

func init() {
	for _, d := range fr396DomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// networkDomainActions 是群组服 Network 软标签与代理注册（FR-032 / FR-335）的 MCP 专属 action。
// 与实例分组同权限面（读 instance.read、写 instance.write）：这些操作改的是实例间的归属
// 与代理注册关系，不是实例内容，故不占用 configure/content 等更细能力。
// 全部 V1Allowed=false、HTTPInContract=false（HTTP 侧另有 /networks、/topology 路由）。
var networkDomainActions = []AgentActionDescriptor{
	{Action: AgentActionNetworkList, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	{Action: AgentActionNetworkRead, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	{Action: AgentActionNetworkCreate, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionNetworkUpdate, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionNetworkDelete, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationDestructive, RequiresConfirm: true},
	{Action: AgentActionNetworkAddMembers, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionNetworkRemoveMember, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionTopologyGet, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	{Action: AgentActionRegistrationList, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	{Action: AgentActionRegistrationCreate, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionRegistrationDelete, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
}

func init() {
	for _, d := range networkDomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// beaconDomainActions 是 Beacon 拓扑拉取（FR-444，见 ADR-090）的 MCP 专属 action。
// 权限面与实例分组一致（读 instance.read、写 instance.write）：拉取改的是分组树与实例标签
// （归属类信息），不是实例内容，故不占用 configure/content 等更细能力。
// 刻意不标 destructive——拉取是幂等对账（重复拉取不产生重复分组、不删本地数据），
// 与「删除分组」的破坏性语义不同；Beacon 不可达时更是一字节不改。
// 全部 V1Allowed=false、HTTPInContract=false（HTTP 侧另有 /beacon/topology 路由）。
var beaconDomainActions = []AgentActionDescriptor{
	{Action: AgentActionBeaconTopologyPull, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionBeaconTopologyStatus, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
}

func init() {
	for _, d := range beaconDomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// fr470DomainActions 是崩溃诊断增强（FR-470）的 MCP 专属 action：崩溃趋势读。
// 与既有 instance_list_crash_snapshots 同权限面（observability.read）——趋势是观测聚合，
// 不改实例，故只读。V1Allowed=false、HTTPInContract=false（HTTP 侧另有 /crash-trend 路由）。
var fr470DomainActions = []AgentActionDescriptor{
	{Action: AgentActionInstanceCrashTrend, V2Capability: AgentCapabilityObservabilityRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
}

func init() {
	for _, d := range fr470DomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// fr468DomainActions 是二进制/Beacon 版本管理（FR-468）的 MCP 专属 action。
// 读走 instance.read；升级/回滚改的是实例的二进制版本（影响可执行内容），
// 故走 instance.write（比 destructive 轻——不回数据、删文件，且可一级回滚）。
// V1Allowed=false、HTTPInContract=false（HTTP 侧另有 /binary-version 三端点）。
var fr468DomainActions = []AgentActionDescriptor{
	{Action: AgentActionInstanceBinaryVersionGet, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	{Action: AgentActionInstanceBinaryUpgrade, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceBinaryRollback, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
}

func init() {
	for _, d := range fr468DomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// fr467DomainActions 是运行期配额强制（FR-467）的 MCP 专属 action：配额与实时用量读。
// V1Allowed=false、HTTPInContract=false（HTTP 侧另有 /instances/:id/quota）。
var fr467DomainActions = []AgentActionDescriptor{
	{Action: AgentActionInstanceQuotaStatus, V2Capability: AgentCapabilityObservabilityRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
}

func init() {
	for _, d := range fr467DomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// fr466DomainActions 是实例整机快照（FR-466）的 MCP 专属 action。
// 列表走 instance.read；创建快照与一键回滚走 instance.write——回滚会覆盖工作目录，
// 但它是**可再回滚**的（强制 pre_rollback 快照），故不落 destructive；
// 真正的破坏性动作（删除快照）暂不通过 MCP 暴露。
// V1Allowed=false、HTTPInContract=false（HTTP 侧另有 /instances/:id/snapshots 等）。
var fr466DomainActions = []AgentActionDescriptor{
	{Action: AgentActionInstanceSnapshotList, V2Capability: AgentCapabilityInstanceRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead},
	{Action: AgentActionInstanceSnapshotCreate, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
	{Action: AgentActionInstanceSnapshotRollback, V2Capability: AgentCapabilityInstanceWrite, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite},
}

func init() {
	for _, d := range fr466DomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// botDomainActions 是 FR-398 新增 action 的紧凑声明：action → (capability, resource, operation)。
// 全部 V1Allowed=false、HTTPInContract=false，故不逐条重复这两个字段。
var botDomainActions = []AgentActionDescriptor{
	{Action: AgentActionBotList, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		Summary: "列出实例下的 Bot"},
	{Action: AgentActionBotGet, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceBot, Operation: AgentOperationRead,
		Summary: "查看单个 Bot 详情"},
	{Action: AgentActionBotCreate, V2Capability: AgentCapabilityBotManage, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "为实例创建 Bot"},
	{Action: AgentActionBotSetBehavior, V2Capability: AgentCapabilityBotManage, ResourceType: AgentResourceBot, Operation: AgentOperationWrite,
		Summary: "设置 Bot 行为参数"},
	{Action: AgentActionBotSendCommand, V2Capability: AgentCapabilityBotManage, ResourceType: AgentResourceBot, Operation: AgentOperationWrite,
		Summary: "向 Bot 下发指令"},
	{Action: AgentActionBotDelete, V2Capability: AgentCapabilityBotManage, ResourceType: AgentResourceBot, Operation: AgentOperationDestructive,
		Summary: "删除 Bot（不可恢复）"},

	// 模板不绑定实例：ResourceType=none，仅由 capability 把关（ADR-080 附注：持 bot.load 管理全部模板）。
	{Action: AgentActionLoadTestTemplateList, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceNone, Operation: AgentOperationRead,
		Summary: "列出压测模板"},
	{Action: AgentActionLoadTestTemplateGet, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceNone, Operation: AgentOperationRead,
		Summary: "查看压测模板详情"},
	{Action: AgentActionLoadTestTemplateCreate, V2Capability: AgentCapabilityBotLoad, ResourceType: AgentResourceNone, Operation: AgentOperationWrite,
		Summary: "创建压测模板"},
	{Action: AgentActionLoadTestTemplateUpdate, V2Capability: AgentCapabilityBotLoad, ResourceType: AgentResourceNone, Operation: AgentOperationWrite,
		Summary: "更新压测模板"},
	{Action: AgentActionLoadTestTemplateDelete, V2Capability: AgentCapabilityBotLoad, ResourceType: AgentResourceNone, Operation: AgentOperationDestructive,
		Summary: "删除压测模板（不可恢复）"},

	{Action: AgentActionLoadTestRunCreate, V2Capability: AgentCapabilityBotLoad, ResourceType: AgentResourceInstance, Operation: AgentOperationWrite,
		Summary: "创建压测运行"},
	{Action: AgentActionLoadTestRunList, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceInstance, Operation: AgentOperationRead,
		Summary: "列出压测运行"},
	{Action: AgentActionLoadTestRunGet, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceBotRun, Operation: AgentOperationRead,
		Summary: "查看压测运行详情"},
	{Action: AgentActionLoadTestNodeCapacity, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceNode, Operation: AgentOperationRead,
		Summary: "查询节点压测容量"},
	{Action: AgentActionLoadTestRunPreflight, V2Capability: AgentCapabilityBotLoad, ResourceType: AgentResourceBotRun, Operation: AgentOperationWrite,
		Summary: "压测运行前置检查"},
	{Action: AgentActionLoadTestRunStart, V2Capability: AgentCapabilityBotLoad, ResourceType: AgentResourceBotRun, Operation: AgentOperationWrite,
		Summary: "启动压测运行"},
	{Action: AgentActionLoadTestRunStop, V2Capability: AgentCapabilityBotLoad, ResourceType: AgentResourceBotRun, Operation: AgentOperationWrite,
		Summary: "停止压测运行"},
	{Action: AgentActionLoadTestRunRetryFailed, V2Capability: AgentCapabilityBotLoad, ResourceType: AgentResourceBotRun, Operation: AgentOperationWrite,
		Summary: "重试压测中的失败 Bot"},

	{Action: AgentActionLoadTestRunBots, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceBotRun, Operation: AgentOperationRead,
		Summary: "列出压测运行中的 Bot"},
	{Action: AgentActionLoadTestRunFailures, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceBotRun, Operation: AgentOperationRead,
		Summary: "查看压测运行失败明细"},
	{Action: AgentActionLoadTestRunEvents, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceBotRun, Operation: AgentOperationRead,
		Summary: "查看压测运行事件流"},
	{Action: AgentActionLoadTestRunMetrics, V2Capability: AgentCapabilityObservabilityRead, ResourceType: AgentResourceBotRun, Operation: AgentOperationRead,
		Summary: "查看压测运行指标"},
	{Action: AgentActionLoadTestRunReport, V2Capability: AgentCapabilityBotRead, ResourceType: AgentResourceBotRun, Operation: AgentOperationRead,
		Summary: "生成压测运行报告"},
}

// init 把 FR-398 的 Bot 域 action 追加进唯一真源目录，保持 catalog 的追加式演进。
func init() {
	for _, d := range botDomainActions {
		agentActionCatalog[d.Action] = d
	}
}

// init 追加 FR-397 内容运维 action（文件/配置/插件/传输票据）。
// 全部 V1Allowed=false、MCP 专属（无 HTTP 契约投影），资源恒为 instance。
func init() {
	registerAgentInstanceActions(AgentCapabilityInstanceRead, AgentOperationRead,
		AgentActionFileList, AgentActionFileCheckAccess, AgentActionFileReadText,
		AgentActionFileVersions, AgentActionFileDiff,
		AgentActionConfigDiscover, AgentActionConfigRead, AgentActionConfigCrossCheck,
		AgentActionConfigVersions, AgentActionConfigDiff,
		AgentActionPluginList,
	)
	registerAgentInstanceActions(AgentCapabilityInstanceContent, AgentOperationWrite,
		AgentActionFileWriteText, AgentActionFileRename, AgentActionFileChmod,
		AgentActionFileRollback, AgentActionFileIssueTransferTicket,
		AgentActionPluginDeployFromAsset, AgentActionPluginToggle,
	)
	registerAgentInstanceActions(AgentCapabilityInstanceContent, AgentOperationDestructive,
		AgentActionFileDelete, AgentActionPluginDelete,
	)
	registerAgentInstanceActions(AgentCapabilityInstanceConfigure, AgentOperationWrite,
		AgentActionConfigWriteText, AgentActionConfigWriteFields, AgentActionConfigRollback,
	)
}

// registerAgentInstanceActions 批量登记同能力同操作等级的实例类 action。
func registerAgentInstanceActions(capability, operation string, actions ...string) {
	for _, action := range actions {
		agentActionCatalog[action] = AgentActionDescriptor{
			Action:       action,
			V2Capability: capability,
			ResourceType: AgentResourceInstance,
			Operation:    operation,
			Summary:      agentActionFallbackSummary(action, operation),
		}
	}
}

// agentActionFallbackSummary 为未逐条撰写摘要的批量 action 生成可读摘要。
// 优先取 agentActionSummaries 中的手写文案；缺失时按 action 名与操作等级兜底，
// 保证 MCP tool 描述与 CLI 帮助不出现空摘要（ADR-111）。
func agentActionFallbackSummary(action, operation string) string {
	if s, ok := agentActionSummaries[action]; ok {
		return s
	}
	return summarizeActionName(action, operation)
}

// agentActionSummaries 为需要精确措辞的批量 action 提供手写摘要。
var agentActionSummaries = map[string]string{
	AgentActionFileDelete:              "删除实例内文件（不可恢复）",
	AgentActionPluginDelete:            "删除实例插件（不可恢复）",
	AgentActionFileRollback:            "回滚实例文件到指定版本",
	AgentActionFileIssueTransferTicket: "签发文件传输票据",
	AgentActionConfigRollback:          "回滚实例配置到指定版本",
	AgentActionConfigCrossCheck:        "交叉校验实例配置一致性",
	AgentActionPluginDeployFromAsset:   "从制品部署插件",
}

// summarizeActionName 从 action 名推导中文摘要兜底。
func summarizeActionName(action, operation string) string {
	name := strings.TrimPrefix(action, "agent.")
	name = strings.ReplaceAll(name, "_", " ")
	switch operation {
	case AgentOperationRead:
		return "读取 " + name
	case AgentOperationWrite:
		return "执行 " + name
	case AgentOperationDestructive:
		return "执行 " + name + "（破坏性，不可恢复）"
	default:
		return name
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
//
// 摘要在此统一兜底（ADR-111）：批量登记路径（networkDomainActions 等）不逐条写字，
// 但下游 MCP 工具描述与 Catalog 契约要求每项都有摘要，故在出口补全而不改登记点。
func DescribeAgentAction(action string) (AgentActionDescriptor, bool) {
	d, ok := agentActionCatalog[action]
	if ok && d.Summary == "" {
		d.Summary = agentActionFallbackSummary(d.Action, d.Operation)
	}
	return d, ok
}

// AgentActionCatalog 返回全部已登记 action 描述（无序）。
func AgentActionCatalog() []AgentActionDescriptor {
	out := make([]AgentActionDescriptor, 0, len(agentActionCatalog))
	for _, d := range agentActionCatalog {
		if d.Summary == "" {
			d.Summary = agentActionFallbackSummary(d.Action, d.Operation)
		}
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
	case AgentResourceInstance, AgentResourceBot, AgentResourceBotRun:
		// bot/botrun 的可发现性复用实例分支：目标最终都回落到所属实例。
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

// PrincipalHasNodeScope 判断主体是否具备节点 scope（V2 节点 scope 可继承实例）。
//
// 供实例分组、群组等「非实例、非节点」目标的 destructive 授权使用：这类资源自身无独立资源类型
// （descriptor 复用 instance 以便按节点/实例 scope 判定可发现性），其 id 也不是实例 id，
// 无法按实例目标授权；改按节点 scope 判定（与可发现性口径一致），避免把分组/群组 id 误当实例 id
// 而导致授权恒拒（FR-562）。
func PrincipalHasNodeScope(p *AgentPrincipal) bool {
	return p != nil && p.PolicyVersion == AgentPolicyVersionV2 && len(p.ScopedNodeIDs) > 0
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
	case AgentResourceInstance, AgentResourceBot, AgentResourceBotRun:
		// bot/botrun 由 AuthorizeBotAction/AuthorizeBotRunAction 从 CP 数据解析出所属实例后传入；
		// executor 节点的逐一校验在那两个方法内追加，此处只做实例归属判断。
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
		// whoami 与知识读取不涉及业务资源，任何有效 Token 均可访问（ADR-111）。
		if isResourceFreeReadAction(d.Action) {
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

// isResourceFreeReadAction 判定无需 capability 的只读 action。
// 仅限不触达任何业务资源、且内容公开给所有 Token 的入口；
// 新增成员必须同时满足「ResourceType=none」与「Operation=read」。
func isResourceFreeReadAction(action string) bool {
	switch action {
	case AgentActionWhoami:
		return true
	default:
		return false
	}
}
