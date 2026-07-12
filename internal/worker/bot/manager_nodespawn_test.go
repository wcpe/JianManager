package bot

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/runtimescan"
)

// FR-300：Manager spawn bot-worker 时须用 NodeResolver 的解析结果，
// 无候选回退 PATH "node"（现行为不变），解析缓存失效时 spawn 前重扫重试一次。

// spawnedNodePath 取本代子进程实际使用的 node（Args[0]），加锁避免与 waitChild 竞态。
func spawnedNodePath(m *Manager) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd == nil || len(m.cmd.Args) == 0 {
		return ""
	}
	return m.cmd.Args[0]
}

func realNodePath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("node")
	if err != nil {
		t.Skip("测试环境无 node，跳过 spawn 解析测试")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("node 路径转绝对失败: %v", err)
	}
	return abs
}

func TestManager_Start_UsesResolvedNode(t *testing.T) {
	nodeAbs := realNodePath(t)
	script := writeScript(t, `setInterval(() => {}, 1000);`)

	resolver := NewNodeResolver("", func() []runtimescan.Candidate {
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Major: 22, Path: nodeAbs}}
	})
	mgr := NewManager(ManagerConfig{BotWorkerPath: script, NodeResolver: resolver})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer mgr.Stop()

	if got := spawnedNodePath(mgr); got != nodeAbs {
		t.Fatalf("spawn 应使用解析出的 node 绝对路径 %q，got %q", nodeAbs, got)
	}
}

func TestManager_Start_ExplicitNodePathField(t *testing.T) {
	nodeAbs := realNodePath(t)
	script := writeScript(t, `setInterval(() => {}, 1000);`)

	// 显式配置字段（V1 无 UI 只留结构）经 NewManager 内部构造的解析器生效。
	mgr := NewManager(ManagerConfig{BotWorkerPath: script, NodePath: nodeAbs})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer mgr.Stop()

	if got := spawnedNodePath(mgr); got != nodeAbs {
		t.Fatalf("spawn 应使用显式配置的 node 路径 %q，got %q", nodeAbs, got)
	}
}

func TestManager_Start_FallbackKeepsPathNode(t *testing.T) {
	requireNode(t)
	script := writeScript(t, `setInterval(() => {}, 1000);`)

	resolver := NewNodeResolver("", func() []runtimescan.Candidate { return nil })
	mgr := NewManager(ManagerConfig{BotWorkerPath: script, NodeResolver: resolver})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer mgr.Stop()

	if got := spawnedNodePath(mgr); got != "node" {
		t.Fatalf("无候选应回退 PATH \"node\"（现行为不变），got %q", got)
	}
}

func TestManager_Start_RetriesAfterStaleResolution(t *testing.T) {
	requireNode(t)
	script := writeScript(t, `setInterval(() => {}, 1000);`)
	bogus := filepath.Join(t.TempDir(), "removed", "node.exe") // 模拟托管 node 被删/移动

	scanCalls := 0
	resolver := NewNodeResolver("", func() []runtimescan.Candidate {
		scanCalls++
		if scanCalls == 1 {
			return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Major: 99, Path: bogus}}
		}
		return nil // 重扫无候选 → 回退 PATH "node"
	})
	mgr := NewManager(ManagerConfig{BotWorkerPath: script, NodeResolver: resolver})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("首个候选失效后应重扫重试成功，got err: %v", err)
	}
	defer mgr.Stop()

	if scanCalls != 2 {
		t.Fatalf("spawn 失败应恰好触发一次重扫，scanCalls=%d", scanCalls)
	}
	if got := spawnedNodePath(mgr); got != "node" {
		t.Fatalf("重试应用重扫后的回退结果 \"node\"，got %q", got)
	}
	if !mgr.IsRunning() {
		t.Fatal("重试成功后应 running=true")
	}
}
