package service

import (
	"fmt"
	"log/slog"
	"sort"
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

	// alertMetricFreshWindow 实例级/饱和度阈值取"最新样本"的新鲜窗口，避免陈样本误判为当前值。
	alertMetricFreshWindow = 5 * time.Minute

	// baselineDefaultWindowSec 动态基线默认窗口（秒）。
	baselineDefaultWindowSec = 3600
)

// alertMetricSource 是基线/饱和度/实例级阈值评估所需的时序数据源（*MetricService 实现）。
type alertMetricSource interface {
	// QuerySeries 取窗口内时序曲线（node/instance）。
	QuerySeries(q SeriesQuery) (string, []Series, error)
	// LatestValue 取某目标某指标在 since 之后的最新非空样本值。
	LatestValue(scope model.MetricScope, nodeUUID, instanceID, metricKey string, since time.Time) (*float64, error)
}

// AlertEvaluator 周期性评估「轮询型」告警规则（指标阈值 + 节点离线），触发事件经 AlertDispatcher 分发（FR-011 + FR-085）。
// 事件驱动型触发（实例崩溃 / 日志关键字 / 玩家事件 / 备份失败）由 AlertEventTriggers 监听，不在此循环。
type AlertEvaluator struct {
	db         *gorm.DB
	dispatcher *AlertDispatcher
	// metrics 时序数据源（FR-462）。可为 nil：此时基线/饱和度/实例级 metric 规则不评估。
	metrics alertMetricSource
	stopCh  chan struct{}
	running bool
	mu      sync.Mutex
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

// SetMetrics 注入时序数据源（FR-462）。须在 Start 前调用；未注入时基线/饱和度规则跳过。
func (e *AlertEvaluator) SetMetrics(m alertMetricSource) {
	e.metrics = m
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
			switch rule.TargetType {
			case "node":
				e.evaluateNodeMetricRule(rule, onlineNodes)
			case "instance":
				// FR-462：补齐实例级 metric 规则静默不评估的缺口。
				e.evaluateInstanceMetricRule(rule)
			}
		case model.AlertTriggerNodeOffline:
			e.evaluateNodeOfflineRule(rule)
		case model.AlertTriggerBaseline:
			e.evaluateBaselineRule(rule)
		case model.AlertTriggerSaturation:
			e.evaluateSaturationRule(rule)
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

// evaluateInstanceMetricRule 评估实例级指标阈值规则（FR-462）。当前值取自时序库最新样本，
// 而非节点快照——这是既往前只评估 node 目标导致实例级规则静默的缺口修补。
func (e *AlertEvaluator) evaluateInstanceMetricRule(rule *model.AlertRule) {
	if e.metrics == nil {
		return
	}
	metricKey, ok := resolveMetricKey("instance", rule.Metric)
	if !ok {
		return // 不支持的实例指标
	}
	instances := e.metricTargetInstances(rule.TargetID)
	since := time.Now().UTC().Add(-alertMetricFreshWindow)
	for i := range instances {
		inst := &instances[i]
		value, err := e.metrics.LatestValue(model.MetricScopeInstance, "", inst.UUID, metricKey, since)
		if err != nil {
			slog.Warn("告警评估：查询实例指标失败", "rule", rule.Name, "instance", inst.Name, "error", err)
			continue
		}
		if value == nil {
			continue // 无近期样本，跳过
		}
		key := fmt.Sprintf("metric:%d:%d:%s", rule.ID, inst.ID, rule.Metric)
		if compareOp(*value, rule.Operator, rule.Threshold) {
			e.dispatcher.Fire(AlertTrigger{
				Rule:       rule,
				TargetID:   inst.ID,
				DedupKey:   key,
				Value:      *value,
				Message:    fmt.Sprintf("实例 %s 指标 %s（当前 %g）", inst.Name, formatAlertMessage(rule.Metric, rule.Operator, rule.Threshold), *value),
				Resolvable: true,
			})
		} else {
			e.dispatcher.Resolve(rule, key, fmt.Sprintf("实例 %s 指标 %s 已恢复", inst.Name, rule.Metric))
		}
	}
}

// evaluateBaselineRule 评估动态基线规则（FR-462）：EWMA / 同环比 / 突升突降，从时序库现算。
func (e *AlertEvaluator) evaluateBaselineRule(rule *model.AlertRule) {
	if e.metrics == nil {
		return
	}
	scope := ruleScope(rule)
	metricKey, ok := resolveMetricKey(scope, rule.Metric)
	if !ok {
		return
	}
	window := rule.BaselineWindowSec
	if window <= 0 {
		window = baselineDefaultWindowSec
	}
	cfg := BaselineConfig{
		Method:      rule.BaselineMethod,
		Sensitivity: rule.Sensitivity,
		MinDelta:    rule.MinDelta,
		Direction:   rule.Direction,
		DurationSec: rule.DurationSec,
	}
	now := time.Now().UTC()
	from := now.Add(-time.Duration(window) * time.Second)
	e.forEachMetricTarget(scope, rule.TargetID, func(nodeUUID, instanceID string, targetID uint, name string) {
		e.evaluateBaselineTarget(rule, scope, metricKey, cfg, nodeUUID, instanceID, targetID, name, from, now)
	})
}

// evaluateBaselineTarget 对单目标求基线并触发/恢复。
func (e *AlertEvaluator) evaluateBaselineTarget(rule *model.AlertRule, scope, metricKey string, cfg BaselineConfig, nodeUUID, instanceID string, targetID uint, name string, from, now time.Time) {
	points, err := e.fetchBaselinePoints(scope, nodeUUID, instanceID, metricKey, from, now)
	if err != nil {
		slog.Warn("告警评估：查询基线窗口失败", "rule", rule.Name, "target", targetID, "error", err)
		return
	}
	if len(points) < baselineMinSamples {
		return // 冷启动：样本不足一个合理窗口，跳过（不发告警，避免开服误报）
	}
	result := EvaluateBaseline(points, cfg)
	key := fmt.Sprintf("baseline:%d:%d:%s", rule.ID, targetID, rule.Metric)
	if result.Breach {
		e.dispatcher.Fire(AlertTrigger{
			Rule:       rule,
			TargetID:   targetID,
			DedupKey:   key,
			Value:      result.Value,
			Message:    fmt.Sprintf("%s %s 基线%s偏离：当前 %g，基线 %g，偏离 %g", scopeLabel(scope), name, directionLabel(result.Direction), result.Value, result.Baseline, result.Deviation),
			Resolvable: true,
		})
	} else {
		e.dispatcher.Resolve(rule, key, fmt.Sprintf("%s %s 基线已恢复正常", scopeLabel(scope), name))
	}
}

// evaluateSaturationRule 评估饱和度规则（FR-462）：used/max 逼近上限持续触发（disk/mem/heap）。
func (e *AlertEvaluator) evaluateSaturationRule(rule *model.AlertRule) {
	if e.metrics == nil {
		return
	}
	scope := ruleScope(rule)
	usedKey, ok := resolveMetricKey(scope, rule.Metric)
	if !ok {
		return
	}
	limitKey := saturationLimitKey(usedKey)
	metricScope := metricScopeOf(scope)
	since := time.Now().UTC().Add(-alertMetricFreshWindow)
	e.forEachMetricTarget(scope, rule.TargetID, func(nodeUUID, instanceID string, targetID uint, name string) {
		used, err := e.metrics.LatestValue(metricScope, nodeUUID, instanceID, usedKey, since)
		if err != nil {
			slog.Warn("告警评估：查询饱和度已用值失败", "rule", rule.Name, "target", targetID, "error", err)
			return
		}
		var limit *float64
		if limitKey != "" {
			limit, err = e.metrics.LatestValue(metricScope, nodeUUID, instanceID, limitKey, since)
			if err != nil {
				return
			}
		}
		pct, ok := saturationPercent(usedKey, used, limit)
		if !ok {
			return
		}
		key := fmt.Sprintf("saturation:%d:%d:%s", rule.ID, targetID, rule.Metric)
		if pct >= rule.Threshold {
			e.dispatcher.Fire(AlertTrigger{
				Rule:       rule,
				TargetID:   targetID,
				DedupKey:   key,
				Value:      pct,
				Message:    fmt.Sprintf("%s %s %s 饱和度 %.1f%% ≥ %.1f%%", scopeLabel(scope), name, rule.Metric, pct, rule.Threshold),
				Resolvable: true,
			})
		} else {
			e.dispatcher.Resolve(rule, key, fmt.Sprintf("%s %s %s 饱和度已回落", scopeLabel(scope), name, rule.Metric))
		}
	})
}

// forEachMetricTarget 遍历某 scope 下需要评估的目标（受 rule.TargetID 收敛）。
func (e *AlertEvaluator) forEachMetricTarget(scope string, targetID *uint, fn func(nodeUUID, instanceID string, id uint, name string)) {
	if scope == "instance" {
		for _, inst := range e.metricTargetInstances(targetID) {
			fn("", inst.UUID, inst.ID, inst.Name)
		}
		return
	}
	var nodes []model.Node
	q := e.db.Model(&model.Node{}).Select("id, uuid, name")
	if targetID != nil {
		q = q.Where("id = ?", *targetID)
	}
	if err := q.Find(&nodes).Error; err != nil {
		slog.Warn("告警评估：查询评估节点失败", "error", err)
		return
	}
	for i := range nodes {
		fn(nodes[i].UUID, "", nodes[i].ID, nodes[i].Name)
	}
}

// metricTargetInstances 返回需评估的实例；未指定 TargetID 时取运行中的实例。
func (e *AlertEvaluator) metricTargetInstances(targetID *uint) []model.Instance {
	var instances []model.Instance
	q := e.db.Model(&model.Instance{}).Select("id, uuid, name")
	if targetID != nil {
		q = q.Where("id = ?", *targetID)
	} else {
		q = q.Where("status = ?", model.InstanceStatusRunning)
	}
	if err := q.Find(&instances).Error; err != nil {
		slog.Warn("告警评估：查询评估实例失败", "error", err)
		return nil
	}
	return instances
}

// fetchBaselinePoints 经 MetricService 取窗口内某指标的时序点（按时间升序）。
func (e *AlertEvaluator) fetchBaselinePoints(scope, nodeUUID, instanceID, metricKey string, from, to time.Time) ([]BaselinePoint, error) {
	_, series, err := e.metrics.QuerySeries(SeriesQuery{
		Scope:      metricScopeOf(scope),
		NodeUUID:   nodeUUID,
		InstanceID: instanceID,
		MetricKeys: []string{metricKey},
		From:       from,
		To:         to,
	})
	if err != nil {
		return nil, err
	}
	points := make([]BaselinePoint, 0)
	for _, s := range series {
		if s.MetricKey != metricKey || s.World != "" {
			continue
		}
		for _, p := range s.Points {
			if p.Avg == nil {
				continue
			}
			points = append(points, BaselinePoint{TS: p.TS, Value: *p.Avg})
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].TS.Before(points[j].TS) })
	return points, nil
}

// ruleScope 解析规则评估维度：显式 Scope 优先，否则按 TargetType 推导。
func ruleScope(rule *model.AlertRule) string {
	if rule.Scope == "node" || rule.Scope == "instance" {
		return rule.Scope
	}
	if rule.TargetType == "instance" {
		return "instance"
	}
	return "node"
}

func metricScopeOf(scope string) model.MetricScope {
	if scope == "instance" {
		return model.MetricScopeInstance
	}
	return model.MetricScopeNode
}

func scopeLabel(scope string) string {
	if scope == "instance" {
		return "实例"
	}
	return "节点"
}

func directionLabel(direction string) string {
	if direction == model.BaselineDirectionDown {
		return "下"
	}
	if direction == model.BaselineDirectionUp {
		return "上"
	}
	return ""
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
