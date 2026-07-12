package bot

import (
	"testing"

	"github.com/wcpe/JianManager/internal/worker/runtimescan"
)

// FR-300：spawn bot-worker 用的 node 可执行解析策略。
// 优先级：显式配置 > 本地扫描最高 major Node > 回退 PATH "node"（现行为）。

func TestNodeResolver_ExplicitConfigWins(t *testing.T) {
	scanCalls := 0
	r := NewNodeResolver(`C:\custom\node.exe`, func() []runtimescan.Candidate {
		scanCalls++
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Major: 22, Path: "/opt/node22/bin/node"}}
	})

	res := r.Resolve()
	if res.Path != `C:\custom\node.exe` {
		t.Fatalf("显式配置应恒优先，got path=%q", res.Path)
	}
	if res.Source != NodeSourceExplicit {
		t.Fatalf("来源应为 %s，got %q", NodeSourceExplicit, res.Source)
	}
	if scanCalls != 0 {
		t.Fatalf("显式配置命中时不应触发扫描，scanCalls=%d", scanCalls)
	}
}

func TestNodeResolver_PicksHighestMajorNode(t *testing.T) {
	// 故意乱序 + 混入 jdk 候选：解析器须只认 nodejs 且取 major 最高者。
	r := NewNodeResolver("", func() []runtimescan.Candidate {
		return []runtimescan.Candidate{
			{Type: runtimescan.TypeNodeJS, Major: 18, Path: "/usr/bin/node"},
			{Type: runtimescan.TypeJDK, Major: 99, Path: "/usr/lib/jvm/jdk-99"},
			{Type: runtimescan.TypeNodeJS, Major: 22, Path: "/opt/node22/bin/node"},
			{Type: runtimescan.TypeNodeJS, Major: 20, Path: "/usr/local/bin/node"},
		}
	})

	res := r.Resolve()
	if res.Path != "/opt/node22/bin/node" {
		t.Fatalf("应选 major 最高的 nodejs 候选，got path=%q", res.Path)
	}
	if res.Source != NodeSourceManagedScan {
		t.Fatalf("来源应为 %s，got %q", NodeSourceManagedScan, res.Source)
	}
}

func TestNodeResolver_FallbackToPathNode(t *testing.T) {
	r := NewNodeResolver("", func() []runtimescan.Candidate { return nil })

	res := r.Resolve()
	if res.Path != "node" {
		t.Fatalf("无候选应回退 PATH \"node\"（现行为），got path=%q", res.Path)
	}
	if res.Source != NodeSourcePathFallback {
		t.Fatalf("来源应为 %s，got %q", NodeSourcePathFallback, res.Source)
	}
}

func TestNodeResolver_ResolveCachesScan(t *testing.T) {
	scanCalls := 0
	r := NewNodeResolver("", func() []runtimescan.Candidate {
		scanCalls++
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Major: 20, Path: "/usr/bin/node"}}
	})

	_ = r.Resolve()
	_ = r.Resolve()
	if scanCalls != 1 {
		t.Fatalf("解析应做一次并缓存（免每次 spawn 重扫），scanCalls=%d", scanCalls)
	}
}

func TestNodeResolver_RefreshRescans(t *testing.T) {
	scanCalls := 0
	r := NewNodeResolver("", func() []runtimescan.Candidate {
		scanCalls++
		if scanCalls == 1 {
			return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Major: 20, Path: "/gone/node"}}
		}
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Major: 22, Path: "/opt/node22/bin/node"}}
	})

	if got := r.Resolve().Path; got != "/gone/node" {
		t.Fatalf("首次解析应用第一轮扫描结果，got %q", got)
	}
	refreshed := r.Refresh()
	if refreshed.Path != "/opt/node22/bin/node" {
		t.Fatalf("Refresh 应失效缓存并重扫，got %q", refreshed.Path)
	}
	if got := r.Resolve().Path; got != "/opt/node22/bin/node" {
		t.Fatalf("Refresh 后缓存应更新，got %q", got)
	}
	if scanCalls != 2 {
		t.Fatalf("Resolve+Refresh+Resolve 应恰好扫两次，scanCalls=%d", scanCalls)
	}
}
