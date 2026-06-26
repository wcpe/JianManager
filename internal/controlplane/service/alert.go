package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	ErrAlertRuleNotFound  = errors.New("告警规则不存在")
	ErrAlertEventNotFound = errors.New("告警事件不存在")
)

// AlertService 告警服务（FR-011 + FR-085）。
type AlertService struct {
	db *gorm.DB
}

// NewAlertService 创建告警服务。
func NewAlertService(db *gorm.DB) *AlertService {
	return &AlertService{db: db}
}

// CreateRuleRequest 创建告警规则请求（FR-011 + FR-085 扩展）。
type CreateRuleRequest struct {
	Name        string `json:"name" binding:"required"`
	TriggerType string `json:"triggerType"`
	Level       string `json:"level"`
	TargetType  string `json:"targetType" binding:"required"`
	TargetID    *uint  `json:"targetId"`

	// metric 触发
	Metric      string  `json:"metric"`
	Operator    string  `json:"operator"`
	Threshold   float64 `json:"threshold"`
	DurationSec int     `json:"durationSec"`

	// 非指标触发
	Keyword    string `json:"keyword"`
	EventMatch string `json:"eventMatch"`

	// 聚合 / 静默 / 路由
	ChannelIDs     []uint `json:"channelIds"`
	DedupWindowSec int    `json:"dedupWindowSec"`
	SilenceStart   string `json:"silenceStart"`
	SilenceEnd     string `json:"silenceEnd"`
	NotifyRecover  *bool  `json:"notifyRecover"`

	// FR-011 兼容
	NotifyType   string `json:"notifyType"`
	NotifyTarget string `json:"notifyTarget"`
}

// validTriggerTypes / validLevels 合法枚举集。
var validTriggerTypes = map[string]bool{
	model.AlertTriggerMetric: true, model.AlertTriggerInstanceCrash: true,
	model.AlertTriggerNodeOffline: true, model.AlertTriggerLogKeyword: true,
	model.AlertTriggerPlayerEvent: true, model.AlertTriggerBackupFailed: true,
}
var validLevels = map[string]bool{
	model.AlertLevelInfo: true, model.AlertLevelWarn: true, model.AlertLevelCritical: true,
}

// CreateRule 创建告警规则。校验触发类型/级别合法，按类型填默认值。
func (s *AlertService) CreateRule(req CreateRuleRequest) (*model.AlertRule, error) {
	triggerType := req.TriggerType
	if triggerType == "" {
		triggerType = model.AlertTriggerMetric
	}
	if !validTriggerTypes[triggerType] {
		return nil, fmt.Errorf("非法触发类型: %s", triggerType)
	}
	level := req.Level
	if level == "" {
		level = model.AlertLevelWarn
	}
	if !validLevels[level] {
		return nil, fmt.Errorf("非法告警级别: %s", level)
	}

	notifyRecover := true
	if req.NotifyRecover != nil {
		notifyRecover = *req.NotifyRecover
	}

	channelIDs := ""
	if len(req.ChannelIDs) > 0 {
		raw, _ := json.Marshal(req.ChannelIDs)
		channelIDs = string(raw)
	}

	rule := &model.AlertRule{
		Name:           req.Name,
		TriggerType:    triggerType,
		Level:          level,
		TargetType:     req.TargetType,
		TargetID:       req.TargetID,
		Metric:         req.Metric,
		Operator:       req.Operator,
		Threshold:      req.Threshold,
		DurationSec:    req.DurationSec,
		Keyword:        req.Keyword,
		EventMatch:     req.EventMatch,
		ChannelIDs:     channelIDs,
		DedupWindowSec: req.DedupWindowSec,
		SilenceStart:   req.SilenceStart,
		SilenceEnd:     req.SilenceEnd,
		NotifyRecover:  notifyRecover,
		NotifyType:     req.NotifyType,
		NotifyTarget:   req.NotifyTarget,
		Enabled:        true,
	}
	if err := s.db.Create(rule).Error; err != nil {
		return nil, fmt.Errorf("创建告警规则失败: %w", err)
	}
	return rule, nil
}

// ListRules 返回告警规则列表。
func (s *AlertService) ListRules() ([]model.AlertRule, error) {
	var rules []model.AlertRule
	if err := s.db.Order("id DESC").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// UpdateRuleRequest 更新告警规则的可变字段（指针=可选更新）。
type UpdateRuleRequest struct {
	Enabled        *bool    `json:"enabled"`
	Threshold      *float64 `json:"threshold"`
	Level          *string  `json:"level"`
	ChannelIDs     *[]uint  `json:"channelIds"`
	DedupWindowSec *int     `json:"dedupWindowSec"`
	SilenceStart   *string  `json:"silenceStart"`
	SilenceEnd     *string  `json:"silenceEnd"`
	NotifyRecover  *bool    `json:"notifyRecover"`
	Keyword        *string  `json:"keyword"`
	EventMatch     *string  `json:"eventMatch"`
}

// UpdateRule 更新告警规则。
func (s *AlertService) UpdateRule(id uint, req UpdateRuleRequest) (*model.AlertRule, error) {
	updates := map[string]interface{}{}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Threshold != nil {
		updates["threshold"] = *req.Threshold
	}
	if req.Level != nil {
		if !validLevels[*req.Level] {
			return nil, fmt.Errorf("非法告警级别: %s", *req.Level)
		}
		updates["level"] = *req.Level
	}
	if req.ChannelIDs != nil {
		raw, _ := json.Marshal(*req.ChannelIDs)
		updates["channel_ids"] = string(raw)
	}
	if req.DedupWindowSec != nil {
		updates["dedup_window_sec"] = *req.DedupWindowSec
	}
	if req.SilenceStart != nil {
		updates["silence_start"] = *req.SilenceStart
	}
	if req.SilenceEnd != nil {
		updates["silence_end"] = *req.SilenceEnd
	}
	if req.NotifyRecover != nil {
		updates["notify_recover"] = *req.NotifyRecover
	}
	if req.Keyword != nil {
		updates["keyword"] = *req.Keyword
	}
	if req.EventMatch != nil {
		updates["event_match"] = *req.EventMatch
	}
	if len(updates) > 0 {
		result := s.db.Model(&model.AlertRule{}).Where("id = ?", id).Updates(updates)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			return nil, ErrAlertRuleNotFound
		}
	}
	var rule model.AlertRule
	if err := s.db.First(&rule, id).Error; err != nil {
		return nil, ErrAlertRuleNotFound
	}
	return &rule, nil
}

// DeleteRule 删除告警规则。
func (s *AlertService) DeleteRule(id uint) error {
	return s.db.Delete(&model.AlertRule{}, id).Error
}

// EventFilter 告警事件列表筛选条件（FR-085 + FR-149：关键字 / 时间范围 / 分页）。
type EventFilter struct {
	RuleID       *uint
	Resolved     *bool
	Acknowledged *bool
	Level        string
	TriggerType  string
	Keyword      string     // 模糊匹配 message（FR-149）
	From         *time.Time // fired_at 下界（含），FR-149
	To           *time.Time // fired_at 上界（含），FR-149
	Page         int        // 页码，从 1 起；FR-149
	PageSize     int        // 每页条数，<=0 取默认 50；FR-149
}

// ListEvents 返回告警事件分页列表（FR-011 + FR-085 多维筛选 + FR-149 关键字/时间范围/分页）。
// 预加载规则名；返回当前页与命中总数。
func (s *AlertService) ListEvents(f EventFilter) ([]model.AlertEvent, int64, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	q := s.db.Model(&model.AlertEvent{})
	if f.RuleID != nil {
		q = q.Where("rule_id = ?", *f.RuleID)
	}
	if f.Resolved != nil {
		q = q.Where("resolved = ?", *f.Resolved)
	}
	if f.Acknowledged != nil {
		q = q.Where("acknowledged = ?", *f.Acknowledged)
	}
	if f.Level != "" {
		q = q.Where("level = ?", f.Level)
	}
	if f.TriggerType != "" {
		q = q.Where("trigger_type = ?", f.TriggerType)
	}
	if f.Keyword != "" {
		q = q.Where("message LIKE ?", "%"+f.Keyword+"%")
	}
	if f.From != nil {
		q = q.Where("fired_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("fired_at <= ?", *f.To)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var events []model.AlertEvent
	if err := q.Preload("Rule").Order("fired_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).Find(&events).Error; err != nil {
		return nil, 0, err
	}
	if events == nil {
		events = []model.AlertEvent{}
	}
	return events, total, nil
}

// Acknowledge 确认/认领一条告警事件（FR-085）。记录确认人与时间，并置为已读。
func (s *AlertService) Acknowledge(eventID uint, userID uint) (*model.AlertEvent, error) {
	now := time.Now()
	result := s.db.Model(&model.AlertEvent{}).Where("id = ?", eventID).Updates(map[string]interface{}{
		"acknowledged":    true,
		"acknowledged_by": userID,
		"acknowledged_at": now,
		"read":            true,
	})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrAlertEventNotFound
	}
	var event model.AlertEvent
	if err := s.db.Preload("Rule").First(&event, eventID).Error; err != nil {
		return nil, ErrAlertEventNotFound
	}
	return &event, nil
}

// MarkRead 标记一条或全部告警事件为已读（FR-085 站内已读）。eventID 为 0 时标记全部未读。
func (s *AlertService) MarkRead(eventID uint) error {
	q := s.db.Model(&model.AlertEvent{}).Where("read = ?", false)
	if eventID != 0 {
		q = q.Where("id = ?", eventID)
	}
	return q.Update("read", true).Error
}

// UnreadCount 返回未读告警数（站内角标）。
func (s *AlertService) UnreadCount() (int64, error) {
	var n int64
	err := s.db.Model(&model.AlertEvent{}).Where("read = ?", false).Count(&n).Error
	return n, err
}
