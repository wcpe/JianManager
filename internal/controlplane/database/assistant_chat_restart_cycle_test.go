package database

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 迁移三启周期：首启建表 → 停 → 二启（脏库）→ 停 → 三启。
// 判据：每一启 AutoMigrate 都成功，且索引仍在、数据仍在。
func TestAssistantChatRestartCycle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cycle.db")

	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		return db
	}

	// 首启：建表 + 建索引
	db := open()
	require.NoError(t, AutoMigrate(db))
	require.NoError(t, db.Create(&model.AssistantConversation{OwnerUserID: 1, Title: "首启会话"}).Error)
	require.NoError(t, db.Create(&model.AssistantRun{
		ConversationID: 1, OwnerUserID: 1, Status: model.AssistantRunStatusRunning, ModelSnapshot: "{}",
	}).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	// 二启（脏库：有活跃 run 与未完成数据）
	db = open()
	require.NoError(t, AutoMigrate(db), "二启迁移失败")
	var idx1, idx2 int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='ux_assistant_runs_active'`).Scan(&idx1).Error)
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='ux_assistant_runs_idem'`).Scan(&idx2).Error)
	require.Equal(t, int64(1), idx1, "二启后活跃唯一索引丢失")
	require.Equal(t, int64(1), idx2, "二启后幂等唯一索引丢失")
	var n int64
	require.NoError(t, db.Model(&model.AssistantConversation{}).Count(&n).Error)
	require.Equal(t, int64(1), n, "二启后数据丢失")
	sqlDB, err = db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	// 三启
	db = open()
	require.NoError(t, AutoMigrate(db), "三启迁移失败")
	require.NoError(t, db.Model(&model.AssistantRun{}).Count(&n).Error)
	require.Equal(t, int64(1), n, "三启后数据丢失")

	// 三启后索引依然有效（能拒绝第二个活跃 run）
	require.Error(t, db.Create(&model.AssistantRun{
		ConversationID: 1, OwnerUserID: 1, Status: model.AssistantRunStatusQueued, ModelSnapshot: "{}",
	}).Error, "三启后活跃唯一约束失效")
}
