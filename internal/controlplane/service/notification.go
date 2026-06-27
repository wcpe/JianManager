package service

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ErrNotificationNotFound 站内信不存在（或不属于当前用户）。
var ErrNotificationNotFound = errors.New("站内信不存在")

// NotificationService 站内信服务（FR-183，见 ADR-040）。
// 投递给用户的消息（任务完成/失败等），支持列表、未读计数、标记已读。
type NotificationService struct {
	db *gorm.DB
}

// NewNotificationService 创建站内信服务。
func NewNotificationService(db *gorm.DB) *NotificationService {
	return &NotificationService{db: db}
}

// Create 投递一条站内信给 userID。taskID 可空（非任务类通知）。
func (s *NotificationService) Create(userID uint, level model.NotificationLevel, title, body, taskID string) error {
	n := &model.Notification{
		UserID: userID,
		Level:  level,
		Title:  title,
		Body:   body,
		TaskID: taskID,
	}
	if err := s.db.Create(n).Error; err != nil {
		return fmt.Errorf("创建站内信失败: %w", err)
	}
	return nil
}

// List 列出某用户的站内信（倒序）。onlyUnread 为 true 时只返回未读。limit 默认 50。
func (s *NotificationService) List(userID uint, onlyUnread bool, limit int) ([]model.Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	q := s.db.Where("user_id = ?", userID)
	if onlyUnread {
		q = q.Where("read_at IS NULL")
	}
	var out []model.Notification
	if err := q.Order("created_at DESC, id DESC").Limit(limit).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("查询站内信失败: %w", err)
	}
	return out, nil
}

// UnreadCount 返回某用户的未读站内信数量。
func (s *NotificationService) UnreadCount(userID uint) (int64, error) {
	var n int64
	if err := s.db.Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("统计未读站内信失败: %w", err)
	}
	return n, nil
}

// MarkRead 把某用户的一条站内信标记为已读。归属不符或不存在返回 ErrNotificationNotFound。
func (s *NotificationService) MarkRead(userID, id uint) error {
	now := time.Now()
	res := s.db.Model(&model.Notification{}).
		Where("id = ? AND user_id = ? AND read_at IS NULL", id, userID).
		Update("read_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 可能已读（幂等成功）或不存在/越权。区分一下：存在且属于该用户 → 幂等返回 nil。
		var cnt int64
		s.db.Model(&model.Notification{}).Where("id = ? AND user_id = ?", id, userID).Count(&cnt)
		if cnt == 0 {
			return ErrNotificationNotFound
		}
	}
	return nil
}

// MarkAllRead 把某用户的所有未读站内信标记为已读，返回受影响条数。
func (s *NotificationService) MarkAllRead(userID uint) (int64, error) {
	now := time.Now()
	res := s.db.Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Update("read_at", now)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
