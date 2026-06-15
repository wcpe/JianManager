package service

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// AuditService 审计日志服务。
type AuditService struct {
	db *gorm.DB
}

// NewAuditService 创建审计服务。
func NewAuditService(db *gorm.DB) *AuditService {
	return &AuditService{db: db}
}

// Record 记录审计日志。
func (s *AuditService) Record(userID uint, action, targetType, targetID, detail, ip string) error {
	log := &model.AuditLog{
		UserID:     userID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
		IP:         ip,
	}
	if err := s.db.Create(log).Error; err != nil {
		return fmt.Errorf("记录审计日志失败: %w", err)
	}
	return nil
}

// List 查询审计日志。
type AuditFilter struct {
	UserID     *uint
	Action     *string
	TargetType *string
	From       *string
	To         *string
	Limit      int
}

// List 查询审计日志列表。
func (s *AuditService) List(filter AuditFilter) ([]model.AuditLog, error) {
	var logs []model.AuditLog
	q := s.db.Model(&model.AuditLog{}).Preload("User")

	if filter.UserID != nil {
		q = q.Where("user_id = ?", *filter.UserID)
	}
	if filter.Action != nil {
		q = q.Where("action = ?", *filter.Action)
	}
	if filter.TargetType != nil {
		q = q.Where("target_type = ?", *filter.TargetType)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}

	if err := q.Order("created_at DESC").Limit(limit).Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}
