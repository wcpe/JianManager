package metrics

import (
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/stretchr/testify/require"
)

// iowait 归一（FR-485）：用构造输入精确验证——改坏归一逻辑或除零保护即转红。
//
// 为什么不对 Collect 直接断言「IOWait > 0」：真实机器 iowait 常在 0 附近
// （尤其是低负载的开发机），那种断言会变成随机假红。归一逻辑抽成纯函数后，
// 这里既能精确验证，又在末尾保留一条「接线未断」的真实采集冒烟。
func TestIowaitRatio(t *testing.T) {
	// 各态合计 200，其中 Iowait=50 → 0.25
	got := IOWaitRatio(cpu.TimesStat{
		User: 100, System: 20, Idle: 20, Iowait: 50, Irq: 5, Softirq: 5,
	})
	require.InDelta(t, 0.25, float64(got), 1e-6)

	// 全空闲 → 占比 0
	require.Equal(t, float32(0), IOWaitRatio(cpu.TimesStat{Idle: 100}))

	// 全为 IO 等待 → 占比 1
	require.Equal(t, float32(1), IOWaitRatio(cpu.TimesStat{Iowait: 42}))

	// 除零保护：首次采样各态可能全为 0，不得产生 NaN
	require.Equal(t, float32(0), IOWaitRatio(cpu.TimesStat{}))

	// 接线冒烟：Collect 仍能跑通且取值域为 0..1
	m := NewCollector(0).Collect()
	require.GreaterOrEqual(t, m.IOWait, float32(0))
	require.LessOrEqual(t, m.IOWait, float32(1))
}
