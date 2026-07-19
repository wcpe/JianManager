package bot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func allowTestNode(r *NodeResolver) {
	r.probe = func(string) (string, error) { return minimumTestNodeVersion, nil }
}

func TestManager_Start_UsesResolvedNode(t *testing.T) {
	nodeAbs := realNodePath(t)
	script := writeScript(t, `setInterval(() => {}, 1000);`)

	resolver := NewNodeResolver("", func() []runtimescan.Candidate {
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Major: 22, Path: nodeAbs}}
	})
	allowTestNode(resolver)
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
	allowTestNode(resolver)
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
	allowTestNode(resolver)
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

func TestManager_Start_RetriesAfterSamePathBecomesAvailable(t *testing.T) {
	helperName := "sh"
	if runtime.GOOS == "windows" {
		helperName = "cmd.exe"
	}
	helper, err := exec.LookPath(helperName)
	if err != nil {
		t.Skipf("测试环境无 %s: %v", helperName, err)
	}
	script := writeScript(t, `setInterval(() => {}, 1000);`)
	target := filepath.Join(t.TempDir(), "node-retry"+filepath.Ext(helper))
	resolver := NewNodeResolver("", func() []runtimescan.Candidate {
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Path: target}}
	})
	probeCalls := 0
	resolver.probe = func(string) (string, error) {
		probeCalls++
		if probeCalls == 2 {
			data, err := os.ReadFile(helper)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(target, data, 0o755); err != nil {
				return "", err
			}
		}
		return minimumTestNodeVersion, nil
	}

	mgr := NewManager(ManagerConfig{BotWorkerPath: script, NodeResolver: resolver})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("相同文本路径在重探后恢复可用时应重试成功: %v", err)
	}
	defer mgr.Stop()
	if probeCalls != 2 || spawnedNodePath(mgr) != target {
		t.Fatalf("应以同一路径重探并重试，probeCalls=%d node=%q", probeCalls, spawnedNodePath(mgr))
	}
}

func TestManager_Start_PreparesRuntimeBeforeDependencyCheck(t *testing.T) {
	nodeAbs := realNodePath(t)
	script := writeScript(t, `setInterval(() => {}, 1000);`)
	resolver := NewNodeResolver("", func() []runtimescan.Candidate {
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Path: nodeAbs}}
	})
	allowTestNode(resolver)

	var order []string
	mgr := NewManager(ManagerConfig{
		BotWorkerPath: script,
		NodeResolver:  resolver,
		PrepareSpawn: func(distDir string) error {
			order = append(order, "prepare")
			return os.MkdirAll(filepath.Join(distDir, "node_modules", "mineflayer"), 0o755)
		},
		DepsPrecheck: func(distDir string) error {
			order = append(order, "check")
			if _, err := os.Stat(filepath.Join(distDir, "node_modules", "mineflayer")); err != nil {
				return errors.New("运行时刷新尚未完成")
			}
			return nil
		},
	})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("运行时准备应先于依赖预检: %v", err)
	}
	defer mgr.Stop()
	if len(order) != 2 || order[0] != "prepare" || order[1] != "check" {
		t.Fatalf("调用顺序不正确: %v", order)
	}
}
