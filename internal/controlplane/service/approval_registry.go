package service

import (
	"fmt"
	"sort"
	"strings"
)

// 高危操作审批登记表（FR-492）。
//
// ## 为什么是**代码常量**而不是 DB 配置
//
// 漏登记必须是**测试期可见**的。放 DB 里，一次配置遗漏就是一次静默放行，
// 而放行的高危操作与正常操作在日志里长得一模一样。
//
// ## 为什么按 **action 粒度**而不是 `Operation` 分类
//
// `instance_start` 与 `instance_stop` 同属 `write`，但 PRD 只要求 `stop` 进审批
// （停服会赶走玩家，启动不会）。按分类无法表达这个差异——这正是 PRD 明确点名的要求。
//
// ## 失败关闭
//
// 未在本表、且不在免审白名单里的写类操作 → **拒绝执行**，不是放行。
// 默认拒绝是这里唯一安全的默认值：新增工具的人必须显式决定它要不要审批。

// approvalRequiredActions 必须走审批的动作（action 名与 MCP 工具名逐字一致）。
//
// PRD 点名的「三类绕过口子」必须全在表内，见每组注释。
var approvalRequiredActions = map[string]string{
	// ── 口子①：文件写入的完整绕过路径 ──
	// `file_write_text` 被硬拒，但「签发票据 + 流式上传」能写盘，是等价能力。
	// 只堵前者不堵后者等于没堵。
	"file_issue_transfer_ticket": "签发文件传输票据（可写盘，等价文件写入）",

	// ── 口子②：控制台命令 ≡ 任意代码执行 ──
	// 当前连「参数名精确确认」都没有，是最直接的一条提权路径。
	"instance_send_command": "向实例下发控制台命令（等价任意代码执行）",

	// ── 口子③：三者均等价「下载并执行任意二进制」 ──
	"instance_update_config":    "修改实例配置（改 startCommand 即改可执行命令）",
	"instance_binary_upgrade":   "替换实例二进制（换掉要执行的东西）",
	"instance_binary_rollback":  "回滚实例二进制（同上）",
	"instance_provision_server": "搭建服务器（支持远程 URL 二进制来源）",

	// ── 覆盖既有数据 ──
	"instance_snapshot_rollback": "回滚实例快照（覆盖当前工作目录）",

	// ── PRD 明确要求：停服需审，启停的另外两个不要求 ──
	"instance_stop": "停止实例（会赶走在线玩家）",

	// ── 聚合型写操作：等价「批量停服」，必须与单实例停服同等对待 ──
	//
	// 这三条是「换个入口绕过审批」的典型：`instance_stop` 需审，而
	// `instance_batch(op=stop)` 能一次停 N 台、`node_drain` 能停掉整节点全部实例，
	// 效果是 N 次 `instance_stop`，此前**零审批**。想批量停服只需换个工具名。
	//
	// 它们也没被「危险工具漏登记」门禁抓到——那门禁只认 delete/rollback/write 等词根，
	// 而 drain / batch / maintenance 不在其中。门禁靠词根匹配，就必然对
	// 「语义危险但命名温和」的工具有盲区；补词根与补名单要一起做（见 DangerousToolNames）。
	"instance_batch":         "批量操作实例（op=stop/restart 等价批量停服）",
	"node_drain":             "排空节点（停止其上全部实例并进入维护）",
	"node_maintenance_enter": "节点进入维护（通常伴随停服）",

	// ── 由「危险工具漏登记」门禁（mcp 包 TestRegistryCoversAllDangerousTools）揪出的缺口 ──
	//
	// 这批的价值：它们**不是**由人凭直觉想出来的，而是测试把「名字含 delete/rollback/
	// write/purge 等词根的写工具」与审批名单做了全量比对后暴露的。手写名单永远会漏，
	// 这也是那条门禁存在的全部理由。

	// 删实例：不可逆，连带工作目录与文件。
	"instance_delete": "删除实例（不可逆，连带工作目录）",
	// 永久删除已归档节点：比归档更彻底，不可恢复。
	"node_purge_archived": "永久删除已归档节点（不可恢复）",
	// 文件写入与删除：直接改/删实例内文件。
	"file_write_text": "写入实例内文件（覆盖内容）",
	"file_delete":     "删除实例内文件",
	"file_rollback":   "回滚实例内文件到历史版本（覆盖当前内容）",
	// 配置写入与回滚：改配置即可改启动命令等执行上下文。
	"config_write_text": "整份写入实例配置（可改 startCommand）",
	"config_rollback":   "回滚实例配置（覆盖当前配置）",
	// Bot 下发命令：Bot 在服务器内的行为，等价代为操作。
	"bot_send_command": "让 Bot 下发命令（等价代为操作）",
	// 后端注册关系删除：会把后端从代理摘掉，影响线上流量。
	"registration_delete": "删除代理后端注册（影响线上流量）",
	// 从分组/群组移出成员：影响调度与可见范围。
	"instance_group_remove_members": "把实例移出分组（影响调度与可见范围）",
	"network_remove_member":         "把实例移出群组（影响归属）",
}

// ApprovalRequirement 一次调用的审批判定结果。
type ApprovalRequirement struct {
	// Required 是否需要审批。
	Required bool
	// Action 判定所用的 action 名（去掉 mcp. 前缀后的原始工具名）。
	Action string
	// Reason 需要审批时的中文说明（给申请人/批准人看）。
	Reason string
}

// ClassifyApprovalRequirement 判定某个 action 是否需要审批。
//
// ## 为什么默认不是「所有写操作都需审」
//
// 平台的 MCP 工具面有一百多个写操作，其中绝大多数已有**参数名精确确认**
// （`confirmBotName` 这类，FR-396 语义）或本身就是低风险可逆操作。
// 若一律要求审批，结果是「什么都要批」——批准人只会闭眼点同意，
// 审批就从把关退化成橡皮图章。这是比不做审批更危险的形态。
//
// 因此规则是：**危险名单内必须审批**；名单外沿用既有确认语义。
//
// ## 那「漏登记」怎么防
//
// 不靠运行期拦截全部写操作，而靠**测试**：
// `approval_registry_test.go` 断言危险名单覆盖 PRD 点名的每一个口子，
// 且对「新增了看起来危险的工具」有显式清单比对（见 `TestRegistryCoversAllMCPWriteTools`）。
// 运行期失败关闭只保留给**真正无法判定**的场合（见 InterceptForApproval 的 Approval=nil 分支）。
func ClassifyApprovalRequirement(action string, isWrite bool) (ApprovalRequirement, error) {
	action = strings.TrimSpace(action)
	if action == "" {
		return ApprovalRequirement{}, fmt.Errorf("%w: 缺少 action", ErrApprovalNotRegistered)
	}
	if !isWrite {
		// 只读不需要审批（但仍受各自的能力与 scope 约束，那是另一层）。
		return ApprovalRequirement{Required: false, Action: action}, nil
	}
	if reason, ok := approvalRequiredActions[action]; ok {
		return ApprovalRequirement{Required: true, Action: action, Reason: reason}, nil
	}
	// 名单外：沿用既有确认语义（精确确认 / 低风险可逆）。
	return ApprovalRequirement{Required: false, Action: action}, nil
}

// ApprovalRequiredActionNames 返回需审的 action 名（稳定顺序，供文档与断言）。
func ApprovalRequiredActionNames() []string {
	out := make([]string, 0, len(approvalRequiredActions))
	for k := range approvalRequiredActions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ApprovalDecisionFor 按 action 取中文说明（不存在返回空串）。
func ApprovalDecisionFor(action string) string {
	return approvalRequiredActions[action]
}

// ApprovalToolNameForAgentAction 把 agent 运维面 action 名（`agent.instance_stop`）
// 映射为本登记表与执行器 adapter 所用的工具名（`instance_stop`）。
//
// ## 为什么需要它
//
// FR-492 要求 CLI / MCP / curl **三入口同策略**。MCP 侧的工具名本来就是登记表键
// （`instance_stop`），而 HTTP/CLI 走的是 agent action 目录（`agent.instance_stop`），
// 两套命名若不做映射，同一件事在一条入口需审、在另一条直接执行——
// 一个持有 Agent Token 的调用方只要换用 curl 就能绕过审批。
//
// 只做「去 `agent.` 前缀」这一种确定映射，并且**必须命中登记表**才返回 ok：
// 不猜、不做模糊匹配，避免把不该审的 action 误判成需审。
func ApprovalToolNameForAgentAction(action string) (string, bool) {
	name := strings.TrimPrefix(strings.TrimSpace(action), "agent.")
	if name == "" {
		return "", false
	}
	if _, ok := approvalRequiredActions[name]; !ok {
		return "", false
	}
	return name, true
}

// AgentActionNeedsApproval 判定某个 agent 运维面 action 是否需要审批。
func AgentActionNeedsApproval(action string) (string, bool) {
	return ApprovalToolNameForAgentAction(action)
}
