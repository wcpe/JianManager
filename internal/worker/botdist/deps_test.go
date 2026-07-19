package botdist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeRequiredDeps(t *testing.T, nodeModules string) {
	t.Helper()
	for _, dep := range requiredDeps {
		if err := os.MkdirAll(filepath.Join(nodeModules, dep), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCheckDeps_MissingGivesGuidance 缺依赖时给『全局包管理』指引并点名缺哪个（FR-308）。
func TestCheckDeps_MissingGivesGuidance(t *testing.T) {
	err := CheckDeps(t.TempDir())
	if err == nil {
		t.Fatal("全缺应报错")
	}
	for _, want := range []string{"全局包管理", "mineflayer", "mineflayer-pathfinder"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("指引缺 %q: %v", want, err)
		}
	}
}

// TestCheckDeps_OnlyAcceptsESMVisibleRoots 只认可入口目录及其父目录的 node_modules。
func TestCheckDeps_OnlyAcceptsESMVisibleRoots(t *testing.T) {
	base := t.TempDir()
	dist := filepath.Join(base, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}

	makeRequiredDeps(t, filepath.Join(dist, "node_modules"))
	if err := CheckDeps(dist); err != nil {
		t.Fatalf("dist 同级依赖应对 ESM 可见: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dist, "node_modules")); err != nil {
		t.Fatal(err)
	}
	makeRequiredDeps(t, filepath.Join(base, "node_modules"))
	if err := CheckDeps(dist); err != nil {
		t.Fatalf("仓库旧布局依赖应对 ESM 可见: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(base, "node_modules")); err != nil {
		t.Fatal(err)
	}
	nestedDist := filepath.Join(base, "apps", "bot-worker", "dist")
	if err := os.MkdirAll(nestedDist, 0o755); err != nil {
		t.Fatal(err)
	}
	makeRequiredDeps(t, filepath.Join(base, "node_modules"))
	if err := CheckDeps(nestedDist); err != nil {
		t.Fatalf("更高层 ESM 祖先 node_modules 也应可见: %v", err)
	}
}

// TestCheckDeps_DoesNotTrustBareManagedRoot 裸受控根不在 ESM 祖先链上时不能直接放行。
func TestCheckDeps_DoesNotTrustBareManagedRoot(t *testing.T) {
	base := t.TempDir()
	dist := filepath.Join(base, "bot-worker", "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	makeRequiredDeps(t, filepath.Join(base, "runtimes", "global", "node_modules"))
	if err := CheckDeps(dist); err == nil {
		t.Fatal("裸受控根即使依赖完整，也不能绕过 ESM 可见路径预检")
	}
}

// TestCheckDeps_PartialMissingNamed 只缺一个也点名到位。
func TestCheckDeps_PartialMissingNamed(t *testing.T) {
	dist := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dist, "node_modules", "mineflayer"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := CheckDeps(dist)
	if err == nil || !strings.Contains(err.Error(), "mineflayer-pathfinder") {
		t.Fatalf("应点名缺 mineflayer-pathfinder: %v", err)
	}
}

// TestCheckDeps_RejectsSplitVisibleRoots 两个依赖分散在不同 ESM 可见根时不能误判为可运行。
func TestCheckDeps_RejectsSplitVisibleRoots(t *testing.T) {
	base := t.TempDir()
	dist := filepath.Join(base, "dist")
	if err := os.MkdirAll(filepath.Join(dist, "node_modules", "mineflayer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "node_modules", "mineflayer-pathfinder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckDeps(dist); err == nil {
		t.Fatal("依赖分散在不同 ESM 根时应拒绝启动")
	}
}

// TestGlobalNodeModulesCandidates_NewRootFirst 新受控根始终排在旧真全局目录前。
func TestGlobalNodeModulesCandidates_NewRootFirst(t *testing.T) {
	root := t.TempDir()
	got := GlobalNodeModulesCandidates(root)
	wantNew := filepath.Join(root, "global", "node_modules")
	wantLegacy := filepath.Join(root, "global", "lib", "node_modules")
	if len(got) != 2 || got[0] != wantNew || got[1] != wantLegacy {
		t.Fatalf("候选顺序不正确: %v", got)
	}
}

// TestGlobalNodeModulesDir_PrefersCompleteNewAndFallsBackLegacy 路径切换只认完整依赖集。
func TestGlobalNodeModulesDir_PrefersCompleteNewAndFallsBackLegacy(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, "global", "node_modules")
	legacyDir := filepath.Join(root, "global", "lib", "node_modules")
	if got := GlobalNodeModulesDir(root); got != newDir {
		t.Fatalf("目录都不存在时应默认新根: %s", got)
	}
	makeRequiredDeps(t, legacyDir)
	if got := GlobalNodeModulesDir(root); got != legacyDir {
		t.Fatalf("旧根依赖完整时应继续兼容旧根: %s", got)
	}
	if err := os.MkdirAll(filepath.Join(newDir, requiredDeps[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := GlobalNodeModulesDir(root); got != legacyDir {
		t.Fatalf("新根依赖不完整时不得切断旧根: %s", got)
	}
	if err := os.MkdirAll(filepath.Join(newDir, requiredDeps[1]), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := GlobalNodeModulesDir(root); got != newDir {
		t.Fatalf("新根依赖完整后应切换到新根: %s", got)
	}
}

// TestRefreshNodeModulesLink_SwitchesOnlyAfterNewRootComplete spawn 前刷新只在新根完整后切换旧链接。
func TestRefreshNodeModulesLink_SwitchesOnlyAfterNewRootComplete(t *testing.T) {
	runtimesRoot := filepath.Join(t.TempDir(), "runtimes")
	dist := filepath.Join(t.TempDir(), "bot-worker")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	candidates := GlobalNodeModulesCandidates(runtimesRoot)
	newDir, legacyDir := candidates[0], candidates[1]
	makeRequiredDeps(t, legacyDir)
	if err := os.WriteFile(filepath.Join(legacyDir, "root.txt"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RefreshNodeModulesLink(dist, runtimesRoot); err != nil {
		t.Fatal(err)
	}
	assertLinkedRoot(t, dist, "legacy")

	if err := os.MkdirAll(filepath.Join(newDir, requiredDeps[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RefreshNodeModulesLink(dist, runtimesRoot); err != nil {
		t.Fatal(err)
	}
	assertLinkedRoot(t, dist, "legacy")

	if err := os.MkdirAll(filepath.Join(newDir, requiredDeps[1]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "root.txt"), []byte("managed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RefreshNodeModulesLink(dist, runtimesRoot); err != nil {
		t.Fatal(err)
	}
	assertLinkedRoot(t, dist, "managed")
	if err := CheckDeps(dist); err != nil {
		t.Fatalf("刷新后的 ESM 链接应通过预检: %v", err)
	}
}

func assertLinkedRoot(t *testing.T, dist, want string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dist, "node_modules", "root.txt"))
	if err != nil {
		t.Fatalf("读取链接目标探针失败: %v", err)
	}
	if string(raw) != want {
		t.Fatalf("链接目标不正确: got=%q want=%q", raw, want)
	}
}

// TestNodePathEnv 两个候选目录拼进 NODE_PATH（CJS 兜底）。
func TestNodePathEnv(t *testing.T) {
	got := NodePathEnv([]string{"/a/node_modules", "/a/lib/node_modules"})
	if !strings.HasPrefix(got, "NODE_PATH=") ||
		!strings.Contains(got, "/a/node_modules") || !strings.Contains(got, "/a/lib/node_modules") {
		t.Fatalf("NODE_PATH 组装不对: %s", got)
	}
}
