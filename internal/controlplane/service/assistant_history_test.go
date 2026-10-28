package service

import (
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 历史编辑与回退（FR-543）。核心断言都围绕「不删数据、可解释、不越权」。
func newHistoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openTestSQLite(t)
	if err := db.AutoMigrate(&model.AssistantConversation{}, &model.AssistantMessage{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 每个用例独立：内存库共享，先清空。
	db.Exec("DELETE FROM assistant_messages")
	db.Exec("DELETE FROM assistant_conversations")
	return db
}

func seedConversation(t *testing.T, db *gorm.DB, owner uint, activeRun *uint) *model.AssistantConversation {
	t.Helper()
	conv := &model.AssistantConversation{OwnerUserID: owner, Title: "测试会话", ActiveRunID: activeRun}
	if err := db.Create(conv).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	return conv
}

func seedMessage(t *testing.T, db *gorm.DB, convID uint, role string, turn int, content string) *model.AssistantMessage {
	t.Helper()
	m := &model.AssistantMessage{
		ConversationID: convID, Role: role, Turn: turn, Content: content,
		State: model.AssistantMessageStateComplete,
	}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("建消息失败: %v", err)
	}
	return m
}

// TestEditUserMessage_KeepsOriginal 编辑只改内容并保留原文，不覆盖历史。
func TestEditUserMessage_KeepsOriginal(t *testing.T) {
	db := newHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	conv := seedConversation(t, db, 7, nil)
	msg := seedMessage(t, db, conv.ID, model.AssistantMessageRoleUser, 1, "原问题")

	got, err := svc.EditUserMessage(7, msg.ID, "更正后的问题")
	if err != nil {
		t.Fatalf("编辑应成功: %v", err)
	}
	if got.Content != "更正后的问题" {
		t.Fatalf("内容应被更新，实际 %q", got.Content)
	}
	if got.OriginalContent == nil || *got.OriginalContent != "原问题" {
		t.Fatalf("首次编辑必须保留原文，实际 %v", got.OriginalContent)
	}
	if got.EditCount != 1 || got.EditedAt == nil {
		t.Fatalf("编辑计数与时间应记录，实际 count=%d at=%v", got.EditCount, got.EditedAt)
	}

	// 第二次编辑：原文**不得**被第二次覆盖成「更正后的问题」。
	if _, err := svc.EditUserMessage(7, msg.ID, "再改一次"); err != nil {
		t.Fatalf("二次编辑应成功: %v", err)
	}
	var after model.AssistantMessage
	if err := db.First(&after, msg.ID).Error; err != nil {
		t.Fatalf("查回失败: %v", err)
	}
	if after.OriginalContent == nil || *after.OriginalContent != "原问题" {
		t.Fatalf("原文应始终是最初一版，实际 %v", after.OriginalContent)
	}
	if after.EditCount != 2 {
		t.Fatalf("编辑次数应为 2，实际 %d", after.EditCount)
	}
}

// TestEditUserMessage_Rejects 只允许改自己的 user 消息。
func TestEditUserMessage_Rejects(t *testing.T) {
	db := newHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	conv := seedConversation(t, db, 7, nil)
	userMsg := seedMessage(t, db, conv.ID, model.AssistantMessageRoleUser, 1, "x")
	assistantMsg := seedMessage(t, db, conv.ID, model.AssistantMessageRoleAssistant, 2, "y")

	// 他人会话 → 与「不存在」同一错误（不泄露存在性）。
	if _, err := svc.EditUserMessage(999, userMsg.ID, "改"); err != ErrAssistantChatNotFound {
		t.Fatalf("越权应返回 NOT_FOUND，实际 %v", err)
	}
	// assistant 消息不可编辑（回复是历史事实）。
	if _, err := svc.EditUserMessage(7, assistantMsg.ID, "改"); err == nil {
		t.Fatal("assistant 消息不应可编辑")
	}
	// 空内容拒绝。
	if _, err := svc.EditUserMessage(7, userMsg.ID, "   "); err == nil {
		t.Fatal("空内容应被拒绝")
	}
}

// TestRollbackToMessage_SupersedesAfterOnly 只取代锚点之后的消息，锚点本身保留。
func TestRollbackToMessage_SupersedesAfterOnly(t *testing.T) {
	db := newHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	conv := seedConversation(t, db, 7, nil)
	m1 := seedMessage(t, db, conv.ID, model.AssistantMessageRoleUser, 1, "一")
	m2 := seedMessage(t, db, conv.ID, model.AssistantMessageRoleAssistant, 1, "二")
	m3 := seedMessage(t, db, conv.ID, model.AssistantMessageRoleUser, 2, "三")

	affected, err := svc.RollbackToMessage(7, conv.ID, m1.ID)
	if err != nil {
		t.Fatalf("回退应成功: %v", err)
	}
	if affected != 2 {
		t.Fatalf("应取代 2 条（m2、m3），实际 %d", affected)
	}

	var rows []model.AssistantMessage
	if err := db.Where("conversation_id = ?", conv.ID).Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("查回失败: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("不得物理删除任何消息，实际剩 %d 条", len(rows))
	}
	if rows[0].SupersededAt != nil {
		t.Fatal("锚点消息不得被取代（回退到它意味着它仍有效）")
	}
	if rows[1].SupersededAt == nil || rows[2].SupersededAt == nil {
		t.Fatal("锚点之后的消息必须被取代")
	}
	_ = m2
	_ = m3
}

// TestRollbackToMessage_BlockedByActiveRun 有进行中的 run 时拒绝回退。
func TestRollbackToMessage_BlockedByActiveRun(t *testing.T) {
	db := newHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	runID := uint(9)
	conv := seedConversation(t, db, 7, &runID)
	msg := seedMessage(t, db, conv.ID, model.AssistantMessageRoleUser, 1, "x")

	if _, err := svc.RollbackToMessage(7, conv.ID, msg.ID); err != ErrAssistantRollbackBlocked {
		t.Fatalf("活跃 run 期间应拒绝回退，实际 %v", err)
	}
}

// TestListConversationsFiltered_KeywordHitsTitleAndContent 搜索命中标题与正文，且不越权。
func TestListConversationsFiltered_KeywordHitsTitleAndContent(t *testing.T) {
	db := newHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	mine := seedConversation(t, db, 7, nil)
	seedMessage(t, db, mine.ID, model.AssistantMessageRoleUser, 1, "讨论一下 paper 服务端的内存")
	byTitle := &model.AssistantConversation{OwnerUserID: 7, Title: "paper 调优"}
	if err := db.Create(byTitle).Error; err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	// 他人的会话含同一关键词，**不得**出现在结果里。
	other := seedConversation(t, db, 999, nil)
	seedMessage(t, db, other.ID, model.AssistantMessageRoleUser, 1, "paper 别人家的事")

	rows, _, err := svc.ListConversationsFiltered(7, AssistantConversationFilter{Keyword: "paper"}, "", 20)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	got := map[uint]bool{}
	for _, r := range rows {
		got[r.ID] = true
	}
	if !got[mine.ID] || !got[byTitle.ID] {
		t.Fatalf("正文与标题命中都应返回，实际 %v", got)
	}
	if got[other.ID] {
		t.Fatal("不得返回他人会话（检索会变成越权读）")
	}
}

// TestListConversationsFiltered_LikeWildcardEscaped 通配符必须被转义。
//
// 不转义时搜 `100%` 会变成「以 100 开头」，把无关会话全捞出来，
// 而用户只会觉得「搜索不准」。
func TestListConversationsFiltered_LikeWildcardEscaped(t *testing.T) {
	db := newHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	a := &model.AssistantConversation{OwnerUserID: 7, Title: "100% 负载"}
	b := &model.AssistantConversation{OwnerUserID: 7, Title: "1000 台机器"}
	for _, c := range []*model.AssistantConversation{a, b} {
		if err := db.Create(c).Error; err != nil {
			t.Fatalf("建会话失败: %v", err)
		}
	}

	rows, _, err := svc.ListConversationsFiltered(7, AssistantConversationFilter{Keyword: "100%"}, "", 20)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != a.ID {
		t.Fatalf("`100%%` 应只命中字面包含 %% 的会话，实际 %d 条", len(rows))
	}
}

// TestRollbackToMessage_OnlyAffectsTargetConversation 回退不跨会话。
func TestRollbackToMessage_OnlyAffectsTargetConversation(t *testing.T) {
	db := newHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	c1 := seedConversation(t, db, 7, nil)
	c2 := seedConversation(t, db, 7, nil)
	m1 := seedMessage(t, db, c1.ID, model.AssistantMessageRoleUser, 1, "a")
	later := seedMessage(t, db, c1.ID, model.AssistantMessageRoleAssistant, 1, "b")
	otherMsg := seedMessage(t, db, c2.ID, model.AssistantMessageRoleUser, 5, "c")

	if _, err := svc.RollbackToMessage(7, c1.ID, m1.ID); err != nil {
		t.Fatalf("回退应成功: %v", err)
	}
	var o model.AssistantMessage
	if err := db.First(&o, otherMsg.ID).Error; err != nil {
		t.Fatalf("查回失败: %v", err)
	}
	if o.SupersededAt != nil {
		t.Fatal("回退只应影响目标会话，不得波及其它会话")
	}
	_ = later
}
