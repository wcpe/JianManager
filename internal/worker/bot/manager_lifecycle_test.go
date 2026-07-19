package bot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

func TestManager_WaitReadyReturnsWhenChildExitsAndNextGenerationCanSucceed(t *testing.T) {
	requireNode(t)
	exitScript := writeScript(t, `process.exit(0);`)
	readyScript := writeScript(t, `
console.log(JSON.stringify({evt:"worker-ready",workerEpoch:"epoch-next",workerEpochGeneration:2,maxBots:50,features:["fleet-v1"],capacityGeneration:1}));
setInterval(() => {}, 1000);
`)

	mgr := NewManager(ManagerConfig{BotWorkerPath: exitScript})
	require.NoError(t, mgr.Start(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	startedAt := time.Now()
	err := mgr.WaitReady(ctx)
	require.ErrorContains(t, err, "进程已退出")
	require.Less(t, time.Since(startedAt), time.Second, "ready 前退出不应等待 context 超时")

	mgr.SetBotWorkerPath(readyScript)
	require.Eventually(t, func() bool { return !mgr.IsRunning() }, time.Second, 10*time.Millisecond)
	require.NoError(t, mgr.Start(context.Background()))
	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	defer readyCancel()
	require.NoError(t, mgr.WaitReady(readyCtx))
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

func TestManager_ChildExit_InvalidatesFleetRuntime(t *testing.T) {
	requireNode(t)
	script := writeScript(t, `
console.log(JSON.stringify({evt:"worker-ready",workerEpoch:"epoch-1",workerEpochGeneration:1,maxBots:50,features:["fleet-v1"],capacityGeneration:7}));
console.log(JSON.stringify({evt:"bot-state",bots:[{id:"ghost-bot",status:"connected",sessionId:"run-1",workerEpochGeneration:1,eventSeq:1}]}));
console.log(JSON.stringify({evt:"heartbeat",activeBots:1,connectingBots:0,capacityGeneration:7}));
setTimeout(() => process.exit(0), 100);
`)

	mgr := NewManager(ManagerConfig{BotWorkerPath: script})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.CapacitySnapshot().Ready && len(mgr.FleetSnapshot("")) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mgr.CapacitySnapshot().Ready || len(mgr.FleetSnapshot("")) != 1 {
		t.Fatal("测试子进程退出前未建立旧 Bot 运行态")
	}
	generationBeforeExit := mgr.CapacitySnapshot().CapacityGeneration

	deadline = time.Now().Add(5 * time.Second)
	for mgr.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if mgr.IsRunning() {
		t.Fatal("子进程退出后 Manager 未归位")
	}

	capacity := mgr.CapacitySnapshot()
	if capacity.Ready || capacity.ActiveBots != 0 || capacity.ConnectingBots != 0 {
		t.Fatalf("子进程退出后容量运行态未清零: %+v", capacity)
	}
	if capacity.CapacityGeneration <= generationBeforeExit {
		t.Fatalf("子进程退出应递增容量语义世代: before=%d after=%d", generationBeforeExit, capacity.CapacityGeneration)
	}
	if fleet := mgr.FleetSnapshot(""); len(fleet) != 0 {
		t.Fatalf("子进程退出后仍保留幽灵 Bot: %+v", fleet)
	}
}
