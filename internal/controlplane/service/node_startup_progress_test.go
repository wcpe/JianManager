package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 范围②：「正在启动的节点」读模型。
//
// 关键取舍都在这几条用例里固定下来：
//   - 陈旧行**不进列表**（否则面板会把死掉的启动显示成仍在加载）；
//   - Stale 由**后端**计算下发（前端自行比较会受客户端时钟偏差影响）；
//   - 表还没建时按空列表处理（老库未迁移不该把节点页打挂）。

func newStartupProgressSvcDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:nodestartup_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}

	// 关闭底层连接（共享内存库在最后一个连接关闭后才释放，见 testdb_test.go）。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Node{}, &model.NodeStartupProgress{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// TestListStartupProgress_ExcludesStale 陈旧行不出现在列表里。
//
// 这是本读模型最重要的判据：Worker 中途崩溃不会清理进度行，
// 若不过滤，面板会永远显示「启动中 · ingest_state_load」——运维会以为它在慢慢加载，
// 而实际进程早没了。这种「看起来在进展」比「什么都没有」更有害。
func TestListStartupProgress_ExcludesStale(t *testing.T) {
	db := newStartupProgressSvcDB(t)
	svc := NewNodeService(db)

	fresh := model.NodeStartupProgress{
		Key: "name:fresh-node", NodeName: "fresh-node", Phase: "ingest_state_load",
		PhasesDone: 5, PhasesTotal: 7, UpdatedAt: time.Now().Add(-10 * time.Second),
	}
	dead := model.NodeStartupProgress{
		Key: "name:dead-node", NodeName: "dead-node", Phase: "config",
		PhasesDone: 1, PhasesTotal: 7, UpdatedAt: time.Now().Add(-30 * time.Minute),
	}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatalf("建 fresh 失败: %v", err)
	}
	if err := db.Create(&dead).Error; err != nil {
		t.Fatalf("建 dead 失败: %v", err)
	}

	items, err := svc.ListStartupProgress()
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("只应返回 1 条（新鲜），实际 %d 条", len(items))
	}
	if items[0].NodeName != "fresh-node" {
		t.Fatalf("应返回 fresh-node，实际 %q", items[0].NodeName)
	}
	if items[0].Stale {
		t.Fatal("新鲜行不应标记 stale")
	}
}

// TestListStartupProgress_EmptyIsNotNil 空结果返回空切片而不是 nil。
//
// nil 在 JSON 里序列化成 null，前端 `items.map` 会直接崩；空切片是 []。
func TestListStartupProgress_EmptyIsNotNil(t *testing.T) {
	db := newStartupProgressSvcDB(t)
	svc := NewNodeService(db)

	items, err := svc.ListStartupProgress()
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if items == nil {
		t.Fatal("空结果应返回空切片，不能是 nil（前端会把它当 null 崩掉）")
	}
	if len(items) != 0 {
		t.Fatalf("应无条目，实际 %d", len(items))
	}
}

// TestListStartupProgress_MissingTableIsTolerated 表不存在时按空列表处理。
//
// 老库升级到这个版本但迁移还没跑时就是这样。节点列表是核心页面，
// 不该因为一个可选的可观测性表缺失而整体 500。
func TestListStartupProgress_MissingTableIsTolerated(t *testing.T) {
	dsn := "file:nodestartup_missing_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	// 刻意不迁移 NodeStartupProgress
	svc := NewNodeService(db)

	items, err := svc.ListStartupProgress()
	if err != nil {
		t.Fatalf("表缺失时不应报错: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("应返回空列表，实际 %d", len(items))
	}
}

// TestCleanupStaleStartupProgress 清理只删陈旧行。
func TestCleanupStaleStartupProgress(t *testing.T) {
	db := newStartupProgressSvcDB(t)
	svc := NewNodeService(db)

	if err := db.Create(&model.NodeStartupProgress{
		Key: "name:fresh2", NodeName: "fresh2", Phase: "config", UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("建 fresh 失败: %v", err)
	}
	if err := db.Create(&model.NodeStartupProgress{
		Key: "name:dead2", NodeName: "dead2", Phase: "config", UpdatedAt: time.Now().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("建 dead 失败: %v", err)
	}

	n, err := svc.CleanupStaleStartupProgress()
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应删除 1 条陈旧行，实际 %d", n)
	}
	var left []model.NodeStartupProgress
	db.Find(&left)
	if len(left) != 1 || left[0].NodeName != "fresh2" {
		t.Fatalf("应只剩 fresh2，实际 %+v", left)
	}
}
