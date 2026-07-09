package bot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// 复现真机缺陷：bot-worker 子进程死亡后 Manager 未感知——running 卡在 true，
// ensureBotManager 懒重拉永不触发，后续 IPC 全部写入死管道（Bot 永远 connecting）。
// 期望：子进程退出后 IsRunning() 归 false，且可再次 Start 重拉。

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("测试环境无 node，跳过子进程生命周期测试")
	}
}

func writeScript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-bot-worker.js")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写测试脚本失败: %v", err)
	}
	return path
}

func TestManager_ChildExit_MarksNotRunning(t *testing.T) {
	requireNode(t)
	// 子进程启动后立刻自亡（模拟 bot-worker 崩溃），stderr 留一行现场。
	script := writeScript(t, `console.error("boom: simulated crash"); process.exit(3);`)

	mgr := NewManager(ManagerConfig{BotWorkerPath: script})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for mgr.IsRunning() {
		select {
		case <-deadline:
			t.Fatal("子进程已退出但 Manager 仍自认 running=true（懒重拉被卡死，IPC 将写入死管道）")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestManager_ChildExit_CanRestart(t *testing.T) {
	requireNode(t)
	script := writeScript(t, `process.exit(0);`)

	mgr := NewManager(ManagerConfig{BotWorkerPath: script})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("首次 Start 失败: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for mgr.IsRunning() {
		select {
		case <-deadline:
			t.Fatal("子进程退出后 Manager 未归位 running=false")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// ensureBotManager 的语义：不在运行即可重拉。
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("子进程死亡后重拉 Start 失败: %v", err)
	}
	mgr.Stop()
}

func TestManager_Stop_StillCleanShutdown(t *testing.T) {
	requireNode(t)
	// 常驻子进程（模拟正常 bot-worker），验证修复后 Stop 语义不回归。
	script := writeScript(t, `setInterval(() => {}, 1000);`)

	mgr := NewManager(ManagerConfig{BotWorkerPath: script})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if !mgr.IsRunning() {
		t.Fatal("常驻子进程存活期间应 running=true")
	}
	mgr.Stop()
	if mgr.IsRunning() {
		t.Fatal("Stop 后应 running=false")
	}
}
