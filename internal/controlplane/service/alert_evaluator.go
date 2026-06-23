package service

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const (
	// evalInterval 告警求值周期。
	evalInterval = 60 * time.Second

	// onlineThreshold 节点被认为是"在线"的心跳阈值。
	onlineThreshold = 90 * time.Second
)

// AlertEvaluator 周期性评估「轮询型」告警规则（指标阈值 + 节点离线），触发事件经 AlertDispatcher 分发（FR-011 + FR-085）。
// 事件驱动型触发（实例崩溃 / 日志关键字 / 玩家事件 / 备份失败）由 AlertEventTriggers 监听，不在此循环。
type AlertEvaluator struct {
	db         *gorm.DB
	dispatcher *AlertDispatcher
	stopCh     chan struct{}
	running    bool
	mu         sync.Mutex
	// nodeOffline 记录上一轮已离线告警过的节点 ID，避免重复触发（恢复后清除）。
	nodeOffline map[uint]bool
}

// NewAlertEvaluator 创建告警评估器。
func NewAlertEvaluator(db *gorm.DB, dispatcher *AlertDispatcher) *AlertEvaluator {
	return &AlertEvaluator{
		db:          db,
		dispatcher:  dispatcher,
		stopCh:      make(chan struct{}),
		nodeOffline: make(map[uint]bool),
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

// evaluate 单次评估：指标阈值 + 节点离线。
func (e *AlertEvaluator) evaluate() {
	var rules []model.AlertRule
	if err := e.db.Where("enabled = ?", true).Find(&rules).Error; err != nil {
		slog.Error("告警评估：查询告警规则失败", "error", err)
		return
	}
	if len(rules) == 0 {
		return
	}

	// 在线节点（指标阈值评估用）。
	var onlineNodes []model.Node
	cutoff := time.Now().Add(-onlineThreshold)
	if err := e.db.Where("status = ? AND last_heartbeat > ?", model.NodeStatusOnline, cutoff).Find(&onlineNodes).Error; err != nil {
		slog.Error("告警评估：查询在线节点失败", "error", err)
		return
	}

	for i := range rules {
		rule := &rules[i]
		switch ruleTriggerType(rule) {
		case model.AlertTriggerMetric:
			if rule.TargetType == "node" {
				e.evaluateNodeMetricRule(rule, onlineNodes)
			}
		case model.AlertTriggerNodeOffline:
			e.evaluateNodeOfflineRule(rule)
		}
	}
}

// evaluateNodeMetricRule 评估单条节点级指标阈值规则（FR-011），命中经 dispatcher 处理。
func (e *AlertEvaluator) evaluateNodeMetricRule(rule *model.AlertRule, nodes []model.Node) {
	for i := range nodes {
		node := &nodes[i]
		if rule.TargetID != nil && *rule.TargetID != node.ID {
			continue
		}
		value := getNodeMetric(node, rule.Metric)
		if value < 0 {
			continue // 不支持的指标
		}
		key := fmt.Sprintf("metric:%d:%d:%s", rule.ID, node.ID, rule.Metric)
		if compareOp(value, rule.Operator, rule.Threshold) {
			e.dispatcher.Fire(AlertTrigger{
				Rule:       rule,
				TargetID:   node.ID,
				DedupKey:   key,
				Value:      value,
				Message:    fmt.Sprintf("节点 %s 指标 %s", node.Name, formatAlertMessage(rule.Metric, rule.Operator, rule.Threshold)),
				Resolvable: true,
			})
		} else {
			e.dispatcher.Resolve(rule, key, fmt.Sprintf("节点 %s 指标 %s 已恢复", node.Name, rule.Metric))
		}
	}
}

// evaluateNodeOfflineRule 评估节点离线规则（FR-085）。
// 节点心跳超时被离线检测器标记 offline 后触发；恢复在线后发恢复通知。
func (e *AlertEvaluator) evaluateNodeOfflineRule(rule *model.AlertRule) {
	var nodes []model.Node
	q := e.db.Model(&model.Node{})
	if rule.TargetID != nil {
		q = q.Where("id = ?", *rule.TargetID)
	}
	if err := q.Find(&nodes).Error; err != nil {
		return
	}
	for i := range nodes {
		node := &nodes[i]
		key := fmt.Sprintf("node_offline:%d:%d", rule.ID, node.ID)
		offline := node.Status == model.NodeStatusOffline
		e.mu.Lock()
		wasOffline := e.nodeOffline[node.ID]
		e.mu.Unlock()
		if offline && !wasOffline {
			e.mu.Lock()
			e.nodeOffline[node.ID] = true
			e.mu.Unlock()
			e.dispatcher.Fire(AlertTrigger{
				Rule:       rule,
				TargetID:   node.ID,
				DedupKey:   key,
				Message:    fmt.Sprintf("节点 %s 离线", node.Name),
				Resolvable: true,
			})
		} else if !offline && wasOffline {
			e.mu.Lock()
			delete(e.nodeOffline, node.ID)
			e.mu.Unlock()
			e.dispatcher.Resolve(rule, key, fmt.Sprintf("节点 %s 已恢复在线", node.Name))
		}
	}
}

// getNodeMetric 从节点对象获取指标值。
func getNodeMetric(node *model.Node, metric string) float64 {
	switch metric {
	case "cpu", "cpu_usage":
		return float64(node.CPUUsage)
	case "memory", "memory_usage":
		return float64(node.MemoryUsage)
	case "disk", "disk_usage":
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

// formatAlertMessage 生成指标告警消息片段。
func formatAlertMessage(metric, operator string, threshold float64) string {
	return fmt.Sprintf("%s %s %g", metric, operator, threshold)
}
