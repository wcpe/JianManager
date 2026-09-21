package service

import (
	"math"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 动态基线检测（FR-462）。
//
// 全部为无状态纯函数：输入一段时序窗口（已按时间升序、去掉缺测点）与规则配置，
// 输出是否越界及偏离量。评估器每周期从时序库现算，不保留跨进程状态，重启无副作用。

// BaselinePoint 基线窗口内一个非空样本点。
type BaselinePoint struct {
	TS    time.Time
	Value float64
}

// BaselineConfig 基线检测配置（来自 AlertRule，缺省值由评估器补全）。
type BaselineConfig struct {
	// Method ewma（默认）| roc | mom | yoy。
	Method string
	// Sensitivity ewma/roc 为 k 倍尺度；mom/yoy 为偏离比阈值。默认 3。
	Sensitivity float64
	// MinDelta 最小绝对增量（滤小噪声）。
	MinDelta float64
	// Direction up | down | both（默认 both）。
	Direction string
	// DurationSec 需要连续越界的时长（秒），换算为窗口内连续点数。0 表示单点即触发。
	DurationSec int
	// Sustain 覆盖式连续点数（>0 时优先于 DurationSec，便于单测）。
	Sustain int
}

// BaselineResult 基线检测结果。
type BaselineResult struct {
	Breach    bool
	Value     float64 // 触发点当前值
	Baseline  float64 // 基线估计
	Deviation float64 // 偏离量（带符号）
	Direction string  // up | down
}

const (
	// baselineMinSamples 冷启动阈值：样本不足一个合理窗口时不评估（跳过，避免开服误报）。
	baselineMinSamples = 5
	baselineEpsilon    = 1e-9
	// madToSigma MAD 换算正态标准差的常数（1/0.6745）。
	madToSigma = 1.4826
	// baselineEWMAMemory EWMA 有效记忆拍数上限（缩短预热，兼顾突变与缓变）。
	baselineEWMAMemory = 10
)

// EvaluateBaseline 按配置方法求值窗口，返回是否越界。
func EvaluateBaseline(points []BaselinePoint, cfg BaselineConfig) BaselineResult {
	sensitivity := cfg.Sensitivity
	if sensitivity <= 0 {
		sensitivity = 3
	}
	direction := cfg.Direction
	if direction == "" {
		direction = model.BaselineDirectionBoth
	}
	sustain := cfg.Sustain
	if sustain <= 0 {
		sustain = desiredSustain(points, cfg.DurationSec)
	}
	switch cfg.Method {
	case model.BaselineMethodROC:
		return evaluateRateOfChange(points, sensitivity, cfg.MinDelta, direction, sustain)
	case model.BaselineMethodMoM:
		return evaluatePeriod(points, sensitivity, cfg.MinDelta, direction, 0)
	case model.BaselineMethodYoY:
		return evaluatePeriod(points, sensitivity, cfg.MinDelta, direction, 1)
	default: // ewma
		return evaluateEWMA(points, sensitivity, cfg.MinDelta, direction, sustain)
	}
}

// evaluateEWMA 对最近 sustain 个点逐一比对「其前缀 EWMA 基线」，全部同向越界才算触发。
// 缓慢劣化（如内存每小时 +3%）会使最新值显著偏离 EWMA 基线而残差 σ 很小，故可无死阈值触发。
func evaluateEWMA(points []BaselinePoint, k, minDelta float64, direction string, sustain int) BaselineResult {
	n := len(points)
	if n < baselineMinSamples+sustain {
		return BaselineResult{}
	}
	var last BaselineResult
	var dir string
	for e := n - sustain; e < n; e++ {
		prefix := points[:e]
		if len(prefix) < baselineMinSamples {
			return BaselineResult{}
		}
		values := pointValues(prefix)
		// 固定记忆的 EWMA（记忆上限 baselineEWMAMemory 拍，缩短预热）：基线与残差同口径。
		// 缓慢劣化时残差收敛为恒定的滞后量、σ 很小，故最新值虽只偏离少许也能越界。
		mem := len(values)
		if mem > baselineEWMAMemory {
			mem = baselineEWMAMemory
		}
		alpha := 2.0 / (float64(mem) + 1)
		ewmas := make([]float64, len(values))
		residuals := make([]float64, len(values))
		ewmas[0] = values[0]
		for i := 1; i < len(values); i++ {
			ewmas[i] = alpha*values[i] + (1-alpha)*ewmas[i-1]
			residuals[i] = values[i] - ewmas[i]
		}
		baseline := ewmas[len(values)-1]
		scale := math.Max(stdDev(residuals), math.Max(mad(residuals)*madToSigma, baselineEpsilon))
		deviation := points[e].Value - baseline
		d, ok := matchDirection(deviation, k*scale, minDelta, direction)
		if !ok {
			return BaselineResult{}
		}
		if dir == "" {
			dir = d
		} else if dir != d {
			return BaselineResult{}
		}
		last = BaselineResult{Breach: true, Value: points[e].Value, Baseline: baseline, Deviation: deviation, Direction: d}
	}
	return last
}

// evaluateRateOfChange 检测突升突降：窗口内相邻差中最新的越界台阶即触发。
// 尺度以差分的 max(σ, MAD) 估计，偏离取「差分 − 差分中位」（故等幅渐变恒为 0，不会误报）；
// 台阶式突降/突升会显著偏离 0 而被捕获。方向由 Direction 过滤。
func evaluateRateOfChange(points []BaselinePoint, k, minDelta float64, direction string, _ int) BaselineResult {
	n := len(points)
	if n < baselineMinSamples {
		return BaselineResult{}
	}
	diffs := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		diffs = append(diffs, points[i].Value-points[i-1].Value)
	}
	if len(diffs) < baselineMinSamples {
		return BaselineResult{}
	}
	med := median(diffs)
	scale := math.Max(stdDev(diffs), math.Max(mad(diffs)*madToSigma, baselineEpsilon))
	for idx := len(diffs) - 1; idx >= 0; idx-- {
		deviation := diffs[idx] - med
		if d, ok := matchDirection(deviation, k*scale, minDelta, direction); ok {
			return BaselineResult{Breach: true, Value: points[idx+1].Value, Baseline: points[idx].Value, Deviation: deviation, Direction: d}
		}
	}
	return BaselineResult{}
}

// evaluatePeriod 环比/同比：把窗口按段比对（gap=0 相邻半段=环比；gap=1 隔一段=同比）。
// 偏离比 = |recent-ref| / max(|ref|, ε)，超过 Sensitivity 视为越界。
func evaluatePeriod(points []BaselinePoint, sensitivity, minDelta float64, direction string, gap int) BaselineResult {
	n := len(points)
	seg := n / (2 + gap)
	if seg < baselineMinSamples {
		return BaselineResult{} // 段过短或历史不足（yoy 需更长历史），冷启动跳过
	}
	recentStart := n - seg
	refEnd := recentStart - gap*seg
	if refEnd < seg {
		return BaselineResult{}
	}
	ref := mean(pointValues(points[refEnd-seg : refEnd]))
	recent := mean(pointValues(points[recentStart:]))
	deviation := recent - ref
	threshold := sensitivity * math.Max(math.Abs(ref), baselineEpsilon)
	if _, ok := matchDirection(deviation, threshold, minDelta, direction); !ok {
		return BaselineResult{}
	}
	d := directionOf(deviation)
	return BaselineResult{Breach: true, Value: recent, Baseline: ref, Deviation: deviation, Direction: d}
}

// matchDirection 判定偏离是否越界且方向被允许，返回方向。
func matchDirection(deviation, threshold, minDelta float64, direction string) (string, bool) {
	if math.Abs(deviation) <= threshold || math.Abs(deviation) <= minDelta {
		return "", false
	}
	d := directionOf(deviation)
	if direction != model.BaselineDirectionBoth && direction != d {
		return "", false
	}
	return d, true
}

func directionOf(deviation float64) string {
	if deviation < 0 {
		return model.BaselineDirectionDown
	}
	return model.BaselineDirectionUp
}

// desiredSustain 由连续越界时长换算窗口内连续点数（至少 1）。
func desiredSustain(points []BaselinePoint, durationSec int) int {
	if durationSec <= 0 || len(points) < 2 {
		return 1
	}
	span := points[len(points)-1].TS.Sub(points[0].TS).Seconds()
	if span <= 0 {
		return 1
	}
	interval := span / float64(len(points)-1)
	if interval <= 0 {
		return 1
	}
	count := int(math.Round(float64(durationSec) / interval))
	if count < 1 {
		return 1
	}
	return count
}

// ── 统计工具 ──

func pointValues(points []BaselinePoint) []float64 {
	out := make([]float64, len(points))
	for i, p := range points {
		out[i] = p.Value
	}
	return out
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := mean(values)
	var sum float64
	for _, v := range values {
		d := v - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(values)))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// mad 中位绝对偏差（median absolute deviation）。
func mad(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	med := median(values)
	devs := make([]float64, len(values))
	for i, v := range values {
		devs[i] = math.Abs(v - med)
	}
	return median(devs)
}

// ── 指标键解析与饱和度 ──

// nodeMetricAliases 把 node 目标的指标别名解析为标准 metric_key。
var nodeMetricAliases = map[string]string{
	"cpu": model.MetricNodeCPUPct, "cpu_usage": model.MetricNodeCPUPct, "cpu_pct": model.MetricNodeCPUPct,
	model.MetricNodeCPUPct: model.MetricNodeCPUPct,
	"memory":               model.MetricNodeMemUsed, "memory_usage": model.MetricNodeMemUsed, "mem": model.MetricNodeMemUsed,
	model.MetricNodeMemUsed: model.MetricNodeMemUsed,
	"disk":                  model.MetricNodeDiskUsed, "disk_usage": model.MetricNodeDiskUsed,
	model.MetricNodeDiskUsed: model.MetricNodeDiskUsed,
	"load":                   model.MetricNodeLoad,
	model.MetricNodeLoad:     model.MetricNodeLoad,
}

// instanceMetricAliases 把 instance 目标的指标别名解析为标准 metric_key。
var instanceMetricAliases = map[string]string{
	"tps": model.MetricInstTPS, model.MetricInstTPS: model.MetricInstTPS,
	"mspt": model.MetricInstMSPT, model.MetricInstMSPT: model.MetricInstMSPT,
	"players": model.MetricInstPlayersOnline, "players_online": model.MetricInstPlayersOnline,
	model.MetricInstPlayersOnline: model.MetricInstPlayersOnline,
	"heap":                        model.MetricInstHeapUsed, "heap_used": model.MetricInstHeapUsed,
	model.MetricInstHeapUsed: model.MetricInstHeapUsed,
	"threads":                model.MetricInstThreads, model.MetricInstThreads: model.MetricInstThreads,
	"cpu": model.MetricInstCPUPct, "cpu_pct": model.MetricInstCPUPct, model.MetricInstCPUPct: model.MetricInstCPUPct,
	"uptime": model.MetricInstUptime, model.MetricInstUptime: model.MetricInstUptime,
}

// resolveMetricKey 把规则里的指标名解析为 (scope, metric_key)。scope 用于选择别名表。
func resolveMetricKey(scope, metric string) (string, bool) {
	table := nodeMetricAliases
	if scope == "instance" {
		table = instanceMetricAliases
	} else if _, ok := nodeMetricAliases[metric]; !ok {
		// node 目标未命中别名时，回退尝试实例别名（容错跨 scope 书写）。
		if key, ok := instanceMetricAliases[metric]; ok {
			return key, true
		}
	}
	key, ok := table[metric]
	return key, ok
}

// saturationMaxKey 由「已用」指标键推导上限指标键（饱和度检测用）。
var saturationMaxKey = map[string]string{
	model.MetricNodeDiskUsed: model.MetricNodeDiskTotal,
	model.MetricNodeMemUsed:  model.MetricNodeMemTotal,
	model.MetricInstHeapUsed: model.MetricInstHeapMax,
}

// saturationPercent 计算饱和度百分比。有配对上限指标时取 used/max*100；
// 无配对上限但自身即百分比指标（cpu）时直接取值。返回 (pct, ok)。
func saturationPercent(usedKey string, used, limit *float64) (float64, bool) {
	if used == nil {
		return 0, false
	}
	if _, has := saturationMaxKey[usedKey]; has {
		if limit == nil || *limit <= 0 {
			return 0, false
		}
		return *used / *limit * 100, true
	}
	// 百分比型指标（cpu）自身即饱和度。
	switch usedKey {
	case model.MetricNodeCPUPct, model.MetricInstCPUPct:
		return *used, true
	}
	return 0, false
}

// saturationLimitKey 返回某「已用」指标对应的上限指标键（无则空）。
func saturationLimitKey(usedKey string) string {
	return saturationMaxKey[usedKey]
}
