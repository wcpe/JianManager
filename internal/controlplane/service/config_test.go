package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/config.db"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.InstanceConfigVersion{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestConfigService_Versions_ReturnsLatestFirst(t *testing.T) {
	db := newConfigTestDB(t)
	svc := NewConfigService(db, nil)

	if err := db.Create(&model.InstanceConfigVersion{InstanceID: 1, FilePath: "server.properties", Content: "a=1\n", ContentHash: "h1", Message: "init"}).Error; err != nil {
		t.Fatalf("插入版本 1 失败: %v", err)
	}
	if err := db.Create(&model.InstanceConfigVersion{InstanceID: 1, FilePath: "server.properties", Content: "a=2\n", ContentHash: "h2", Message: "change"}).Error; err != nil {
		t.Fatalf("插入版本 2 失败: %v", err)
	}

	var total int64
	if err := db.Model(&model.InstanceConfigVersion{}).Where("instance_id = ? AND file_path = ?", 1, "server.properties").Count(&total).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("数据库中应保留 2 条记录, 实际 %d", total)
	}

	versions, err := svc.Versions(1, "server.properties")
	if err != nil {
		t.Fatalf("Versions 失败: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("期望 2 个版本, 实际 %d", len(versions))
	}
	if versions[0].ID <= versions[1].ID {
		t.Fatalf("版本应按 ID 倒序: %+v", versions)
	}
}

func TestConfigService_Versions_IsolatedByInstance(t *testing.T) {
	db := newConfigTestDB(t)
	svc := NewConfigService(db, nil)
	if err := db.Create(&model.InstanceConfigVersion{InstanceID: 1, FilePath: "x.yml", Content: "a: 1\n", ContentHash: "x1"}).Error; err != nil {
		t.Fatal(err.Error())
	}
	if err := db.Create(&model.InstanceConfigVersion{InstanceID: 2, FilePath: "x.yml", Content: "b: 2\n", ContentHash: "x2"}).Error; err != nil {
		t.Fatal(err.Error())
	}

	got1, err := svc.Versions(1, "x.yml")
	if err != nil || len(got1) != 1 {
		t.Fatalf("实例 1 应只看到 1 条, 实际 %d err=%v", len(got1), err)
	}
	got2, err := svc.Versions(2, "x.yml")
	if err != nil || len(got2) != 1 || got2[0].ID == got1[0].ID {
		t.Fatalf("实例 2 应看到独立的版本记录: %+v err=%v", got2, err)
	}
}

func TestConfigService_Versions_EmptyWhenNoRecord(t *testing.T) {
	db := newConfigTestDB(t)
	svc := NewConfigService(db, nil)
	versions, err := svc.Versions(99, "missing.properties")
	if err != nil {
		t.Fatalf("Versions 不应报错: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("期望空版本列表, 实际 %+v", versions)
	}
}
