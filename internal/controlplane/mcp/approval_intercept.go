package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 审批拦截的 MCP 侧接线。
//
// 放在 mcp 包而不是 service 包，是因为它需要 `p *service.AgentPrincipal` 与
// 工具名→action 的映射，而这两样只有 mcp 包有；判定逻辑本身在
// `service.ApprovalService.InterceptForApproval`，本文件只负责「收集上下文 + 转成 ToolResult」。

// init 把工具名→action 解析器注入 service 包。
//
// 为什么用注入：`mcp` 已 import `service`，反向 import 会成环。
// 注入也顺带明确了「工具目录的真源是 mcp 包」这件事——
// service 侧不应自己维护一份工具名列表，否则两份迟早漂移。
func init() {
	service.SetApprovalToolActionResolver(toolActionByName)
}

// interceptApproval 判定并（必要时）拦截一次 MCP 工具调用。
//
// 返回 (blocked, result)：blocked=true 时调用方**必须直接返回 result**，
// 不得继续执行工具——这是「零副作用」的落点。
func interceptApproval(
	ctx context.Context, deps ToolDeps, p *service.AgentPrincipal,
	name, action string, args map[string]any,
) (bool, ToolResult) {
	operation := service.ActionOperationOf(name)
	targetType, targetID := toolTargetByName(name, args)

	// 先判定「是否需审」，再检查审批服务是否装配。
	//
	// 顺序关键：若反过来（先判 nil 就拒绝），所有写操作在未装配时都会被挡，
	// 包括那些压根不需要审批的工具（bot_delete、配置写入等沿用精确确认语义）。
	// 那会让「审批服务没接」变成一个全域故障，而不是仅影响需审操作。
	// 测试旁路：让「精确确认 / scope / 参数校验」类用例能绕过审批，直接测工具自身逻辑。
	//
	// 为什么需要它：审批与精确确认是两个层次的闸——
	// **审批**决定「这件事要不要做」，**精确确认**保证「执行时目标没搞错」。
	// 正确顺序是「审批 → 精确确认」，因此那些验证后者的用例必须先让前者通过。
	// 生产路径上这个开关恒为 false（没有任何导出接口能打开它）。
	if approvalInterceptBypass {
		return false, ToolResult{}
	}

	req, classifyErr := service.ClassifyApprovalRequirement(name, isWriteOperation(operation))
	if classifyErr != nil {
		return true, toolErr(classifyErr.Error())
	}
	if !req.Required {
		// 不需审：交给既有路径（精确确认 / 低风险可逆）。
		return false, ToolResult{}
	}

	// 需审但审批服务未装配 → **失败关闭**。
	//
	// 这个选择必须明确：放行一个需要审批的高危操作是安全事故，拒绝只是功能不可用。
	if deps.Approval == nil {
		return true, toolErr("审批服务未装配，该高危操作已被拒绝（失败关闭）")
	}

	// **先授权、再出票**：越权请求不应变成一张等人批准的票。
	//
	// 理由：审批是「有权限的人决定要不要做」，不是「给没有权限的人开后门」。
	// 若先出票，一个越权的 Agent 就能通过提交申请，让某位管理员在批准的瞬间
	// 替它完成了越权——批准人不该被安排成提权的最后一步。
	//
	// 注：这里只做**能力与 scope** 判定；工具自己的参数级精确确认（confirmXxx）
	// 仍留在工具内部（它在放行后执行），两者是不同层次的闸。
	if needsTarget(name) {
		id, ok := approvalTargetID(name, args)
		if !ok {
			return true, toolErr("缺少有效的目标 id 参数（未提交审批）")
		}
		if _, authErr := service.Authorize(p, action, targetForApproval(deps, action, id)); authErr != nil {
			return true, toolForbidden(authErr)
		}
	}

	// 申请人：Token 归属的管理员用户（不是 Token 自己——审批要落到一个真实的人）。
	applicantUserID := tokenOwnerUserID(deps, p.TokenID)

	res := deps.Approval.InterceptForApproval(service.ApprovalInterceptParams{
		ToolName:        name,
		Action:          action,
		Operation:       operation,
		TargetType:      targetType,
		TargetID:        targetID,
		Payload:         args,
		Evidence:        service.CaptureTargetSnapshot(deps.Instance, deps.Node, targetType, targetID),
		ApplicantUserID: applicantUserID,
		ApplicantKind:   model.ApprovalApplicantAgent,
		TokenID:         fmt.Sprintf("%d", p.TokenID),
	})
	if !res.Blocked {
		return false, ToolResult{}
	}
	if res.Err != nil {
		return true, toolErr(res.Message)
	}
	// 已落票：返回 requestId（供人工在审批中心核对；MCP 工具面无审批查询工具）。
	return true, toolOK(map[string]any{
		"status":    "awaiting_approval",
		"requestId": res.RequestID,
		"ticketId":  res.TicketID,
		"message":   res.Message,
	})
}

// isWriteOperation 操作类型是否为写/破坏性。
func isWriteOperation(op string) bool {
	return op == service.AgentOperationWrite || op == service.AgentOperationDestructive
}

// approvalInterceptBypass 仅供测试置位（见 interceptApproval 内的说明）。
var approvalInterceptBypass bool

// targetForApproval 按 action 的资源类型构造授权目标。
//
// 只填 id：本层无法把 uuid 解析成实例归属（那需要查库），
// 因此这里做的是「同类型 id 的 scope 判定」这一层；更精确的归属解析
// 由工具内部与执行器负责。
// targetForApproval 按 action 的资源类型构造**用于授权判定**的可信目标。
//
// 实例类必须带上 NodeID，否则 V2 的「节点 scope 继承实例」永远判不出来：
// principalCanAccessInstance 只在 `nodeID != 0` 时才会去匹配 ScopedNodeIDs，
// 拿不到归属就只剩「实例 id 显式在 scope 里」这一条路 —— 于是给节点签发的
// V2 Token 会对该节点上的每个实例都被判越权，所有实例类高危 action 的审批申请
// 在出票前就被拒绝（表现为笼统的「能力/scope 不足」）。
//
// 归属由 `ResolveInstanceTarget` 从库里解析（与工具内部授权用的是同一条解析路径），
// 解析失败时退回只带 InstanceID：宁可多判一次越权，也不把未知归属当成有权限。
// targetForApproval 按 action 的资源类型解析**用于授权判定**的可信目标。
//
// ## 为什么必须按类型分派（而不是「非 node 一律当实例」）
//
// `id` 参数的语义随工具而变：实例类工具是实例 id，**Bot 类工具是 Bot 的 id**。
// 此前这里把非 node 的目标一律传给 `ResolveInstanceTarget`，于是 Bot 类动作
// （`bot_send_command` 等）拿 Bot id 去查实例：
//   - 该 id 恰好也存在同名实例时**碰巧通过**——正是这一点让缺陷长期没暴露
//     （测试与演示环境里 bot.id 与 instance.id 常常都是 1）；
//   - 不同时解析失败，回退成 `{InstanceID: botID}`，再被判越权，对外只剩
//     笼统的「能力/scope 不足」。平台管理员在自己完全可访问的实例上发一条 Bot 命令
//     也会被拒，而错误信息完全不指向真实原因。
func targetForApproval(deps ToolDeps, action string, id uint) service.AgentTrustedTarget {
	d, ok := service.DescribeAgentAction(action)
	if !ok {
		return service.AgentTrustedTarget{}
	}
	switch d.ResourceType {
	case service.AgentResourceNode:
		// 节点类：目标就是节点 id。
		return service.AgentTrustedTarget{NodeID: id}
	case service.AgentResourceBot, service.AgentResourceBotRun:
		// Bot 类：从 Bot 记录解析它所属的实例与节点。
		if deps.Agent != nil {
			if target, _, err := deps.Agent.ResolveBotTarget(id); err == nil {
				return target
			}
		}
		// 解析不出来就**不给任何可用目标**（空目标在下游必然被判越权）。
		// 不能退化成 `{InstanceID: id}`——那等于把 bot id 当实例 id 蒙一次。
		return service.AgentTrustedTarget{}
	default:
		if deps.Agent != nil {
			if target, _, err := deps.Agent.ResolveInstanceTarget(id); err == nil {
				return target
			}
		}
		return service.AgentTrustedTarget{InstanceID: id}
	}
}

// needsTarget 该工具是否需要 `id` 参数。
//
// 依据 catalog 的 ResourceType：`none` 表示无资源目标（如 whoami），
// 其余（instance / node）都需要 id。与 `toolTargetByName` 用同一份元数据，
// 不另写一套判断——两处漂移会出现「拦截时认为需要 id、工具自己不认」的错配。
func needsTarget(name string) bool {
	action, ok := toolActionByName(name)
	if !ok {
		return false
	}
	d, ok := service.DescribeAgentAction(action)
	if !ok {
		return false
	}
	return d.ResourceType != service.AgentResourceNone && d.ResourceType != ""
}

// tokenOwnerUserID 查 Token 归属用户（审批申请人）。
//
// 查不到时返回 0：审批仍会落票，但申请人显示为「未知」——
// 这比「因为查不到归属就放行」安全得多。
func tokenOwnerUserID(deps ToolDeps, tokenID uint) uint {
	if deps.Agent == nil || tokenID == 0 {
		return 0
	}
	return deps.Agent.TokenOwnerUserID(tokenID)
}

// DangerousToolNames 返回「按名字看起来会造成不可逆后果」的写类工具名（升序）。
//
// 用途只有一个：**测试用它来比对审批登记表**（`TestRegistryCoversAllDangerousTools`）。
// 这样新增一个 `xxx_delete` / `xxx_rollback` 工具时，如果没进审批名单，
// 测试会红——把「漏登记」从运行期静默放行，变成 CI 阶段的确定性失败。
//
// 判据是**名字词根**而不是完整列表：手写完整列表会随新工具漂移，
// 而这正是要防的事。词根覆盖不到的（如 `send_command` 的语义）由显式白名单补。
func DangerousToolNames() []string {
	roots := []string{
		"delete", "remove", "destroy", "purge", "rollback", "restore",
		"upgrade", "downgrade", "provision", "send_command", "transfer",
		"write", "issue", "update_config", "archive", "permanent_delete",
		// 聚合型写操作（新增）：这些名字不含传统危险词根，但效果等价于批量停服。
		// 词根匹配天生对「语义危险但命名温和」的工具失效——补词根是让门禁能继续
		// 抓到**未来**同类工具，而不只是给已发现的三个补名单。
		"drain", "batch", "maintenance",
	}
	var out []string
	for _, spec := range allToolSpecs {
		name := spec.Def.Name
		for _, root := range roots {
			if strings.Contains(name, root) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// WithApprovalBypassForTest 在测试中临时绕过审批拦截（返回恢复函数）。
//
// 只在 `_test.go` 里被调用；生产代码无法触达（未导出给 mcp 包外，
// 且没有任何 HTTP/工具入口能改变它）。
func WithApprovalBypassForTest(t interface{ Cleanup(func()) }) func() {
	prev := approvalInterceptBypass
	approvalInterceptBypass = true
	restore := func() { approvalInterceptBypass = prev }
	t.Cleanup(restore)
	return restore
}

// approvalTargetID 取**用于审批授权判定**的目标实例/节点 id。
//
// 不能只认 `args["id"]`：多数工具的实例参数是 `id`，但少数工具的实例是被操作对象的
// 容器而非本身——例如 instance_snapshot_rollback 的实例由快照归属决定，它只收 snapshotId。
// 这些工具在拦截层直接取 `id` 会恒判「缺少 id」，表现为**该工具永远无法提交审批**，
// 而工具自身的授权路径是能通过的（它自己解析快照→实例）。
//
// 取舍：这里只做「拿到一个可用于 scope 判定的 id」。对 snapshotId 这类**非实例 id**，
// 用它去判实例 scope 会偏严（可能误拒）——因此宁可显式承认这一点：
// 若工具 schema 里没有 `id`，就退回按它的首个 `*Id` 数字参数判定，
// 真正的归属解析仍由工具内部（它持有 deps）完成。
// 复查条件：新增「实例不是 id 参数」的工具时，检查这里是否取到了合适的 id。
func approvalTargetID(name string, args map[string]any) (uint, bool) {
	if args == nil {
		return 0, false
	}
	if id, err := toUint(args["id"]); err == nil && id != 0 {
		return id, true
	}
	spec, ok := findToolSpec(name)
	if !ok {
		return 0, false
	}
	// 按 schema 属性名稳定取值（map 遍历无序，排序后取，保证同一请求结果确定）。
	names := make([]string, 0, 4)
	if props, ok := spec.Def.InputSchema["properties"].(map[string]any); ok {
		for k := range props {
			lower := strings.ToLower(k)
			if lower == "id" || !strings.HasSuffix(lower, "id") {
				continue
			}
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		if id, err := toUint(args[k]); err == nil && id != 0 {
			return id, true
		}
	}
	return 0, false
}
