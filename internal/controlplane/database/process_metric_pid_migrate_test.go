package database

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestMigrateProcessMetricPIDColumn_LegacyPID 保留 GORM 曾生成的 p_id 数据并迁移为 pid。
// 真机验收 FR-407 抓到：ProcessMetricSnapshot.PID 无显式列名时 GORM 蛇形化为 p_id，
// 与 managedProcessHistory 的 WHERE pid 查询不一致导致 no such column: pid。
func TestMigrateProcessMetricPIDColumn_LegacyPID(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE process_metric_snapshots (id integer primary key, instance_uuid varchar(64), p_id integer, rss_bytes integer, sampled_at datetime)").Error)
	require.NoError(t, db.Exec("INSERT INTO process_metric_snapshots (id, instance_uuid, p_id, rss_bytes, sampled_at) VALUES (1, 'inst-1', 33112, 1024, '2026-08-23 16:00:00')").Error)

	require.NoError(t, migrateProcessMetricPIDColumn(db))
	require.True(t, db.Migrator().HasColumn("process_metric_snapshots", "pid"))
	require.False(t, db.Migrator().HasColumn("process_metric_snapshots", "p_id"))

	var pid int
	require.NoError(t, db.Raw("SELECT pid FROM process_metric_snapshots WHERE id = 1").Scan(&pid).Error)
	require.Equal(t, 33112, pid)
}

// TestMigrateProcessMetricPIDColumn_Idempotent 无 p_id 列时迁移幂等无错（新库路径）。
func TestMigrateProcessMetricPIDColumn_Idempotent(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE process_metric_snapshots (id integer primary key, instance_uuid varchar(64), pid integer, rss_bytes integer, sampled_at datetime)").Error)

	require.NoError(t, migrateProcessMetricPIDColumn(db))
	require.True(t, db.Migrator().HasColumn("process_metric_snapshots", "pid"))
}
