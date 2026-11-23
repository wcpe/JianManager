package register

import "testing"

// TestRealHostProcCounts 进程计数在真机上取得到且为正。
func TestRealHostProcCounts(t *testing.T) {
	running, total, err := hostProcCounts()
	if err != nil {
		// 非 Linux 平台取不到是预期内的（CP 侧按缺测处理），跳过而不是失败。
		t.Skipf("本平台不提供 /proc/loadavg: %v", err)
	}
	if total <= 0 {
		t.Fatalf("总进程数应为正，实际 %d", total)
	}
	if running < 0 {
		t.Fatalf("运行中进程数不应为负，实际 %d", running)
	}
	if running > total {
		t.Fatalf("运行中(%d) 不应大于总数(%d)", running, total)
	}
}
