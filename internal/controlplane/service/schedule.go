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

// ScheduleView 定时任务列表项：在 model.Schedule 之上补一个实例名。
//
// 【可见性说明——有意为之的取舍，决策记录】本端点目前**没有 scope 收敛**（不区分调用者可访问的
// 实例范围），加上 instanceName 等于把实例名交给每个能读调度列表的人。我们显式接受这一扩大：
// 改造前前端为了在调度行里显示实例名，直接拉取**全量实例列表**（千级约 1MB + 30 秒兜底轮询），
// 那对调用者暴露的实例清单更完整。若将来为本端点补上 scope 收敛，应同步复查此处。
type ScheduleView struct {
	model.Schedule
	InstanceName string `json:"instanceName,omitempty"`
}

// List 返回定时任务列表（含实例名）。
func (s *ScheduleService) List(instanceID *uint) ([]ScheduleView, error) {
	var schedules []model.Schedule
	q := s.db.Model(&model.Schedule{})
	if instanceID != nil {
		q = q.Where("instance_id = ?", *instanceID)
	}
	if err := q.Find(&schedules).Error; err != nil {
		return nil, err
	}

	views := make([]ScheduleView, len(schedules))
	for i := range schedules {
		views[i] = ScheduleView{Schedule: schedules[i]}
	}

	// 一次主键 IN 查询回填实例名，避免按行查（N+1）。实例是软删除：已删实例查不到名字，
	// 此处留空、由前端回退显示 #id——刻意不用 Unscoped()，那会把已删实例的存在也暴露出去。
	ids := make([]uint, 0, len(schedules))
	seen := make(map[uint]struct{}, len(schedules))
	for _, sc := range schedules {
		if _, ok := seen[sc.InstanceID]; ok {
			continue
		}
		seen[sc.InstanceID] = struct{}{}
		ids = append(ids, sc.InstanceID)
	}
	if len(ids) > 0 {
		var insts []model.Instance
		s.db.Select("id", "name").Where("id IN ?", ids).Find(&insts)
		nameByID := make(map[uint]string, len(insts))
		for _, in := range insts {
			nameByID[in.ID] = in.Name
		}
		for i := range views {
			views[i].InstanceName = nameByID[views[i].InstanceID]
		}
	}

	return views, nil
}

// Update 更新定时任务。
func (s *ScheduleService) Update(id uint, cronExpr *string, enabled *bool, action *string, payload *string) (*model.Schedule, error) {
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
	if payload != nil {
		updates["payload"] = *payload
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
