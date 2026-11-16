package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 核心契约：长时间没有进展上报的在途任务必须被回收，否则它永久占住互斥组。
//
// 这条用例的对手是「闸门变永久锁」：如果不回收，用户在这台节点上再也装不了 JDK，
// 且除了改数据库没有出路。做变异（把 sweepOnce 改成直接 return 0）时本用例必红。
func TestTaskReaper_ReclaimsStaleRunningTask(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)

	_, _, err := svc.AdmitTask("t-stale", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "安装 JDK 21",
		IdemKey: BuildIdemKey(1, model.TaskKindJDKInstall, "Temurin|21|x64|"),
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-stale").
		Update("state", model.TaskStateRunning).Error)

	// 把最后进展时间推到很久以前。
	old := time.Now().Add(-3 * taskStaleAfter)
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-stale").
		UpdateColumn("updated_at", old).Error)

	reaper := NewTaskReaper(db)
	require.Equal(t, 1, reaper.sweepOnce(time.Now()), "失联任务应被回收")

	var got model.Task
	require.NoError(t, db.Where("task_id = ?", "t-stale").First(&got).Error)
	require.Equal(t, model.TaskStateFailed, got.State)
	require.Contains(t, got.Error, "失联", "失败原因要说明是被判定失联，不是用户取消")

	// 释放后必须能重新装（这是回收的全部意义）。
	_, reused, err := svc.AdmitTask("t-retry", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "安装 JDK 21",
		IdemKey: BuildIdemKey(1, model.TaskKindJDKInstall, "Temurin|21|x64|"),
	})
	require.NoError(t, err, "回收后必须能重新发起，否则闸门就是永久锁")
	require.False(t, reused)
}

// 正常在跑的任务不许被误杀：它刚刚才上报过进展。
func TestTaskReaper_KeepsFreshTask(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)

	_, _, err := svc.AdmitTask("t-fresh", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "安装 JDK 21",
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-fresh").
		Update("state", model.TaskStateRunning).Error)

	require.Equal(t, 0, NewTaskReaper(db).sweepOnce(time.Now()))

	var got model.Task
	require.NoError(t, db.Where("task_id = ?", "t-fresh").First(&got).Error)
	require.Equal(t, model.TaskStateRunning, got.State, "刚开始的任务不该被当成失联")
}

// 已请求取消的任务交给心跳/取消链路收尾，回收器不许抢先置终态。
func TestTaskReaper_SkipsCancelRequested(t *testing.T) {
	db := newTaskTestDB(t)
	svc := NewTaskService(db)

	_, _, err := svc.AdmitTask("t-canceling", TaskAdmitSpec{
		NodeID: 1, Kind: model.TaskKindJDKInstall, Title: "安装 JDK 21",
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-canceling").
		Updates(map[string]any{
			"state":            model.TaskStateRunning,
			"cancel_requested": true,
			"updated_at":       time.Now().Add(-3 * taskStaleAfter),
		}).Error)

	require.Equal(t, 0, NewTaskReaper(db).sweepOnce(time.Now()),
		"正在取消中的任务不该被回收器改写为失败")

	var got model.Task
	require.NoError(t, db.Where("task_id = ?", "t-canceling").First(&got).Error)
	require.Equal(t, model.TaskStateRunning, got.State)
}

// 不参与互斥的长任务（备份等）不归本回收器管。
func TestTaskReaper_IgnoresNonExclusiveKinds(t *testing.T) {
	db := newTaskTestDB(t)
	require.NoError(t, db.Create(&model.Task{
		TaskID: "t-backup", NodeID: 1, Kind: model.TaskKindBackupCreate,
		State: model.TaskStateRunning, Title: "备份",
		UpdatedAt: time.Now().Add(-3 * taskStaleAfter),
	}).Error)
	// Create 会覆盖 updated_at，再显式推旧一次。
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-backup").
		UpdateColumn("updated_at", time.Now().Add(-3*taskStaleAfter)).Error)

	require.Equal(t, 0, NewTaskReaper(db).sweepOnce(time.Now()))

	var got model.Task
	require.NoError(t, db.Where("task_id = ?", "t-backup").First(&got).Error)
	require.Equal(t, model.TaskStateRunning, got.State)
}
