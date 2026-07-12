package pkgmgr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GlobalPackage 托管全局目录下的一个已装包（FR-307）。
type GlobalPackage struct {
	Name    string
	Version string
	Latest  string // outdated 探测到的最新版（空=已最新/探测失败）
}

// runner 执行 PM 命令的注入点：单测替身注入伪输出，生产走真 exec。
// onLine 非 nil 时逐行回调合并后的 stdout/stderr（安装进度日志）。
type runner func(ctx context.Context, name string, args, env []string, onLine func(string)) (stdout []byte, exitErr error)

// realRun 真实执行：stdout 捕获返回；onLine 时 stdout+stderr 合并逐行回调（同时缓存 stdout）。
func realRun(ctx context.Context, name string, args, env []string, onLine func(string)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	if onLine == nil {
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		return out.Bytes(), err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return nil, err
	}
	pw.Close()
	var out bytes.Buffer
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		out.WriteString(line)
		out.WriteByte('\n')
		onLine(line)
	}
	pr.Close()
	return out.Bytes(), cmd.Wait()
}

// GlobalDir 返回托管全局包目录（PM global prefix 指向，FR-307）。
func (m *Manager) GlobalDir() string { return filepath.Join(m.runtimesRoot, "global") }

// SetRunner 注入命令执行器（仅测试）。
func (m *Manager) SetRunner(r runner) { m.run = r }

func (m *Manager) runner() runner {
	if m.run != nil {
		return m.run
	}
	return realRun
}

// pmName/pmVersion 包名与版本约束：防命令行参数注入（不得以 - 开头、字符集白名单）。
var (
	pkgNameRe    = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	pkgVersionRe = regexp.MustCompile(`^[a-zA-Z0-9^~><=.x*+-]*$`)
)

// pmExecEnv 组装 PM 执行环境：托管 node bin 目录前置 PATH + 托管 .npmrc 作 userconfig +
// pnpm 全局家目录。返回 (pm 可执行绝对路径, env, error)。
func (m *Manager) pmExecEnv(pm string) (string, []string, error) {
	if err := validatePM(pm); err != nil {
		return "", nil, err
	}
	if pm == "yarn" {
		return "", nil, fmt.Errorf("yarn 的全局包管理暂不支持（corepack yarn 无全局安装语义），请切换 npm 或 pnpm")
	}
	nodeBin := m.findNodeBin()
	if nodeBin == "" {
		return "", nil, fmt.Errorf("未找到托管 Node.js（先在运行时分区安装 Node），全局包操作依赖它")
	}
	binDir := nodeBinDir(nodeBin)
	pmPath := filepath.Join(binDir, pmExeName(pm))
	if !fileExists(pmPath) {
		if pm == "npm" {
			// npm 随 Node 分发必在；缺失即安装不完整。
			return "", nil, fmt.Errorf("托管 Node 缺少 npm（%s），请重装 Node 运行时", pmPath)
		}
		return "", nil, fmt.Errorf("%s 未激活（先在包管理器区选择 %s 保存以经 corepack 激活）", pm, pm)
	}
	if err := os.MkdirAll(m.GlobalDir(), 0o755); err != nil {
		return "", nil, fmt.Errorf("创建全局包目录失败: %w", err)
	}
	env := append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NPM_CONFIG_USERCONFIG="+m.NpmrcPath(),
	)
	if pm == "pnpm" {
		pnpmHome := filepath.Join(m.GlobalDir(), "pnpm")
		if err := os.MkdirAll(pnpmHome, 0o755); err != nil {
			return "", nil, fmt.Errorf("创建 pnpm 全局目录失败: %w", err)
		}
		env = append(env, "PNPM_HOME="+pnpmHome,
			"PATH="+pnpmHome+string(os.PathListSeparator)+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return pmPath, env, nil
}

// globalArgs 按 PM 返回把全局操作固定到托管目录的参数。
func (m *Manager) globalArgs(pm string) []string {
	if pm == "npm" {
		return []string{"--global", "--prefix", m.GlobalDir()}
	}
	return []string{"--global"} // pnpm 经 PNPM_HOME 定位
}

// ListGlobal 列出托管全局目录已装包，并 best-effort 标注可更新版本（FR-307）。
func (m *Manager) ListGlobal(ctx context.Context, pm string) ([]GlobalPackage, error) {
	pmPath, env, err := m.pmExecEnv(pm)
	if err != nil {
		return nil, err
	}
	args := append([]string{"ls", "--json", "--depth=0"}, m.globalArgs(pm)...)
	out, runErr := m.runner()(ctx, pmPath, args, env, nil)
	pkgs, parseErr := parseLsJSON(pm, out)
	if parseErr != nil && runErr != nil {
		return nil, fmt.Errorf("列出全局包失败: %v（%s）", runErr, firstLine(out))
	}
	// outdated best-effort：命令对「有可更新项」按惯例退出码非 0，只要 stdout 可解析就采纳。
	oArgs := append([]string{"outdated", "--json"}, m.globalArgs(pm)...)
	if oOut, _ := m.runner()(ctx, pmPath, oArgs, env, nil); len(oOut) > 0 {
		if latest := parseOutdatedJSON(oOut); len(latest) > 0 {
			for i := range pkgs {
				if v, ok := latest[pkgs[i].Name]; ok && v != pkgs[i].Version {
					pkgs[i].Latest = v
				}
			}
		}
	}
	return pkgs, nil
}

// InstallGlobal 安装/升级一个全局包（version 空=latest）；onLine 逐行回调 PM 输出（任务日志）。
func (m *Manager) InstallGlobal(ctx context.Context, pm, name, version string, onLine func(string)) error {
	if !pkgNameRe.MatchString(name) {
		return fmt.Errorf("非法包名: %q", name)
	}
	if version != "" && !pkgVersionRe.MatchString(version) {
		return fmt.Errorf("非法版本: %q", version)
	}
	pmPath, env, err := m.pmExecEnv(pm)
	if err != nil {
		return err
	}
	spec := name
	if version != "" {
		spec = name + "@" + version
	} else {
		spec = name + "@latest"
	}
	verb := "install"
	extra := []string{"--no-fund", "--no-audit"}
	if pm == "pnpm" {
		verb, extra = "add", nil
	}
	args := append(append([]string{verb, spec}, m.globalArgs(pm)...), extra...)
	out, runErr := m.runner()(ctx, pmPath, args, env, onLine)
	if runErr != nil {
		return fmt.Errorf("安装失败: %v（%s）", runErr, firstLine(out))
	}
	return nil
}

// RemoveGlobal 卸载一个全局包（FR-307）。
func (m *Manager) RemoveGlobal(ctx context.Context, pm, name string) error {
	if !pkgNameRe.MatchString(name) {
		return fmt.Errorf("非法包名: %q", name)
	}
	pmPath, env, err := m.pmExecEnv(pm)
	if err != nil {
		return err
	}
	verb := "uninstall"
	if pm == "pnpm" {
		verb = "remove"
	}
	args := append([]string{verb, name}, m.globalArgs(pm)...)
	out, runErr := m.runner()(ctx, pmPath, args, env, nil)
	if runErr != nil {
		return fmt.Errorf("卸载失败: %v（%s）", runErr, firstLine(out))
	}
	return nil
}

// parseLsJSON 解析 `<pm> ls --json` 输出：npm 为对象 {dependencies:{}}，pnpm 为数组。
func parseLsJSON(pm string, out []byte) ([]GlobalPackage, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return []GlobalPackage{}, nil
	}
	type depEntry struct {
		Version string `json:"version"`
	}
	collect := func(deps map[string]depEntry) []GlobalPackage {
		pkgs := make([]GlobalPackage, 0, len(deps))
		for name, d := range deps {
			pkgs = append(pkgs, GlobalPackage{Name: name, Version: d.Version})
		}
		return pkgs
	}
	if pm == "pnpm" {
		var arr []struct {
			Dependencies map[string]depEntry `json:"dependencies"`
		}
		if err := json.Unmarshal(out, &arr); err != nil {
			return nil, err
		}
		var pkgs []GlobalPackage
		for _, it := range arr {
			pkgs = append(pkgs, collect(it.Dependencies)...)
		}
		if pkgs == nil {
			pkgs = []GlobalPackage{}
		}
		return pkgs, nil
	}
	var obj struct {
		Dependencies map[string]depEntry `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		return nil, err
	}
	return collect(obj.Dependencies), nil
}

// parseOutdatedJSON 解析 `<pm> outdated --json`：npm 为 {name:{latest}}，pnpm 同构（含 current/latest）。
func parseOutdatedJSON(out []byte) map[string]string {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil
	}
	var m map[string]struct {
		Latest string `json:"latest"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		return nil
	}
	latest := make(map[string]string, len(m))
	for name, v := range m {
		if v.Latest != "" {
			latest[name] = v.Latest
		}
	}
	return latest
}

func firstLine(out []byte) string {
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
