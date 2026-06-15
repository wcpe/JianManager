package service

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// ScheduleExecutorImpl 定时任务执行器实现。
type ScheduleExecutorImpl struct {
	db          *gorm.DB
	instanceSvc *InstanceService
	backupSvc   *BackupService
}

// NewScheduleExecutorImpl 创建执行器。
func NewScheduleExecutorImpl(db *gorm.DB, instanceSvc *InstanceService, backupSvc *BackupService) *ScheduleExecutorImpl {
	return &ScheduleExecutorImpl{
		db:          db,
		instanceSvc: instanceSvc,
		backupSvc:   backupSvc,
	}
}

// ExecuteSchedule 执行定时任务。
func (e *ScheduleExecutorImpl) ExecuteSchedule(schedule *model.Schedule) error {
	switch schedule.Action {
	case "start":
		return e.instanceSvc.Start(schedule.InstanceID)
	case "stop":
		return e.instanceSvc.Stop(schedule.InstanceID)
	case "restart":
		return e.instanceSvc.Restart(schedule.InstanceID)
	case "backup":
		_, err := e.backupSvc.Create(schedule.InstanceID, fmt.Sprintf("定时备份-%s", schedule.Name))
		return err
	case "command":
		// TODO: 发送命令给实例（需要 gRPC 连接 Worker）
		return fmt.Errorf("command 操作待实现")
	default:
		return fmt.Errorf("未知的定时任务操作: %s", schedule.Action)
	}
}
