package database

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newMigratedDB 建一个已完整迁移（含节点名活跃唯一索引）的内存库。
func newMigratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, AutoMigrate(db))
	return db
}

// TestNodeNameUnique_IndexCreated AutoMigrate 后节点名活跃唯一索引存在（ADR-039 §3）。
func TestNodeNameUnique_IndexCreated(t *testing.T) {
	db := newMigratedDB(t)
	require.True(t, db.Migrator().HasIndex(&model.Node{}, nodeNameUniqueIndexName),
		"AutoMigrate 应建节点名活跃唯一索引")
}

// TestNodeNameUnique_BlocksDuplicateActive 两个活跃节点同名：第二个 Create 应被唯一索引拒绝。
func TestNodeNameUnique_BlocksDuplicateActive(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Create(&model.Node{Name: "edge-a", Host: "10.0.0.1", Secret: "s1"}).Error)

	err := db.Create(&model.Node{Name: "edge-a", Host: "10.0.0.2", Secret: "s2"}).Error
	require.Error(t, err, "活跃同名节点应被唯一索引拒绝")
}

// TestNodeNameUnique_AllowsReuseAfterSoftDelete 软删除节点后，同名新节点可创建（name 释放，ADR-039）。
func TestNodeNameUnique_AllowsReuseAfterSoftDelete(t *testing.T) {
	db := newMigratedDB(t)
	n := &model.Node{Name: "edge-a", Host: "10.0.0.1", Secret: "s1"}
	require.NoError(t, db.Create(n).Error)

	// 软删除（gorm.DeletedAt 置位）。
	require.NoError(t, db.Delete(n).Error)

	// 同名新节点应可创建（部分唯一索引仅约束 deleted_at IS NULL 的活跃行）。
	require.NoError(t, db.Create(&model.Node{Name: "edge-a", Host: "10.0.0.9", Secret: "s2"}).Error,
		"软删旧节点后应能复用其名")
}

// TestDedupeActiveNodeNames_RenamesExtras 存量重名活跃节点：去重保留最近心跳者、其余加后缀，
// 且之后能成功建唯一索引（ADR-039 §修复，migration 前去重）。
func TestDedupeActiveNodeNames_RenamesExtras(t *testing.T) {
	// 先建一个「无唯一索引」的库以注入重名脏数据，再跑去重+建索引。
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}))

	// 注入三个同名活跃节点（绕过唯一索引——此时尚未建）。
	require.NoError(t, db.Create(&model.Node{Name: "dup", Host: "10.0.0.1", Secret: "s1"}).Error)
	require.NoError(t, db.Create(&model.Node{Name: "dup", Host: "10.0.0.2", Secret: "s2"}).Error)
	require.NoError(t, db.Create(&model.Node{Name: "dup", Host: "10.0.0.3", Secret: "s3"}).Error)

	// 去重 + 建索引。
	require.NoError(t, migrateNodeNameUnique(db))

	// 仅剩一个叫 "dup" 的活跃节点，其余被改名为 dup-dup-<id>。
	var cnt int64
	require.NoError(t, db.Model(&model.Node{}).Where("name = ?", "dup").Count(&cnt).Error)
	require.Equal(t, int64(1), cnt, "去重后应只剩一个保留原名")

	var total int64
	require.NoError(t, db.Model(&model.Node{}).Count(&total).Error)
	require.Equal(t, int64(3), total, "去重只改名不删行")

	require.True(t, db.Migrator().HasIndex(&model.Node{}, nodeNameUniqueIndexName),
		"去重后应成功建唯一索引")
}

// TestMigrateNodeNameUnique_Idempotent 重复调用不报错（重启/多次迁移幂等）。
func TestMigrateNodeNameUnique_Idempotent(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, migrateNodeNameUnique(db), "第二次迁移应幂等无错")
}

// TestMigrateGRPCPortColumn_GormLegacyColumn 保留 GORM 曾生成的 g_r_p_c_port 数据。
func TestMigrateGRPCPortColumn_GormLegacyColumn(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE nodes (id integer primary key, g_r_p_c_port integer)").Error)
	require.NoError(t, db.Exec("INSERT INTO nodes (id, g_r_p_c_port) VALUES (1, 19132)").Error)

	require.NoError(t, migrateGRPCPortColumn(db))
	require.True(t, db.Migrator().HasColumn("nodes", "grpc_port"))
	require.False(t, db.Migrator().HasColumn("nodes", "g_r_p_c_port"))

	var port int
	require.NoError(t, db.Raw("SELECT grpc_port FROM nodes WHERE id = 1").Scan(&port).Error)
	require.Equal(t, 19132, port)
}

// TestDedupeActiveNodeNames_AvoidsExistingSuffixCollision 去重名称不能覆盖已有活跃节点名。
func TestDedupeActiveNodeNames_AvoidsExistingSuffixCollision(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}))
	require.NoError(t, db.Create(&model.Node{ID: 1, Name: "dup", Host: "10.0.0.1", Secret: "s1"}).Error)
	require.NoError(t, db.Create(&model.Node{ID: 2, Name: "dup", Host: "10.0.0.2", Secret: "s2"}).Error)
	require.NoError(t, db.Create(&model.Node{ID: 3, Name: "dup-dup-2", Host: "10.0.0.3", Secret: "s3"}).Error)

	require.NoError(t, migrateNodeNameUnique(db))

	var names []string
	require.NoError(t, db.Model(&model.Node{}).Order("id ASC").Pluck("name", &names).Error)
	require.Len(t, names, 3)
	require.NotEqual(t, names[1], names[2], "自动去重名不得与已有活跃节点冲突")
}

// TestMigrateScopedUniqueIndexes_BackupStorageAllowsReuse 验证旧的全局唯一索引被替换为软删感知索引。
func TestMigrateScopedUniqueIndexes_BackupStorageAllowsReuse(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.BackupStorage{}))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_backup_storages_name ON backup_storages(name)").Error)

	// 模拟升级前已经存在、尚未回填 active_name_key 的活跃记录。
	activeName := "archive"
	first := &model.BackupStorage{Name: "archive", ActiveNameKey: &activeName, Type: model.BackupStorageS3}
	require.NoError(t, db.Create(first).Error)
	require.NoError(t, db.Model(first).Update("active_name_key", nil).Error)

	require.NoError(t, migrateBackupStorageActiveNameKey(db))
	require.NoError(t, migrateScopedUniqueIndexes(db))

	var migrated model.BackupStorage
	require.NoError(t, db.First(&migrated, first.ID).Error)
	require.NotNil(t, migrated.ActiveNameKey)
	require.Equal(t, "archive", *migrated.ActiveNameKey)
	require.NoError(t, db.Model(&model.BackupStorage{}).Where("id = ?", first.ID).Update("active_name_key", nil).Error)
	require.NoError(t, db.Delete(&model.BackupStorage{}, first.ID).Error)
	require.NoError(t, db.Create(&model.BackupStorage{Name: "archive", Type: model.BackupStorageS3}).Error)
}
