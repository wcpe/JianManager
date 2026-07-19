package bot

import (
	"errors"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/runtimescan"
)

const minimumTestNodeVersion = "22.13.0"

func setNodeProbe(r *NodeResolver, versions map[string]string, calls *map[string]int) {
	r.probe = func(path string) (string, error) {
		if calls != nil {
			(*calls)[path]++
		}
		version, ok := versions[path]
		if !ok {
			return "", errors.New("探测失败")
		}
		return version, nil
	}
}

func TestNodeResolver_ExplicitConfigMustPassRealProbe(t *testing.T) {
	scanCalls := 0
	r := NewNodeResolver(`C:\custom\node.exe`, func() []runtimescan.Candidate {
		scanCalls++
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Version: "99.0.0", Path: "/opt/node/bin/node"}}
	})
	calls := map[string]int{}
	setNodeProbe(r, map[string]string{`C:\custom\node.exe`: "22.12.0"}, &calls)

	_, err := r.Resolve()
	if err == nil || !strings.Contains(err.Error(), minimumTestNodeVersion) {
		t.Fatalf("显式 Node 低于最低版本时应拒绝并提示 %s，实际: %v", minimumTestNodeVersion, err)
	}
	if calls[`C:\custom\node.exe`] != 1 {
		t.Fatalf("显式 Node 必须真实探测一次，calls=%v", calls)
	}
	if scanCalls != 0 {
		t.Fatalf("显式配置失败不应静默改用扫描结果，scanCalls=%d", scanCalls)
	}
}

func TestNodeResolver_ExplicitConfigWinsAfterProbe(t *testing.T) {
	r := NewNodeResolver(`C:\custom\node.exe`, func() []runtimescan.Candidate {
		t.Fatal("显式 Node 可用时不应扫描")
		return nil
	})
	setNodeProbe(r, map[string]string{`C:\custom\node.exe`: "22.13.0"}, nil)

	res, err := r.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != `C:\custom\node.exe` || res.Source != NodeSourceExplicit || res.Version != "22.13.0" {
		t.Fatalf("显式解析结果不正确: %+v", res)
	}
}

func TestNodeResolver_ManagedCandidatesUseProbedFullVersion(t *testing.T) {
	const lower = "/opt/node-lower/bin/node"
	const higher = "/opt/node-higher/bin/node"
	r := NewNodeResolver("", func() []runtimescan.Candidate {
		return []runtimescan.Candidate{
			{Type: runtimescan.TypeNodeJS, Version: "99.0.0", Major: 99, Path: lower},
			{Type: runtimescan.TypeJDK, Version: "999.0.0", Major: 999, Path: "/jdk"},
			{Type: runtimescan.TypeNodeJS, Version: "1.0.0", Major: 1, Path: higher},
		}
	})
	calls := map[string]int{}
	setNodeProbe(r, map[string]string{
		lower:  "22.13.9",
		higher: "22.14.1",
	}, &calls)

	res, err := r.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != higher || res.Version != "22.14.1" || res.Source != NodeSourceManagedScan {
		t.Fatalf("应按真实完整版本选择 22.14.1，实际: %+v", res)
	}
	if calls[lower] != 1 || calls[higher] != 1 {
		t.Fatalf("每个托管候选都应真实探测: %v", calls)
	}
}

func TestNodeResolver_ManagedRejectsOldThenProbesPath(t *testing.T) {
	const managed = "/opt/node/bin/node"
	r := NewNodeResolver("", func() []runtimescan.Candidate {
		return []runtimescan.Candidate{{Type: runtimescan.TypeNodeJS, Version: "99.0.0", Major: 99, Path: managed}}
	})
	calls := map[string]int{}
	setNodeProbe(r, map[string]string{managed: "22.12.9", "node": "22.13.0"}, &calls)

	res, err := r.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "node" || res.Source != NodeSourcePathFallback || res.Version != "22.13.0" {
		t.Fatalf("旧托管 Node 应跳过并使用通过真实探测的 PATH Node，实际: %+v", res)
	}
	if calls[managed] != 1 || calls["node"] != 1 {
		t.Fatalf("托管与 PATH 都必须真实探测: %v", calls)
	}
}

func TestNodeResolver_PathMustMeetMinimumVersion(t *testing.T) {
	r := NewNodeResolver("", func() []runtimescan.Candidate { return nil })
	setNodeProbe(r, map[string]string{"node": "20.19.4"}, nil)

	_, err := r.Resolve()
	if err == nil || !strings.Contains(err.Error(), minimumTestNodeVersion) {
		t.Fatalf("PATH Node 低于最低版本时应拒绝，实际: %v", err)
	}
}

func TestNodeResolver_OnlyCachesSuccessfulResolution(t *testing.T) {
	r := NewNodeResolver("", func() []runtimescan.Candidate { return nil })
	probeCalls := 0
	r.probe = func(path string) (string, error) {
		probeCalls++
		if probeCalls == 1 {
			return "", errors.New("暂时不可用")
		}
		return minimumTestNodeVersion, nil
	}

	if _, err := r.Resolve(); err == nil {
		t.Fatal("首次探测应失败")
	}
	res, err := r.Resolve()
	if err != nil {
		t.Fatalf("失败结果不应缓存，第二次应重新探测成功: %v", err)
	}
	if res.Path != "node" {
		t.Fatalf("第二次应解析 PATH Node，实际: %+v", res)
	}
	if _, err := r.Resolve(); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 2 {
		t.Fatalf("失败不缓存、成功缓存，探测次数应为 2，实际 %d", probeCalls)
	}
}

func TestNodeResolver_RefreshFailureClearsCachedSuccess(t *testing.T) {
	r := NewNodeResolver("", func() []runtimescan.Candidate { return nil })
	probeCalls := 0
	r.probe = func(path string) (string, error) {
		probeCalls++
		switch probeCalls {
		case 1, 3:
			return minimumTestNodeVersion, nil
		default:
			return "", errors.New("暂时不可用")
		}
	}

	if _, err := r.Resolve(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(); err == nil {
		t.Fatal("Refresh 应暴露探测失败")
	}
	if _, err := r.Resolve(); err != nil {
		t.Fatalf("Refresh 失败不得保留旧缓存，后续 Resolve 应再次探测: %v", err)
	}
	if probeCalls != 3 {
		t.Fatalf("预期探测三次，实际 %d", probeCalls)
	}
}
