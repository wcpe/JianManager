package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

var ErrAlertRuleNotFound = errors.New("告警规则不存在")

// AlertService 告警服务。
type AlertService struct {
	db *gorm.DB
}

// NewAlertService 创建告警服务。
func NewAlertService(db *gorm.DB) *AlertService {
	return &AlertService{db: db}
}

// CreateRuleRequest 创建告警规则请求。
type CreateRuleRequest struct {
	Name         string   `json:"name" binding:"required"`
	TargetType   string   `json:"targetType" binding:"required"`
	TargetID     *uint    `json:"targetId"`
	Metric       string   `json:"metric" binding:"required"`
	Operator     string   `json:"operator" binding:"required"`
	Threshold    float64  `json:"threshold" binding:"required"`
	DurationSec  int      `json:"durationSec"`
	NotifyType   string   `json:"notifyType"`
	NotifyTarget string   `json:"notifyTarget"`
}

// CreateRule 创建告警规则。
func (s *AlertService) CreateRule(req CreateRuleRequest) (*model.AlertRule, error) {
	rule := &model.AlertRule{
		Name:         req.Name,
		TargetType:   req.TargetType,
		TargetID:     req.TargetID,
		Metric:       req.Metric,
		Operator:     req.Operator,
		Threshold:    req.Threshold,
		DurationSec:  req.DurationSec,
		NotifyType:   req.NotifyType,
		NotifyTarget: req.NotifyTarget,
		Enabled:      true,
	}
	if err := s.db.Create(rule).Error; err != nil {
		return nil, fmt.Errorf("创建告警规则失败: %w", err)
	}
	return rule, nil
}

// ListRules 返回告警规则列表。
func (s *AlertService) ListRules() ([]model.AlertRule, error) {
	var rules []model.AlertRule
	if err := s.db.Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// UpdateRule 更新告警规则。
func (s *AlertService) UpdateRule(id uint, enabled *bool, threshold *float64) (*model.AlertRule, error) {
	updates := map[string]interface{}{}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if threshold != nil {
		updates["threshold"] = *threshold
	}
	if len(updates) > 0 {
		result := s.db.Model(&model.AlertRule{}).Where("id = ?", id).Updates(updates)
		if result.RowsAffected == 0 {
			return nil, ErrAlertRuleNotFound
		}
	}
	var rule model.AlertRule
	s.db.First(&rule, id)
	return &rule, nil
}

// DeleteRule 删除告警规则。
func (s *AlertService) DeleteRule(id uint) error {
	return s.db.Delete(&model.AlertRule{}, id).Error
}

// ListEvents 返回告警事件列表。
func (s *AlertService) ListEvents(ruleID *uint, resolved *bool) ([]model.AlertEvent, error) {
	var events []model.AlertEvent
	q := s.db.Model(&model.AlertEvent{})
	if ruleID != nil {
		q = q.Where("rule_id = ?", *ruleID)
	}
	if resolved != nil {
		q = q.Where("resolved = ?", *resolved)
	}
	if err := q.Order("fired_at DESC").Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}
