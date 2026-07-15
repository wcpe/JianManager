package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/database"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func setupScheduleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmpDir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmpDir+"/test.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})
	return db
}

// TestScheduleExecutor_RestartUsesInstanceLongOpGate 验证定时重启复用中心化 Restart 闸，
// 长操作在途时执行失败并把同一原因写入执行日志。
func TestScheduleExecutor_RestartUsesInstanceLongOpGate(t *testing.T) {
	db := setupScheduleTestDB(t)
	node := &model.Node{Name: "fr331-schedule-node", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{Name: "fr331-schedule", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.Task{TaskID: "fr331-schedule-task", NodeID: node.ID, InstanceID: inst.ID,
		Kind: model.TaskKindProvision, State: model.TaskStateRunning}).Error)
	schedule := &model.Schedule{InstanceID: inst.ID, Name: "在途重启", CronExpr: "0 * * * *", Action: "restart", Enabled: true}
	require.NoError(t, db.Create(schedule).Error)

	instanceSvc := NewInstanceService(db, NewGroupService(db), nil)
	t.Cleanup(instanceSvc.Shutdown)
	executor := NewScheduleExecutorImpl(db, instanceSvc, nil, nil)

	err := executor.ExecuteSchedule(schedule)
	require.Error(t, err)
	require.Contains(t, err.Error(), "搭建中")

	var log model.ScheduleExecutionLog
	require.NoError(t, db.Where("schedule_id = ?", schedule.ID).First(&log).Error)
	require.Equal(t, model.ScheduleLogStatusFailed, log.Status)
	require.Contains(t, log.Error, "搭建中")
}

func TestScheduleService_ListExecutionLogs(t *testing.T) {
	db := setupScheduleTestDB(t)
	svc := NewScheduleService(db)

	// 创建定时任务
	schedule := &model.Schedule{
		InstanceID: 1,
		Name:       "test-schedule",
		CronExpr:   "0 * * * *",
		Action:     "restart",
		Enabled:    true,
	}
	require.NoError(t, db.Create(schedule).Error)

	// 创建执行日志
	for i := 0; i < 5; i++ {
		log := &model.ScheduleExecutionLog{
			ScheduleID: schedule.ID,
			Action:     "restart",
			Status:     model.ScheduleLogStatusSuccess,
			StartedAt:  time.Now().Add(-time.Duration(i) * time.Hour),
			FinishedAt: time.Now().Add(-time.Duration(i)*time.Hour + time.Second),
		}
		require.NoError(t, db.Create(log).Error)
	}

	// 查询第一页
	logs, total, err := svc.ListExecutionLogs(schedule.ID, 1, 3)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), total)
	assert.Len(t, logs, 3)

	// 查询第二页
	logs2, total2, err := svc.ListExecutionLogs(schedule.ID, 2, 3)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), total2)
	assert.Len(t, logs2, 2)

	// 查询不存在的任务
	logs3, total3, err := svc.ListExecutionLogs(9999, 1, 20)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), total3)
	assert.Len(t, logs3, 0)
}

func TestScheduleService_ListExecutionLogs_DefaultPageSize(t *testing.T) {
	db := setupScheduleTestDB(t)
	svc := NewScheduleService(db)

	// pageSize=0 应默认为 20
	logs, total, err := svc.ListExecutionLogs(1, 1, 0)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Len(t, logs, 0)
}

func TestScheduleService_ListExecutionLogs_FailedLog(t *testing.T) {
	db := setupScheduleTestDB(t)
	svc := NewScheduleService(db)

	schedule := &model.Schedule{
		InstanceID: 1,
		Name:       "test-schedule",
		CronExpr:   "0 * * * *",
		Action:     "command",
		Enabled:    true,
	}
	require.NoError(t, db.Create(schedule).Error)

	// 创建失败日志
	log := &model.ScheduleExecutionLog{
		ScheduleID: schedule.ID,
		Action:     "command",
		Status:     model.ScheduleLogStatusFailed,
		Error:      "节点未连接",
		StartedAt:  time.Now(),
		FinishedAt: time.Now().Add(time.Second),
	}
	require.NoError(t, db.Create(log).Error)

	logs, total, err := svc.ListExecutionLogs(schedule.ID, 1, 20)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, logs, 1)
	assert.Equal(t, model.ScheduleLogStatusFailed, logs[0].Status)
	assert.Equal(t, "节点未连接", logs[0].Error)
}
