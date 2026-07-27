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
var validTargetTypes = map[string]bool{
	"node": true, "instance": true,
}
var validPlayerEventMatches = map[string]bool{
	"": true, "join": true, "quit": true, "chat": true, "cross_server": true,
}

// normalizeRuleTypeAndLevel 填充 FR-011 兼容默认值，并校验触发类型/级别枚举。
func normalizeRuleTypeAndLevel(triggerType, level string) (string, string, error) {
	if triggerType == "" {
		triggerType = model.AlertTriggerMetric
	}
	if !validTriggerTypes[triggerType] {
		return "", "", fmt.Errorf("非法触发类型: %s", triggerType)
	}
	if level == "" {
		level = model.AlertLevelWarn
	}
	if !validLevels[level] {
		return "", "", fmt.Errorf("非法告警级别: %s", level)
	}
	return triggerType, level, nil
}

// expectedTargetTypeForTrigger 返回触发类型要求的目标类型。
func expectedTargetTypeForTrigger(triggerType string) string {
	switch triggerType {
	case model.AlertTriggerMetric, model.AlertTriggerNodeOffline:
		return "node"
	default:
		return "instance"
	}
}

// validateSilenceTime 校验 HH:MM 静默时间，空串表示未设置。
func validateSilenceTime(value string) error {
	if value == "" {
		return nil
	}
	if _, ok := parseHHMM(value); !ok {
		return fmt.Errorf("非法静默时间: %s", value)
	}
	return nil
}

// validateRuleFields 校验规则字段之间的语义约束。
func validateRuleFields(triggerType, targetType, keyword, eventMatch, silenceStart, silenceEnd string, durationSec, dedupWindowSec int) error {
	if !validTargetTypes[targetType] {
		return fmt.Errorf("非法目标类型: %s", targetType)
	}
	if want := expectedTargetTypeForTrigger(triggerType); targetType != want {
		return fmt.Errorf("触发类型 %s 必须使用 %s 目标", triggerType, want)
	}
	if durationSec < 0 {
		return fmt.Errorf("持续时间不能为负")
	}
	if dedupWindowSec < 0 {
		return fmt.Errorf("去抖窗口不能为负")
	}
	if err := validateSilenceTime(silenceStart); err != nil {
		return err
	}
	if err := validateSilenceTime(silenceEnd); err != nil {
		return err
	}
	if triggerType == model.AlertTriggerLogKeyword && keyword == "" {
		return fmt.Errorf("日志关键字不能为空")
	}
	if triggerType == model.AlertTriggerPlayerEvent && !validPlayerEventMatches[eventMatch] {
		return fmt.Errorf("非法玩家事件类型: %s", eventMatch)
	}
	return nil
}

// validateChannelIDs 校验规则引用的通道均存在，避免路由保存悬空引用。
func (s *AlertService) validateChannelIDs(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	seen := map[uint]struct{}{}
	unique := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return fmt.Errorf("通知通道不存在: %d", id)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	var count int64
	if err := s.db.Model(&model.AlertChannel{}).Where("id IN ?", unique).Count(&count).Error; err != nil {
		return err
	}
	if int(count) != len(unique) {
		return fmt.Errorf("通知通道不存在")
	}
	return nil
}

// CreateRule 创建告警规则。校验触发类型/级别合法，按类型填默认值。
func (s *AlertService) CreateRule(req CreateRuleRequest) (*model.AlertRule, error) {
	triggerType, level, err := normalizeRuleTypeAndLevel(req.TriggerType, req.Level)
	if err != nil {
		return nil, err
	}
	if err := validateRuleFields(triggerType, req.TargetType, req.Keyword, req.EventMatch, req.SilenceStart, req.SilenceEnd, req.DurationSec, req.DedupWindowSec); err != nil {
		return nil, err
	}
	if err := s.validateChannelIDs(req.ChannelIDs); err != nil {
		return nil, err
	}
	if err := validateLegacyWebhook(req.NotifyType, req.NotifyTarget); err != nil {
		return nil, err
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
	for i := range rules {
		rules[i].NotifyTarget = ""
	}
	return rules, nil
}

// validateLegacyWebhook 收紧 FR-011 兼容直发通道：URL 常含 token，只允许环境变量引用。
func validateLegacyWebhook(notifyType, notifyTarget string) error {
	if notifyType == "" && notifyTarget == "" {
		return nil
	}
	if notifyType != model.ChannelTypeWebhook || !envRefPattern.MatchString(notifyTarget) {
		return errors.New("webhook 目标必须以 ${ENV_VAR} 形式引用环境变量")
	}
	return nil
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
	var current model.AlertRule
	if err := s.db.First(&current, id).Error; err != nil {
		return nil, ErrAlertRuleNotFound
	}

	level := current.Level
	if level == "" {
		level = model.AlertLevelWarn
	}
	if req.Level != nil {
		level = *req.Level
	}
	if !validLevels[level] {
		return nil, fmt.Errorf("非法告警级别: %s", level)
	}

	dedup := current.DedupWindowSec
	if req.DedupWindowSec != nil {
		dedup = *req.DedupWindowSec
	}
	keyword := current.Keyword
	if req.Keyword != nil {
		keyword = *req.Keyword
	}
	eventMatch := current.EventMatch
	if req.EventMatch != nil {
		eventMatch = *req.EventMatch
	}
	silenceStart := current.SilenceStart
	if req.SilenceStart != nil {
		silenceStart = *req.SilenceStart
	}
	silenceEnd := current.SilenceEnd
	if req.SilenceEnd != nil {
		silenceEnd = *req.SilenceEnd
	}
	if err := validateRuleFields(ruleTriggerType(&current), current.TargetType, keyword, eventMatch, silenceStart, silenceEnd, current.DurationSec, dedup); err != nil {
		return nil, err
	}
	if req.ChannelIDs != nil {
		if err := s.validateChannelIDs(*req.ChannelIDs); err != nil {
			return nil, err
		}
	}

	updates := map[string]interface{}{}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Threshold != nil {
		updates["threshold"] = *req.Threshold
	}
	if req.Level != nil {
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
		if err := s.db.Model(&current).Updates(updates).Error; err != nil {
			return nil, err
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
