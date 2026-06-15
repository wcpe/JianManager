package service

import (
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// Scheduler 定时任务调度器。
type Scheduler struct {
	db       *gorm.DB
	stopCh   chan struct{}
	running  bool
	mu       sync.Mutex
	executor ScheduleExecutor
}

// ScheduleExecutor 定时任务执行器接口。
type ScheduleExecutor interface {
	ExecuteSchedule(schedule *model.Schedule) error
}

// NewScheduler 创建调度器。
func NewScheduler(db *gorm.DB, executor ScheduleExecutor) *Scheduler {
	return &Scheduler{
		db:       db,
		stopCh:   make(chan struct{}),
		executor: executor,
	}
}

// Start 启动调度器（每分钟检查一次）。
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-s.stopCh:
				return
			case now := <-ticker.C:
				s.checkAndRun(now)
			}
		}
	}()

	slog.Info("定时任务调度器已启动")
}

// Stop 停止调度器。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	close(s.stopCh)
	s.running = false
	slog.Info("定时任务调度器已停止")
}

// checkAndRun 检查并执行到期的定时任务。
func (s *Scheduler) checkAndRun(now time.Time) {
	var schedules []model.Schedule
	if err := s.db.Where("enabled = ?", true).Find(&schedules).Error; err != nil {
		slog.Error("查询定时任务失败", "error", err)
		return
	}

	for i := range schedules {
		if s.shouldRun(&schedules[i], now) {
			go s.runSchedule(&schedules[i])
		}
	}
}

// shouldRun 判断定时任务是否应该执行。
// 简化实现：只支持基本的 cron 格式匹配。
func (s *Scheduler) shouldRun(schedule *model.Schedule, now time.Time) bool {
	// 如果有上次执行时间，检查是否已过了一分钟以上
	if schedule.LastRun != nil {
		if now.Sub(*schedule.LastRun) < time.Minute {
			return false
		}
	}

	// 简化的 cron 匹配（完整实现需要 cron 解析库）
	return matchesCron(schedule.CronExpr, now)
}

// matchesCron 简化的 cron 表达式匹配。
// 支持格式: "分 时 日 月 周"
// 特殊值: * 表示任意, 数字表示精确匹配
func matchesCron(expr string, now time.Time) bool {
	// TODO: 实现完整的 cron 表达式解析
	// 目前返回 false，避免误触发
	_ = expr
	_ = now
	return false
}

// runSchedule 执行定时任务。
func (s *Scheduler) runSchedule(schedule *model.Schedule) {
	slog.Info("执行定时任务", "scheduleId", schedule.UUID, "action", schedule.Action)

	now := time.Now()
	if err := s.executor.ExecuteSchedule(schedule); err != nil {
		slog.Error("定时任务执行失败", "scheduleId", schedule.UUID, "error", err)
	}

	// 更新最后执行时间
	s.db.Model(&model.Schedule{}).Where("id = ?", schedule.ID).Update("last_run", &now)
}
