package register

import (
	"testing"

	"github.com/wcpe/JianManager/internal/worker/hostobs"
)

// 心跳负载是否真的带上了主机级字段。
//
// 为什么不靠真机日志断言：心跳负载是内存结构，日志里看不到。
// 真机验证只能看「落库结果」，而落库为空时无法区分「没人采到」与「采到了没写」。
// 这里直接构造负载，把「采集→负载」这一环单独钉死。

// fakeHostObs 构造固定快照的 reader。
type fakeHostObs struct {
	snap hostobs.Snapshot
}

func (f fakeHostObs) Snapshot() hostobs.Snapshot { return f.snap }

// TestCollectHeartbeatCarriesHostObservations 采集器的数据必须进入心跳负载。
func TestCollectHeartbeatCarriesHostObservations(t *testing.T) {
	obs := fakeHostObs{snap: hostobs.Snapshot{
		Valid: true,
		TopProcesses: []hostobs.ProcessSample{
			{PID: 42, Comm: "java", CPUPct: 88.5, RSSBytes: 1 << 30},
			{PID: 7, Comm: "nginx", CPUPct: 3.2, RSSBytes: 1 << 20},
		},
		Mounts: []hostobs.MountUsage{
			{Mountpoint: "/", TotalBytes: 100 << 30, UsedBytes: 60 << 30, UsedPct: 60},
			{Mountpoint: "/var", TotalBytes: 200 << 30, UsedBytes: 190 << 30, UsedPct: 95},
		},
	}}

	req := CollectHeartbeatDataWithHost("node-uuid", obs)

	if len(req.HostTopProcesses) != 2 {
		t.Fatalf("应带 2 个进程样本，实际 %d", len(req.HostTopProcesses))
	}
	if req.HostTopProcesses[0].Comm != "java" || req.HostTopProcesses[0].CpuPct != 88.5 {
		t.Fatalf("进程样本字段不符: %+v", req.HostTopProcesses[0])
	}
	if len(req.HostMounts) != 2 {
		t.Fatalf("应带 2 个挂载点，实际 %d", len(req.HostMounts))
	}
	if req.HostMounts[1].Mountpoint != "/var" || req.HostMounts[1].UsedPct != 95 {
		t.Fatalf("挂载点字段不符: %+v", req.HostMounts[1])
	}
}

// TestCollectHeartbeatWithoutHostObserver 未注入采集器时不带主机级字段，其余字段照常。
//
// 老装配/测试构造不该因为多了这个特性而坏掉。
func TestCollectHeartbeatWithoutHostObserver(t *testing.T) {
	req := CollectHeartbeatData("node-uuid")
	if len(req.HostTopProcesses) != 0 || len(req.HostMounts) != 0 {
		t.Fatal("未注入采集器时不应带主机级数据")
	}
	if req.NodeUuid != "node-uuid" {
		t.Fatal("基础字段仍应正常填充")
	}
}

// TestCollectHeartbeatSkipsInvalidHostSnapshot 采集器快照无效时不上报。
//
// 上报无效快照会被 CP 当成「全是 0」，比不上报更容易误导。
func TestCollectHeartbeatSkipsInvalidHostSnapshot(t *testing.T) {
	obs := fakeHostObs{snap: hostobs.Snapshot{
		Valid:        false,
		TopProcesses: []hostobs.ProcessSample{{PID: 1, Comm: "stale"}},
	}}
	req := CollectHeartbeatDataWithHost("node-uuid", obs)
	if len(req.HostTopProcesses) != 0 {
		t.Fatal("无效快照不应上报（含残留数据也不行）")
	}
}

// TestCollectHeartbeatCarriesProcCounts 进程计数进入负载。
//
// hostProcCounts 在 Linux 上一定能取到（/proc/loadavg 恒定存在）；
// 其他平台取不到则跳过，因为 CP 侧对缺测已有处理（null）。
func TestCollectHeartbeatCarriesProcCounts(t *testing.T) {
	req := CollectHeartbeatData("node-uuid")
	if req.ProcsTotal == 0 {
		t.Skip("本平台不提供进程计数（CP 按缺测处理）")
	}
	if req.ProcsRunning < 0 || req.ProcsTotal < 0 {
		t.Fatalf("进程计数不应为负: %d/%d", req.ProcsRunning, req.ProcsTotal)
	}
	if req.ProcsRunning > req.ProcsTotal {
		t.Fatalf("运行中(%d) 不应大于总数(%d)", req.ProcsRunning, req.ProcsTotal)
	}
}
