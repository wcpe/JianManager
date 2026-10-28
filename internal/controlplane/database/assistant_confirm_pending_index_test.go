package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/**
 * FR-516 §8「同 run 最多 1 张 pending 票据」的迁移验收。
 *
 * 两个必须验证的点，缺一个这条迁移就是不完备的：
 *  1. **脏库能迁**：早期版本不限制这一条，历史库里可能已有同 run 多张 pending。
 *     直接建索引会让 CP 启动失败——而启动失败是运维最难自己诊断的一类故障。
 *  2. **索引真的生效**：建完索引后必须能拒绝第二张 pending，否则「迁移成功」
 *     只是建了个空壳（例如谓词写错、建成了非唯一索引）。
 */

// createLegacyPendingRows 在**未建索引**的库里造同 run 多张 pending，模拟旧版本数据。
func createLegacyPendingRows(t *testing.T, db *gorm.DB, runID uint) {
	t.Helper()
	for _, callID := range []string{"legacy-a", "legacy-b", "legacy-c"} {
		require.NoError(t, db.Create(&model.AssistantConfirmation{
			Source: model.AssistantConfirmSourceChat, OwnerUserID: 1, ConversationID: 1,
			RunID: runID, CallID: callID, ToolName: "system.agent.instance_start",
			ArgsHash: "h", ArgsFrozen: "{}", Policy: model.AssistantPolicySelfConfirm,
			Status: model.AssistantConfirmStatusPending, ExpiresAt: time.Now().Add(time.Minute),
		}).Error)
	}
}

func TestAssistantConfirmPendingIndex_DirtyDBCollapsesDuplicates(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dirty.db")

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)

	// 先只建表（不跑 AutoMigrate，避免索引已被建上），造出旧版本的脏数据。
	require.NoError(t, db.AutoMigrate(&model.AssistantConfirmation{}))
	createLegacyPendingRows(t, db, 5)
	createLegacyPendingRows(t, db, 6)

	// 现在跑真实迁移：它必须先归正，再建索引。
	require.NoError(t, migrateAssistantConfirmPendingIndex(db), "脏库迁移不应失败")

	// 判据 1：每个 run 只剩一张 pending，且保留的是最新那张。
	for _, runID := range []uint{5, 6} {
		var pending []model.AssistantConfirmation
		require.NoError(t, db.Where("run_id = ? AND status = ?", runID, model.AssistantConfirmStatusPending).
			Find(&pending).Error)
		require.Len(t, pending, 1, "run %d 应只剩一张 pending", runID)
		require.Equal(t, "legacy-c", pending[0].CallID, "run %d 应保留最新那张", runID)
	}

	// 判据 2：归正只改状态，不删行——审计要能查到「这些票据曾经存在过」。
	var total int64
	require.NoError(t, db.Model(&model.AssistantConfirmation{}).Count(&total).Error)
	require.Equal(t, int64(6), total, "归正不得删除任何票据行")

	var invalidated int64
	require.NoError(t, db.Model(&model.AssistantConfirmation{}).
		Where("status = ?", model.AssistantConfirmStatusInvalidated).Count(&invalidated).Error)
	require.Equal(t, int64(4), invalidated, "每个 run 应有 2 张被标为 invalidated")

	// 判据 3：索引真的生效——现在插第二张 pending 必须被拒。
	require.Error(t, db.Create(&model.AssistantConfirmation{
		Source: model.AssistantConfirmSourceChat, OwnerUserID: 1, ConversationID: 1,
		RunID: 5, CallID: "after-migration", ToolName: "system.agent.instance_start",
		ArgsHash: "h", ArgsFrozen: "{}", Policy: model.AssistantPolicySelfConfirm,
		Status: model.AssistantConfirmStatusPending, ExpiresAt: time.Now().Add(time.Minute),
	}).Error, "迁移后同 run 第二张 pending 必须被拒绝")

	// 判据 4：重复迁移是幂等的（CP 每次启动都会跑）。
	require.NoError(t, migrateAssistantConfirmPendingIndex(db), "重复迁移应幂等")
}

func TestAssistantConfirmPendingIndex_IdempotentOnCleanDB(t *testing.T) {
	// 全新库（跑过完整 AutoMigrate）上重复迁移也必须成功：
	// 真实部署里每次启动都会调它，任何非幂等行为都会在第二次启动时暴露成启动失败。
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "clean.db")),
		&gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AssistantConfirmation{}))

	require.NoError(t, migrateAssistantConfirmPendingIndex(db))
	require.NoError(t, migrateAssistantConfirmPendingIndex(db))

	// 建完后约束生效。
	seed := func(callID string) *model.AssistantConfirmation {
		return &model.AssistantConfirmation{
			Source: model.AssistantConfirmSourceChat, OwnerUserID: 1, ConversationID: 1,
			RunID: 9, CallID: callID, ToolName: "system.agent.instance_start",
			ArgsHash: "h", ArgsFrozen: "{}", Policy: model.AssistantPolicySelfConfirm,
			Status: model.AssistantConfirmStatusPending, ExpiresAt: time.Now().Add(time.Minute),
		}
	}
	require.NoError(t, db.Create(seed("one")).Error)
	require.Error(t, db.Create(seed("two")).Error, "第二张 pending 必须被拒绝")
}
