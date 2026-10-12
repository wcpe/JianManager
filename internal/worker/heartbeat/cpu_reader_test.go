package heartbeat

import (
	"context"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/metrics"
)

// 主进程注入的 owner 必须贯通实际心跳负载构造，重建请求也不能推进 CPU 基线。
func TestCPUOwnerHeartbeatInjection(t *testing.T) {
	now := time.Unix(100, 0)
	current := cpu.TimesStat{Idle: 100000}
	calls := 0
	owner := metrics.NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		calls++
		return []cpu.TimesStat{current}, nil
	}, func() time.Time { return now })
	h := New("unused", "cpu-node", "unused", time.Second, nil)
	h.SetCPUReader(owner)
	owner.Sample(context.Background())
	warmup, _ := h.buildHeartbeatRequest()
	require.Zero(t, warmup.Iowait)
	require.Zero(t, warmup.CpuUsage)

	current.Iowait += 90
	current.Idle += 10
	now = now.Add(time.Second)
	owner.Sample(context.Background())
	before := owner.Snapshot()
	for range 3 {
		req, _ := h.buildHeartbeatRequest()
		require.InDelta(t, .9, req.Iowait, 1e-6)
		require.Zero(t, req.CpuUsage)
		require.Equal(t, "cpu-node", req.NodeUuid)
	}
	require.Equal(t, 2, calls, "实际心跳请求重建不得触发 CPU 读取")
	require.Equal(t, before, owner.Snapshot())
}
