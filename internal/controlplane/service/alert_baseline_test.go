package service

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// makePoints 生成等间隔（30s）时序点，valueFn 据索引给出数值。
func makePoints(n int, valueFn func(i int) float64) []BaselinePoint {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pts := make([]BaselinePoint, n)
	for i := 0; i < n; i++ {
		pts[i] = BaselinePoint{TS: base.Add(time.Duration(i) * 30 * time.Second), Value: valueFn(i)}
	}
	return pts
}

func TestEvaluateBaseline_EWMA_FlatNoiseDoesNotFire(t *testing.T) {
	// 在均值附近小幅波动（< k·σ）不应触发。
	points := makePoints(120, func(i int) float64 {
		if i%2 == 0 {
			return 100
		}
		return 100.02
	})
	got := EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodEWMA, Sensitivity: 3})
	assert.False(t, got.Breach, "小幅波动不应触发基线告警")
}

func TestEvaluateBaseline_EWMA_SlowDegradationFires(t *testing.T) {
	// 缓慢劣化：内存类指标每窗口匀速 +N，无人工阈值也应越界（方向 up）。
	points := makePoints(120, func(i int) float64 { return 1000 + float64(i)*5 })
	got := EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodEWMA, Sensitivity: 3, Direction: model.BaselineDirectionBoth})
	require.True(t, got.Breach, "缓慢劣化应触发基线越界")
	assert.Equal(t, model.BaselineDirectionUp, got.Direction)
	assert.Positive(t, got.Deviation)
}

func TestEvaluateBaseline_EWMA_DirectionFilter(t *testing.T) {
	// 缓慢下降但只允许 up → 不触发。
	points := makePoints(120, func(i int) float64 { return 1000 - float64(i)*5 })
	got := EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodEWMA, Sensitivity: 3, Direction: model.BaselineDirectionUp})
	assert.False(t, got.Breach)
	// 允许 down → 触发。
	got = EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodEWMA, Sensitivity: 3, Direction: model.BaselineDirectionDown})
	require.True(t, got.Breach)
	assert.Equal(t, model.BaselineDirectionDown, got.Direction)
}

func TestEvaluateBaseline_EWMA_SustainRequiresConsecutive(t *testing.T) {
	// 仅在最后一个点注入孤立尖峰：要求连续 5 点越界时不应触发。
	points := makePoints(120, func(i int) float64 {
		if i == 119 {
			return 5000
		}
		return 1000
	})
	got := EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodEWMA, Sensitivity: 3, Sustain: 5})
	assert.False(t, got.Breach, "孤立尖峰不应满足连续越界要求")
	// 允许单点（Sustain=1）时触发。
	got = EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodEWMA, Sensitivity: 3, Sustain: 1})
	assert.True(t, got.Breach)
}

func TestEvaluateBaseline_ColdStartSkips(t *testing.T) {
	points := makePoints(3, func(i int) float64 { return float64(i) * 100 })
	got := EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodEWMA, Sensitivity: 3})
	assert.False(t, got.Breach, "样本不足一个窗口时应跳过（冷启动）")
}

func TestEvaluateBaseline_ROC_StepDown(t *testing.T) {
	// TPS 台阶下坠：前 60 点 500，后 60 点 200。
	points := makePoints(120, func(i int) float64 {
		if i < 60 {
			return 500
		}
		return 200
	})
	got := EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodROC, Sensitivity: 3, Direction: model.BaselineDirectionBoth})
	require.True(t, got.Breach, "台阶下坠应被突降检测捕获")
	assert.Equal(t, model.BaselineDirectionDown, got.Direction)
	assert.Positive(t, math.Abs(got.Deviation))
}

func TestEvaluateBaseline_ROC_DirectionFilter(t *testing.T) {
	points := makePoints(120, func(i int) float64 {
		if i < 60 {
			return 500
		}
		return 200
	})
	got := EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodROC, Sensitivity: 3, Direction: model.BaselineDirectionUp})
	assert.False(t, got.Breach, "只允许 up 时台阶下坠不应触发")
}

func TestEvaluateBaseline_ROC_SteadyRampNotFlagged(t *testing.T) {
	// 等幅渐变：差分恒定，偏离中位为 0，不应被突降突升误报。
	points := makePoints(120, func(i int) float64 { return 1000 + float64(i)*3 })
	got := EvaluateBaseline(points, BaselineConfig{Method: model.BaselineMethodROC, Sensitivity: 3, Direction: model.BaselineDirectionBoth})
	assert.False(t, got.Breach)
}

func TestSaturationPercent(t *testing.T) {
	used := 92.0
	limit := 100.0
	pct, ok := saturationPercent(model.MetricNodeDiskUsed, &used, &limit)
	require.True(t, ok)
	assert.InDelta(t, 92, pct, 0.001)

	// heap：used/max。
	heapUsed, heapMax := 900.0, 1000.0
	pct, ok = saturationPercent(model.MetricInstHeapUsed, &heapUsed, &heapMax)
	require.True(t, ok)
	assert.InDelta(t, 90, pct, 0.001)

	// 缺上限 → 不可评估。
	_, ok = saturationPercent(model.MetricNodeDiskUsed, &used, nil)
	assert.False(t, ok)

	// cpu 百分比型指标自身即饱和度。
	cpu := 97.0
	pct, ok = saturationPercent(model.MetricNodeCPUPct, &cpu, nil)
	require.True(t, ok)
	assert.InDelta(t, 97, pct, 0.001)
}

func TestResolveMetricKey(t *testing.T) {
	key, ok := resolveMetricKey("node", "cpu")
	require.True(t, ok)
	assert.Equal(t, model.MetricNodeCPUPct, key)

	key, ok = resolveMetricKey("node", "memory")
	require.True(t, ok)
	assert.Equal(t, model.MetricNodeMemUsed, key)

	key, ok = resolveMetricKey("instance", "tps")
	require.True(t, ok)
	assert.Equal(t, model.MetricInstTPS, key)

	key, ok = resolveMetricKey("instance", model.MetricInstPlayersOnline)
	require.True(t, ok)
	assert.Equal(t, model.MetricInstPlayersOnline, key)

	_, ok = resolveMetricKey("node", "not_a_metric")
	assert.False(t, ok)
}

func TestSaturationLimitKey(t *testing.T) {
	assert.Equal(t, model.MetricNodeDiskTotal, saturationLimitKey(model.MetricNodeDiskUsed))
	assert.Equal(t, model.MetricInstHeapMax, saturationLimitKey(model.MetricInstHeapUsed))
	assert.Equal(t, "", saturationLimitKey(model.MetricNodeCPUPct))
}
