package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNearestRankPercentile_FixedVectors(t *testing.T) {
	// n=0 → 无值
	_, ok := NearestRankPercentile(nil, 0.5)
	require.False(t, ok)

	// n=1
	v, ok := NearestRankPercentile([]float64{42}, 0.5)
	require.True(t, ok)
	require.Equal(t, 42.0, v)

	// n=10: rank(p50)=ceil(5)=5 → 第 5 项（1-based）= 排序后 index 4
	samples := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	v50, ok := NearestRankPercentile(samples, 0.50)
	require.True(t, ok)
	require.Equal(t, 50.0, v50)

	// p95: ceil(0.95*10)=10 → 100
	v95, ok := NearestRankPercentile(samples, 0.95)
	require.True(t, ok)
	require.Equal(t, 100.0, v95)

	// p99: ceil(0.99*10)=10 → 100
	v99, ok := NearestRankPercentile(samples, 0.99)
	require.True(t, ok)
	require.Equal(t, 100.0, v99)

	// 不修改原切片顺序
	require.Equal(t, []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}, samples)
}

func TestNearestRankPercentile_UnsortedInput(t *testing.T) {
	samples := []float64{100, 10, 50}
	// sorted 10,50,100; p50 ceil(1.5)=2 → 50
	v, ok := NearestRankPercentile(samples, 0.5)
	require.True(t, ok)
	require.Equal(t, 50.0, v)
}

func TestComputeLatencyPercentiles_EmptyIsNil(t *testing.T) {
	p := ComputeLatencyPercentiles(nil)
	require.Nil(t, p.P50)
	require.Nil(t, p.P95)
	require.Nil(t, p.P99)
}

func TestClampNonNegativeLatency(t *testing.T) {
	require.Equal(t, int64(0), ClampNonNegativeLatency(-5))
	require.Equal(t, int64(10), ClampNonNegativeLatency(10))
}
