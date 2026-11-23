package database

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newAssistantChatDB 建一个只迁移助手运行时四张表（含手工部分唯一索引）的内存库。
//
// 只迁移需要的表而不是跑全量 AutoMigrate：这些用例验证的是 run 表的索引语义，
// 全量迁移会把无关表的建表开销与无关迁移的失败都混进来。
func newAssistantChatDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.AssistantConversation{},
		&model.AssistantMessage{},
		&model.AssistantRun{},
		&model.AssistantEvent{},
	))
	require.NoError(t, migrateAssistantRunUniqueIndexes(db))
	return db
}

// newRun 构造一条指定会话/状态/幂等键的 run。
func newRun(conversationID, ownerUserID uint, status string, idempotencyKey *string) *model.AssistantRun {
	return &model.AssistantRun{
		ConversationID: conversationID,
		OwnerUserID:    ownerUserID,
		Status:         status,
		ModelSnapshot:  `{"protocol":"openai-chat","modelId":"gpt-4o"}`,
		IdempotencyKey: idempotencyKey,
		Version:        1,
	}
}

// TestAssistantChat_MigrationIdempotent 重复执行建表与建索引不报错（每次 CP 启动都会跑）。
//
// 重启幂等是硬要求：迁移一旦非幂等，第二次启动就起不来。
func TestAssistantChat_MigrationIdempotent(t *testing.T) {
	db := newAssistantChatDB(t)

	// 第二次建表 + 第二次建索引：两个入口都必须幂等。
	require.NoError(t, db.AutoMigrate(
		&model.AssistantConversation{},
		&model.AssistantMessage{},
		&model.AssistantRun{},
		&model.AssistantEvent{},
	), "重复 AutoMigrate 应幂等无错")
	require.NoError(t, migrateAssistantRunUniqueIndexes(db), "重复建索引应幂等无错")

	// 索引确实存在，且列名与手工 DDL 里的字面量一致（防 GORM 蛇形化漂移让 DDL 悄悄建错列）。
	require.True(t, db.Migrator().HasIndex(&model.AssistantRun{}, assistantRunActiveIndexName))
	require.True(t, db.Migrator().HasIndex(&model.AssistantRun{}, assistantRunIdemIndexName))
	require.True(t, db.Migrator().HasColumn(&model.AssistantRun{}, "conversation_id"))
	require.True(t, db.Migrator().HasColumn(&model.AssistantRun{}, "owner_user_id"))
	require.True(t, db.Migrator().HasColumn(&model.AssistantRun{}, "idempotency_key"))
	require.True(t, db.Migrator().HasColumn(&model.AssistantEvent{}, "seq"))
	require.True(t, db.Migrator().HasColumn(&model.AssistantMessage{}, "tool_call_id"))
	require.True(t, db.Migrator().HasColumn(&model.AssistantConversation{}, "active_run_id"))
}

// TestAssistantChat_BlocksSecondActiveRun 同一会话第二条活跃 run 被唯一索引拒绝（§5.2）。
func TestAssistantChat_BlocksSecondActiveRun(t *testing.T) {
	db := newAssistantChatDB(t)

	require.NoError(t, db.Create(newRun(10, 1, model.AssistantRunStatusQueued, nil)).Error)
	err := db.Create(newRun(10, 1, model.AssistantRunStatusQueued, nil)).Error
	require.Error(t, err, "同一会话的第二条 queued run 应被 ux_assistant_runs_active 拒绝")
	require.NotContains(t, err.Error(), "idempotency_key", "应由活跃槽索引而非幂等索引拦下")

	// 不同会话互不影响：约束是「每会话一个槽」而不是「全局一个槽」。
	require.NoError(t, db.Create(newRun(11, 1, model.AssistantRunStatusRunning, nil)).Error)
}

// TestAssistantChat_AllowsNewActiveRunAfterTerminal 首条 run 进入终态后，可再插入活跃 run。
//
// 这正是「部分」索引的意义：普通唯一索引会让一个会话永久只能有一个 run。
func TestAssistantChat_AllowsNewActiveRunAfterTerminal(t *testing.T) {
	db := newAssistantChatDB(t)

	first := newRun(20, 1, model.AssistantRunStatusQueued, nil)
	require.NoError(t, db.Create(first).Error)

	// 走 CAS 口径修改状态（与运行时一致：WHERE id=? AND version=?）。
	res := db.Model(&model.AssistantRun{}).
		Where("id = ? AND version = ?", first.ID, first.Version).
		Updates(map[string]any{"status": model.AssistantRunStatusCompleted, "version": first.Version + 1})
	require.NoError(t, res.Error)
	require.Equal(t, int64(1), res.RowsAffected, "CAS 应命中一行")

	require.NoError(t, db.Create(newRun(20, 1, model.AssistantRunStatusQueued, nil)).Error,
		"首条 run 已终态，应能再建活跃 run")

	// 历史 run 仍在：约束不删除任何行。
	var total int64
	require.NoError(t, db.Model(&model.AssistantRun{}).Where("conversation_id = ?", 20).Count(&total).Error)
	require.Equal(t, int64(2), total)
}

// TestAssistantChat_ActiveStatusesConflict 《active 集合内任意两个状态互斥》：running 与
// waiting_confirmation 不能同时占同一会话的槽（等待确认期间用户不能再提交一轮）。
func TestAssistantChat_ActiveStatusesConflict(t *testing.T) {
	cases := []struct {
		name    string
		first   string
		second  string
		allowed bool
	}{
		{"running 占槽后 waiting_confirmation 被拒", model.AssistantRunStatusRunning, model.AssistantRunStatusWaitingConfirmation, false},
		{"waiting_confirmation 占槽后 running 被拒", model.AssistantRunStatusWaitingConfirmation, model.AssistantRunStatusRunning, false},
		{"waiting_confirmation 占槽后 queued 被拒", model.AssistantRunStatusWaitingConfirmation, model.AssistantRunStatusQueued, false},
		{"终态不占槽：completed 后 running 放行", model.AssistantRunStatusCompleted, model.AssistantRunStatusRunning, true},
		{"终态不占槽：failed 后 waiting_confirmation 放行", model.AssistantRunStatusFailed, model.AssistantRunStatusWaitingConfirmation, true},
		{"failed → cancelled 也放行（两个终态都不占槽）", model.AssistantRunStatusFailed, model.AssistantRunStatusCancelled, true},
		{"running → interrupted 放行（interrupted 是终态）", model.AssistantRunStatusRunning, model.AssistantRunStatusInterrupted, true},
		{"interrupted 收尾后 running 放行", model.AssistantRunStatusInterrupted, model.AssistantRunStatusRunning, true},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newAssistantChatDB(t)
			conversationID := uint(i + 1)
			require.NoError(t, db.Create(newRun(conversationID, 1, c.first, nil)).Error)
			err := db.Create(newRun(conversationID, 1, c.second, nil)).Error
			if c.allowed {
				require.NoError(t, err, "非活跃态不应占用会话槽")
				return
			}
			require.Error(t, err, "同一会话的两个活跃 run 必须冲突")
		})
	}
}

// TestAssistantChat_IdempotencyKeyUnique 幂等键唯一，且空键不参与唯一约束（§6.1）。
func TestAssistantChat_IdempotencyKeyUnique(t *testing.T) {
	db := newAssistantChatDB(t)
	key := "idem-key-1"

	first := newRun(30, 7, model.AssistantRunStatusQueued, &key)
	require.NoError(t, db.Create(first).Error)

	// 同一 (owner, conversation, key) 第二次插入必须被拒——这是「插入冲突即回读」的前提。
	// 断言错误里出现 idempotency_key 列名，才能确认是幂等索引而非活跃槽索引拦下的。
	err := db.Create(newRun(30, 7, model.AssistantRunStatusQueued, &key)).Error
	require.Error(t, err, "同 owner+conversation+idempotency_key 的第二条 run 应被拒绝")
	require.Contains(t, err.Error(), "idempotency_key", "应由幂等唯一索引拦下")

	// 首条进入终态释放活跃槽后再插同键：仍然必须被拒。
	// 这一步把两个索引拆开验证——此时活跃槽是空的，唯一可能的拦截者就是幂等索引。
	require.NoError(t, db.Model(&model.AssistantRun{}).
		Where("id = ?", first.ID).
		Update("status", model.AssistantRunStatusCompleted).Error)
	err = db.Create(newRun(30, 7, model.AssistantRunStatusQueued, &key)).Error
	require.Error(t, err, "活跃槽已释放，同幂等键仍应被拒")
	require.Contains(t, err.Error(), "idempotency_key")

	// 换会话或换 owner 则互不冲突：唯一键是三元组，不是全局键。
	other := "idem-key-1"
	require.NoError(t, db.Create(newRun(31, 7, model.AssistantRunStatusQueued, &other)).Error, "换会话应放行")
	require.NoError(t, db.Create(newRun(32, 8, model.AssistantRunStatusQueued, &other)).Error, "换 owner 应放行")

	// 空键（NULL 与空串）不参与唯一约束：多条可共存，否则不带幂等头的请求会互相顶掉。
	// 为规避活跃槽约束干扰，这里每条用不同会话。
	empty := ""
	for _, conversationID := range []uint{40, 41} {
		require.NoError(t, db.Create(newRun(conversationID, 9, model.AssistantRunStatusQueued, nil)).Error,
			"NULL 幂等键应可共存")
	}
	for _, conversationID := range []uint{40, 41} {
		require.NoError(t, db.Create(newRun(conversationID, 9, model.AssistantRunStatusCompleted, &empty)).Error,
			"空串幂等键应可共存")
	}
	// 同一会话再多插一条空串键：唯一的活跃槽只被 queued 那条占着，但空串键不参与幂等唯一性，
	// 所以这里换成另一会话是为了不撞活跃槽；关键点是两条空串键行本身能同时存在。
	third := ""
	require.NoError(t, db.Create(newRun(42, 9, model.AssistantRunStatusCompleted, &third)).Error,
		"空串幂等键跨行不应互相顶掉")
	var emptyKeyCount int64
	require.NoError(t, db.Model(&model.AssistantRun{}).
		Where("owner_user_id = ? AND idempotency_key = ?", 9, "").
		Count(&emptyKeyCount).Error)
	require.Equal(t, int64(3), emptyKeyCount)
	var nullKeyCount int64
	require.NoError(t, db.Model(&model.AssistantRun{}).
		Where("owner_user_id = ? AND idempotency_key IS NULL", 9).
		Count(&nullKeyCount).Error)
	require.Equal(t, int64(2), nullKeyCount)
}

// TestAssistantChat_EventSeqUnique (run_id, seq) 唯一：seq 不连续无约束，但同 run 同 seq 必须拒绝。
func TestAssistantChat_EventSeqUnique(t *testing.T) {
	db := newAssistantChatDB(t)

	require.NoError(t, db.Create(&model.AssistantEvent{
		ConversationID: 50, RunID: 500, Seq: 1, Type: model.AssistantEventTypeTextDelta, Data: `{"text":"hi"}`,
	}).Error)
	err := db.Create(&model.AssistantEvent{
		ConversationID: 50, RunID: 500, Seq: 1, Type: model.AssistantEventTypeRunEnd, Data: `{"status":"completed"}`,
	}).Error
	require.Error(t, err, "同一 run 的重复 seq 应被拒绝")

	// 不同 run 可以各自从 1 开始计数。
	require.NoError(t, db.Create(&model.AssistantEvent{
		ConversationID: 50, RunID: 501, Seq: 1, Type: model.AssistantEventTypeTextDelta, Data: `{"text":"yo"}`,
	}).Error)
}
