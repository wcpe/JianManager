package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newGlobalTestManager 建一个带托管 Node 假布局 + 伪 runner 的 Manager。
func newGlobalTestManager(t *testing.T, fake runner) *Manager {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "nodejs-22", "node-v22.0.0-fake", "bin")
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
	for _, want := range []string{"install", "mineflayer@latest", "--global", "--prefix"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("npm 参数缺 %q: %v", want, gotArgs)
		}
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
