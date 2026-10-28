package hostobs

import (
	"testing"
	"time"
)

// FR-513 真机验收：在本机实际采集一次，确认三项数据都拿得到且有合理取值。
//
// 与 collector_test.go 的差别：那些用例用注入的假数据测逻辑；
// 这里**故意读真实主机**——因为「能不能在真机上采到」本身就是要验的东西
// （gopsutil 在各平台行为不同，纯 mock 测不出这一点）。
//
// 断言只取「必然成立」的性质，不锁死具体数值：CPU 占用率、进程数都会随机器负载变化，
// 断言具体值只会得到与环境耦合的脆弱用例。
func TestRealHostCollection(t *testing.T) {
	if testing.Short() {
		t.Skip("真机采集用例在 -short 下跳过")
	}
	c := New(Options{TopN: 5})
	c.collectOnce(t.Context())

	snap := c.Snapshot()
	if !snap.Valid {
		t.Fatalf("真机采集应成功，实际 Error=%q", snap.Error)
	}
	if snap.SampledAt.IsZero() {
		t.Fatal("应有采样时间")
	}

	// 1) 整机 top-N 进程
	if len(snap.TopProcesses) == 0 {
		t.Fatal("应至少采到一个进程——本机不可能没有进程")
	}
	if len(snap.TopProcesses) > 5 {
		t.Fatalf("TopN 应被截断到 5，实际 %d", len(snap.TopProcesses))
	}
	for _, p := range snap.TopProcesses {
		if p.PID == 0 {
			t.Fatal("进程 PID 不应为 0")
		}
		if p.Comm == "" {
			t.Fatalf("进程 %d 缺 comm", p.PID)
		}
		// 隐私边界（spec §2 不做）：comm 是进程名，不应包含空格分隔的完整命令行。
		// cmdline 里常带密码/token，这条断言防止将来有人把 cmdline 塞进 Comm。
		if len(p.Comm) > 64 {
			t.Fatalf("comm 过长（疑似混入了 cmdline）: %q", p.Comm)
		}
	}
	// 按 CPU 降序
	for i := 1; i < len(snap.TopProcesses); i++ {
		if snap.TopProcesses[i].CPUPct > snap.TopProcesses[i-1].CPUPct {
			t.Fatalf("应按 CPU 降序，实际 %+v", snap.TopProcesses)
		}
	}

	// 2) 分挂载点磁盘
	if len(snap.Mounts) == 0 {
		t.Fatal("应至少采到一个挂载点")
	}
	for _, m := range snap.Mounts {
		if m.Mountpoint == "" {
			t.Fatal("挂载点路径不应为空")
		}
		if m.TotalBytes == 0 {
			t.Fatalf("挂载点 %s 总量不应为 0", m.Mountpoint)
		}
		if m.UsedBytes > m.TotalBytes {
			t.Fatalf("挂载点 %s 已用 %d > 总量 %d", m.Mountpoint, m.UsedBytes, m.TotalBytes)
		}
		if m.UsedPct < 0 || m.UsedPct > 100 {
			t.Fatalf("挂载点 %s 使用率越界: %f", m.Mountpoint, m.UsedPct)
		}
	}
	// 根分区一定存在，且不应被伪文件系统过滤掉
	var hasRoot bool
	for _, m := range snap.Mounts {
		if m.Mountpoint == "/" {
			hasRoot = true
		}
	}
	if !hasRoot {
		t.Fatalf("应包含根分区 /，实际 %+v", snap.Mounts)
	}
}

// TestRealHostCollectionThrottle 两次采集之间的间隔受 10s 下限保护。
//
// 只验归一化结果，不真的等 10s（那会让用例变慢十倍且无额外信息）。
func TestRealHostCollectionThrottle(t *testing.T) {
	c := New(Options{Interval: 100 * time.Millisecond})
	if c.interval != minInterval {
		t.Fatalf("间隔应被抬到下限 %v，实际 %v", minInterval, c.interval)
	}
}
