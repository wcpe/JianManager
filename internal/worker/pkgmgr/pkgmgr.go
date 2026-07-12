// Package pkgmgr 管理节点包管理器偏好（npm/pnpm/yarn，经 corepack 激活）与多 registry
// 配置（FR-306）。配置落托管 .npmrc（NPM_CONFIG_USERCONFIG 指向，不污染用户 ~/.npmrc），
// 供 FR-307 全局包管理 / FR-308 bot-worker 依赖装全局复用。
package pkgmgr

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Registry 单条 registry 配置（scope 空=默认源）。
type Registry struct {
	Name  string
	URL   string
	Scope string
	Token string
}

// Config 节点 PM 配置读取结果。
type Config struct {
	PM                string
	CorepackAvailable bool
	PMVersion         string
	Registries        []Registry
	NodeBin           string
}

// Manager 管理托管 .npmrc 与 corepack 激活。runtimesRoot=<数据根>/opt/runtimes（含 nodejs-* 与 .npmrc）。
type Manager struct {
	runtimesRoot string // 托管运行时根：nodejs-* 子目录 + 托管 .npmrc/global 落此
	configDir    string // .npmrc 落盘目录（默认 = runtimesRoot）
	run          runner // PM 命令执行器（nil=真 exec；测试经 SetRunner 注入，FR-307）
}

// NewManager 创建包管理器。runtimesRoot 为 <数据根>/opt/runtimes。
func NewManager(runtimesRoot string) *Manager {
	return &Manager{runtimesRoot: runtimesRoot, configDir: runtimesRoot}
}

// NpmrcPath 返回托管 .npmrc 路径（FR-307 全局包操作以此作 NPM_CONFIG_USERCONFIG）。
func (m *Manager) NpmrcPath() string { return filepath.Join(m.configDir, ".npmrc") }

var validPM = map[string]bool{"npm": true, "pnpm": true, "yarn": true}

func validatePM(pm string) error {
	if !validPM[pm] {
		return fmt.Errorf("不支持的包管理器 %q（支持 npm/pnpm/yarn）", pm)
	}
	return nil
}

var scopeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func validateRegistry(r Registry) error {
	u, err := url.Parse(strings.TrimSpace(r.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("registry 地址须为 http(s) URL: %q", r.URL)
	}
	if r.Scope != "" && !scopeRe.MatchString(r.Scope) {
		return fmt.Errorf("scope 须为裸名（不含 @ 前缀）: %q", r.Scope)
	}
	return nil
}

// renderNpmrc 按 registry 列表生成 .npmrc 内容（FR-306）。
func renderNpmrc(regs []Registry) string {
	var b strings.Builder
	for _, r := range regs {
		u := strings.TrimSpace(r.URL)
		if u == "" {
			continue
		}
		if r.Scope == "" {
			fmt.Fprintf(&b, "registry=%s\n", u)
		} else {
			fmt.Fprintf(&b, "@%s:registry=%s\n", r.Scope, u)
		}
		if strings.TrimSpace(r.Token) != "" {
			if pu, err := url.Parse(u); err == nil && pu.Host != "" {
				p := pu.Path
				if !strings.HasSuffix(p, "/") {
					p += "/"
				}
				fmt.Fprintf(&b, "//%s%s:_authToken=%s\n", pu.Host, p, r.Token)
			}
		}
	}
	return b.String()
}

// parseNpmrc 反解托管 .npmrc 回 registry 列表（token 不回传，置空）。
func parseNpmrc(content string) []Registry {
	var regs []Registry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue // _authToken 行不回读
		}
		if strings.HasPrefix(line, "registry=") {
			regs = append(regs, Registry{URL: strings.TrimPrefix(line, "registry=")})
			continue
		}
		if i := strings.Index(line, ":registry="); i > 0 && strings.HasPrefix(line, "@") {
			scope := strings.TrimPrefix(line[:i], "@")
			regs = append(regs, Registry{Scope: scope, URL: line[i+len(":registry="):]})
		}
	}
	return regs
}

// writeNpmrc 原子写托管 .npmrc（临时文件 + rename）。
func (m *Manager) writeNpmrc(regs []Registry) error {
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(m.configDir, ".npmrc-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(renderNpmrc(regs)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, m.NpmrcPath())
}

// findNodeBin 定位最高 major 托管 Node 的 bin/node（兼容 <top>/bin/node 布局）。空=未找到。
func (m *Manager) findNodeBin() string {
	entries, err := os.ReadDir(m.runtimesRoot)
	if err != nil {
		return ""
	}
	type cand struct {
		major int
		bin   string
	}
	var cands []cand
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "nodejs-") {
			continue
		}
		major, _ := strconv.Atoi(strings.TrimPrefix(e.Name(), "nodejs-"))
		nodeDir := filepath.Join(m.runtimesRoot, e.Name())
		// 归档解出的顶层子目录 <top>/bin/node
		subs, _ := os.ReadDir(nodeDir)
		for _, s := range subs {
			if !s.IsDir() {
				continue
			}
			for _, rel := range []string{filepath.Join("bin", "node"), "node.exe"} {
				bin := filepath.Join(nodeDir, s.Name(), rel)
				if st, err := os.Stat(bin); err == nil && !st.IsDir() {
					cands = append(cands, cand{major, bin})
				}
			}
		}
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].major > cands[j].major })
	return cands[0].bin
}

// nodeBinDir 返回 node 可执行所在目录（corepack/npm/pnpm shim 同目录）。
func nodeBinDir(nodeBin string) string { return filepath.Dir(nodeBin) }

// Get 读取当前 PM 配置：探测 corepack 可用性、PM 版本、回读托管 .npmrc registry。
// pm 为期望偏好（来自 CP DB，Worker 不持久化偏好，仅落 .npmrc 与激活 shim）。
func (m *Manager) Get(pm string) Config {
	cfg := Config{PM: pm}
	if cfg.PM == "" {
		cfg.PM = "npm"
	}
	nodeBin := m.findNodeBin()
	cfg.NodeBin = nodeBin
	if nodeBin != "" {
		binDir := nodeBinDir(nodeBin)
		if _, err := os.Stat(filepath.Join(binDir, corepackName())); err == nil {
			cfg.CorepackAvailable = true
		}
		cfg.PMVersion = pmVersion(binDir, cfg.PM)
	}
	if b, err := os.ReadFile(m.NpmrcPath()); err == nil {
		cfg.Registries = parseNpmrc(string(b))
	}
	return cfg
}

// Set 应用 PM 偏好（pnpm/yarn 经 corepack enable 激活）并写托管 .npmrc。返回激活后的 PM 版本。
func (m *Manager) Set(ctx context.Context, pm string, regs []Registry) (string, error) {
	if err := validatePM(pm); err != nil {
		return "", err
	}
	for _, r := range regs {
		if err := validateRegistry(r); err != nil {
			return "", err
		}
	}
	nodeBin := m.findNodeBin()
	if nodeBin == "" {
		return "", fmt.Errorf("节点无托管 Node，无法配置包管理器（请先安装 Node 运行时）")
	}
	binDir := nodeBinDir(nodeBin)
	if pm != "npm" {
		corepack := filepath.Join(binDir, corepackName())
		if _, err := os.Stat(corepack); err != nil {
			return "", fmt.Errorf("托管 Node 无 corepack，无法激活 %s（请用 npm）", pm)
		}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, corepack, "enable", pm)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("corepack enable %s 失败: %v: %s", pm, err, strings.TrimSpace(string(out)))
		}
	}
	if err := m.writeNpmrc(regs); err != nil {
		return "", err
	}
	return pmVersion(binDir, pm), nil
}

// pmVersion 取 PM 版本（<binDir> 优先 PATH 下的 <pm> --version）；失败返回空。
func pmVersion(binDir, pm string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exe := pm
	if p := filepath.Join(binDir, pmExeName(pm)); fileExists(p) {
		exe = p
	}
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
