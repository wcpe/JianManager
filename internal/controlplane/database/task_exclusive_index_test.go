package database

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/*
 * 任务队列语义（同节点同类只跑一个 + 同参数幂等复用）的库约束验收。
 *
 * 三个必须验证的点，缺一个这条迁移就是空壳：
 *  1. 互斥索引真的能拦住同节点同组第二条非终态任务——谓词写错或建成非唯一索引都会让
 *     「一次只能一个」失守，而这在应用层看不见（两个并发请求都会插入成功）；
 *  2. **老库脏数据自愈**：建索引前必须先把历史重复的非终态任务收敛，否则 CP 启动直接失败；
 *  3. 重复迁移幂等：CP 每次启动都跑，任何非幂等行为都会在第二次启动炸成启动失败。
 */

func newTaskIndexDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 内存库（shared cache）：本仓其它 database 测试的统一写法，见 node_name_unique_test.go。
	// 用磁盘 t.TempDir() 会在 Windows 上撞「文件仍被占用无法删除」的清理失败。
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)

	/*
		必须显式关闭底层 sql.DB 连接。

		【为什么非关不可】Windows 不能删除仍有打开句柄的文件，而未关闭的 SQLite
		连接会让 `t.TempDir()` 的清理失败，报：
		    TempDir RemoveAll cleanup: unlinkat ...tasks.db: The process cannot
		    access the file because it is being used by another process.
		注意此时**测试断言全部通过**——失败发生在清理阶段，所以 Linux 上永远是绿的，
		只有 go-test-windows 会红。本仓其它测试都用 `t.Cleanup` + `sqlDB.Close()`
		的同一写法（见 beacon_type_backfill_test.go 等）。
	*/
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, db.AutoMigrate(&model.Task{}))
	return db
}

func TestTaskExclusiveIndex_BlocksSameGroupActive(t *testing.T) {
	db := newTaskIndexDB(t)
	require.NoError(t, migrateTaskExclusiveIndexes(db))

	require.NoError(t, db.Create(&model.Task{
		TaskID: "a", NodeID: 1, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		ExclusiveGroup: "runtime_install", Title: "first",
	}).Error)

	err := db.Create(&model.Task{
		TaskID: "b", NodeID: 1, Kind: model.TaskKindRuntimeInstall, State: model.TaskStatePending,
		ExclusiveGroup: "runtime_install", Title: "second",
	}).Error
	require.Error(t, err, "同节点同互斥组的第二条非终态任务必须被数据库拒绝")

	// 终态可共存：历史记录要能堆。
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "a").
		Update("state", model.TaskStateSucceeded).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "c", NodeID: 1, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		ExclusiveGroup: "runtime_install", Title: "third",
	}).Error, "上一条已终态时必须能再起一个")

	// 别的节点互不影响。
	require.NoError(t, db.Create(&model.Task{
		TaskID: "d", NodeID: 2, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		ExclusiveGroup: "runtime_install", Title: "other node",
	}).Error)

	// 不参与互斥的 kind 完全不受影响。
	for _, id := range []string{"k1", "k2"} {
		require.NoError(t, db.Create(&model.Task{
			TaskID: id, NodeID: 1, Kind: model.TaskKindBackupCreate, State: model.TaskStateRunning,
			Title: "备份",
		}).Error)
	}
}

func TestTaskIdemIndex_BlocksSameKeyActive(t *testing.T) {
	db := newTaskIndexDB(t)
	require.NoError(t, migrateTaskExclusiveIndexes(db))

	require.NoError(t, db.Create(&model.Task{
		TaskID: "a", NodeID: 1, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		IdemKey: "same-key", Title: "first",
	}).Error)
	require.Error(t, db.Create(&model.Task{
		TaskID: "b", NodeID: 1, Kind: model.TaskKindJDKInstall, State: model.TaskStatePending,
		IdemKey: "same-key", Title: "dup",
	}).Error, "同节点同幂等键的第二条非终态任务必须被拒绝")

	// 不同幂等键允许（不同参数会被互斥组拦，那是另一层，不在本索引职责内）。
	require.NoError(t, db.Create(&model.Task{
		TaskID: "c", NodeID: 2, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		IdemKey: "same-key", Title: "other node",
	}).Error)
}

// 这条是老库真实会遇到的场景：改造前没有约束，库里可能躺着两条同组 running。
// 若迁移只建索引不收敛数据，CP 会因为建索引失败而起不来——那是最糟的失败方式。
func TestTaskExclusiveIndex_HealsLegacyDuplicates(t *testing.T) {
	db := newTaskIndexDB(t)
	// 模拟改造前的脏数据：同节点同组两条 running，且都带着幂等键（可能互不相同）。
	require.NoError(t, db.Create(&model.Task{
		TaskID: "old-1", NodeID: 1, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		ExclusiveGroup: "runtime_install", IdemKey: "k1", Title: "老任务 1",
	}).Error)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "old-2", NodeID: 1, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		ExclusiveGroup: "runtime_install", IdemKey: "k2", Title: "老任务 2",
	}).Error)

	// 迁移必须成功（而不是因为脏数据失败）。
	require.NoError(t, migrateTaskExclusiveIndexes(db),
		"老库有多条同组在途任务时，迁移必须自愈而不是报错让 CP 起不来")

	// 收敛后：同节点同组只剩一条非终态，且留下的是 id 最大的（最新那次操作）。
	var active []model.Task
	require.NoError(t, db.Where("node_id = ? AND exclusive_group = ? AND state IN ?",
		1, "runtime_install",
		[]model.TaskState{model.TaskStatePending, model.TaskStateRunning}).
		Order("id asc").Find(&active).Error)
	require.Len(t, active, 1, "同一互斥组只应留一条在途任务")
	require.Equal(t, "old-2", active[0].TaskID, "应保留最新（id 最大）的那条")

	// 被收掉的那条要写明原因，不能是无声消失。
	var dead model.Task
	require.NoError(t, db.Where("task_id = ?", "old-1").First(&dead).Error)
	require.Equal(t, model.TaskStateFailed, dead.State)
	require.NotEmpty(t, dead.Error, "被自动收尾的任务必须留下原因")
}

// CP 每次启动都会跑迁移，必须幂等。
func TestTaskExclusiveIndex_IdempotentMigration(t *testing.T) {
	db := newTaskIndexDB(t)
	require.NoError(t, migrateTaskExclusiveIndexes(db))
	require.NoError(t, migrateTaskExclusiveIndexes(db), "重复迁移必须无副作用")

	// 索引确实在，且唯一性仍然有效（不是第二次跑把它删了又建失败）。
	require.NoError(t, db.Create(&model.Task{
		TaskID: "a", NodeID: 1, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		ExclusiveGroup: "runtime_install", Title: "x",
	}).Error)
	require.Error(t, db.Create(&model.Task{
		TaskID: "b", NodeID: 1, Kind: model.TaskKindJDKInstall, State: model.TaskStateRunning,
		ExclusiveGroup: "runtime_install", Title: "y",
	}).Error)
}
