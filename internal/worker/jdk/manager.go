// Package jdk 提供节点本地 JDK 探测、下载安装、删除与注册表管理。
//
// 设计目标：
//   - List 扫描 Worker 托管目录 (<serversDir>/jdks) 与可选系统探测路径，
//     通过解析 bin/java -XshowSettings:properties 拿到 major/version；
//   - Install 从 Adoptium Temurin API 下载官方归档并解压；
//   - Remove 删除托管目录。
//
// 所有操作只针对 Worker 本地文件系统；CP 通过 gRPC 触发并把结果写回
// CP 侧 model.NodeJDK 表。
package jdk

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Info Worker 本地探测到的 JDK 信息。
type Info struct {
	Vendor       string `json:"vendor"`
	MajorVersion int    `json:"majorVersion"`
	Version      string `json:"version"`
	Arch         string `json:"arch"`
	Path         string `json:"path"`
	Managed      bool   `json:"managed"`
}

// Manager 维护 Worker 本地 JDK 注册表（基于目录扫描，无持久化文件）。
// 多协程安全：Install/Remove 串行化执行以避免并发解压冲突。
type Manager struct {
	mu         sync.Mutex
	rootDir    string // 托管根目录（默认 <serversDir>/jdks）
	managed    map[string]Info
	systemDirs []string // 可选系统 JDK 探测路径
	// httpClient JDK 归档/元数据下载所用出站 client（经进程级代理，FR-174/ADR-037）。
	// 为 nil 时 download 路径回退一个 15min 超时的默认 client（向后兼容）。
	httpClient *http.Client
	// httpProvider 运行时出站持有者（FR-185/ADR-043）：非 nil 时每次下载取当前 client，
	// 使 CP 经心跳下发的代理改动即时生效。优先于 httpClient。
	httpProvider func() *http.Client
}

// NewManager 创建 JDK 管理器。
// rootDir 是托管目录（Install 写入的目录）。systemDirs 是探测时也会扫描
// 的系统路径，allow nil。
func NewManager(rootDir string, systemDirs []string) *Manager {
	return &Manager{
		rootDir:    rootDir,
		managed:    make(map[string]Info),
		systemDirs: systemDirs,
	}
}

// SetHTTPClient 注入出站 client（经进程级代理，FR-174/ADR-037）：JDK 下载经此 client。
// 由 main 装配；不调用则下载路径回退裸默认 client（向后兼容）。
// 下载不设「总超时」上限，卡死由 stall 看门狗判定（FIX-4）；注入 client 若设了 Timeout 会被克隆去掉。
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

// downloadClient 返回 JDK 下载所用 client：优先运行时持有者（取当前），其次固定注入，否则裸默认 client。
// 下载不设「总超时」上限——大归档慢但在进展不应被拖死；卡死由调用方 stall 看门狗判定（FIX-4）。
// 注入 client 若设了 Timeout 则克隆去掉（不改原 client）。须在持有 m.mu 时调用（Install 内已持锁）。
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

// RootDir 返回托管根目录。
func (m *Manager) RootDir() string { return m.rootDir }

// List 扫描并返回所有 JDK 信息（managed 优先，然后系统）。
func (m *Manager) List() ([]Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[string]bool)
	var out []Info

	// 1) 托管目录
	if err := os.MkdirAll(m.rootDir, 0o755); err == nil {
		entries, err := os.ReadDir(m.rootDir)
		if err != nil {
			return nil, fmt.Errorf("读取托管 JDK 目录失败: %w", err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(m.rootDir, e.Name())
			if seen[dir] {
				continue
			}
			info, ok := detectAt(dir)
			if !ok {
				continue
			}
			info.Managed = true
			seen[dir] = true
			out = append(out, info)
		}
	}

	// 2) 系统目录
	for _, root := range m.systemDirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			dir := filepath.Join(root, e.Name())
			if seen[dir] {
				continue
			}
			info, ok := detectAt(dir)
			if !ok {
				continue
			}
			seen[dir] = true
			out = append(out, info)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].MajorVersion != out[j].MajorVersion {
			return out[i].MajorVersion > out[j].MajorVersion
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// Progress 报告安装进度的回调（FR-183，见 ADR-040）。
// percent 为 0~100 的下载百分比（无 Content-Length 时可能停在某值，由 line 补充阶段说明）；
// line 为可选的人类可读日志行（空表示仅更新百分比）。回调内不得长时间阻塞。
type Progress func(percent int, line string)

// Install 下载并安装指定 JDK（同步，无进度回调）。等价于 InstallWithProgress(... "", nil)。
func (m *Manager) Install(vendor string, major int, arch, installDir, mirrorBase string) (Info, error) {
	return m.InstallWithProgress(context.Background(), vendor, major, "", arch, installDir, mirrorBase, nil)
}

// InstallWithProgress 下载并安装指定 JDK 到 installDir（默认 <rootDir>/<vendor>-<major>），
// 期间经 progress 回调上报下载百分比与阶段日志（FR-183 任务中心，见 ADR-040；progress 可为 nil）。
// vendor/major/arch 必填；version 可选（非空时经 foojay 按具体版本解析，FR-178）；
// mirrorBase 非空时作下载基址（CP 从平台设置下发，使镜像源真生效），为空回退本地 env/默认源。
// 下载完成后自动 detect。
func (m *Manager) InstallWithProgress(ctx context.Context, vendor string, major int, version, arch, installDir, mirrorBase string, progress Progress) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if vendor == "" || major == 0 {
		return Info{}, fmt.Errorf("vendor 与 major_version 必填")
	}
	if arch == "" {
		arch = defaultArch()
	}
	if installDir == "" {
		suffix := fmt.Sprintf("%s-%d", strings.ToLower(vendor), major)
		if strings.TrimSpace(version) != "" {
			suffix = fmt.Sprintf("%s-%s", strings.ToLower(vendor), strings.TrimSpace(version))
		}
		installDir = filepath.Join(m.rootDir, suffix)
	}
	report := func(percent int, line string) {
		if progress != nil {
			progress(percent, line)
		}
	}

	if _, err := os.Stat(installDir); err == nil {
		// 有 bin/java 完成标记 = 完好已装目录，仍拒绝覆盖（语义不变）；
		// 无标记 = 上次安装失败/取消遗留的残骸，自动清除重装，不再堵死重试（FR-291，
		// 真机复现：卡死任务遗留半截目录致同版本重装永远撞「目标目录已存在」）。
		if dirLooksLikeJDK(installDir) {
			return Info{}, fmt.Errorf("目标目录已存在: %s", installDir)
		}
		if err := os.RemoveAll(installDir); err != nil {
			return Info{}, fmt.Errorf("清理上次安装残留目录失败: %w", err)
		}
		slog.Info("检测到上次安装残留目录，已自动清理重装", "dir", installDir)
		report(0, "检测到上次安装残留目录，已自动清理")
	}

	client := m.downloadClient()
	report(0, fmt.Sprintf("解析下载源 %s %d (%s)", vendor, major, arch))
	downloadURL, err := buildDownloadURLV(client, vendor, major, version, arch, mirrorBase)
	if err != nil {
		return Info{}, err
	}
	slog.Info("开始下载 JDK", "vendor", vendor, "major", major, "arch", arch, "url", downloadURL)
	report(0, "开始下载归档")

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return Info{}, fmt.Errorf("创建安装目录失败: %w", err)
	}
	if err := downloadAndExtractWithProgress(ctx, client, downloadURL, installDir, report); err != nil {
		_ = os.RemoveAll(installDir)
		return Info{}, err
	}
	report(100, "解压完成，正在校验")

	// 部分归档外层多包一层目录；detect 时会找到 bin/java。
	info, ok := detectAt(installDir)
	if !ok {
		// 尝试向上找一级
		info, ok = detectAt(filepath.Join(installDir, findFirstSubdir(installDir)))
		if !ok {
			_ = os.RemoveAll(installDir)
			return Info{}, fmt.Errorf("已下载但未找到 bin/java，JDK 可能不完整")
		}
	}
	info.Managed = true
	report(100, fmt.Sprintf("安装完成：%s %s", info.Vendor, info.Version))
	return info, nil
}

// Remove 删除指定路径的托管 JDK。系统 JDK 不允许通过本方法删除。
func (m *Manager) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if path == "" {
		return fmt.Errorf("path 必填")
	}
	// 安全：仅允许删除 rootDir 下的子目录
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析路径失败: %w", err)
	}
	rootAbs, err := filepath.Abs(m.rootDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	// rel="." 即托管根本身：整根删除会清空全部托管 JDK，一并拒绝（FR-292）。
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("只能删除托管目录 (%s) 下的 JDK", m.rootDir)
	}
	if !strings.HasPrefix(abs, rootAbs) {
		return fmt.Errorf("只能删除托管目录 (%s) 下的 JDK", m.rootDir)
	}
	// 归一到托管根下的顶层子目录整体删除（FR-292）：登记路径可能是解压后嵌套的
	// 内层（…/temurin-11/jdk-11.0.31+11），只删内层会留父壳空目录在托管根下占位。
	top := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		top = rel[:i]
	}
	return os.RemoveAll(filepath.Join(rootAbs, top))
}

// detectAt 探测给定目录是否为 JDK：找 bin/java 并运行 -XshowSettings:properties
// 解析 java.version / os.arch / java.vendor。
func detectAt(dir string) (Info, bool) {
	javaBin := filepath.Join(dir, "bin", "java")
	if runtime.GOOS == "windows" {
		javaBin += ".exe"
	}
	if _, err := os.Stat(javaBin); err != nil {
		return Info{}, false
	}
	out, err := exec.Command(javaBin, "-XshowSettings:properties", "-version").CombinedOutput()
	if err != nil {
		// -XshowSettings:properties 在某些 JDK 不会因 -version 退出失败，但兜底再读 stdout
		out2, err2 := exec.Command(javaBin, "-XshowSettings:properties").CombinedOutput()
		if err2 != nil {
			return Info{}, false
		}
		out = out2
	}
	text := string(out)

	vendor := parseProp(text, "java.vendor") // "Eclipse Adoptium" / "Azul Systems" ...
	major := parseMajor(parseProp(text, "java.version"))
	version := parseProp(text, "java.version")
	arch := parseProp(text, "os.arch")

	return Info{
		Vendor:       normalizeVendor(vendor),
		MajorVersion: major,
		Version:      version,
		Arch:         arch,
		Path:         dir,
		Managed:      false,
	}, true
}

// Detect 探测给定目录是否为 JDK（detectAt 的导出薄封装）。
// 供运行时扫描器复用同一探测语义（FR-298，见 internal/worker/runtimescan）。
func Detect(dir string) (Info, bool) { return detectAt(dir) }

// normalizeJDKHome 把 path 归一为 JDK home（FR-228）：若指向 bin/java[.exe] 则取上两级（去掉 bin/java），否则原样。
func normalizeJDKHome(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	switch strings.ToLower(filepath.Base(clean)) {
	case "java", "java.exe":
		return filepath.Dir(filepath.Dir(clean)) // .../bin/java → .../（bin 的父目录）
	}
	return clean
}

// Probe 探测某路径的 JDK 信息（FR-228）：path 可为 JDK home 或 java 可执行文件，归一后 detectAt。
// 找不到 bin/java 或取不到版本时返回 error，供登记前自动填厂商/版本/架构（不再手填）。
func (m *Manager) Probe(path string) (Info, error) {
	if strings.TrimSpace(path) == "" {
		return Info{}, fmt.Errorf("路径为空")
	}
	home := normalizeJDKHome(path)
	info, ok := detectAt(home)
	if !ok {
		return Info{}, fmt.Errorf("该路径不是有效的 JDK：未找到 bin/java 或无法读取版本（%s）", home)
	}
	return info, nil
}

func parseProp(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+" =") || strings.HasPrefix(line, key+"=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// parseMajor 解析 "21.0.4+9" / "17" / "1.8.0_412" 形式。
func parseMajor(v string) int {
	if v == "" {
		return 0
	}
	// Java 8 之前是 1.x
	if strings.HasPrefix(v, "1.") {
		parts := strings.SplitN(v[2:], ".", 2)
		if n, err := strconvAtoi(parts[0]); err == nil {
			return n
		}
		return 0
	}
	parts := strings.SplitN(v, ".", 2)
	if n, err := strconvAtoi(parts[0]); err == nil {
		return n
	}
	return 0
}

func strconvAtoi(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not int: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	return n, nil
}

func normalizeVendor(v string) string {
	switch {
	case strings.Contains(v, "Adoptium"), strings.Contains(v, "Temurin"), strings.Contains(v, "Eclipse"):
		return "Temurin"
	case strings.Contains(v, "Azul"), strings.Contains(v, "Zulu"):
		return "Zulu"
	case strings.Contains(v, "Amazon"), strings.Contains(v, "Corretto"):
		return "Corretto"
	case strings.Contains(v, "Microsoft"), strings.Contains(v, "OpenJDK"):
		return "OpenJDK"
	}
	if v == "" {
		return "Unknown"
	}
	return v
}

func defaultArch() string {
	switch runtime.GOARCH {
	case "amd64", "x86_64", "x64":
		return "x64"
	case "arm64", "aarch64":
		return "aarch64"
	}
	return runtime.GOARCH
}

func findFirstSubdir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	// 优先选包含 bin/java 的那个
	for _, e := range entries {
		if e.IsDir() {
			cand := filepath.Join(dir, e.Name(), "bin", "java")
			if _, err := os.Stat(cand); err == nil {
				return e.Name()
			}
		}
	}
	if entries[0].IsDir() {
		return entries[0].Name()
	}
	return ""
}

// hasJavaBin 报告 dir 下是否有 bin/java[.exe]（安装完成标记，FR-291）。
func hasJavaBin(dir string) bool {
	for _, name := range []string{"java", "java.exe"} {
		if st, err := os.Stat(filepath.Join(dir, "bin", name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// dirLooksLikeJDK 报告 dir 是否像一个完好的已装 JDK：bin/java 在目录本身或
// 一级子目录下（归档外层多包一层的布局，与 detectAt 的探测路径一致）。
// 用于区分「完好已装（拒绝覆盖）」与「失败残骸（自动清除重装）」（FR-291）；
// 只查文件存在、不执行 java，避免安装入口引入进程执行开销。
func dirLooksLikeJDK(dir string) bool {
	if hasJavaBin(dir) {
		return true
	}
	if sub := findFirstSubdir(dir); sub != "" && hasJavaBin(filepath.Join(dir, sub)) {
		return true
	}
	return false
}

// MarshalInfo 把 Info 转成 JSON 字符串，便于注册表持久化与跨进程传递。
func (i Info) Marshal() string {
	b, err := json.Marshal(i)
	if err != nil {
		slog.Error("序列化 JDK 信息失败", "error", err)
		return ""
	}
	return string(b)
}
