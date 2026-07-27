package service

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const (
	botLoadCapacitySafetyMargin = 0.25
	botLoadTargetReserveBytes   = int64(8 * 1024 * 1024 * 1024)
)

// BotLoadCapacityReport 是终态报告中的资源容量数据。
type BotLoadCapacityReport struct {
	SchemaVersion     int                        `json:"schemaVersion"`
	TestedScale       BotLoadTestedScale         `json:"testedScale"`
	TargetHostMemory  BotLoadTargetHostMemory    `json:"targetHostMemory"`
	SafetyMarginRatio float64                    `json:"safetyMarginRatio"`
	Stages            []BotLoadCapacityStage     `json:"stages"`
	MeasuredPeak      BotLoadCapacityPeak        `json:"measuredPeak"`
	Recommended       BotLoadCapacityRecommended `json:"recommended"`
	Environment       BotLoadCapacityEnvironment `json:"environment"`
	Disclaimer        string                     `json:"disclaimer"`
}

// BotLoadTestedScale 记录本次运行实际达到的规模。
type BotLoadTestedScale struct {
	PeakBots                  *int `json:"peakBots"`
	PlannedExecutorNodeCount  int  `json:"plannedExecutorNodeCount"`
	ObservedExecutorNodeCount int  `json:"observedExecutorNodeCount"`
	ClaimedAs500              bool `json:"claimedAs500"`
	MaxStableBots             *int `json:"maxStableBots"`
}

// BotLoadTargetHostMemory 记录目标主机的固定内存预留约束。
type BotLoadTargetHostMemory struct {
	TotalBytes    *int64 `json:"totalBytes"`
	UsedPeakBytes *int64 `json:"usedPeakBytes"`
	ReserveBytes  int64  `json:"reserveBytes"`
	BudgetBytes   *int64 `json:"budgetBytes"`
	WithinReserve *bool  `json:"withinReserve"`
}

// BotLoadCapacityMetric 是单项阶段统计值。
type BotLoadCapacityMetric struct {
	Baseline    *float64 `json:"baseline"`
	Peak        *float64 `json:"peak"`
	P95         *float64 `json:"p95"`
	Delta       *float64 `json:"delta"`
	SlopePerBot *float64 `json:"slopePerBot"`
}

// BotLoadCapacityStage 是一个压测阶段的资源统计。
type BotLoadCapacityStage struct {
	StageIndex      int                              `json:"stageIndex"`
	Target          map[string]BotLoadCapacityMetric `json:"target"`
	Executors       []BotLoadCapacityExecutorStage   `json:"executors"`
	ExecutorCluster map[string]BotLoadCapacityMetric `json:"executorCluster"`
	Grand           map[string]BotLoadCapacityMetric `json:"grand"`
	Unavailable     []string                         `json:"unavailable"`
}

// BotLoadCapacityExecutorStage 是单个发压节点在一个阶段的统计。
type BotLoadCapacityExecutorStage struct {
	NodeID      uint                             `json:"nodeId"`
	Health      string                           `json:"health"`
	Metrics     map[string]BotLoadCapacityMetric `json:"metrics"`
	Unavailable []string                         `json:"unavailable"`
}

// BotLoadCapacityObservedPeak 记录一个全程峰值及其观测位置。
type BotLoadCapacityObservedPeak struct {
	Value      *int64     `json:"value"`
	ObservedAt *time.Time `json:"observedAt"`
	StageIndex *int       `json:"stageIndex"`
}

// BotLoadCapacityPeak 区分资源的独立实测峰值。
type BotLoadCapacityPeak struct {
	Bots                             *int                        `json:"bots"`
	TargetProcessRssBytes            BotLoadCapacityObservedPeak `json:"targetProcessRssBytes"`
	ExecutorBotWorkerRssBytesSum     BotLoadCapacityObservedPeak `json:"executorBotWorkerRssBytesSum"`
	ExecutorWorkerProcessRssBytesSum BotLoadCapacityObservedPeak `json:"executorWorkerProcessRssBytesSum"`
	TargetHostMemUsedBytes           BotLoadCapacityObservedPeak `json:"targetHostMemUsedBytes"`
}

// BotLoadCapacityRecommended 是对实测整数资源增加 25% 余量后的建议值。
type BotLoadCapacityRecommended struct {
	TargetProcessRssBytes            *int64 `json:"targetProcessRssBytes"`
	ExecutorBotWorkerRssBytesSum     *int64 `json:"executorBotWorkerRssBytesSum"`
	ExecutorWorkerProcessRssBytesSum *int64 `json:"executorWorkerProcessRssBytesSum"`
}

// BotLoadCapacityEnvironment 保留环境元数据的不可用说明，避免以空值伪造环境事实。
type BotLoadCapacityEnvironment struct {
	Target      map[string]any `json:"target"`
	Executors   []any          `json:"executors"`
	Unavailable []string       `json:"unavailable"`
}

type botLoadCapacitySample struct {
	at        time.Time
	stage     int
	connected *float64
	target    map[string]*float64
	executors map[uint]botLoadCapacityExecutorSample
}

type botLoadCapacityExecutorSample struct {
	health  string
	metrics map[string]*float64
}

type botLoadCapacitySeriesPoint struct {
	value *float64
	bots  *float64
}

var botLoadTargetMetricKeys = []string{
	"processRssBytes", "heapUsedBytes", "heapMaxBytes", "cpuPercent", "uptimeSeconds",
	"hostMemUsedBytes", "hostMemTotalBytes", "tps", "mspt", "onlinePlayers",
}

var botLoadExecutorMetricKeys = []string{
	"activeBots", "botWorkerRssBytes", "eventLoopP95Ms", "nodeMemUsedBytes",
	"nodeMemTotalBytes", "nodeCpuPercent", "workerProcessRssBytes",
}

func buildBotLoadCapacityReport(sess model.BotStressSession, rows []model.BotLoadMetricSample) *BotLoadCapacityReport {
	samples := decodeBotLoadCapacitySamples(rows)
	peakBots := capacityPeakBots(samples)
	observed := capacityObservedExecutorCount(samples)
	memory := capacityTargetHostMemory(samples)
	claim := capacityCanClaimAs500(sess, peakBots, observed, memory.WithinReserve)
	maxStable := capacityMaxStableBots(sess, memory.WithinReserve)
	return &BotLoadCapacityReport{
		SchemaVersion: 1, TestedScale: BotLoadTestedScale{PeakBots: peakBots, PlannedExecutorNodeCount: capacityPlannedExecutorCount(sess), ObservedExecutorNodeCount: observed, ClaimedAs500: claim, MaxStableBots: maxStable},
		TargetHostMemory: memory, SafetyMarginRatio: botLoadCapacitySafetyMargin, Stages: capacityStages(samples),
		MeasuredPeak: capacityMeasuredPeak(samples, peakBots), Recommended: capacityRecommended(samples),
		Environment: BotLoadCapacityEnvironment{Target: map[string]any{}, Executors: []any{}, Unavailable: []string{"target.metadata:UNAVAILABLE", "executors.metadata:UNAVAILABLE"}},
		Disclaimer:  "资源报告不证明玩法正确性；命令成功边界见 ADR-075。measured 为实测，recommended 为 measured×(1+safetyMarginRatio)。",
	}
}

func decodeBotLoadCapacitySamples(rows []model.BotLoadMetricSample) []botLoadCapacitySample {
	out := make([]botLoadCapacitySample, 0, len(rows))
	for _, row := range rows {
		out = append(out, decodeBotLoadCapacitySample(row))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

func decodeBotLoadCapacitySample(row model.BotLoadMetricSample) botLoadCapacitySample {
	counts := decodeJSONMap(row.CountsJSON)
	target := map[string]*float64{}
	if row.TargetLegacyJSON != nil {
		target = capacityTargetMetrics(decodeJSONMap(*row.TargetLegacyJSON))
	}
	return botLoadCapacitySample{at: row.SampledAt.UTC(), stage: row.StageIndex, connected: capacityNumber(counts["connected"]), target: target, executors: capacityExecutorSamples(row.ExecutorJSON)}
}

func capacityTargetMetrics(raw map[string]any) map[string]*float64 {
	resource, ok := raw["targetResource"].(map[string]any)
	if !ok {
		return map[string]*float64{}
	}
	return capacityMetrics(resource, botLoadTargetMetricKeys)
}

func capacityExecutorSamples(raw string) map[uint]botLoadCapacityExecutorSample {
	var items []map[string]any
	if json.Unmarshal([]byte(raw), &items) != nil {
		return map[uint]botLoadCapacityExecutorSample{}
	}
	out := make(map[uint]botLoadCapacityExecutorSample, len(items))
	for _, item := range items {
		nodeID := capacityNodeID(item["nodeId"])
		if nodeID == 0 {
			continue
		}
		health, _ := item["health"].(string)
		out[nodeID] = botLoadCapacityExecutorSample{health: health, metrics: capacityMetrics(item, botLoadExecutorMetricKeys)}
	}
	return out
}

func capacityMetrics(raw map[string]any, keys []string) map[string]*float64 {
	out := make(map[string]*float64, len(keys))
	for _, key := range keys {
		out[key] = capacityNumber(raw[key])
	}
	return out
}

func capacityNumber(value any) *float64 {
	switch v := value.(type) {
	case float64:
		return &v
	case float32:
		out := float64(v)
		return &out
	case int:
		out := float64(v)
		return &out
	case int64:
		out := float64(v)
		return &out
	case json.Number:
		out, err := v.Float64()
		if err == nil {
			return &out
		}
	}
	return nil
}

func capacityNodeID(value any) uint {
	number := capacityNumber(value)
	if number == nil || *number <= 0 || math.Trunc(*number) != *number {
		return 0
	}
	return uint(*number)
}

func capacityStages(samples []botLoadCapacitySample) []BotLoadCapacityStage {
	byStage := make(map[int][]botLoadCapacitySample)
	for _, sample := range samples {
		byStage[sample.stage] = append(byStage[sample.stage], sample)
	}
	indices := make([]int, 0, len(byStage))
	for index := range byStage {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	out := make([]BotLoadCapacityStage, 0, len(indices))
	for _, index := range indices {
		out = append(out, capacityStage(index, byStage[index]))
	}
	return out
}

func capacityStage(index int, samples []botLoadCapacitySample) BotLoadCapacityStage {
	target, unavailable := capacityMetricMap(samples, botLoadTargetMetricKeys, func(s botLoadCapacitySample, key string) *float64 { return s.target[key] })
	nodes := capacityStageNodeIDs(samples)
	executors := make([]BotLoadCapacityExecutorStage, 0, len(nodes))
	for _, nodeID := range nodes {
		executors = append(executors, capacityExecutorStage(nodeID, samples))
	}
	cluster, clusterUnavailable := capacityClusterMetrics(samples, nodes)
	grand, grandUnavailable := capacityGrandMetrics(samples, nodes)
	return BotLoadCapacityStage{StageIndex: index, Target: target, Executors: executors, ExecutorCluster: cluster, Grand: grand, Unavailable: append(append(unavailable, clusterUnavailable...), grandUnavailable...)}
}

func capacityMetricMap(samples []botLoadCapacitySample, keys []string, extract func(botLoadCapacitySample, string) *float64) (map[string]BotLoadCapacityMetric, []string) {
	return capacityMetricMapWithBots(samples, keys, extract, func(s botLoadCapacitySample) *float64 { return s.connected })
}

func capacityMetricMapWithBots(samples []botLoadCapacitySample, keys []string, extract func(botLoadCapacitySample, string) *float64, bots func(botLoadCapacitySample) *float64) (map[string]BotLoadCapacityMetric, []string) {
	out := make(map[string]BotLoadCapacityMetric, len(keys))
	var unavailable []string
	for _, key := range keys {
		metric, reasons := capacityMetricWithBots(samples, extract, key, bots)
		out[key] = metric
		unavailable = appendMetricUnavailable(unavailable, key, reasons)
	}
	return out, unavailable
}

func capacityExecutorStage(nodeID uint, samples []botLoadCapacitySample) BotLoadCapacityExecutorStage {
	metrics, unavailable := capacityMetricMapWithBots(samples, botLoadExecutorMetricKeys, func(s botLoadCapacitySample, key string) *float64 {
		return s.executors[nodeID].metrics[key]
	}, func(s botLoadCapacitySample) *float64 { return s.executors[nodeID].metrics["activeBots"] })
	return BotLoadCapacityExecutorStage{NodeID: nodeID, Health: capacityStageHealth(samples, nodeID), Metrics: metrics, Unavailable: unavailable}
}

func capacityClusterMetrics(samples []botLoadCapacitySample, nodes []uint) (map[string]BotLoadCapacityMetric, []string) {
	keys := []string{"activeBots", "botWorkerRssBytes", "nodeMemUsedBytes", "nodeMemTotalBytes", "workerProcessRssBytes"}
	return capacityMetricMap(samples, keys, func(s botLoadCapacitySample, key string) *float64 { return capacitySumExecutors(s, nodes, key) })
}

func capacityGrandMetrics(samples []botLoadCapacitySample, nodes []uint) (map[string]BotLoadCapacityMetric, []string) {
	keys := []string{"rssBytes"}
	return capacityMetricMap(samples, keys, func(s botLoadCapacitySample, _ string) *float64 {
		return capacitySumValues(s.target["processRssBytes"], capacitySumExecutors(s, nodes, "botWorkerRssBytes"), capacitySumExecutors(s, nodes, "workerProcessRssBytes"))
	})
}

func capacityMetric(samples []botLoadCapacitySample, extract func(botLoadCapacitySample, string) *float64, key string) (BotLoadCapacityMetric, []string) {
	return capacityMetricWithBots(samples, extract, key, func(s botLoadCapacitySample) *float64 { return s.connected })
}

func capacityMetricWithBots(samples []botLoadCapacitySample, extract func(botLoadCapacitySample, string) *float64, key string, bots func(botLoadCapacitySample) *float64) (BotLoadCapacityMetric, []string) {
	points := make([]botLoadCapacitySeriesPoint, 0, len(samples))
	for _, sample := range samples {
		points = append(points, botLoadCapacitySeriesPoint{value: extract(sample, key), bots: bots(sample)})
	}
	return capacityMetricFromPoints(points)
}

func capacityMetricFromPoints(points []botLoadCapacitySeriesPoint) (BotLoadCapacityMetric, []string) {
	if len(points) < 4 {
		return BotLoadCapacityMetric{}, []string{"INSUFFICIENT_SAMPLES"}
	}
	points = points[3:]
	if !capacityPointsAvailable(points) {
		return BotLoadCapacityMetric{}, []string{"UNAVAILABLE"}
	}
	return capacityAvailableMetric(points)
}

func capacityPointsAvailable(points []botLoadCapacitySeriesPoint) bool {
	for _, point := range points {
		if point.value == nil || point.bots == nil {
			return false
		}
	}
	return true
}

func capacityAvailableMetric(points []botLoadCapacitySeriesPoint) (BotLoadCapacityMetric, []string) {
	baseline := *points[0].value
	peak := baseline
	values := make([]float64, 0, len(points))
	maxBots := *points[0].bots
	for _, point := range points {
		values = append(values, *point.value)
		peak = max(peak, *point.value)
		maxBots = max(maxBots, *point.bots)
	}
	delta := peak - baseline
	metric := BotLoadCapacityMetric{Baseline: &baseline, Peak: &peak, P95: capacityP95(values), Delta: &delta}
	if deltaBots := maxBots - *points[0].bots; deltaBots > 0 {
		slope := delta / deltaBots
		metric.SlopePerBot = &slope
		return metric, nil
	}
	return metric, []string{"DELTA_BOTS_NOT_POSITIVE"}
}

func capacityP95(values []float64) *float64 {
	sort.Float64s(values)
	index := int(math.Ceil(float64(len(values))*0.95)) - 1
	result := values[index]
	return &result
}

func appendMetricUnavailable(out []string, key string, reasons []string) []string {
	for _, reason := range reasons {
		out = append(out, key+":"+reason)
	}
	return out
}

func capacityStageNodeIDs(samples []botLoadCapacitySample) []uint {
	set := map[uint]struct{}{}
	for _, sample := range samples {
		for nodeID := range sample.executors {
			set[nodeID] = struct{}{}
		}
	}
	out := make([]uint, 0, len(set))
	for nodeID := range set {
		out = append(out, nodeID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func capacityStageHealth(samples []botLoadCapacitySample, nodeID uint) string {
	for index := len(samples) - 1; index >= 0; index-- {
		if health := samples[index].executors[nodeID].health; health != "" {
			return health
		}
	}
	return "unavailable"
}

func capacitySumExecutors(sample botLoadCapacitySample, nodes []uint, key string) *float64 {
	values := make([]*float64, 0, len(nodes))
	for _, nodeID := range nodes {
		values = append(values, sample.executors[nodeID].metrics[key])
	}
	return capacitySumValues(values...)
}

func capacitySumValues(values ...*float64) *float64 {
	var total float64
	for _, value := range values {
		if value == nil {
			return nil
		}
		total += *value
	}
	return &total
}

func capacityPeakBots(samples []botLoadCapacitySample) *int {
	var peak *int
	for _, sample := range samples {
		if sample.connected == nil {
			continue
		}
		value := int(*sample.connected)
		if peak == nil || value > *peak {
			peak = &value
		}
	}
	return peak
}

func capacityObservedExecutorCount(samples []botLoadCapacitySample) int {
	set := map[uint]struct{}{}
	for _, sample := range samples {
		for nodeID, executor := range sample.executors {
			if active := executor.metrics["activeBots"]; active != nil && *active > 0 {
				set[nodeID] = struct{}{}
			}
		}
	}
	return len(set)
}

func capacityTargetHostMemory(samples []botLoadCapacitySample) BotLoadTargetHostMemory {
	total := capacityIntegerPeak(samples, func(s botLoadCapacitySample) *float64 { return s.target["hostMemTotalBytes"] })
	used := capacityIntegerPeak(samples, func(s botLoadCapacitySample) *float64 { return s.target["hostMemUsedBytes"] })
	memory := BotLoadTargetHostMemory{TotalBytes: total, UsedPeakBytes: used, ReserveBytes: botLoadTargetReserveBytes}
	if total == nil || *total < botLoadTargetReserveBytes || used == nil {
		return memory
	}
	budget := *total - botLoadTargetReserveBytes
	within := *used <= budget
	memory.BudgetBytes, memory.WithinReserve = &budget, &within
	return memory
}

func capacityCanClaimAs500(sess model.BotStressSession, peakBots *int, observed int, withinReserve *bool) bool {
	return peakBots != nil && *peakBots >= 500 && observed >= 10 && withinReserve != nil && *withinReserve && sess.Verdict != nil && *sess.Verdict == model.BotLoadVerdictPassed
}

func capacityMaxStableBots(sess model.BotStressSession, withinReserve *bool) *int {
	if sess.Verdict == nil || *sess.Verdict != model.BotLoadVerdictPassed || withinReserve == nil || !*withinReserve {
		return nil
	}
	return sess.MaxStableBots
}

func capacityPlannedExecutorCount(sess model.BotStressSession) int {
	plan, err := DecodeBotLoadAllocationPlan(sess.AllocationPlan)
	if err != nil {
		return 0
	}
	set := map[uint]struct{}{}
	for _, allocation := range plan.Allocations {
		set[allocation.ExecutorNodeID] = struct{}{}
	}
	return len(set)
}

func capacityMeasuredPeak(samples []botLoadCapacitySample, bots *int) BotLoadCapacityPeak {
	return BotLoadCapacityPeak{Bots: bots,
		TargetProcessRssBytes: capacityObservedPeak(samples, func(s botLoadCapacitySample) *float64 { return s.target["processRssBytes"] }),
		ExecutorBotWorkerRssBytesSum: capacityObservedPeak(samples, func(s botLoadCapacitySample) *float64 {
			return capacitySumExecutors(s, capacityAllNodeIDs(samples), "botWorkerRssBytes")
		}),
		ExecutorWorkerProcessRssBytesSum: capacityObservedPeak(samples, func(s botLoadCapacitySample) *float64 {
			return capacitySumExecutors(s, capacityAllNodeIDs(samples), "workerProcessRssBytes")
		}),
		TargetHostMemUsedBytes: capacityObservedPeak(samples, func(s botLoadCapacitySample) *float64 { return s.target["hostMemUsedBytes"] }),
	}
}

func capacityAllNodeIDs(samples []botLoadCapacitySample) []uint { return capacityStageNodeIDs(samples) }

func capacityObservedPeak(samples []botLoadCapacitySample, extract func(botLoadCapacitySample) *float64) BotLoadCapacityObservedPeak {
	var result BotLoadCapacityObservedPeak
	for _, sample := range samples {
		value := extract(sample)
		if value == nil || (result.Value != nil && *value <= float64(*result.Value)) {
			continue
		}
		integer := int64(*value)
		at, stage := sample.at, sample.stage
		result.Value, result.ObservedAt, result.StageIndex = &integer, &at, &stage
	}
	return result
}

func capacityIntegerPeak(samples []botLoadCapacitySample, extract func(botLoadCapacitySample) *float64) *int64 {
	return capacityObservedPeak(samples, extract).Value
}

func capacityRecommended(samples []botLoadCapacitySample) BotLoadCapacityRecommended {
	nodes := capacityAllNodeIDs(samples)
	return BotLoadCapacityRecommended{
		TargetProcessRssBytes:            capacityRecommendedValue(capacityObservedPeak(samples, func(s botLoadCapacitySample) *float64 { return s.target["processRssBytes"] }).Value),
		ExecutorBotWorkerRssBytesSum:     capacityRecommendedValue(capacityObservedPeak(samples, func(s botLoadCapacitySample) *float64 { return capacitySumExecutors(s, nodes, "botWorkerRssBytes") }).Value),
		ExecutorWorkerProcessRssBytesSum: capacityRecommendedValue(capacityObservedPeak(samples, func(s botLoadCapacitySample) *float64 { return capacitySumExecutors(s, nodes, "workerProcessRssBytes") }).Value),
	}
}

func capacityRecommendedValue(value *int64) *int64 {
	if value == nil {
		return nil
	}
	recommended := int64(math.Ceil(float64(*value) * (1 + botLoadCapacitySafetyMargin)))
	return &recommended
}
