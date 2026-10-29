package database

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestAutoMigrate_DropsRetiredBusinessTables 验证ADR-106 的退役迁移：
// 存量库里的三张业务镜像表在 AutoMigrate 后被删除，且重复迁移幂等。
func TestAutoMigrate_DropsRetiredBusinessTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "retire.db")
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		return db
	}

	// 造出「升级前的存量库」：手工建三张业务表并写入数据，模拟 V1 业务镜像仍在的环境。
	db := open()
	retired := []string{"business_events", "economy_balance_mirrors", "economy_ledger_entries"}
	for _, table := range retired {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, domain TEXT)").Error)
		require.NoError(t, db.Exec("INSERT INTO "+table+" (domain) VALUES ('economy')").Error)
	}

	// 迁移后三表必须不存在（直接删表，不备份）。
	require.NoError(t, AutoMigrate(db))
	for _, table := range retired {
		require.False(t, db.Migrator().HasTable(table), "退役表 %s 应已被删除", table)
	}

	// 幂等：再启一次不报错，仍无这三张表。
	require.NoError(t, AutoMigrate(db), "重复迁移应幂等无错")
	for _, table := range retired {
		require.False(t, db.Migrator().HasTable(table), "退役表 %s 重复迁移后仍不应存在", table)
	}

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}
