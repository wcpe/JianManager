package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var ErrScheduleNotFound = errors.New("定时任务不存在")

// ScheduleService 定时任务服务。
type ScheduleService struct {
	db *gorm.DB
}

// NewScheduleService 创建定时任务服务。
func NewScheduleService(db *gorm.DB) *ScheduleService {
	return &ScheduleService{db: db}
}

// CreateScheduleRequest 创建定时任务请求。
type CreateScheduleRequest struct {
	InstanceID uint   `json:"instanceId" binding:"required"`
	Name       string `json:"name" binding:"required"`
	CronExpr   string `json:"cronExpr" binding:"required"`
	Action     string `json:"action" binding:"required"`
	Payload    string `json:"payload"`
}

// Create 创建定时任务。
func (s *ScheduleService) Create(req CreateScheduleRequest) (*model.Schedule, error) {
	schedule := &model.Schedule{
		InstanceID: req.InstanceID,
		Name:       req.Name,
		CronExpr:   req.CronExpr,
		Action:     req.Action,
		Payload:    req.Payload,
		Enabled:    true,
	}
	if err := s.db.Create(schedule).Error; err != nil {
		return nil, fmt.Errorf("创建定时任务失败: %w", err)
	}
	return schedule, nil
}

// List 返回定时任务列表。
func (s *ScheduleService) List(instanceID *uint) ([]model.Schedule, error) {
	var schedules []model.Schedule
	q := s.db.Model(&model.Schedule{})
	if instanceID != nil {
		q = q.Where("instance_id = ?", *instanceID)
	}
	if err := q.Find(&schedules).Error; err != nil {
		return nil, err
	}
	return schedules, nil
}

// Update 更新定时任务。
func (s *ScheduleService) Update(id uint, cronExpr *string, enabled *bool, action *string) (*model.Schedule, error) {
	updates := map[string]interface{}{}
	if cronExpr != nil {
		updates["cron_expr"] = *cronExpr
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if action != nil {
		updates["action"] = *action
	}
	if len(updates) > 0 {
		result := s.db.Model(&model.Schedule{}).Where("id = ?", id).Updates(updates)
		if result.RowsAffected == 0 {
			return nil, ErrScheduleNotFound
		}
	}
	var schedule model.Schedule
	s.db.First(&schedule, id)
	return &schedule, nil
}

// Delete 删除定时任务。
func (s *ScheduleService) Delete(id uint) error {
	return s.db.Delete(&model.Schedule{}, id).Error
}

// ListExecutionLogs 返回指定定时任务的执行日志列表。
func (s *ScheduleService) ListExecutionLogs(scheduleID uint, page, pageSize int) ([]model.ScheduleExecutionLog, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	if err := s.db.Model(&model.ScheduleExecutionLog{}).Where("schedule_id = ?", scheduleID).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("查询执行日志总数失败: %w", err)
	}

	var logs []model.ScheduleExecutionLog
	offset := (page - 1) * pageSize
	if err := s.db.Where("schedule_id = ?", scheduleID).
		Order("started_at DESC").
		Offset(offset).Limit(pageSize).
		Find(&logs).Error; err != nil {
		return nil, 0, fmt.Errorf("查询执行日志失败: %w", err)
	}

	return logs, total, nil
}
