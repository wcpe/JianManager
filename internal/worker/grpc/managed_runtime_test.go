package grpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProcessCPUPercent_FirstSampleAndInvalidIntervalAreUnavailable(t *testing.T) {
	srv := &Server{managedRuntimeCPU: make(map[int]managedRuntimeCPUBaseline)}
	base := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	require.Nil(t, srv.processCPUPercent(12, 3, base), "首帧不得伪造 CPU 零值")
	require.Nil(t, srv.processCPUPercent(12, 3.1, base), "无有效时间差不得计算 CPU")
	got := srv.processCPUPercent(12, 3.3, base.Add(time.Second))
	require.NotNil(t, got)
	require.InDelta(t, 20, *got, 1e-9)
}
