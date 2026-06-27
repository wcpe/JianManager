package database

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
)

// closeDB 关闭底层连接，使 Windows 下打开的 sqlite 文件得以被 t.TempDir() 清理。
func closeDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

// TestNew_CreatesMissingParentDir 复现并守护问题1：DSN 指向尚不存在的目录时，
// New 必须自动创建父目录（含多级）并成功打开数据库。
//
// 修复前：database.New 直接 sqlite.Open(dsn)，父目录不存在 → modernc/glebarez
// 报 SQLITE_CANTOPEN（表象 "unable to open database file: out of memory (14)"），
// 首次部署未手动 mkdir 数据目录即无法启动。
func TestNew_CreatesMissingParentDir(t *testing.T) {
	// 两级均不存在的子目录，验证递归创建。
	dsn := filepath.Join(t.TempDir(), "nosuch", "nested", "jianmanager.db")

	db, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	require.NoError(t, err)
	require.NotNil(t, db)
	defer closeDB(t, db)

	// 能建表说明库文件真正可读写（不仅是打开成功）。
	require.NoError(t, AutoMigrate(db))
}

// TestNew_MemoryDSN 守护：纯内存库 DSN 不应被误当文件路径去创建目录，且可正常打开。
// 防止问题1 的「建父目录」修复误伤 :memory: / file::memory: 形式。
func TestNew_MemoryDSN(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:?cache=shared"} {
		db, err := New(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
		require.NoError(t, err, "dsn=%s", dsn)
		require.NotNil(t, db, "dsn=%s", dsn)
		closeDB(t, db)
	}
}
