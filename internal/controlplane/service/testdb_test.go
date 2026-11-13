package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openTestSQLite 是 service 包测试统一的内存库构造入口。
//
// ## 为什么必须统一（本文件因一次真实故障而存在）
//
// 整包跑 `go test ./internal/controlplane/service/` 曾长时间卡死（单测单独跑却秒过），
// 根因是**测试连接泄漏**：108 个测试文件各自 `gorm.Open("file:...?cache=shared")`
// 却从不关闭，把 SQLite 连接（以及它持有的共享内存库锁）留到进程结束。
// 整包累积数百个未释放句柄后，后续用例的 `AutoMigrate`（要写锁）就长时间等锁——
// 表现为「个别用例随机卡住」，且**每次卡住的用例都不一样**（取决于调度顺序）。
//
// 两条硬约束：
//  1. **库名必须唯一**。`file::memory:?cache=shared`（匿名）会让全进程所有用到它的
//     测试共享同一个库——数据互相污染只是表象，更糟的是它让「测试之间应该独立」
//     这个前提悄悄失效。这里用 `t.Name()`，它含子测试路径，天然唯一。
//  2. **必须关闭**。共享内存库在最后一个连接关闭时才释放；不关就长期占着。
//     关闭交给 `t.Cleanup`，保证失败路径也会执行。
//
// `busy_timeout` 仍保留：即使将来又有别的路径制造瞬时争用，也能等待而不是立刻报错
// ——但**不要用它来掩盖泄漏**，它只是兜底。
func openTestSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Discard,
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	// 关闭底层 *sql.DB。GORM 不提供 Close，必须取到 sql.DB 再关。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}
