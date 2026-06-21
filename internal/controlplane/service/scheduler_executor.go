package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ScheduleExecutorImpl 定时任务执行器实现。
type ScheduleExecutorImpl struct {
	db          *gorm.DB
	instanceSvc *InstanceService
	backupSvc   *BackupService
	pool        *grpc.ClientPool
}

// NewScheduleExecutorImpl 创建执行器。
func NewScheduleExecutorImpl(db *gorm.DB, instanceSvc *InstanceService, backupSvc *BackupService, pool *grpc.ClientPool) *ScheduleExecutorImpl {
	return &ScheduleExecutorImpl{
		db:          db,
		instanceSvc: instanceSvc,
		backupSvc:   backupSvc,
		pool:        pool,
	}
}

// ExecuteSchedule 执行定时任务并记录执行日志。
func (e *ScheduleExecutorImpl) ExecuteSchedule(schedule *model.Schedule) error {
	startedAt := time.Now()

	var execErr error
	switch schedule.Action {
	case "start":
		execErr = e.instanceSvc.Start(schedule.InstanceID)
	case "stop":
		execErr = e.instanceSvc.Stop(schedule.InstanceID)
	case "restart":
		execErr = e.instanceSvc.Restart(schedule.InstanceID)
	case "backup":
		_, execErr = e.backupSvc.Create(schedule.InstanceID, fmt.Sprintf("定时备份-%s", schedule.Name))
	case "command":
		execErr = e.executeCommand(schedule)
	default:
		execErr = fmt.Errorf("未知的定时任务操作: %s", schedule.Action)
	}

	// 写入执行日志
	finishedAt := time.Now()
	logStatus := model.ScheduleLogStatusSuccess
	errMsg := ""
	if execErr != nil {
		logStatus = model.ScheduleLogStatusFailed
		errMsg = execErr.Error()
	}
	log := &model.ScheduleExecutionLog{
		ScheduleID: schedule.ID,
		Action:     schedule.Action,
		Status:     logStatus,
		Error:      errMsg,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	if err := e.db.Create(log).Error; err != nil {
		slog.Error("写入定时任务执行日志失败", "scheduleId", schedule.UUID, "error", err)
	}

	return execErr
}

// executeCommand 通过 gRPC 向实例发送命令。
func (e *ScheduleExecutorImpl) executeCommand(schedule *model.Schedule) error {
	// 查找实例和节点
	var instance model.Instance
	if err := e.db.First(&instance, schedule.InstanceID).Error; err != nil {
		return fmt.Errorf("实例不存在: %w", err)
	}

	var node model.Node
	if err := e.db.First(&node, instance.NodeID).Error; err != nil {
		return fmt.Errorf("节点不存在: %w", err)
	}

	// 获取 gRPC 客户端
	client, ok := e.pool.Get(node.UUID)
	if !ok {
		return fmt.Errorf("节点 %s 未连接", node.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := client.Worker.SendCommand(ctx, &workerpb.SendCommandRequest{
		InstanceUuid: instance.UUID,
		Command:      schedule.Payload,
	})

	if err != nil {
		slog.Error("定时任务命令执行失败", "scheduleId", schedule.UUID, "command", schedule.Payload, "error", err)
		return err
	}

	slog.Info("定时任务命令已发送", "scheduleId", schedule.UUID, "command", schedule.Payload)
	return nil
}
