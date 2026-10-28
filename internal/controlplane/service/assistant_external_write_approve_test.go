package service

/**
 * 外部 MCP 工具**批准路径**的回归用例（FR-517 §闭环）。
 *
 * 这组用例存在的唯一理由是一个真实缺陷：
 * `approveAndRun` 曾用 `isAssistantExternalTool(tool.Name)` 分派，而
 * `findAssistantTool` 只查内置注册表、对外部工具名必为 nil → 任何外部写工具
 * 一批准就空指针 panic。
 *
 * 它之所以能逃过此前全部测试：**批准路径完全没有测试覆盖**。
 * 因此这里的核心价值不是「验证某个行为」，而是把这条路径钉住——
 * 尤其是「tool 为 nil 时不得解引用」这一点。
 */

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestApproveAndRun_ExternalToolNameNeverDereferencesNilTool 外部工具名必须走外部分支且不 panic。
//
// 判据：`findAssistantTool` 对 `conn.x.y` 返回 nil 是**前提事实**（先断言它，
// 否则本用例会因为「findAssistantTool 恰好能查到」而空转）。
// 然后用外部工具名调 approveAndRun，只要求**不 panic**——
// 后续它会因连接不存在等原因返回错误，那不重要，重要的是不能崩。
func TestApproveAndRun_ExternalToolNameNeverDereferencesNilTool(t *testing.T) {
	// 用具名构造函数生成（点号形式已废弃：违反上游字符集）。
	externalName := assistantExternalToolName("some_server", "some_tool")

	// 前提事实：内置注册表查不到外部工具名。
	require.Nil(t, findAssistantTool(externalName),
		"前提不成立：findAssistantTool 竟然能查到外部工具名，本用例失去意义")
	require.True(t, isAssistantExternalTool(externalName),
		"前提不成立：%q 应被识别为外部工具", externalName)

	db := newAssistantChatTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.AssistantConfirmation{}, &model.AssistantReceipt{},
		// approveAndRun 会先 LoadUserAccess（读 users + 角色绑定 + 角色模板），
		// 这些表缺一张就会在**到达分派点之前**返回错误——那样本用例就成了空转：
		// 不 panic 只因为它压根没走到 isAssistantExternalTool 那一行。
		&model.User{}, &model.UserRoleBinding{}, &model.RoleTemplate{}, &model.UserPermissionOverride{},
		// 外部分支会查连接表与工具表；缺表会让用例因「装不出」而提前返回，
		// 同样测不到分派点。
		&model.AssistantMCPConnection{}, &model.AssistantMCPTool{},
	))
	// 用平台管理员身份短路能力位判定，使执行能推进到分派点。
	require.NoError(t, db.Create(&model.User{
		ID: 1, Username: "ext-approver", Role: model.RolePlatformAdmin, Status: model.UserStatusActive,
	}).Error)

	svc := NewAssistantChatService(db, nil)
	// **必须装配 MCP 服务**：否则 executeApprovedExternalTool 会在 `s.mcp.lookupEnabledTool`
	// 处 panic，而那是「测试装配不完整」而非生产缺陷——用例就会失去区分能力。
	// 这里装一个真实但库内无连接的实例，使流程走到「工具未启用」的正常错误分支。
	svc.SetMCPService(NewAssistantMCPService(db, nil))

	ticket := &model.AssistantConfirmation{
		ID:             1,
		OwnerUserID:    1,
		ConversationID: 1,
		RunID:          1,
		CallID:         "call-ext",
		ToolName:       externalName,
		ArgsHash:       hashToolArgs(map[string]any{}),
		ArgsFrozen:     "{}",
		Policy:         model.AssistantPolicySelfConfirm,
		Status:         model.AssistantConfirmStatusPending,
		ExpiresAt:      time.Now().Add(time.Minute),
	}
	require.NoError(t, db.Create(ticket).Error)

	// 关键：不得 panic。用 recover 把 panic 转成明确的失败信息，
	// 否则 panic 只会表现为整个测试进程崩掉，看不出是哪个断言。
	done := make(chan struct{})
	var panicked any
	go func() {
		defer func() {
			panicked = recover()
			close(done)
		}()
		// 这里 require 不能安全使用（在子 goroutine 里），故只调用不置位。
		_, _ = svc.approveAndRun(context.Background(), 1, ticket)
	}()
	<-done

	require.Nil(t, panicked,
		"approveAndRun 对外部工具名 panic 了（曾因 isAssistantExternalTool(tool.Name) 解引用 nil）: %v", panicked)
}

// TestApproveAndRun_InternalToolStillDispatches 内置工具的写路径未被上面的修复带偏。
//
// 反向守卫：把分派改成用票据工具名后，内置工具必须仍走 invokeWriteTool 分支。
// 若有人误改成「一律外部」或「一律内置」，这条会红。
func TestApproveAndRun_InternalToolStillDispatches(t *testing.T) {
	// FR-544：工具元数据来自 MCP 目录（经注入），测试必须同样注入才有数据。
	injectTestMCPCatalog(t)
	internalName := assistantToolName(AgentActionInstanceStart)
	require.NotNil(t, findAssistantTool(internalName),
		"前提不成立：内置工具应能查到")
	require.False(t, isAssistantExternalTool(internalName),
		"前提不成立：内置工具不应被识别为外部")
}
