package service

/* *
 *高危审批登记表的守门用例。
 *
 * 这组断言的价值不在于「表里有什么」，而在于**防止表与真实工具目录漂移**：
 * 新增一个有副作用的 MCP 工具却忘了登记审批，是最危险的静默放行。
 * 而那种遗漏在运行期完全看不出来——工具照常工作，只是不再需要批准。
 */

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApprovalRegistry_CoversPrdNamedBypassHoles PRD 点名的三类绕过口子必须在表内。
//
// 这三条是 PRD 逐字点名的，任何一条漏掉都意味着有一条已知提权路径没被堵。
func TestApprovalRegistry_CoversPrdNamedBypassHoles(t *testing.T) {
	// 口子①：文件写入的完整绕过路径
	require.Contains(t, approvalRequiredActions, "file_issue_transfer_ticket",
		"签发票据+流式上传可写盘，是 file_write_text 的完整绕过路径，必须进审批")

	// 口子②：控制台命令 ≡ 任意代码执行
	require.Contains(t, approvalRequiredActions, "instance_send_command",
		"控制台命令等价任意代码执行，必须进审批")

	// 口子③：三者均等价「下载并执行任意二进制」
	for _, action := range []string{
		"instance_update_config",    // 改 startCommand
		"instance_binary_upgrade",   // 换二进制
		"instance_binary_rollback",  // 换二进制
		"instance_provision_server", // 远程 URL 二进制来源
	} {
		require.Contains(t, approvalRequiredActions, action,
			"%s 等价下载并执行任意二进制，必须进审批", action)
	}

	// 覆盖数据
	require.Contains(t, approvalRequiredActions, "instance_snapshot_rollback")
}

// TestApprovalRegistry_StopRequiresButStartDoesNot **按 action 粒度**的核心断言。
//
// 三者同属 write，但只有 stop 需审。若实现退化成「按 Operation 分类」，这条会红。
func TestApprovalRegistry_StopRequiresButStartDoesNot(t *testing.T) {
	stop, err := ClassifyApprovalRequirement("instance_stop", true)
	require.NoError(t, err)
	require.True(t, stop.Required, "instance_stop 必须需审")

	start, err := ClassifyApprovalRequirement("instance_start", true)
	require.NoError(t, err)
	require.False(t, start.Required, "instance_start 免审（PRD 要求）")

	restart, err := ClassifyApprovalRequirement("instance_restart", true)
	require.NoError(t, err)
	require.False(t, restart.Required, "instance_restart 免审（PRD 要求）")

	// 三者同为写操作：若按分类实现，三个结果必然相同——这里必须不同。
	require.NotEqual(t, stop.Required, start.Required,
		"start 与 stop 同属 write 但审批要求必须不同，否则说明实现按 Operation 分类了")
}

// TestApprovalRegistry_UnlistedWriteKeepsExistingSemantics 名单外的写操作沿用既有确认语义。
//
// 这是刻意的取舍（spec §3 有记录）：平台有一百多个写操作，若一律要审批，
// 批准人只会闭眼点同意，审批退化成橡皮图章——比不做审批更危险。
// 「漏登记」由测试层保障（见 mcp 包的 `TestRegistryCoversAllDangerousTools`）。
func TestApprovalRegistry_UnlistedWriteKeepsExistingSemantics(t *testing.T) {
	got, err := ClassifyApprovalRequirement("some_unlisted_write_tool", true)
	require.NoError(t, err, "名单外不报错（沿用既有确认语义）")
	require.False(t, got.Required, "名单外不需要审批")
}

// TestApprovalRegistry_ReadNeverRequires 读操作一律不需审批。
func TestApprovalRegistry_ReadNeverRequires(t *testing.T) {
	for _, action := range []string{"agent_list_instances", "instance_get_metrics", "agent_whoami"} {
		got, err := ClassifyApprovalRequirement(action, false)
		require.NoError(t, err)
		require.False(t, got.Required, "%s 是读操作，不该需审", action)
	}
}

// TestApprovalRegistry_RequiredListHasReason 每条需审项都要有中文说明。
//
// 理由：批准人看到的「操作」一栏直接取自这里；空说明等于让批准人看不到自己在批什么。
func TestApprovalRegistry_RequiredListHasReason(t *testing.T) {
	for action, reason := range approvalRequiredActions {
		require.NotEmpty(t, reason, "%s 缺少审批说明（批准人将看不到风险描述）", action)
		require.GreaterOrEqual(t, len([]rune(reason)), 6,
			"%s 的说明过短，不足以让批准人理解风险：%q", action, reason)
	}
}

// TestApprovalRegistry_StableOrder 名字列表顺序稳定（供文档与快照断言）。
func TestApprovalRegistry_StableOrder(t *testing.T) {
	a := ApprovalRequiredActionNames()
	b := ApprovalRequiredActionNames()
	require.Equal(t, a, b)
	// 有序：便于人工 diff 与文档生成。
	for i := 1; i < len(a); i++ {
		require.Less(t, a[i-1], a[i], "action 名应升序")
	}
}
