package metrics_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/stretchr/testify/require"
	workergrpc "github.com/wcpe/JianManager/internal/worker/grpc"
	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/internal/worker/register"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 用真实 collector、注册心跳构造和节点 RPC 三条消费路径验证同一 CPU owner。
func TestCPUOwnerThreeProductionConsumers(t *testing.T) {
	var calls atomic.Int64
	now := time.Unix(100, 0)
	current := cpu.TimesStat{Idle: 100000}
	owner := metrics.NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		calls.Add(1)
		return []cpu.TimesStat{current}, nil
	}, func() time.Time { return now })
	collector := metrics.NewCollector(time.Second, owner)
	server := workergrpc.NewServer(&process.Manager{}, "cpu-node", collector, nil, nil)
	owner.Sample(context.Background())

	for _, waiting := range []float64{90, 0, 20} {
		now = now.Add(time.Second)
		current.Iowait += waiting
		current.Idle += 100 - waiting
		owner.Sample(context.Background())
		before := owner.Snapshot()
		count := calls.Load()
		var periodic metrics.NodeMetrics
		var heartbeat *workerpb.HeartbeatRequest
		var rpc *workerpb.GetNodeMetricsResponse
		var rpcErr error
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); periodic = collector.Collect() }()
		go func() { defer wg.Done(); heartbeat = register.CollectHeartbeatData("cpu-node", owner) }()
		go func() {
			defer wg.Done()
			rpc, rpcErr = server.GetNodeMetrics(context.Background(), &workerpb.GetNodeMetricsRequest{})
		}()
		wg.Wait()
		require.NoError(t, rpcErr)
		require.InDelta(t, waiting/100, periodic.IOWait, 1e-6)
		require.Equal(t, periodic.IOWait, heartbeat.Iowait)
		require.Equal(t, periodic.IOWait, rpc.Iowait)
		require.Equal(t, periodic.CPUUsage, heartbeat.CpuUsage)
		require.Equal(t, periodic.CPUUsage, rpc.CpuUsage)
		require.Zero(t, periodic.CPUUsage)
		require.Equal(t, count, calls.Load(), "三个生产消费者不得增加读取次数")
		require.Equal(t, before, owner.Snapshot(), "交错消费不得推进或破坏 baseline")
		owner.Sample(context.Background())
		require.Equal(t, count, calls.Load(), "同窗口 owner 也只能采一次")
	}
	require.EqualValues(t, 4, calls.Load(), "预热加三个窗口，共读取四次")
}

// 无 owner 的旧调用保留签名，但不得私建第二个采样者；现有标量降级为零。
func TestCPUOwnerMissingReaderCompatibility(t *testing.T) {
	collector := metrics.NewCollector(time.Second)
	m := collector.Collect()
	req := register.CollectHeartbeatData("cpu-node")
	require.Zero(t, m.CPUUsage)
	require.Zero(t, m.IOWait)
	require.Zero(t, req.CpuUsage)
	require.Zero(t, req.Iowait)
}
