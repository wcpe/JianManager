package service

import (
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
)

// 助手工具登记表（FR-516 §4）。
//
// 三条不可动摇的规则（spec §3 冻结项）：
//  1. **工具目录复用 `agentActionCatalog`，只收窄不扩张**——单一真源，
//     避免「同一个动作在两处有两套说法」。
//  2. **读自动、写必确认**：不看模型自述、不看 MCP annotations。
//     即使目录里 `RequiresConfirm=false`，write/destructive 也强制确认——
//     「写必确认」是本 FR 的不变量。
//  3. **平台治理类永久排除**：助手不得改自身配置、不得读秘密明文、不得任意读写 CP 文件。

// assistantToolSourceSystem 内置工具来源前缀（`system_<action>`）。
//
// 用命名空间而不是裸 action 名，是为了不与未来外部 MCP 连接的工具名冲突（FR-517）。
const assistantToolSourceSystem = "system"

// assistantToolNameSeparator 工具名分隔符，**只能用下划线**。
//
// ## 为什么不用点号（真机联调发现）
//
// 原先工具名是 `system.<action>`。用真实上游（nustarapi / deepseek-v4.1-flash）联调时，
// 只要该轮带工具，请求就被 400 拒：
//
//	{"code":11152,"msg":"the tool name is invalid or duplicated"}
//
// 因为 OpenAI 工具名规范只允许 `^[a-zA-Z0-9_-]{1,64}$`，点号不在字符集内。
// 假上游不校验字符集，所以「只用假上游跑通」永远发现不了这一条——
// 这是 `assistant_probe_ping`（纯下划线）能通过、而 `system.agent_whoami` 被拒的分水岭。
//
// 结论：分隔符必须是下划线。改回点号会让「带工具的任何一轮对话」在上游直接失败。
const assistantToolNameSeparator = "_"

// assistantToolName 拼出内置工具的对外名。
//
// action 名本身含点号（`agent.get_instance`，因为 action 目录是「命名空间.动作」），
// 所以拼装时必须**把点号也换成下划线**，否则 `system_agent.get_instance` 依旧非法。
// 反向查找不依赖解析这个字符串：`findAssistantTool` 是按生成好的 Name 精确匹配的，
// 因此这里只改生成侧就闭环，不需要写一套「拆名还原 action」的逻辑。
func assistantToolName(action string) string {
	normalized := strings.NewReplacer(".", assistantToolNameSeparator).Replace(action)
	return assistantToolSourceSystem + assistantToolNameSeparator + normalized
}

// assistantToolSchemaVersion 内置工具的 schema 指纹版本。
//
// 票据里存的是**字符串指纹**而不是整数版本：外部 MCP 工具只有 `schema_hash`（FR-517 §4.2），
// 统一成「字符串不等即失效」后，两类工具走同一套失效判定，不必假设存在单调递增版本号。
const assistantToolSchemaVersion = "system.v1"

// assistantToolPolicyVersion 执行策略版本。策略变化（如新增禁止项）时递增，
// 使旧确认票据失效（FR-516 §6.3）。
const assistantToolPolicyVersion = 1

// AssistantToolAction 一个可交给模型的工具的**执行元数据**（FR-544）。
//
// 导出是为了让 mcp 包能从单一真源（allToolSpecs）构造它并注入回来；
// 助手侧不再自己定义任何工具。
type AssistantToolAction struct {
	// Name 对外名（`system.<action>`）。
	Name string
	// Action 原始 action 名（回到 agentActionCatalog 用）。
	Action string
	// Capability 所需能力节点（执行前按它重验）。
	Capability string
	// ResourceType 目标资源类型（instance / node / ...），空表示无特定目标。
	ResourceType string
	// Operation read / write / destructive。
	Operation string
	// RequiresConfirm 是否必须确认。write/destructive 恒为 true。
	RequiresConfirm bool
	// Description 给模型看的描述（中文，动作语义 + 风险提示）。
	Description string
	// InputSchema JSON Schema（对象）。
	InputSchema map[string]any
}

// isWrite 是否属于需要确认的写类操作。
func (t AssistantToolAction) isWrite() bool {
	return t.RequiresConfirm
}

// assistantToolForbiddenActions 永久排除的动作（spec §3「明确不做」）。
//
// 这些即使出现在 `agentActionCatalog` 里也不得暴露给模型：
// 通用 Shell、CP 任意文件读写、平台治理、秘密明文、数据库浏览、助手自身配置、自更新。
// 用显式**动作名黑名单**而不是按 capability 前缀排除：
// 后者会随新能力命名漂移而悄悄放开，前者是「加进来必须显式删掉这里的一行」。
var assistantToolForbiddenActions = map[string]struct{}{
	// 文件写入（CP 任意文件）
	"file_write_text": {},
	// 配置写入
	"config_write_text":   {},
	"config_write_fields": {},
	"config_rollback":     {},
	// 助手自身配置（模型/供应商/外部连接/权限）
	"assistant_config": {},
	// 平台治理
	"rbac_manage":     {},
	"system_settings": {},
	"system_update":   {},
}

// assistantToolExtraActions 已删除（FR-544）：三个受限只读（cluster_overview /
// list_tasks / list_alerts）原本在助手侧独立定义，但它们的实现与真源都在 MCP 目录之外，
// 与「工具单一真源 = MCP」冲突。现在它们是正式的 MCP 工具（mcp/tools_overview.go），
// 实现留在 service/assistant_overview.go，助手经 search/call 使用。

// assistantToolSpecs 返回模型可见的工具定义——现在只剩三个元工具。
//
// 业务工具不再常驻下发：它们经 search/describe 按需取（渐进式披露）。
func assistantToolSpecs() []ai.ToolSpec {
	return assistantMetaToolSpecs()
}

// findAssistantTool 按名字查工具元数据（不存在返回 nil）。
//
// 数据来源是 MCP 目录（注入），不是本地注册表——助手侧不再有工具定义。
func findAssistantTool(name string) *AssistantToolAction {
	t, ok := assistantToolMeta(name)
	if !ok {
		return nil
	}
	return t
}
