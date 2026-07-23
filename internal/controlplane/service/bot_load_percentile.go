package service

import (
	"math"
	"sort"
)

// NearestRankPercentile 按共享 API 冻结的 nearest-rank 计算百分位。
// 排序后 rank=ceil(p*n)，取 1-based 第 rank 项；n=0 返回 ok=false（对应 JSON null）。
// samples 会被拷贝排序，不修改调用方切片。
func NearestRankPercentile(samples []float64, p float64) (value float64, ok bool) {
	n := len(samples)
	if n == 0 || p <= 0 || p > 1 || math.IsNaN(p) || math.IsInf(p, 0) {
		return 0, false
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1], true
}

// LatencyPercentiles 计算 p50/p95/p99；零样本对应字段保持 nil。
type LatencyPercentiles struct {
	P50 *float64 `json:"p50"`
	P95 *float64 `json:"p95"`
	P99 *float64 `json:"p99"`
}

// ComputeLatencyPercentiles 对有效样本计算三类百分位。
func ComputeLatencyPercentiles(samples []float64) LatencyPercentiles {
	var out LatencyPercentiles
	if v, ok := NearestRankPercentile(samples, 0.50); ok {
		out.P50 = &v
	}
	if v, ok := NearestRankPercentile(samples, 0.95); ok {
		out.P95 = &v
	}
	if v, ok := NearestRankPercentile(samples, 0.99); ok {
		out.P99 = &v
	}
	return out
}

// ClampNonNegativeLatency 将终点早于起点的样本钳制为 0（CLOCK_SKEW）。
func ClampNonNegativeLatency(deltaMS int64) int64 {
	if deltaMS < 0 {
		return 0
	}
	return deltaMS
}
