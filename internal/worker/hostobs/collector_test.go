package hostobs

import (
	"errors"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// FR-513：主机级观测采集。
//
// 这些用例只测「不依赖真实主机」的部分：节流归一化、伪文件系统过滤、
// 失败降级、TopN 选取。真实采集由真机验收覆盖（CI 上 /var 未必独立分区，
// 拿真实主机做断言会得到与环境耦合的脆弱用例）。

// TestNew_NormalizesInterval 采集间隔有 10s 硬下限。
//
// 这条守的是「采集开销受控」这条验收线：整机进程扫描是 O(全部进程)，
// 间隔配得再小只是白烧 CPU——心跳 30s 才消费一次，更密毫无收益。
func TestNew_NormalizesInterval(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"零值用默认", 0, defaultInterval},
		{"负数用默认", -time.Second, defaultInterval},
		{"低于下限被抬高", time.Second, minInterval},
		{"恰好下限", minInterval, minInterval},
		{"高于下限保留", 30 * time.Second, 30 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := New(Options{Interval: c.in}).interval; got != c.want {
				t.Fatalf("interval(%v) = %v，期望 %v", c.in, got, c.want)
			}
		})
	}
}

// TestNew_NormalizesTopN TopN 缺省 5、上限 10。
func TestNew_NormalizesTopN(t *testing.T) {
	if got := New(Options{}).topN; got != defaultTopN {
		t.Fatalf("默认 topN = %d，期望 %d", got, defaultTopN)
	}
	if got := New(Options{TopN: -3}).topN; got != defaultTopN {
		t.Fatalf("负值应回退默认，实际 %d", got)
	}
	if got := New(Options{TopN: 999}).topN; got != maxTopN {
		t.Fatalf("超上限应截断到 %d，实际 %d", maxTopN, got)
	}
}

// TestCollectMounts_SkipsPseudoFilesystems 伪文件系统不进结果。
//
// 为什么重要：tmpfs 用量随内存波动、overlay 在容器里指向镜像层、proc/sys 恒为 0。
// 把它们当磁盘展示，会让「哪个盘快满了」这个判断被噪声淹没。
func TestCollectMounts_SkipsPseudoFilesystems(t *testing.T) {
	c := New(Options{})
	c.partitions = func(bool) ([]disk.PartitionStat, error) {
		return []disk.PartitionStat{
			{Mountpoint: "/", Fstype: "ext4"},
			{Mountpoint: "/var", Fstype: "xfs"},
			{Mountpoint: "/proc", Fstype: "proc"},
			{Mountpoint: "/sys", Fstype: "sysfs"},
			{Mountpoint: "/dev/shm", Fstype: "tmpfs"},
			{Mountpoint: "/run/containers", Fstype: "overlay"},
		}, nil
	}
	c.usage = func(path string) (*disk.UsageStat, error) {
		return &disk.UsageStat{Total: 1000, Used: 500, UsedPercent: 50}, nil
	}

	got, err := c.collectMounts()
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应只剩 2 个真实分区，实际 %d 个: %+v", len(got), got)
	}
	for _, m := range got {
		if m.Mountpoint == "/proc" || m.Mountpoint == "/sys" || m.Mountpoint == "/dev/shm" ||
			m.Mountpoint == "/run/containers" {
			t.Fatalf("伪文件系统不应出现: %s", m.Mountpoint)
		}
	}
}

// TestCollectMounts_WhitelistOverridesDiscovery 白名单优先于自动发现。
//
// 容器部署时自动发现可能看到宿主挂载，白名单是收窄手段（spec §6 风险 6）。
func TestCollectMounts_WhitelistOverridesDiscovery(t *testing.T) {
	c := New(Options{Mounts: []string{"/data"}})
	c.partitions = func(bool) ([]disk.PartitionStat, error) {
		return []disk.PartitionStat{{Mountpoint: "/", Fstype: "ext4"}}, nil
	}
	c.usage = func(path string) (*disk.UsageStat, error) {
		return &disk.UsageStat{Total: 100, Used: 90, UsedPercent: 90}, nil
	}

	got, err := c.collectMounts()
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(got) != 1 || got[0].Mountpoint != "/data" {
		t.Fatalf("应只采白名单里的 /data，实际 %+v", got)
	}
}

// TestPublish_KeepsPreviousOnFailure 采集失败时保留上一份有效数据。
//
// 一次瞬时失败就让面板清空，比显示略旧的数据更糟——运维会以为主机状态丢了。
func TestPublish_KeepsPreviousOnFailure(t *testing.T) {
	c := New(Options{})
	good := Snapshot{
		Valid:        true,
		TopProcesses: []ProcessSample{{PID: 1, Comm: "java", CPUPct: 42}},
		Mounts:       []MountUsage{{Mountpoint: "/", UsedPct: 50}},
	}
	c.publish(good)

	c.publish(Snapshot{Valid: false, Error: "进程采集失败"})

	got := c.Snapshot()
	if len(got.TopProcesses) != 1 || got.TopProcesses[0].Comm != "java" {
		t.Fatalf("失败后应保留上次进程数据，实际 %+v", got.TopProcesses)
	}
	if len(got.Mounts) != 1 {
		t.Fatalf("失败后应保留上次磁盘数据，实际 %+v", got.Mounts)
	}
	if got.Error == "" {
		t.Fatal("失败原因应保留，否则看不出这份数据是旧的")
	}
}

// TestSortByCPUTopN 取 CPU 最高的前 N 个。
func TestSortByCPUTopN(t *testing.T) {
	items := []ProcessSample{
		{PID: 1, CPUPct: 5}, {PID: 2, CPUPct: 90}, {PID: 3, CPUPct: 20},
		{PID: 4, CPUPct: 70}, {PID: 5, CPUPct: 1},
	}
	sortByCPUTopN(items, 3)
	if items[0].PID != 2 || items[1].PID != 4 || items[2].PID != 3 {
		t.Fatalf("前 3 应为 PID 2(90),4(70),3(20)，实际 %+v", items[:3])
	}
}

// TestSnapshot_NilSafe 零值可用：装配层未注入采集器时不得 panic。
func TestSnapshot_NilSafe(t *testing.T) {
	var c *Collector
	s := c.Snapshot()
	if s.Valid {
		t.Fatal("nil collector 的快照不应标记 valid")
	}
	c.Start(t.Context()) // 不 panic 即通过
}

// TestCollectMounts_UsageErrorSkipsOnlyThatMount 单个挂载点读取失败只跳过它。
//
// 一个挂载点不可读（如权限受限的 /root 分区）不该让整组磁盘数据消失。
func TestCollectMounts_UsageErrorSkipsOnlyThatMount(t *testing.T) {
	c := New(Options{})
	c.partitions = func(bool) ([]disk.PartitionStat, error) {
		return []disk.PartitionStat{
			{Mountpoint: "/", Fstype: "ext4"},
			{Mountpoint: "/secret", Fstype: "ext4"},
		}, nil
	}
	c.usage = func(path string) (*disk.UsageStat, error) {
		if path == "/secret" {
			return nil, errors.New("permission denied")
		}
		return &disk.UsageStat{Total: 100, Used: 50, UsedPercent: 50}, nil
	}

	got, err := c.collectMounts()
	if err != nil {
		t.Fatalf("不应整体失败: %v", err)
	}
	if len(got) != 1 || got[0].Mountpoint != "/" {
		t.Fatalf("应只保留可读的 /，实际 %+v", got)
	}
}
