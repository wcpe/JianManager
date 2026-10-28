package mcp

/**
 * 审批登记表与**真实工具目录**的一致性（FR-492 的防漂移门禁）。
 *
 * 这是「漏登记」唯一的保障点。因为运行期不拦截全部写操作（那会让审批变成橡皮图章），
 * 所以必须在这里用编译期可见的方式兜住：新增一个看起来危险的写工具却忘了进审批名单，
 * 这条测试就会红——而不是等到某天有人发现某个 delete 工具从来不需要批准。
 */

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// TestRegistryCoversAllDangerousTools 名字看起来危险的写工具必须进审批名单。
//
// 允许的例外要写在这里并给出理由（fail-closed 于「新工具必须有决定」）：
// 每个例外都必须在 exemptFromApproval 里显式列出。
func TestRegistryCoversAllDangerousTools(t *testing.T) {
	// 显式例外：既有「参数名精确确认」已足够，或操作本身可逆且不影响数据。
	exemptFromApproval := map[string]string{
		// Bot / 压测模板 / 制品删除：已有 confirmXxxName 精确确认（FR-396 语义）。
		"bot_delete":               "已有 confirmBotName 精确确认",
		"loadtest_template_delete": "已有 confirmTemplateName 精确确认",
		"instance_group_delete":    "已有 confirmGroupName 精确确认",
		"network_delete":           "已有 confirmName 精确确认",
		"plugin_delete":            "已有 confirmName 精确确认",
		"files_delete":             "已有 confirm 语义（文件级显式路径确认）",
		// 文件写入本身已被硬拒（不在工具目录），其绕过路径已在审批名单里。
		"config_write_fields": "配置写入为常规运维操作，非不可逆",
		// 只读/查询类名字里恰好含 update/restore/archived 等词根的，不是写操作。
		"node_list_archived":      "只读查询（名字含 archived 但无副作用）",
		"instance_quota_status":   "只读查询",
		"metrics_series_query":    "只读查询",
		"history_commands_list":   "只读查询",
		"message_payload_request": "只读申请（正文另需审批消费）",
		"asset_preview_request":   "只读申请",
		"approvals_own_withdraw":  "撤回自己的申请，不产生副作用",
		// 补 `maintenance` 词根后新增的匹配项：离开维护是**恢复**可用性，
		// 与进入维护（会停服）方向相反，不需要审批。
		// 注意这条例外是「补词根」的副作用而非新工具——写在这里也顺便记录了
		// 「词根匹配会把一对正反操作一起圈进来」这一事实，将来再补词根时值得留意。
		"node_maintenance_leave": "离开维护是恢复操作，不产生副作用",
	}

	dangerous := DangerousToolNames()
	require.NotEmpty(t, dangerous, "危险工具枚举为空，断言失去意义")

	var missing []string
	for _, name := range dangerous {
		if _, ok := exemptFromApproval[name]; ok {
			continue
		}
		// 已在审批名单里 → 通过。
		if service.ApprovalDecisionFor(name) != "" {
			continue
		}
		// 不在审批名单且不在例外表 → 漏登记。
		missing = append(missing, name)
	}
	require.Empty(t, missing,
		"以下看起来危险的工具既不在审批名单、也不在例外表：%v\n"+
			"要么加进 approvalRequiredActions（需审批），要么在 exemptFromApproval 里写明理由。",
		missing)
}

// TestApprovalRegistryCoversAllPrdNamedTools PRD 点名的工具必须在审批名单里。
//
// 与上一条互补：那条防「新增危险工具漏登记」，这条防「PRD 明确要求的被删掉」。
func TestApprovalRegistryCoversAllPrdNamedTools(t *testing.T) {
	// PRD FR-492 逐字点名的必须覆盖项。
	prdNamed := []string{
		"file_issue_transfer_ticket",
		"instance_send_command",
		"instance_update_config",
		"instance_binary_upgrade",
		"instance_binary_rollback",
		"instance_provision_server",
		"instance_snapshot_rollback",
		"instance_stop",
	}
	for _, name := range prdNamed {
		// 确认该工具确实存在于目录里（否则断言的是个不存在的名字）。
		_, ok := toolActionByName(name)
		require.True(t, ok, "PRD 点名的工具 %q 不在真实工具目录里，名字可能写错", name)
		require.NotEmpty(t, service.ApprovalDecisionFor(name),
			"PRD 点名要求审批的 %q 不在审批名单里", name)
	}
}

// TestApprovalRegistryEntriesExistInCatalog 审批名单里的每个名字都要是真工具。
//
// 防的是反向漂移：工具改名/删除后名单里留下幽灵条目，看起来「已审批」实际永远不命中。
func TestApprovalRegistryEntriesExistInCatalog(t *testing.T) {
	names := service.ApprovalRequiredActionNames()
	require.NotEmpty(t, names)
	var ghosts []string
	for _, name := range names {
		if _, ok := toolActionByName(name); !ok {
			ghosts = append(ghosts, name)
		}
	}
	sort.Strings(ghosts)
	require.Empty(t, ghosts,
		"审批名单里存在工具目录中不存在的条目（改名或删除后遗留的幽灵条目）：%v", ghosts)
}

// TestApprovalIntercept_StopProducesTicketAndStartDoesNot 端到端：stop 出票、start 不出票。
//
// 这是 FR-492 的行为门禁——不只看判定函数，而是走真实 CallTool 路径。
func TestApprovalIntercept_StopProducesTicketAndStartDoesNot(t *testing.T) {
	deps := newApprovalTestDeps(t)
	p := &service.AgentPrincipal{
		TokenID:           1,
		ScopedInstanceIDs: []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife},
	}

	// instance_stop：应出票、不执行。
	res := CallTool(t.Context(), deps, p, "instance_stop", map[string]any{"id": float64(1)})
	require.False(t, res.IsError, "需审操作应返回受理而不是错误：%s", firstText(res))
	require.Contains(t, firstText(res), "awaiting_approval")
	require.Contains(t, firstText(res), "requestId")

	// instance_start：不应出票（免审，继续走工具本身）。
	res2 := CallTool(t.Context(), deps, p, "instance_start", map[string]any{"id": float64(1)})
	require.NotContains(t, firstText(res2), "awaiting_approval",
		"instance_start 不该产生审批票据")
}

// firstText 取 ToolResult 的首段文本（便于断言）。
func firstText(r ToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

var _ = strings.Contains

// TestApprovalIntercept_V2NodeScopeInheritsInstance 守住「节点 scope 继承实例」在出票前授权里生效。
//
// 这是真机验收中踩到的缺陷（修复前必失败）：拦截阶段用 targetForApproval 构造授权目标，
// 实例类只填了 InstanceID、没填 NodeID；而 principalCanAccessInstance 只在 nodeID != 0
// 时才会去匹配 ScopedNodeIDs。于是给**节点**签发的 V2 Token 对该节点上的每个实例都被判越权，
// 所有实例类高危 action 的申请在出票前就被拒成笼统的「能力/scope 不足」——
// 表现为「审批功能整体不可用」，实际只是授权目标缺了归属字段。
//
// 既有用例用的是 V1 的 ScopedInstanceIDs，正好绕开了这条路径，所以没能发现它。
func TestApprovalIntercept_V2NodeScopeInheritsInstance(t *testing.T) {
	db := newStatelessTestDB(t)
	// 审批表也要 migrate：本用例要验的是「授权通过后能出票」，
	// 不迁表会停在 SubmitApproval 的 SQL 错误上，掩盖授权结论。
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.ApprovalTicket{}, &model.ApprovalReceipt{}))
	// 实例 7 归属节点 3；Token 只签了节点 3 的 scope（不含实例 id）。
	require.NoError(t, db.Create(&model.Instance{
		ID: 7, Name: "acc-inst", NodeID: 3, Type: "generic",
	}).Error)

	deps := ToolDeps{
		Approval: service.NewApprovalService(db),
		Agent:    service.NewAgentTokenService(db),
	}
	p := &service.AgentPrincipal{
		TokenID:       9,
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{3},
		Capabilities:  []string{service.AgentCapabilityInstanceLife},
	}

	res := CallTool(t.Context(), deps, p, "instance_stop", map[string]any{"id": float64(7)})
	require.False(t, res.IsError,
		"节点 scope 应继承实例：申请应出票而不是被判越权。实际返回：%s", firstText(res))
	require.Contains(t, firstText(res), "awaiting_approval")
}

// TestApprovalIntercept_BotTargetUsesBotOwnership 守住「Bot 类目标的 id 语义」。
//
// 这是真机验收中踩到的缺陷（修复前必失败）：`bot_send_command` 的 `id` 参数是
// **Bot 的 id**，但拦截阶段把非 node 目标一律交给 `ResolveInstanceTarget`，
// 于是拿 Bot id 去查实例——
//   - Bot id 恰好等于某个实例 id 时**碰巧通过**（这正是它在测试/演示环境长期潜伏的原因，
//     那里 bot.id 与 instance.id 常常都是 1）；
//   - 不相等时解析失败、回退成 `{InstanceID: botID}`、再被判越权，
//     对外只剩笼统的「能力/scope 不足」。平台管理员在自己完全可访问的实例上
//     发一条 Bot 命令也会被拒，而错误信息完全不指向真实原因。
//
// 本用例刻意让 **bot.id(2) ≠ instance.id(1)**，且把 Bot 挂在实例 1 上：
// 只有真正按 Bot 归属解析，授权才会通过。
func TestApprovalIntercept_BotTargetUsesBotOwnership(t *testing.T) {
	db := newStatelessTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Bot{}, &model.ApprovalTicket{}, &model.ApprovalReceipt{},
	))
	// 实例 1；Bot 2 挂在实例 1 上 —— id 刻意不相等。
	require.NoError(t, db.Create(&model.Instance{
		ID: 1, Name: "host-inst", NodeID: 1, Type: "generic",
	}).Error)
	require.NoError(t, db.Create(&model.Bot{
		ID: 2, UUID: "11111111-2222-3333-4444-555555555555",
		InstanceID: 1, Name: "host-bot",
	}).Error)

	deps := ToolDeps{
		Approval: service.NewApprovalService(db),
		Agent:    service.NewAgentTokenService(db),
		Bot:      service.NewBotService(db, nil),
	}
	// 只签实例 1 的 scope：Bot 2 属于实例 1，因此应当放行。
	p := &service.AgentPrincipal{
		TokenID:           9,
		PolicyVersion:     service.AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{service.AgentCapabilityBotManage},
	}

	res := CallTool(t.Context(), deps, p, "bot_send_command",
		map[string]any{"id": float64(2), "command": "say hi"})

	// 断言「出票」而不是「越权被拒」。
	require.False(t, res.IsError,
		"Bot 2 属于 scope 内的实例 1，申请应出票。若这里失败，说明又在拿 bot id 当实例 id 查——实际返回：%s",
		firstText(res))
	require.Contains(t, firstText(res), "awaiting_approval")

	// 反向断言：Bot 3 挂在**scope 外**的实例 9 → 必须被拒（不能因为修好归属就放宽范围）。
	require.NoError(t, db.Create(&model.Instance{
		ID: 9, Name: "outside-inst", NodeID: 2, Type: "generic",
	}).Error)
	require.NoError(t, db.Create(&model.Bot{
		ID: 3, UUID: "99999999-2222-3333-4444-555555555555",
		InstanceID: 9, Name: "outside-bot",
	}).Error)
	resOutside := CallTool(t.Context(), deps, p, "bot_send_command",
		map[string]any{"id": float64(3), "command": "say hi"})
	require.True(t, resOutside.IsError,
		"scope 外实例上的 Bot 必须被拒，实际返回：%s", firstText(resOutside))
}
