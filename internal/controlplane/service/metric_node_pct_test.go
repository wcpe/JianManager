package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// findSample 在样本里按 key 找一条。
func findSample(samples []Sample, key string) (Sample, bool) {
	for _, s := range samples {
		if s.MetricKey == key {
			return s, true
		}
	}
	return Sample{}, false
}

// TestHeartbeatEmitsMemDiskPct 验证内存/磁盘百分比指标被落库。
//
// 真实缺口就在这里：心跳早就有 memory_usage / disk_usage（0..1），
// Worker 侧也一直在填，但 CP 从未把它们落成指标——于是「内存/磁盘高水位告警」
// 根本写不出来（只能写绝对字节阈值，而节点磁盘差异很大）。
//
// 变异：把两行 nodeSample(MetricNodeMemPct/DiskPct) 删掉，本用例转红。
func TestHeartbeatEmitsMemDiskPct(t *testing.T) {
	req := &workerpb.HeartbeatRequest{
		NodeUuid:    "node-1",
		MemoryUsage: 0.75, // 75%
		DiskUsage:   0.42, // 42%
	}
	now := time.Now().UTC()
	samples := buildNodeSamplesAt(req, now)

	mem, ok := findSample(samples, model.MetricNodeMemPct)
	require.True(t, ok, "必须落 memory 百分比指标")
	require.NotNil(t, mem.Value)
	require.InDelta(t, 75.0, *mem.Value, 0.01, "memory_usage=0.75 应当转成 75%%")
	require.Equal(t, "pct", mem.Unit, "量纲必须与 cpu_pct 一致")

	disk, ok := findSample(samples, model.MetricNodeDiskPct)
	require.True(t, ok, "必须落 disk 百分比指标")
	require.NotNil(t, disk.Value)
	require.InDelta(t, 42.0, *disk.Value, 0.01)
}

// TestHeartbeatSkipsPctWhenZero 值为 0 时**不落点**（而不是落 0）。
//
// 为什么这个区分重要：0% 在图表上是一条平坦的健康线。
// 老 Worker 不上报时 proto 零值也是 0，若落点会让运维误以为「内存一点没用」。
// 真·0% 占用物理上不可能（内核自己就占内存）。
func TestHeartbeatSkipsPctWhenZero(t *testing.T) {
	req := &workerpb.HeartbeatRequest{NodeUuid: "node-1"} // 不填 usage
	samples := buildNodeSamplesAt(req, time.Now().UTC())

	_, ok := findSample(samples, model.MetricNodeMemPct)
	require.False(t, ok, "值为 0 时不应当落点（否则图表显示成健康的 0%%）")
	_, ok = findSample(samples, model.MetricNodeDiskPct)
	require.False(t, ok)
}

// TestHeartbeatPctClamped 采集口径不一致导致 >1 时，钳位到 100。
func TestHeartbeatPctClamped(t *testing.T) {
	req := &workerpb.HeartbeatRequest{NodeUuid: "node-1", MemoryUsage: 1.05}
	samples := buildNodeSamplesAt(req, time.Now().UTC())
	mem, ok := findSample(samples, model.MetricNodeMemPct)
	require.True(t, ok)
	require.NotNil(t, mem.Value)
	require.LessOrEqual(t, *mem.Value, 100.0, "百分比不得超过 100")
}

// TestHeartbeatIOWaitEmitted iowait 也要落点（与本轮同时补的另一个指标）。
func TestHeartbeatIOWaitEmitted(t *testing.T) {
	req := &workerpb.HeartbeatRequest{NodeUuid: "node-1", Iowait: 0.35}
	samples := buildNodeSamplesAt(req, time.Now().UTC())
	io, ok := findSample(samples, model.MetricNodeIOWait)
	require.True(t, ok, "iowait 必须落点（IO 瓶颈可见性）")
	require.NotNil(t, io.Value)
	require.InDelta(t, 35.0, *io.Value, 0.01)
}
