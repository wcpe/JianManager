package database

/*
 * 业务表退役 —— 重启循环验证。
 *
 * 【为何不能只有单轮用例】
 * 既有 business_retirement_test.go 在**同一个连接**上连跑两次 AutoMigrate，
 * 证的是「函数幂等」；而 .claude/rules/testing-and-quality.md §状态/迁移类变更 要求的是
 * 「迁移 → 首启 → 停 → 二启 → 三启」且二启/三启跑在**脏环境**上（上一轮遗留仍在）。
 * 这两件事不等价：DROP TABLE 是不可逆操作，真正的风险在「已删过的库再次启动」
 * 与「新库首次启动」两种形态。
 *
 * 因此这里每次循环都真正**关闭连接**（落盘文件保留）再重新打开，
 * 模拟 CP 进程重启，并逐轮记录判据字段。
 */

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var retiredBusinessTables = []string{
	"business_events",
	"economy_balance_mirrors",
	"economy_ledger_entries",
}

// openCycle 打开一个指向**同一个磁盘文件**的连接（模拟一次进程启动）。
//
// 关键点：用文件而不是 :memory:——内存库一关就没了，测不出「脏环境」。
func openCycle(t *testing.T, dbPath string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	return db
}

// closeCycle 关闭连接（模拟进程退出）。
//
// 必须关：SQLite 的 DDL 需要释放句柄，不关会让下一轮读到陈旧元信息。
func closeCycle(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

// TestAutoMigrate_RetiredTables_SurviveRestartCycles 迁移 → 首启 → 停 → 二启 → 三启。
//
// 逐轮断言：三张退役表均不存在，且本轮启动不报错、不影响其他表。
// 这是 gate-merge.md「部署后复验：状态/迁移类变更必须再重启一次」的代码化，
// 也是 testing-and-quality.md 要求的「脏环境」形态（第二轮起库里已带上一轮的全部产物）。
func TestAutoMigrate_RetiredTables_SurviveRestartCycles(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart-cycle.db")

	// ── 造「升级前」的存量库：三张业务表带数据 ──
	db := openCycle(t, dbPath)
	for _, table := range retiredBusinessTables {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, domain TEXT)").Error)
		require.NoError(t, db.Exec("INSERT INTO "+table+" (domain) VALUES ('economy')").Error)
		require.True(t, db.Migrator().HasTable(table), "前置：%s 应已存在", table)
	}
	closeCycle(t, db)

	// ── 首启 / 二启 / 三启：每轮都真正关开连接 ──
	for cycle := 1; cycle <= 3; cycle++ {
		db := openCycle(t, dbPath)

		require.NoError(t, AutoMigrate(db), "第 %d 次启动的迁移必须成功", cycle)

		for _, table := range retiredBusinessTables {
			require.False(t, db.Migrator().HasTable(table),
				"第 %d 次启动后，退役表 %s 仍应不存在", cycle, table)
		}

		// 反证：迁移没把无关表一并删掉（用户表是最基础的活体探针）。
		require.True(t, db.Migrator().HasTable("users"),
			"第 %d 次启动后 users 表应存在（迁移不得误删其他表）", cycle)

		// 判据字段：首启后应有 schema 产物（证明迁移真的跑过而不是半途退出）。
		require.True(t, db.Migrator().HasTable("instances"),
			"第 %d 次启动后 instances 表应存在", cycle)

		closeCycle(t, db)
	}
}

// TestAutoMigrate_RetiredTables_FreshInstall 全新库首次启动不应因删表报错。
//
// 新库里本就没有这三张表，`DROP TABLE IF EXISTS` 必须空跑成功——
// 若这里报错，等于新装机直接起不来。
func TestAutoMigrate_RetiredTables_FreshInstall(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	db := openCycle(t, dbPath)
	defer closeCycle(t, db)

	for _, table := range retiredBusinessTables {
		require.False(t, db.Migrator().HasTable(table), "前置：全新库里不应有 %s", table)
	}

	require.NoError(t, AutoMigrate(db), "全新库的首次迁移必须成功")
	for _, table := range retiredBusinessTables {
		require.False(t, db.Migrator().HasTable(table), "全新库迁移后仍不应有 %s", table)
	}
	require.True(t, db.Migrator().HasTable("users"))
}

// TestAutoMigrate_RetiredTables_DirtyEnvironmentWithOnlyOneLeft 脏环境：只残留一张表也必须被删。
//
// 现实里三种形态都会出现：三张都在（完整升级）、只在一张（部分清理过）、
// 一张都不在（新装）。前两种必须都归零。
func TestAutoMigrate_RetiredTables_DirtyEnvironmentWithOnlyOneLeft(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "partial.db")
	db := openCycle(t, dbPath)

	// 只建其中一张。
	require.NoError(t, db.Exec("CREATE TABLE business_events (id INTEGER PRIMARY KEY, domain TEXT)").Error)
	require.NoError(t, db.Exec("INSERT INTO business_events (domain) VALUES ('economy')").Error)
	closeCycle(t, db)

	db = openCycle(t, dbPath)
	defer closeCycle(t, db)
	require.NoError(t, AutoMigrate(db))
	for _, table := range retiredBusinessTables {
		require.False(t, db.Migrator().HasTable(table), "部分残留也必须归零：%s", table)
	}
}
