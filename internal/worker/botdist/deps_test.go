package botdist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckDeps_MissingGivesGuidance 缺依赖时给『全局包管理』指引并点名缺哪个（FR-308）。
func TestCheckDeps_MissingGivesGuidance(t *testing.T) {
	dist := t.TempDir()
	err := CheckDeps(dist, []string{filepath.Join(t.TempDir(), "nm")})
	if err == nil {
		t.Fatal("全缺应报错")
	}
	for _, want := range []string{"全局包管理", "mineflayer", "mineflayer-pathfinder"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("指引缺 %q: %v", want, err)
		}
	}
}

// TestCheckDeps_FoundInGlobalOrLegacy 托管全局候选命中 / 旧仓库式布局（dist 上级 node_modules）命中均放行。
func TestCheckDeps_FoundInGlobalOrLegacy(t *testing.T) {
	mkDeps := func(t *testing.T, nm string) {
		t.Helper()
		for _, d := range []string{"mineflayer", "mineflayer-pathfinder"} {
			if err := os.MkdirAll(filepath.Join(nm, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 托管全局命中。
	globalNM := filepath.Join(t.TempDir(), "global", "lib", "node_modules")
	mkDeps(t, globalNM)
	if err := CheckDeps(t.TempDir(), []string{globalNM}); err != nil {
		t.Fatalf("全局命中应放行: %v", err)
	}

	// 旧布局：bot-worker/node_modules 与 dist 平级（仓库式部署向后兼容）。
	base := t.TempDir()
	dist := filepath.Join(base, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	mkDeps(t, filepath.Join(base, "node_modules"))
	if err := CheckDeps(dist, nil); err != nil {
		t.Fatalf("旧布局命中应放行: %v", err)
	}
}

// TestCheckDeps_PartialMissingNamed 只缺一个也点名到位。
func TestCheckDeps_PartialMissingNamed(t *testing.T) {
	nm := filepath.Join(t.TempDir(), "nm")
	if err := os.MkdirAll(filepath.Join(nm, "mineflayer"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := CheckDeps(t.TempDir(), []string{nm})
	if err == nil || !strings.Contains(err.Error(), "mineflayer-pathfinder") {
		t.Fatalf("应点名缺 mineflayer-pathfinder: %v", err)
	}
}

// TestNodePathEnv 两个候选目录拼进 NODE_PATH（CJS 兜底）。
func TestNodePathEnv(t *testing.T) {
	got := NodePathEnv([]string{"/a/lib/node_modules", "/a/node_modules"})
	if !strings.HasPrefix(got, "NODE_PATH=") ||
		!strings.Contains(got, "/a/lib/node_modules") || !strings.Contains(got, "/a/node_modules") {
		t.Fatalf("NODE_PATH 组装不对: %s", got)
	}
}
