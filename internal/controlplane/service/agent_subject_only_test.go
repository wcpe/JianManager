package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Agent 专属动作的边界（人类禁止申请传输票据）。
//
// 【这组用例守的是什么】「人类申请不了传输票据」在今天之所以成立，靠的是
// 「没人给人类侧加过入口」这个巧合，而不是任何一条可断言的约束。
// 把边界写成 AgentSubjectOnly 数据之后，未来的三件事都会被这里挡住：
//   1. 有人给人类侧加「顺便也能申请」的入口 → 出票拦截用例红；
//   2. 有人新增同类 action 却忘了标记 → 覆盖度用例红；
//   3. 有人为了让人类能用而放宽该标记 → 目录断言红。

func TestAgentSubjectOnly_TransferTicketIsAgentOnly(t *testing.T) {
	d, ok := DescribeAgentAction(AgentActionFileIssueTransferTicket)
	require.True(t, ok, "传输票据必须在 action 目录中")
	require.True(t, d.AgentSubjectOnly,
		"传输票据的凭据只能由 Agent Token 消费（Consume 按 TokenID 重建主体），人类不可申请")
}

// TestAgentSubjectOnly_EveryIssueActionDecidesExplicitly 覆盖度门禁。
//
// 凡需审批、且名字里含 issue 的 action，都必须能在目录里查到条目——
// 也就是说作者确实把它登记过并决定了 AgentSubjectOnly 的取值。
//
// 判据用词根而不是穷举名单，理由与 DangerousToolNames 一致：
// 手写完整列表会随新工具漂移，而这正是要防的事。
func TestAgentSubjectOnly_EveryIssueActionDecidesExplicitly(t *testing.T) {
	for toolName := range approvalRequiredActions {
		if !strings.Contains(toolName, "issue") {
			continue
		}
		action := "agent." + toolName
		d, ok := DescribeAgentAction(action)
		require.True(t, ok, "需审的 issue 类工具必须有目录条目: %s", toolName)
		// 不强制为 true（未来可能有 Agent 与人类都可用的签发类动作），
		// 只要求它能被查到——说明作者确实把取值想过一遍。
		require.Equal(t, action, d.Action)
	}
}
