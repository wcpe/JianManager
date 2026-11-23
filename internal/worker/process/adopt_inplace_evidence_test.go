package process

import (
	"os"
	"testing"
)

// TestAdoptedRunTimeCountsAsAlive 收养的外部进程必须被存活证据认可。
//
// ## 这个用例守的是什么
// 就地收养拿到的进程**不是平台 spawn 的**，所以它没有 strategy，也没有 wrapper PID 文件。
// 若存活探测只看 strategy / PID 文件，就会得出“进程不存在”→ 健康巡检判假死 →
// 把状态覆盖回 STOPPED——而服务器的 java 一直在跑。
// 真机实测：收养成功后 30s 内状态又变回 STOPPED，就是这个原因。
//
// 本用例用**当前进程自己的 PID** 作为 AdoptedPID（必然存活），
// 验证证据链直接把它认作 Running，而不是去拨根本不存在的 wrapper socket。
func TestAdoptedRunTimeCountsAsAlive(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "adopted-1"
	self := currentPID()

	m.mu.Lock()
	m.instances[uuid] = &Instance{
		UUID:        uuid,
		State:       StateRunning,
		AdoptedPID:  self,
		processType: ProcessTypeDaemon,
		// 刻意不设 strategy：外部进程没有平台句柄。
	}
	m.mu.Unlock()

	ev := m.probeOneEvidence(uuid)
	if !ev.Running {
		t.Fatalf("被收养的活进程应判为 Running，实际 Running=%v Detail=%q", ev.Running, ev.Detail)
	}
	if ev.RootPID != self {
		t.Fatalf("RootPID 应为被收养的 pid=%d，实际 %d", self, ev.RootPID)
	}
	if !ev.ProcessAlive {
		t.Fatal("ProcessAlive 应为 true")
	}
}

// TestAdoptedRunTimeExitedIsNotAlive 被收养的进程死了要如实报 dead（不粉饰）。
func TestAdoptedRunTimeExitedIsNotAlive(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "adopted-2"
	// 一个几乎不可能存在的 PID（远大于 Linux 默认 pid_max 常见值）。
	m.mu.Lock()
	m.instances[uuid] = &Instance{
		UUID:        uuid,
		State:       StateRunning,
		AdoptedPID:  4194303,
		processType: ProcessTypeDaemon,
	}
	m.mu.Unlock()

	ev := m.probeOneEvidence(uuid)
	if ev.Running {
		t.Fatal("已退出进程不应判为 Running")
	}
}

// currentPID 返回当前测试进程 pid（保证存活）。
func currentPID() int { return os.Getpid() }
