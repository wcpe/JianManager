package service

import (
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

const (
	// evalInterval 告警求值周期。
	evalInterval = 60 * time.Second

	// onlineThreshold 节点被认为是"在线"的心跳阈值。
	onlineThreshold = 90 * time.Second
)

// AlertEvaluator 周期性评估告警规则，触发告警事件并通过 Webhook 通知。
type AlertEvaluator struct {
	db      *gorm.DB
	webhook *WebhookNotifier
	stopCh  chan struct{}
	running bool
	mu      sync.Mutex
}

// NewAlertEvaluator 创建告警评估器。
func NewAlertEvaluator(db *gorm.DB) *AlertEvaluator {
	return &AlertEvaluator{
		db:      db,
		webhook: NewWebhookNotifier(),
		stopCh:  make(chan struct{}),
	}
}

// Start 启动告警评估循环。
func (e *AlertEvaluator) Start() {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	e.mu.Unlock()

	go func() {
		ticker := time.NewTicker(evalInterval)
		defer ticker.Stop()

		// 启动后立即执行一次
		e.evaluate()

		for {
			select {
			case <-e.stopCh:
				return
			case <-ticker.C:
				e.evaluate()
			}
		}
	}()

	slog.Info("告警评估器已启动", "interval", evalInterval)
}

// Stop 停止告警评估循环。
func (e *AlertEvaluator) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.running {
		return
	}

	close(e.stopCh)
	e.running = false
	slog.Info("告警评估器已停止")
}

// evaluate 单次评估：拉取在线节点指标，对比规则，触发/恢复告警。
func (e *AlertEvaluator) evaluate() {
	// 1. 拉取在线节点
	var nodes []model.Node
	cutoff := time.Now().Add(-onlineThreshold)
	if err := e.db.Where("status = ? AND last_heartbeat > ?", model.NodeStatusOnline, cutoff).Find(&nodes).Error; err != nil {
		slog.Error("告警评估：查询在线节点失败", "error", err)
		return
	}

	if len(nodes) == 0 {
		return
	}

	// 2. 拉取已启用的告警规则
	var rules []model.AlertRule
	if err := e.db.Where("enabled = ?", true).Find(&rules).Error; err != nil {
		slog.Error("告警评估：查询告警规则失败", "error", err)
		return
	}

	if len(rules) == 0 {
		return
	}

	// 3. 逐规则评估
	for i := range rules {
		if rules[i].TargetType != "node" {
			// 当前仅支持节点级告警
			continue
		}
		e.evaluateNodeRule(&rules[i], nodes)
	}
}

// evaluateNodeRule 评估单条节点级告警规则。
func (e *AlertEvaluator) evaluateNodeRule(rule *model.AlertRule, nodes []model.Node) {
	for i := range nodes {
		node := &nodes[i]
		// 如果规则指定了 targetId，只评估该节点
		if rule.TargetID != nil && *rule.TargetID != node.ID {
			continue
		}

		value := getNodeMetric(node, rule.Metric)
		if value < 0 {
			// 不支持的指标
			continue
		}

		triggered := compareOp(value, rule.Operator, rule.Threshold)

		// 查找该规则+节点的最新告警事件
		var lastEvent model.AlertEvent
		lastErr := e.db.Where("rule_id = ? AND target_id = ?", rule.ID, node.ID).
			Order("fired_at DESC").First(&lastEvent).Error
		hasLast := lastErr == nil

		if triggered {
			if hasLast && !lastEvent.Resolved {
				// 已经在告警中，不重复触发
				continue
			}
			// 触发新告警
			e.fireEvent(rule, node.ID, value)
		} else {
			if hasLast && !lastEvent.Resolved {
				// 恢复告警
				e.resolveEvent(&lastEvent, value)
			}
		}
	}
}

// getNodeMetric 从节点对象获取指标值。
func getNodeMetric(node *model.Node, metric string) float64 {
	switch metric {
	case "cpu":
		return float64(node.CPUUsage)
	case "memory":
		return float64(node.MemoryUsage)
	case "disk":
		return float64(node.DiskUsage)
	default:
		return -1
	}
}

// compareOp 比较运算。
func compareOp(value float64, operator string, threshold float64) bool {
	switch operator {
	case ">":
		return value > threshold
	case ">=":
		return value >= threshold
	case "<":
		return value < threshold
	case "<=":
		return value <= threshold
	case "==":
		return value == threshold
	default:
		return false
	}
}

// fireEvent 触发告警事件并发送 Webhook。
func (e *AlertEvaluator) fireEvent(rule *model.AlertRule, targetID uint, value float64) {
	msg := formatAlertMessage(rule.Metric, rule.Operator, rule.Threshold)

	event := &model.AlertEvent{
		RuleID:   rule.ID,
		TargetID: targetID,
		Value:    value,
		Message:  msg,
		Resolved: false,
		FiredAt:  time.Now(),
	}

	if err := e.db.Create(event).Error; err != nil {
		slog.Error("告警评估：创建告警事件失败", "ruleId", rule.UUID, "error", err)
		return
	}

	slog.Warn("告警触发", "rule", rule.Name, "targetId", targetID,
		"metric", rule.Metric, "value", value, "threshold", rule.Threshold)

	// 发送 Webhook
	if rule.NotifyType == "webhook" && rule.NotifyTarget != "" {
		payload := WebhookPayload{
			Event:   "alert_fired",
			RuleID:  rule.UUID,
			Target:  rule.TargetType,
			Value:   value,
			Message: msg,
			Time:    time.Now().Format(time.RFC3339),
		}
		if err := e.webhook.Send(rule.NotifyTarget, payload); err != nil {
			slog.Warn("告警 Webhook 发送失败", "ruleId", rule.UUID, "error", err)
		}
	}
}

// resolveEvent 标记告警事件为已恢复并发送 Webhook。
func (e *AlertEvaluator) resolveEvent(event *model.AlertEvent, value float64) {
	now := time.Now()
	if err := e.db.Model(event).Updates(map[string]interface{}{
		"resolved":    true,
		"resolved_at": now,
	}).Error; err != nil {
		slog.Error("告警评估：标记恢复失败", "eventId", event.ID, "error", err)
		return
	}

	// 查找规则以发送 Webhook
	var rule model.AlertRule
	if err := e.db.First(&rule, event.RuleID).Error; err != nil {
		return
	}

	slog.Info("告警恢复", "rule", rule.Name, "targetId", event.TargetID, "value", value)

	if rule.NotifyType == "webhook" && rule.NotifyTarget != "" {
		payload := WebhookPayload{
			Event:   "alert_resolved",
			RuleID:  rule.UUID,
			Target:  rule.TargetType,
			Value:   value,
			Message: "告警已恢复",
			Time:    now.Format(time.RFC3339),
		}
		if err := e.webhook.Send(rule.NotifyTarget, payload); err != nil {
			slog.Warn("告警恢复 Webhook 发送失败", "ruleId", rule.UUID, "error", err)
		}
	}
}

// formatAlertMessage 生成告警消息。
func formatAlertMessage(metric, operator string, threshold float64) string {
	return metric + " " + operator + " " + formatFloat(threshold)
}

func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return formatInt(int64(v))
	}
	// 简单保留两位小数
	whole := int64(v)
	frac := int64((v - float64(whole)) * 100)
	if frac < 0 {
		frac = -frac
	}
	return formatInt(whole) + "." + formatInt(frac/10) + formatInt(frac%10)
}

func formatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	result := ""
	for v > 0 {
		d := v % 10
		result = string(rune('0'+d)) + result
		v /= 10
	}
	if neg {
		result = "-" + result
	}
	return result
}
