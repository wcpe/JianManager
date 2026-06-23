package service

import (
	"encoding/json"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// AlertTrigger 一次告警触发的语义描述，由各触发源（指标评估器 / 事件驱动监听器）产生，
// 交由 AlertDispatcher 统一处理去抖聚合、静默、分级路由与落库（FR-085）。
type AlertTrigger struct {
	Rule     *model.AlertRule
	TargetID uint
	// DedupKey 去抖键：同一键在窗口内复发聚合为同一活跃事件。通常为 rule.ID + target + 触发标识。
	DedupKey string
	Value    float64
	Message  string
	// Resolvable 标记该触发是否可恢复（metric/instance_crash/node_offline 可；日志/玩家事件为瞬时，不可恢复）。
	Resolvable bool
}

// AlertDispatcher 告警分发器（FR-085）。所有触发源经此统一落库 + 通知，
// 集中实现去抖聚合（dedup window）、静默窗口（silence window）、分级路由（按 rule.ChannelIDs → 通道）、
// 恢复通知与站内通知。无状态于内存（聚合状态以 DB 活跃事件为准），可被多触发源并发调用。
type AlertDispatcher struct {
	db       *gorm.DB
	notifier *ChannelNotifier
	// now 注入式时钟，便于测试静默窗口。
	now func() time.Time
}

// NewAlertDispatcher 创建告警分发器。
func NewAlertDispatcher(db *gorm.DB) *AlertDispatcher {
	return &AlertDispatcher{
		db:       db,
		notifier: NewChannelNotifier(),
		now:      time.Now,
	}
}

// Fire 处理一次告警触发：去抖聚合 → 落库（新建或累计）→ 静默判定 → 分级路由通知 + 站内。
func (d *AlertDispatcher) Fire(trig AlertTrigger) {
	rule := trig.Rule
	now := d.now()

	// 1. 查活跃（未恢复）事件做去抖聚合。
	var active model.AlertEvent
	err := d.db.Where("rule_id = ? AND dedup_key = ? AND resolved = ?", rule.ID, trig.DedupKey, false).
		Order("fired_at DESC").First(&active).Error
	hasActive := err == nil

	level := ruleLevel(rule)

	if hasActive {
		// 去抖窗口内：累计计数、更新复发时间，不重复建事件。
		windowSec := rule.DedupWindowSec
		if windowSec > 0 && now.Sub(deref(active.LastFiredAt, active.FiredAt)) < time.Duration(windowSec)*time.Second {
			d.db.Model(&active).Updates(map[string]interface{}{
				"count":         active.Count + 1,
				"last_fired_at": now,
			})
			// 窗口内复发不再通知（去抖）。
			return
		}
		// 已有活跃事件但超出去抖窗口（或未配置去抖）：对可恢复型不重复建（避免风暴），仅累计。
		if trig.Resolvable {
			d.db.Model(&active).Updates(map[string]interface{}{
				"count":         active.Count + 1,
				"last_fired_at": now,
			})
			return
		}
		// 不可恢复型（日志/玩家事件）：超窗后视为新一轮，落新事件。
	}

	// 2. 新建告警事件。
	event := &model.AlertEvent{
		RuleID:      rule.ID,
		TargetID:    trig.TargetID,
		Level:       level,
		TriggerType: ruleTriggerType(rule),
		DedupKey:    trig.DedupKey,
		Value:       trig.Value,
		Message:     trig.Message,
		Count:       1,
		Resolved:    !trig.Resolvable, // 瞬时事件直接落为已解决（无需后续恢复）。
		FiredAt:     now,
		LastFiredAt: &now,
	}
	if err := d.db.Create(event).Error; err != nil {
		slog.Error("告警分发：创建事件失败", "rule", rule.Name, "error", err)
		return
	}
	slog.Warn("告警触发", "rule", rule.Name, "level", level, "target", trig.TargetID, "message", trig.Message)

	// 3. 站内通知始终落库（确认/历史以事件库为准），已读=false。
	//    站内即事件本身，无需额外表。

	// 4. 静默窗口内：不发外部通知（仍入库）。
	if inSilenceWindow(rule.SilenceStart, rule.SilenceEnd, now) {
		slog.Info("告警处于静默窗口，跳过外部通知", "rule", rule.Name)
		return
	}

	// 5. 分级路由：按 rule.ChannelIDs 投递到各通道。
	d.notify(rule, AlertNotification{
		Event:   "alert_fired",
		RuleID:  rule.UUID,
		Title:   rule.Name,
		Message: trig.Message,
		Level:   level,
		Count:   event.Count,
		Time:    now,
	})
}

// Resolve 标记某去抖键的活跃事件为已恢复，并按 NotifyRecover 发送恢复通知。
func (d *AlertDispatcher) Resolve(rule *model.AlertRule, dedupKey string, message string) {
	var active model.AlertEvent
	err := d.db.Where("rule_id = ? AND dedup_key = ? AND resolved = ?", rule.ID, dedupKey, false).
		Order("fired_at DESC").First(&active).Error
	if err != nil {
		return // 无活跃事件，无需恢复。
	}
	now := d.now()
	if err := d.db.Model(&active).Updates(map[string]interface{}{
		"resolved":    true,
		"resolved_at": now,
	}).Error; err != nil {
		slog.Error("告警分发：标记恢复失败", "eventId", active.ID, "error", err)
		return
	}
	slog.Info("告警恢复", "rule", rule.Name, "target", active.TargetID)

	if !rule.NotifyRecover || inSilenceWindow(rule.SilenceStart, rule.SilenceEnd, now) {
		return
	}
	d.notify(rule, AlertNotification{
		Event:   "alert_resolved",
		RuleID:  rule.UUID,
		Title:   rule.Name + "（已恢复）",
		Message: message,
		Level:   ruleLevel(rule),
		Count:   1,
		Time:    now,
	})
}

// notify 把通知扇出到规则路由的所有通道（FR-085）。
// 优先用 ChannelIDs；为兼容 FR-011，未配置通道但配了 NotifyType=webhook 时回退单 webhook 直发。
func (d *AlertDispatcher) notify(rule *model.AlertRule, note AlertNotification) {
	ids := parseChannelIDs(rule.ChannelIDs)
	if len(ids) == 0 {
		// FR-011 兼容回退：单 webhook 直发。
		if rule.NotifyType == model.ChannelTypeWebhook && rule.NotifyTarget != "" {
			cfg := ChannelConfig{URL: rule.NotifyTarget}
			raw, _ := json.Marshal(cfg)
			if err := d.notifier.Send(model.ChannelTypeWebhook, string(raw), note); err != nil {
				slog.Warn("告警 webhook 直发失败", "rule", rule.Name, "error", err)
			}
		}
		return
	}
	var channels []model.AlertChannel
	if err := d.db.Where("id IN ? AND enabled = ?", ids, true).Find(&channels).Error; err != nil {
		slog.Warn("告警分发：查询通道失败", "rule", rule.Name, "error", err)
		return
	}
	for i := range channels {
		ch := &channels[i]
		if ch.Type == model.ChannelTypeInApp {
			// 站内通知已由事件落库承载，无需外发。
			continue
		}
		if err := d.notifier.Send(ch.Type, ch.Config, note); err != nil {
			slog.Warn("告警通道投递失败", "rule", rule.Name, "channel", ch.Name, "type", ch.Type, "error", err)
		}
	}
}

// ── 纯函数：级别 / 类型 / 静默窗口 ──

// ruleLevel 返回规则级别，空值按 warn。
func ruleLevel(rule *model.AlertRule) string {
	if rule.Level == "" {
		return model.AlertLevelWarn
	}
	return rule.Level
}

// ruleTriggerType 返回规则触发类型，空值按 metric（兼容 FR-011 存量）。
func ruleTriggerType(rule *model.AlertRule) string {
	if rule.TriggerType == "" {
		return model.AlertTriggerMetric
	}
	return rule.TriggerType
}

// inSilenceWindow 判断 t（按本地时钟的时分）是否落在 [start, end) 静默窗口内（"HH:MM"）。
// 支持跨午夜（start > end，如 23:00→07:00）。任一端为空 → 不静默。
func inSilenceWindow(start, end string, t time.Time) bool {
	sm, sok := parseHHMM(start)
	em, eok := parseHHMM(end)
	if !sok || !eok {
		return false
	}
	cur := t.Hour()*60 + t.Minute()
	if sm == em {
		return false // 零宽窗口，视为不静默。
	}
	if sm < em {
		return cur >= sm && cur < em
	}
	// 跨午夜：[start, 24:00) ∪ [00:00, end)
	return cur >= sm || cur < em
}

// parseHHMM 解析 "HH:MM" 为当日分钟数。非法返回 (0,false)。
func parseHHMM(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// deref 返回 *time.Time 的值，nil 时回退 fallback。
func deref(p *time.Time, fallback time.Time) time.Time {
	if p == nil {
		return fallback
	}
	return *p
}
