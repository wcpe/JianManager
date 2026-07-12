// Package runtime 提供节点本地非 JDK 运行时（首批 Node.js）的下载安装与托管删除（FR-299）。
//
// 设计目标（见 docs/specs/node-runtime-library/spec.md §3.2「FR-299 Node 安装器」）：
//   - InstallNodeJS 从 nodejs.org dist（镜像可配）解析该 major 最新版本并下载便携归档，
//     解压到托管目录 <数据根>/opt/runtimes/nodejs-<major>；
//   - 下载复用 jdk 包导出的 DownloadAndExtract 基建：同一出站 client 语义（FR-174/ADR-043）、
//     停滞看门狗判卡死（FR-290）、网络失败可操作引导（FR-279）；
//   - 残骸自愈（FR-291）：完成标记 = node 可执行文件（bin/node | node.exe），无标记的
//     半截目录自动清除重装、有标记的完好目录拒绝覆盖；
//   - Remove 删除归一到托管根下顶层子目录整体删除，拒绝根本身与根外路径（FR-292）。
//
// 所有操作只针对 Worker 本地文件系统；CP 通过 gRPC InstallRuntime/RemoveRuntime 触发，
// 结果经任务心跳终态写回 CP 侧 model.NodeRuntime 表。
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/jdk"
)

// Info Worker 本地安装出的运行时信息（对齐 CP node_runtimes 行）。
type Info struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Major   int    `json:"major"`
	Arch    string `json:"arch"`
	// Path node 可执行文件绝对路径（与扫描器 nodejs 候选同语义，非目录）。
	Path    string `json:"path"`
	Managed bool   `json:"managed"`
}

// Progress 下载进度回调（percent 0~100，line 可选阶段日志），语义同 jdk.Progress。
type Progress = jdk.Progress

// Manager 维护 Worker 托管运行时目录（<数据根>/opt/runtimes）。
// 多协程安全：Install/Remove 串行化执行以避免并发解压冲突（同 jdk.Manager）。
type Manager struct {
	mu      sync.Mutex
	rootDir string
	// httpClient / httpProvider 出站 client 注入语义同 jdk.Manager（FR-174/FR-185/ADR-043）：
	// provider 优先（每次下载取当前，代理改动即时生效），皆空回退裸默认 client。
	httpClient   *http.Client
	httpProvider func() *http.Client
	// stall/interval 下载停滞看门狗阈值（FR-290）；零值用 jdk 包默认，测试注入小值。
	stall, interval time.Duration
}

// NewManager 创建运行时安装管理器。rootDir 是托管根（Install 写入 <rootDir>/nodejs-<major>）。
func NewManager(rootDir string) *Manager {
	return &Manager{rootDir: rootDir}
}

// SetHTTPClient 注入出站 client（经进程级代理，FR-174/ADR-037）。
func (m *Manager) SetHTTPClient(c *http.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpClient = c
}

// SetHTTPClientProvider 注入运行时出站持有者（FR-185/ADR-043）：每次下载取当前 client，
// 使 CP 经心跳下发的代理改动即时生效。优先于 SetHTTPClient 注入的固定 client。
func (m *Manager) SetHTTPClientProvider(p func() *http.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpProvider = p
}

// RootDir 返回托管根目录。
func (m *Manager) RootDir() string { return m.rootDir }

// downloadClient 返回下载所用 client（语义同 jdk.Manager.downloadClient）：优先运行时持有者、
// 其次固定注入、否则裸默认 client；不设总超时（卡死由 stall 看门狗判定，FIX-4），
// 注入 client 若设了 Timeout 则克隆去掉。须在持有 m.mu 时调用。
func (m *Manager) downloadClient() *http.Client {
	c := m.httpClient
	if m.httpProvider != nil {
		if pc := m.httpProvider(); pc != nil {
			c = pc
		}
	}
	if c == nil {
		return &http.Client{}
	}
	if c.Timeout != 0 {
		cc := *c
		cc.Timeout = 0
		return &cc
	}
	return c
}

// InstallNodeJS 下载并安装指定 major 的最新 Node.js 到 <rootDir>/nodejs-<major>（FR-299）。
// arch 用 nodejs dist 命名（x64/arm64，CP 已按类型归一），空取本机推导；
// mirrorBase 非空作下载基址（CP 从平台设置 runtime.mirror.nodejs 下发），空回退 env/官方源。
// 返回的 Info.Path 为 node 可执行文件绝对路径（登记 node_runtimes.path 用）。
func (m *Manager) InstallNodeJS(ctx context.Context, major int, arch, mirrorBase string, progress Progress) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if major <= 0 {
		return Info{}, fmt.Errorf("major 必填")
	}
	if arch == "" {
		arch = defaultNodeArch()
	}
	report := func(percent int, line string) {
		if progress != nil {
			progress(percent, line)
		}
	}
	installDir := filepath.Join(m.rootDir, fmt.Sprintf("nodejs-%d", major))

	if _, err := os.Stat(installDir); err == nil {
		// 有 node 可执行文件完成标记 = 完好已装目录，拒绝覆盖；无标记 = 上次安装失败/取消
		// 遗留的残骸，自动清除重装，不堵死重试（FR-291，语义与 JDK 安装器齐平）。
		if dirLooksLikeNode(installDir) {
			return Info{}, fmt.Errorf("目标目录已存在: %s", installDir)
		}
		if err := os.RemoveAll(installDir); err != nil {
			return Info{}, fmt.Errorf("清理上次安装残留目录失败: %w", err)
		}
		slog.Info("检测到上次安装残留目录，已自动清理重装", "dir", installDir)
		report(0, "检测到上次安装残留目录，已自动清理")
	}

	client := m.downloadClient()
	base := nodeMirrorBase(mirrorBase)
	report(0, fmt.Sprintf("解析 Node.js %d 最新版本", major))
	version, err := resolveNodeVersion(client, base, major)
	if err != nil {
		return Info{}, err
	}
	url := nodeArchiveURL(base, version, arch)
	slog.Info("开始下载 Node.js", "major", major, "version", version, "arch", arch, "url", url)
	report(0, fmt.Sprintf("开始下载归档 node-v%s-%s-%s", version, nodeOSName(), arch))

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return Info{}, fmt.Errorf("创建安装目录失败: %w", err)
	}
	if err := jdk.DownloadAndExtract(ctx, client, url, installDir, Progress(report), m.stall, m.interval); err != nil {
		_ = os.RemoveAll(installDir)
		return Info{}, err
	}
	report(100, "解压完成，正在校验")

	exe, ok := findNodeExecutable(installDir)
	if !ok {
		_ = os.RemoveAll(installDir)
		return Info{}, fmt.Errorf("已下载但未找到 node 可执行文件，安装可能不完整")
	}
	report(100, fmt.Sprintf("安装完成：Node.js %s", version))
	return Info{
		Type:    "nodejs",
		Name:    fmt.Sprintf("Node.js %d", major),
		Version: version,
		Major:   major,
		Arch:    arch,
		Path:    exe,
		Managed: true,
	}, nil
}

// Remove 删除托管运行时目录（FR-292 语义与 JDK 齐平）：登记路径可能是解压后嵌套的内层
// （…/nodejs-22/node-v22.17.0-linux-x64/bin/node），归一到托管根下的顶层子目录整体删除；
// 托管根本身与根外路径一律拒绝。
func (m *Manager) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if path == "" {
		return fmt.Errorf("path 必填")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析路径失败: %w", err)
	}
	rootAbs, err := filepath.Abs(m.rootDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	// rel="." 即托管根本身：整根删除会清空全部托管运行时，一并拒绝（FR-292）。
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("只能删除托管目录 (%s) 下的运行时", m.rootDir)
	}
	top := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		top = rel[:i]
	}
	return os.RemoveAll(filepath.Join(rootAbs, top))
}

// findNodeExecutable 在 dir 本身及一级子目录下找 node 可执行文件（安装完成标记，FR-291）。
// 兼容官方归档两种布局：linux/darwin tar.gz 为 <top>/bin/node，windows zip 为 <top>/node.exe。
func findNodeExecutable(dir string) (string, bool) {
	candidates := []string{
		filepath.Join(dir, "bin", "node"),
		filepath.Join(dir, "node.exe"),
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			candidates = append(candidates,
				filepath.Join(dir, e.Name(), "bin", "node"),
				filepath.Join(dir, e.Name(), "node.exe"),
			)
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, true
		}
	}
	return "", false
}

// dirLooksLikeNode 报告 dir 是否像一个完好的已装 Node.js（有完成标记；只查文件存在、
// 不执行 node，避免安装入口引入进程执行开销，FR-291）。
func dirLooksLikeNode(dir string) bool {
	_, ok := findNodeExecutable(dir)
	return ok
}

// defaultNodeArch 按本机 GOARCH 推导 nodejs dist 命名的 arch（x64/arm64）。
// 注意 nodejs 与 adoptium（x64/aarch64）命名不同，归一表按运行时类型分派、不共用（spec §6）。
func defaultNodeArch() string {
	switch goruntime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	}
	return goruntime.GOARCH
}
