package service

import (
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 历史工具回显（FR-519）：这些数据一直落在事件流里，此前没有读取面。
// 断言围绕三件事：跨事件正确归并、缺 status 时能从 result 推断出终态、
// 以及「别人的会话读不到」。
func newToolHistoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 库名带 t.Name()：同包 2000+ 用例并行/串行跑时，固定库名会让不同用例
	// 落到同一个共享内存库上互相污染（`cache=shared` 是按库名共享的）。
	// 传 nil Logger（而非 logger.Discard）：Discard 会让 gorm 丢掉 Close 之外的
	// 资源统计，排查连接泄漏时看不到任何东西——静默才是这里最大的成本。
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	// **必须关连接**（本文件此前没有，是连接泄漏的一员）：
	// SQLite 的共享内存库在最后一个连接关闭时才释放；不关就留一个句柄给本进程，
	// 整包跑下来累积到数百个未释放的库句柄，后续用例的 AutoMigrate（要写锁）
	// 就会长时间等锁——表现为「单个用例单独跑秒过，整包跑必卡死」。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.AssistantConversation{}, &model.AssistantMessage{}, &model.AssistantEvent{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// seedToolEvent 落一条工具事件。data 会被序列化成事件的 Data 字段。
func seedToolEvent(t *testing.T, db *gorm.DB, convID uint, runID uint, seq int, typ string, messageID, callID *string, data map[string]any) {
	t.Helper()
	blob, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("序列化事件载荷失败: %v", err)
	}
	var msgPtr *uint
	if messageID != nil {
		var id uint
		if err := db.Model(&model.AssistantMessage{}).Where("id = ?", *messageID).Select("id").Scan(&id).Error; err == nil && id != 0 {
			msgPtr = &id
		}
	}
	ev := &model.AssistantEvent{
		ConversationID: convID,
		RunID:          runID,
		Seq:            seq,
		Type:           typ,
		Data:           string(blob),
		MessageID:      msgPtr,
		CallID:         callID,
	}
	if err := db.Create(ev).Error; err != nil {
		t.Fatalf("写事件失败: %v", err)
	}
}

func toolHistStrPtr(s string) *string { return &s }

// TestListMessageToolCalls_MergesEventsByCall 三类事件应归并成一条记录。
func TestListMessageToolCalls_MergesEventsByCall(t *testing.T) {
	db := newToolHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	conv := seedConversation(t, db, 7, nil)
	msg := seedMessage(t, db, conv.ID, "assistant", 1, "已经处理")
	// 用真实消息 id 作为锚点。
	mid := itoa(msg.ID)

	seedToolEvent(t, db, conv.ID, 1, 1, model.AssistantEventTypeToolCall, &mid, nil, map[string]any{
		"callId": "call_a",
		"name":   "system_agent_node_get",
		// 键必须是 argsJson：生产路径（assistant_chat.go）就是以这个名字落事件的，
		// 写成 args 会让「参数被保留」这条断言假通过为空。
		"argsJson": map[string]any{"id": 3},
	})
	seedToolEvent(t, db, conv.ID, 1, 2, model.AssistantEventTypeToolStatus, nil, toolHistStrPtr("call_a"), map[string]any{
		"callId": "call_a",
		"status": "ok",
	})
	seedToolEvent(t, db, conv.ID, 1, 3, model.AssistantEventTypeToolResult, nil, toolHistStrPtr("call_a"), map[string]any{
		"callId":  "call_a",
		"preview": "节点正常",
	})

	got, err := svc.ListMessageToolCalls(7, conv.ID)
	if err != nil {
		t.Fatalf("读取工具历史失败: %v", err)
	}
	recs := got[msg.ID]
	if len(recs) != 1 {
		t.Fatalf("同一次调用应归并成 1 条记录，实际 %d 条", len(recs))
	}
	rec := recs[0]
	if rec.Name != "system_agent_node_get" {
		t.Fatalf("工具名应来自 tool_call 事件，实际 %q", rec.Name)
	}
	if rec.Status != "ok" {
		t.Fatalf("状态应来自 tool_status 事件，实际 %q", rec.Status)
	}
	if rec.Preview != "节点正常" {
		t.Fatalf("预览应来自 tool_result 事件，实际 %q", rec.Preview)
	}
	if rec.ArgsJSON == "" {
		t.Fatal("调用参数应被保留（排障时要看当时传了什么）")
	}
}

// TestListMessageToolCalls_InfersOkFromResult 缺 status 事件时，有结果就推断 ok。
//
// 为什么要有这个推断：历史数据可能来自 status 落库失败或被截断的轮次，
// 而「有结果」本身就是执行完成的证据。显示成 running（永远转圈）会误导用户。
func TestListMessageToolCalls_InfersOkFromResult(t *testing.T) {
	db := newToolHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	conv := seedConversation(t, db, 7, nil)
	msg := seedMessage(t, db, conv.ID, "assistant", 1, "结果如下")
	mid := itoa(msg.ID)

	seedToolEvent(t, db, conv.ID, 1, 1, model.AssistantEventTypeToolCall, &mid, nil, map[string]any{
		"callId": "call_x",
		"name":   "system_agent_instance_list",
	})
	// 只有结果，没有 status。
	seedToolEvent(t, db, conv.ID, 1, 2, model.AssistantEventTypeToolResult, nil, toolHistStrPtr("call_x"), map[string]any{
		"callId":  "call_x",
		"preview": "3 个实例",
	})

	got, err := svc.ListMessageToolCalls(7, conv.ID)
	if err != nil {
		t.Fatalf("读取工具历史失败: %v", err)
	}
	recs := got[msg.ID]
	if len(recs) != 1 {
		t.Fatalf("应有 1 条记录，实际 %d", len(recs))
	}
	if recs[0].Status != "ok" {
		t.Fatalf("有结果但无 status 时应推断为 ok，实际 %q", recs[0].Status)
	}
}

// TestListMessageToolCalls_RejectsOtherOwner 别人的会话读不到工具历史。
func TestListMessageToolCalls_RejectsOtherOwner(t *testing.T) {
	db := newToolHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	conv := seedConversation(t, db, 7, nil)

	if _, err := svc.ListMessageToolCalls(999, conv.ID); err == nil {
		t.Fatal("非所有者读取他人会话的工具历史必须报错（否则等于泄露「谁对谁做了什么」）")
	}
}

// TestListMessageToolCalls_EmptyWhenNoToolEvents 没有工具事件时回空 map，不回 nil。
func TestListMessageToolCalls_EmptyWhenNoToolEvents(t *testing.T) {
	db := newToolHistoryDB(t)
	svc := NewAssistantChatService(db, nil)
	conv := seedConversation(t, db, 7, nil)
	seedMessage(t, db, conv.ID, "user", 1, "纯聊天")

	got, err := svc.ListMessageToolCalls(7, conv.ID)
	if err != nil {
		t.Fatalf("读取工具历史失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("无工具调用时应回空，实际 %d 组", len(got))
	}
}
