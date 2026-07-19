package pkgmgr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newGlobalTestManager 建一个带托管 Node 假布局 + 伪 runner 的 Manager。
func newGlobalTestManager(t *testing.T, fake runner) *Manager {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "nodejs-22", "node-v22.13.0-fake", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// 双平台命名都放：linux 裸名 + windows 的 .cmd/.exe（pmExeName/findNodeBin 按 GOOS 取）。
	for _, n := range []string{"node", "npm", "pnpm", "node.exe", "npm.cmd", "pnpm.cmd"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(root)
	m.SetRunner(fake)
	return m
}

// TestListGlobal_ParsesNpmAndMarksOutdated npm ls/outdated JSON 解析 + 可更新标记（FR-307）。
func TestListGlobal_ParsesNpmAndMarksOutdated(t *testing.T) {
	fake := func(_ context.Context, _ string, args, _ []string, _ func(string)) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"dependencies":{"mineflayer":{"version":"4.20.0"},"typescript":{"version":"5.6.0"}}}`), nil
		}
		// outdated 惯例：有可更新项时退出码非 0，stdout 仍是合法 JSON
		return []byte(`{"mineflayer":{"latest":"4.21.0"}}`), &exitOneErr{}
	}
	m := newGlobalTestManager(t, fake)
	pkgs, err := m.ListGlobal(context.Background(), "npm")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("应 2 包，实际 %d", len(pkgs))
	}
	byName := map[string]GlobalPackage{}
	for _, p := range pkgs {
		byName[p.Name] = p
	}
	if byName["mineflayer"].Latest != "4.21.0" {
		t.Fatalf("mineflayer 应标可更新 4.21.0，实际 %q", byName["mineflayer"].Latest)
	}
	if byName["typescript"].Latest != "" {
		t.Fatalf("typescript 已最新不应标 latest，实际 %q", byName["typescript"].Latest)
	}
}

type exitOneErr struct{}

func (*exitOneErr) Error() string { return "exit status 1" }

// TestInstallGlobal_RejectsIllegalNameAndVersion 参数注入防护（FR-307）。
func TestInstallGlobal_RejectsIllegalNameAndVersion(t *testing.T) {
	m := newGlobalTestManager(t, func(context.Context, string, []string, []string, func(string)) ([]byte, error) {
		t.Fatal("非法入参不应执行命令")
		return nil, nil
	})
	if err := m.InstallGlobal(context.Background(), "npm", "--evil-flag", "", nil); err == nil {
		t.Fatal("以 - 开头的包名应被拒")
	}
	if err := m.InstallGlobal(context.Background(), "npm", "ok-pkg", "1.0.0; rm -rf /", nil); err == nil {
		t.Fatal("含 shell 字符的版本应被拒")
	}
}

// TestInstallGlobal_NpmArgsAndLog npm 安装参数拼装 + 行日志回调（FR-307）。
func TestInstallGlobal_NpmArgsAndLog(t *testing.T) {
	var gotArgs []string
	fake := func(_ context.Context, _ string, args, env []string, onLine func(string)) ([]byte, error) {
		gotArgs = args
		for _, e := range env {
			if strings.HasPrefix(e, "NPM_CONFIG_USERCONFIG=") && strings.HasSuffix(e, ".npmrc") {
				goto envOK
			}
		}
		t.Fatal("环境缺 NPM_CONFIG_USERCONFIG 指向托管 .npmrc")
	envOK:
		if onLine != nil {
			onLine("added 1 package")
		}
		return []byte("added 1 package"), nil
	}
	m := newGlobalTestManager(t, fake)
	var lines []string
	if err := m.InstallGlobal(context.Background(), "npm", "mineflayer", "", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"install", "mineflayer@latest", "--prefix", m.GlobalDir()} {
		if !strings.Contains(joined, want) {
			t.Fatalf("npm 参数缺 %q: %v", want, gotArgs)
		}
	}
	if strings.Contains(joined, "--global") {
		t.Fatalf("npm 不应再使用真全局参数: %v", gotArgs)
	}
	if len(lines) == 0 || lines[0] != "added 1 package" {
		t.Fatalf("行日志回调未生效: %v", lines)
	}
}

// TestGlobal_YarnRejected yarn 全局不支持，明确错误（FR-307 范围约束）。
func TestGlobal_YarnRejected(t *testing.T) {
	m := newGlobalTestManager(t, nil)
	if _, err := m.ListGlobal(context.Background(), "yarn"); err == nil || !strings.Contains(err.Error(), "yarn") {
		t.Fatalf("yarn 应明确拒绝，实际: %v", err)
	}
}

// TestGlobal_NoManagedNodeExplains 无托管 Node 时给可操作错误（FR-307）。
func TestGlobal_NoManagedNodeExplains(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, err := m.ListGlobal(context.Background(), "npm"); err == nil || !strings.Contains(err.Error(), "Node") {
		t.Fatalf("无托管 Node 应有可操作错误，实际: %v", err)
	}
}

// TestListGlobal_EmptyPrefixTreatedAsEmpty 全空全局目录（npm ls 报 ENOENT/exit 254）
// 应视为空列表而非报错——真机复现：全新节点首次打开全局包分区直接报
// 「列出全局包失败: exit status 254（npm error code ENOENT）」（FR-307）。
func TestListGlobal_EmptyPrefixTreatedAsEmpty(t *testing.T) {
	fake := func(_ context.Context, _ string, args, _ []string, _ func(string)) ([]byte, error) {
		if args[0] == "ls" {
			// npm 对空 prefix 的真实行为：stderr 报 ENOENT、退出码非 0、stdout 无 JSON
			return []byte("npm error code ENOENT\nnpm error syscall lstat"), &exitOneErr{}
		}
		return nil, &exitOneErr{}
	}
	m := newGlobalTestManager(t, fake)
	pkgs, err := m.ListGlobal(context.Background(), "npm")
	if err != nil {
		t.Fatalf("空全局目录应回空列表而非报错: %v", err)
	}
	if len(pkgs) != 0 {
		t.Fatalf("应空列表，实际 %d", len(pkgs))
	}
}

// TestEnsurePackageRoot_MergesWithoutDroppingFields 受控 package.json 合并保留依赖与未知字段。
func TestEnsurePackageRoot_MergesWithoutDroppingFields(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := os.MkdirAll(m.GlobalDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{"dependencies":{"mineflayer":"^4.37.1"},"custom":{"keep":true},"overrides":{"left-pad":"1.3.0","@azure/msal-node":"2.16.3"},"pnpm":{"neverBuiltDependencies":["x"],"overrides":{"custom":"2.0.0"}}}`
	path := filepath.Join(m.GlobalDir(), "package.json")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.ensurePackageRoot(); err != nil {
		t.Fatal(err)
	}
	assertManagedPackageJSON(t, path)
}

func assertManagedPackageJSON(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["private"] != true || doc["dependencies"].(map[string]any)["mineflayer"] != "^4.37.1" {
		t.Fatalf("private 或 dependencies 未正确保留: %s", raw)
	}
	if doc["custom"].(map[string]any)["keep"] != true {
		t.Fatalf("未知顶层字段丢失: %s", raw)
	}
	assertManagedOverrides(t, doc, raw)
}

func assertManagedOverrides(t *testing.T, doc map[string]any, raw []byte) {
	t.Helper()
	npm := doc["overrides"].(map[string]any)
	azure := npm["@azure/msal-node"].(map[string]any)
	yggdrasil := npm["yggdrasil"].(map[string]any)
	if npm["left-pad"] != "1.3.0" || azure["."] != "2.16.3" || azure["uuid"] != "11.1.1" || yggdrasil["uuid"] != "11.1.1" {
		t.Fatalf("npm overrides 合并不正确: %s", raw)
	}
	pnpm := doc["pnpm"].(map[string]any)
	pnpmOverrides := pnpm["overrides"].(map[string]any)
	neverBuilt := pnpm["neverBuiltDependencies"].([]any)
	if len(neverBuilt) != 1 || neverBuilt[0] != "x" || pnpmOverrides["custom"] != "2.0.0" ||
		pnpmOverrides["@azure/msal-node>uuid"] != "11.1.1" || pnpmOverrides["yggdrasil>uuid"] != "11.1.1" {
		t.Fatalf("pnpm 字段或 overrides 合并不正确: %s", raw)
	}
}

// TestGlobalCommandsUseManagedProjectRoot npm/pnpm 全部操作固定到受控项目根且不再使用真全局参数。
func TestGlobalCommandsUseManagedProjectRoot(t *testing.T) {
	for _, pm := range []string{"npm", "pnpm"} {
		t.Run(pm, func(t *testing.T) {
			var calls [][]string
			fake := func(_ context.Context, _ string, args, _ []string, _ func(string)) ([]byte, error) {
				calls = append(calls, append([]string(nil), args...))
				if args[0] == "ls" && pm == "pnpm" {
					return []byte(`[]`), nil
				}
				return []byte(`{}`), nil
			}
			m := newGlobalTestManager(t, fake)
			if _, err := m.ListGlobal(context.Background(), pm); err != nil {
				t.Fatal(err)
			}
			if err := m.InstallGlobal(context.Background(), pm, "mineflayer", "", nil); err != nil {
				t.Fatal(err)
			}
			if err := m.RemoveGlobal(context.Background(), pm, "mineflayer"); err != nil {
				t.Fatal(err)
			}
			assertProjectRootCalls(t, pm, m.GlobalDir(), calls)
		})
	}
}

func assertProjectRootCalls(t *testing.T, pm, root string, calls [][]string) {
	t.Helper()
	flag := "--prefix"
	if pm == "pnpm" {
		flag = "--dir"
	}
	for _, args := range calls {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, flag+" "+root) || strings.Contains(joined, "--global") {
			t.Fatalf("%s 命令未固定到受控项目根: %v", pm, args)
		}
	}
	verbs := strings.Join(flattenFirstArgs(calls), " ")
	for _, want := range map[string][]string{"npm": {"ls", "outdated", "install", "uninstall"}, "pnpm": {"ls", "outdated", "add", "remove"}}[pm] {
		if !strings.Contains(verbs, want) {
			t.Fatalf("%s 命令缺少 %s: %v", pm, want, calls)
		}
	}
}

func flattenFirstArgs(calls [][]string) []string {
	verbs := make([]string, 0, len(calls))
	for _, args := range calls {
		if len(args) > 0 {
			verbs = append(verbs, args[0])
		}
	}
	return verbs
}
