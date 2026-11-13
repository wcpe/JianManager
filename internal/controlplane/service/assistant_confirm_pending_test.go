package service

/* *
 *§8「同 run 最多 1 张 pending 票据」的守门用例。
 *
 * 为什么这条约束要放在数据库层而不是应用层「先查再插」：
 * 后者在并发下两个写调用都未命中就会双双插入——与 run 幂等键踩过的是同一个坑。
 * 因此这里的核心断言是**越过 service 直接插第二张也会失败**，
 * 而不是只看 service 有没有返回错误（那证明不了约束真的存在）。
 */

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newConfirmTestDB 建带票据/回执表与 pending 部分唯一索引的测试库。
//
// 索引手工建：真库由 database 包在 AutoMigrate 之后建，但那是另一个包且未导出；
// 若这里不建，「约束是否存在」就无从断言——本组用例的全部价值正在于此。
func newConfirmTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newAssistantChatTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.AssistantConfirmation{},
		&model.AssistantReceipt{},
	))
	require.NoError(t, db.Exec(
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_assistant_confirmations_pending "+
			"ON assistant_confirmations (run_id) WHERE status = 'pending'").Error)
	return db
}

// seedPending 造一张 pending 票据。
func seedPending(t *testing.T, db *gorm.DB, runID uint, callID string) *model.AssistantConfirmation {
	t.Helper()
	tk := &model.AssistantConfirmation{
		Source:         model.AssistantConfirmSourceChat,
		OwnerUserID:    1,
		ConversationID: 10,
		RunID:          runID,
		CallID:         callID,
		ToolName:       "system.agent.instance_start",
		ArgsHash:       "h",
		ArgsFrozen:     "{}",
		Policy:         model.AssistantPolicySelfConfirm,
		Status:         model.AssistantConfirmStatusPending,
		ExpiresAt:      time.Now().Add(time.Minute),
	}
	require.NoError(t, db.Create(tk).Error)
	return tk
}

// TestAssistantConfirmation_DBRejectsSecondPendingPerRun 数据库层必须拦住同 run 第二张 pending。
//
// 这条**绕过 service 直接插入**：若只断言 service 返回错误，实现退化成
// 「先查再插」也能通过，而那个窗口在并发下会放开。
func TestAssistantConfirmation_DBRejectsSecondPendingPerRun(t *testing.T) {
	db := newConfirmTestDB(t)
	seedPending(t, db, 7, "call-a")

	second := &model.AssistantConfirmation{
		Source: model.AssistantConfirmSourceChat, OwnerUserID: 1, ConversationID: 10,
		RunID: 7, CallID: "call-b", ToolName: "system.agent.node_maintenance_enter",
		ArgsHash: "h2", ArgsFrozen: "{}", Policy: model.AssistantPolicySelfConfirm,
		Status: model.AssistantConfirmStatusPending, ExpiresAt: time.Now().Add(time.Minute),
	}
	err := db.Create(second).Error
	require.Error(t, err, "同 run 第二张 pending 必须被数据库拒绝")

	// 反向校验：不是「什么都插不进去」——换个 run 必须能插。
	ok := &model.AssistantConfirmation{
		Source: model.AssistantConfirmSourceChat, OwnerUserID: 1, ConversationID: 10,
		RunID: 8, CallID: "call-c", ToolName: "system.agent.instance_start",
		ArgsHash: "h3", ArgsFrozen: "{}", Policy: model.AssistantPolicySelfConfirm,
		Status: model.AssistantConfirmStatusPending, ExpiresAt: time.Now().Add(time.Minute),
	}
	require.NoError(t, db.Create(ok).Error, "不同 run 的 pending 应允许并存")

	// 终态不受约束：同 run 可以累积多条已决策票据（审计需要保留历史）。
	done := &model.AssistantConfirmation{
		Source: model.AssistantConfirmSourceChat, OwnerUserID: 1, ConversationID: 10,
		RunID: 7, CallID: "call-d", ToolName: "system.agent.instance_start",
		ArgsHash: "h4", ArgsFrozen: "{}", Policy: model.AssistantPolicySelfConfirm,
		Status: model.AssistantConfirmStatusConsumed, ExpiresAt: time.Now().Add(time.Minute),
	}
	require.NoError(t, db.Create(done).Error, "终态票据不该被 pending 索引约束")
}

// TestIssueConfirmation_SecondWriteToolRejected 同一 run 的第二个写调用返回明确错误。
//
// 场景：模型在一个工具轮次里回了两个写调用。此时 run 只能表达「等待确认」一个状态，
// 多张卡片的批准顺序无从界定，因此第二个必须被明确拒绝（而非静默堆给用户）。
func TestIssueConfirmation_SecondWriteToolRejected(t *testing.T) {
	db := newConfirmTestDB(t)
	seedPending(t, db, 42, "call-existing")

	svc := NewAssistantChatService(db, nil)
	ex := &assistantDelegatedExecutor{svc: svc, runID: 42, conversationID: 10, owner: 1}

	injectTestMCPCatalog(t)
	tool := &AssistantToolAction{
		Name: assistantToolName(AgentActionNodeMaintenanceEnter), Action: AgentActionNodeMaintenanceEnter,
		Operation: "write", RequiresConfirm: true, ResourceType: "node",
	}
	_, err := ex.issueConfirmation(&UserAccess{UserID: 1, AuthVersion: 1}, tool,
		ai.ToolCall{CallID: "call-new", ToolName: tool.Name, Args: `{"nodeId":1}`},
		map[string]any{"nodeId": float64(1)}, "node", "1")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAssistantConfirmPendingExists),
		"应返回 ErrAssistantConfirmPendingExists，得到 %v", err)
	// 错误文案要能直接展示给用户：说清楚「先处理已有那张」。
	require.Contains(t, err.Error(), "待确认")

	// 反向校验：同一个 callId 重试不该被当成「第二张」——它应回读原票据。
	// 否则模型重试同一次调用会得到令人困惑的「已有待确认」。
	// 注意原票据的 callId 是 call-existing，故这里换一个已存在的 run 与 callId 组合验证回读语义。
	ex2 := &assistantDelegatedExecutor{svc: svc, runID: 99, conversationID: 10, owner: 1}
	call := ai.ToolCall{CallID: "call-retry", ToolName: tool.Name, Args: `{"nodeId":1}`}
	args := map[string]any{"nodeId": float64(1)}
	first, err := ex2.issueConfirmation(&UserAccess{UserID: 1, AuthVersion: 1}, tool, call, args, "node", "1")
	require.NoError(t, err)
	again, err := ex2.issueConfirmation(&UserAccess{UserID: 1, AuthVersion: 1}, tool, call, args, "node", "1")
	require.NoError(t, err, "同一 callId 重试应回读原票据而不是报错")
	require.Equal(t, first.ID, again.ID, "重试必须复用同一张票据")
}

// TestConfirmPendingMigration_CollapsesLegacyDuplicates 存量同 run 多张 pending 会被归正。
//
// 早期版本不限制这一条，历史库可能已有重复行；直接建索引会让 CP 启动失败。
// 归正的取舍：只保留最新一张，其余标 invalidated。那些票据从未被确认过，
// 标失效不丢「用户已批准」的事实（已批准的票据状态不是 pending）。
func TestConfirmPendingMigration_CollapsesLegacyDuplicates(t *testing.T) {
	db := newAssistantChatTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.AssistantConfirmation{}, &model.AssistantReceipt{}))
	// 不建索引，模拟旧库：允许先插多张 pending。
	for _, callID := range []string{"a", "b", "c"} {
		tk := &model.AssistantConfirmation{
			Source: model.AssistantConfirmSourceChat, OwnerUserID: 1, ConversationID: 10,
			RunID: 5, CallID: callID, ToolName: "system.agent.instance_start",
			ArgsHash: "h", ArgsFrozen: "{}", Policy: model.AssistantPolicySelfConfirm,
			Status: model.AssistantConfirmStatusPending, ExpiresAt: time.Now().Add(time.Minute),
		}
		require.NoError(t, db.Create(tk).Error)
	}

	// 这里复刻迁移的归正逻辑（database 包的那份未导出）。
	// 若两处逻辑分叉，database 包的用例会先红——本用例守的是「归正后索引可建」。
	var dupRunIDs []uint
	require.NoError(t, db.Model(&model.AssistantConfirmation{}).
		Where("status = ?", model.AssistantConfirmStatusPending).
		Group("run_id").Having("COUNT(*) > 1").Pluck("run_id", &dupRunIDs).Error)
	require.Len(t, dupRunIDs, 1, "应识别出 1 个有重复的 run")
	for _, runID := range dupRunIDs {
		var keepID uint
		require.NoError(t, db.Model(&model.AssistantConfirmation{}).
			Where("run_id = ? AND status = ?", runID, model.AssistantConfirmStatusPending).
			Order("id DESC").Limit(1).Pluck("id", &keepID).Error)
		require.NoError(t, db.Model(&model.AssistantConfirmation{}).
			Where("run_id = ? AND status = ? AND id <> ?", runID, model.AssistantConfirmStatusPending, keepID).
			Update("status", model.AssistantConfirmStatusInvalidated).Error)
	}

	// 归正后索引必须能建成功——这正是迁移的验收点。
	err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS ux_assistant_confirmations_pending " +
		"ON assistant_confirmations (run_id) WHERE status = 'pending'").Error
	require.NoError(t, err, "归正后建索引不应失败")

	// 且只剩一张 pending，保留的是 id 最大那张（最新）。
	var pending []model.AssistantConfirmation
	require.NoError(t, db.Where("run_id = ? AND status = ?", 5, model.AssistantConfirmStatusPending).
		Find(&pending).Error)
	require.Len(t, pending, 1, "只应剩一张 pending")
	require.Equal(t, "c", pending[0].CallID, "保留的应是最新那张")

	// 归正不能删行：三条都还在，只是两条变 invalidated（审计需要）。
	var all []model.AssistantConfirmation
	require.NoError(t, db.Where("run_id = ?", 5).Find(&all).Error)
	require.Len(t, all, 3, "归正只改状态，不得删除审计记录")
}

// TestConfirmErrorCode_PendingExistsOnIssueFailure 出票失败要留回执并带专属错误码。
//
// 不写回执会让这次调用在审计里凭空消失，而模型已收到错误结果——两边对不上。
func TestConfirmErrorCode_PendingExistsOnIssueFailure(t *testing.T) {
	db := newConfirmTestDB(t)
	// execute() 会先装载 UserAccess，因此需要真实用户行；不需要权限节点，
	// 本用例走到的是「出票失败」，能力位判定在它之前（instance/ node 维护要求 node.operate，
	// 这里给平台管理员身份即可短路）。
	// execute() 会先装载 UserAccess，因此需要真实用户行与整套权限表
	// （EffectiveForUser 会读 role_templates / user_permission_overrides）。
	// 给平台管理员身份即可短路能力位判定（本用例要测的是「出票失败」，不是权限）。
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.UserRoleBinding{}, &model.RoleTemplate{}, &model.UserPermissionOverride{},
		// resolveToolTarget 对 node 目标会真查节点行。
		&model.Node{},
	))
	require.NoError(t, db.Create(&model.Node{
		ID: 1, UUID: "n-confirm", Name: "n-confirm", Host: "127.0.0.1",
		GRPCPort: 9000, WSPort: 9001, Secret: "s",
	}).Error)
	require.NoError(t, db.Create(&model.User{
		ID: 1, Username: "acc", Role: model.RolePlatformAdmin, Status: model.UserStatusActive,
	}).Error)
	seedPending(t, db, 77, "call-first")

	svc := NewAssistantChatService(db, nil)
	ex := &assistantDelegatedExecutor{svc: svc, runID: 77, conversationID: 10, owner: 1,
		sink: nil}

	injectTestMCPCatalog(t)
	// 走**真实路径**：写操作现在经 call_tool 转交，由 metaCallTool 出票。
	// 旧路径（直接点业务工具名）已被改成「拒绝并指路」，不再产生回执。
	out := ex.execute(t.Context(), 1, ai.ToolCall{
		CallID: "call-second", ToolName: assistantMetaToolCall,
		Args: fmt.Sprintf(`{"name":%q,"arguments":{"nodeId":1}}`, assistantToolName(AgentActionNodeMaintenanceEnter)),
	})
	require.Error(t, out.Err)

	var receipts []model.AssistantReceipt
	require.NoError(t, db.Where("run_id = ? AND call_id = ?", 77, "call-second").
		Find(&receipts).Error)
	require.Len(t, receipts, 1, "出票失败必须留下一条回执")
	require.Equal(t, model.AssistantReceiptFailed, receipts[0].Outcome)
	require.Equal(t, "PENDING_EXISTS", receipts[0].ErrorCode)
	require.True(t, strings.Contains(receipts[0].ErrorMessage, "待确认"))
}
