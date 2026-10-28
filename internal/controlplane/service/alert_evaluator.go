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

	// alertMetricFreshWindow 时序阈值取"最新样本"的新鲜窗口，避免陈样本误判为当前值。
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
		case model.AlertTriggerQuotaExceeded:
			// FR-467：配额超限由 QuotaEnforcer 巡检循环触发（它持有实时用量与配额基准），
			// 不在此评估器内重复判定——两条链路各判一次会产生重复事件。
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
		// iowait 与日志采集面指标不在节点快照中（FR-485）；沿既有别名和 UUID 从时序库取近期样本。
		if isSeriesOnlyNodeMetric(rule.Metric) {
			if e.metrics == nil {
				continue
			}
			metricKey, _ := resolveMetricKey("node", rule.Metric)
			sample, err := e.metrics.LatestValue(model.MetricScopeNode, node.UUID, "", metricKey, time.Now().UTC().Add(-alertMetricFreshWindow))
			if err != nil {
				slog.Warn("告警评估：查询节点指标失败", "rule", rule.Name, "node", node.Name, "error", err)
				continue
			}
			if sample == nil {
				continue // 无近期样本，既不触发也不伪造恢复。
			}
			value = *sample
		}
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
	// 冷启动门槛：窗口内样本时间跨度不足一个窗口时跳过（spec §5），避免开服即误报。
	e.forEachMetricTarget(scope, rule.TargetID, func(nodeUUID, instanceID string, targetID uint, name string) {
		e.evaluateBaselineTarget(rule, scope, metricKey, cfg, nodeUUID, instanceID, targetID, name, from, now, window)
	})
}

// evaluateBaselineTarget 对单目标求基线并触发/恢复。
func (e *AlertEvaluator) evaluateBaselineTarget(rule *model.AlertRule, scope, metricKey string, cfg BaselineConfig, nodeUUID, instanceID string, targetID uint, name string, from, now time.Time, windowSec int) {
	points, err := e.fetchBaselinePoints(scope, nodeUUID, instanceID, metricKey, from, now)
	if err != nil {
		slog.Warn("告警评估：查询基线窗口失败", "rule", rule.Name, "target", targetID, "error", err)
		return
	}
	if !baselineWindowCovered(points, windowSec) {
		return // 冷启动：样本不足一个窗口，跳过（不发告警，避免开服误报）
	}
	result := EvaluateBaseline(points, cfg)
	key := fmt.Sprintf("baseline:%d:%d:%s", rule.ID, targetID, rule.Metric)
	if result.Breach {
		e.dispatcher.Fire(AlertTrigger{
			Rule:       rule,
			TargetID:   targetID,
			DedupKey:   key,
			Value:      result.Value,
			Direction:  result.Direction,
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
	e.forEachSaturationTarget(scope, rule.TargetID, usedKey, func(nodeUUID, instanceID string, targetID uint, name string, snapshotLimit *float64) {
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
		// 回退：无配对上限序列时用节点快照容量（生产从未写入 node_*_total 序列）。
		if limit == nil {
			limit = snapshotLimit
		}
		pct, ok := saturationPercent(usedKey, used, limit)
		if !ok {
			// N2：used/limit 任一缺失时 saturationPercent 返回 false，此处既非 Fire 也非 Resolve。
			// 若因节点容量快照缺失（memory_mb/disk_total_mb ≤ 0，见 nodeSnapshotLimit）而无上限，
			// 明确留痕告警，避免「disk/mem 永不触发」再次静默复发且无任何诊断信号。
			if limit == nil {
				slog.Warn("告警评估：饱和度上限缺失，跳过评估",
					"rule", rule.Name, "target", targetID, "metric", rule.Metric, "scope", scope,
					"reason", "节点容量快照缺失（memory_mb/disk_total_mb 为 0）且无配对上限序列")
			}
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

// forEachSaturationTarget 遍历饱和度评估目标，并在节点维度附带快照上限回退值
// （instance 维度无节点快照，heap 上限由 inst_heap_max 序列提供，故传 nil）。
func (e *AlertEvaluator) forEachSaturationTarget(scope string, targetID *uint, usedKey string, fn func(nodeUUID, instanceID string, id uint, name string, snapshotLimit *float64)) {
	if scope == "instance" {
		for _, inst := range e.metricTargetInstances(targetID) {
			fn("", inst.UUID, inst.ID, inst.Name, nil)
		}
		return
	}
	var nodes []model.Node
	q := e.db.Model(&model.Node{}).Select("id, uuid, name, memory_mb, disk_total_mb")
	if targetID != nil {
		q = q.Where("id = ?", *targetID)
	}
	if err := q.Find(&nodes).Error; err != nil {
		slog.Warn("告警评估：查询评估节点失败", "error", err)
		return
	}
	for i := range nodes {
		fn(nodes[i].UUID, "", nodes[i].ID, nodes[i].Name, nodeSnapshotLimit(usedKey, &nodes[i]))
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
//
// 量纲陷阱（FR-485 取证结论，务必先读完再改本函数）：
// nodes 表的 cpu_usage / memory_usage / disk_usage 存的是 **0..1 的分数**
// （Worker 侧 `UsedPercent / 100.0`，见 internal/worker/register/register.go），
// 而告警规则的阈值在界面与直觉上都是 **百分比**（「磁盘 > 85」= 85%）。
// 两者直接比较时，「磁盘 85%」要写成阈值 0.85 才会触发——几乎没人会这么写，
// 于是这类规则**永不触发且不报错**，是比不实现更糟的静默失效。
//
// 处理方式：**不改旧键语义**（改了会让现网存量规则的阈值含义整体漂移，
// 属于破坏性变更），而是另行提供口径无歧义的百分比键（见 nodeSnapshotPercent）。
func getNodeMetric(node *model.Node, metric string) float64 {
	// 百分比口径键优先（FR-485：让「磁盘 > 85%」这类绝对阈值可表达）。
	if v, ok := nodeSnapshotPercent(node, metric); ok {
		return v
	}
	switch metric {
	case "cpu", "cpu_usage":
		return float64(node.CPUUsage)
	case "memory", "memory_usage":
		return float64(node.MemoryUsage)
	case "disk", "disk_usage":
		return float64(node.DiskUsage)
	case "log_ingest_healthy": // FR-485：采集健康（1=健康，0=停摆）
		if node.LogIngestHealthy {
			return 1
		}
		return 0
	default:
		// FR-485：其余日志采集面指标都在时序库里（心跳写入），不在 nodes 表。
		// 返回 -1 让调用方的时序取值路径接手；这里不做转换是因为本函数拿不到 DB。
		return -1
	}
}

// logMetricKeys 是「只存在于时序库、不在 nodes 表」的日志采集面指标键集合（FR-485）。
//
// 单独成集合而不是塞进 case 分支的理由：evaluateNodeMetricRule 必须先知道
// 「这个指标不该从快照取」，才能改走时序路径；两层各写一份判断会出现
// 「快照返回 -1 但没人去时序取值」的静默不告警。故判据只在这里定义一次。
var logMetricKeys = map[string]struct{}{
	model.MetricLogStateFileSize:    {},
	model.MetricLogStateFileGrowth:  {},
	model.MetricLogPersistWrite:     {},
	model.MetricLogWALBacklog:       {},
	model.MetricLogWALPaused:        {},
	model.MetricLogDeliveryFailRate: {},
	model.MetricLogVerifyBackoff:    {},
	model.MetricLogSlowSQLCount:     {},
	model.MetricLogSlowSQLMaxMs:     {},
}

// isSeriesOnlyNodeMetric 判断节点指标是否只能从时序库取（不在 nodes 快照里）。
//
// iowait 与全部日志采集面指标同属此类：snapshot 里没有它们，只有心跳写入的时序序列。
func isSeriesOnlyNodeMetric(ruleMetric string) bool {
	metricKey, ok := resolveMetricKey("node", ruleMetric)
	if !ok {
		return false
	}
	if metricKey == model.MetricNodeIOWait {
		return true
	}
	_, isLog := logMetricKeys[metricKey]
	return isLog
}

// nodeSnapshotPercent 把节点快照的分数读数换算为百分比（0..100）。
//
// 为什么单列独立键而不是直接改旧键：旧键（cpu/memory/disk 及 cpu_usage 等）
// 的阈值已被现网存量规则引用，原地改变量纲等于把所有存量规则的阈值悄悄放大 100 倍
// （写 90 的人突然要 9000% 才触发），那次变更会比原来的缺陷更难发现。
// 新增键是**加法**，存量零影响。
//
// 为什么也不能复用 cpu_pct：那个名字已被 nodeMetricAliases 占用，指向时序键
// node_cpu_pct（供 iowait 那条同型的时序取值路径使用）。同名不同源会让规则
// 在不同代码路径下取到不同口径的值——这类分叉是排障时最难定位的一种。
//
// 返回 ok=false 表示该指标不属于百分比键（由 getNodeMetric 继续走旧分支）。
func nodeSnapshotPercent(node *model.Node, metric string) (float64, bool) {
	if node == nil {
		return 0, false
	}
	switch metric {
	case "node_cpu_usage_pct":
		return float64(node.CPUUsage) * 100, true
	case "node_memory_usage_pct", "node_mem_usage_pct":
		return float64(node.MemoryUsage) * 100, true
	case "node_disk_usage_pct":
		return float64(node.DiskUsage) * 100, true
	}
	return 0, false
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
