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
	"encoding/json"
	"fmt"
	"log/slog"
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
	mu        sync.Mutex
	rootDir   string // 托管根目录（默认 <serversDir>/jdks）
	managed   map[string]Info
	systemDirs []string // 可选系统 JDK 探测路径
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
		entries, _ := os.ReadDir(m.rootDir)
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
		entries, _ := os.ReadDir(root)
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

// Install 下载并安装指定 Temurin JDK 到 installDir（默认 <rootDir>/<vendor>-<major>）。
// vendor/major/arch 必填；mirrorBase 非空时作下载基址（CP 从平台设置下发，使镜像源真生效），
// 为空回退本地 env/默认源。下载完成后自动 detect。
func (m *Manager) Install(vendor string, major int, arch, installDir, mirrorBase string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if vendor == "" || major == 0 {
		return Info{}, fmt.Errorf("vendor 与 major_version 必填")
	}
	if arch == "" {
		arch = defaultArch()
	}
	if installDir == "" {
		installDir = filepath.Join(m.rootDir, fmt.Sprintf("%s-%d", strings.ToLower(vendor), major))
	}
	if _, err := os.Stat(installDir); err == nil {
		return Info{}, fmt.Errorf("目标目录已存在: %s", installDir)
	}

	downloadURL, err := buildDownloadURL(vendor, major, arch, mirrorBase)
	if err != nil {
		return Info{}, err
	}
	slog.Info("开始下载 JDK", "vendor", vendor, "major", major, "arch", arch, "url", downloadURL)

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return Info{}, fmt.Errorf("创建安装目录失败: %w", err)
	}
	if err := downloadAndExtract(downloadURL, installDir); err != nil {
		_ = os.RemoveAll(installDir)
		return Info{}, err
	}

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
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("只能删除托管目录 (%s) 下的 JDK", m.rootDir)
	}
	if !strings.HasPrefix(abs, rootAbs) {
		return fmt.Errorf("只能删除托管目录 (%s) 下的 JDK", m.rootDir)
	}
	return os.RemoveAll(abs)
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

// MarshalInfo 把 Info 转成 JSON 字符串，便于注册表持久化与跨进程传递。
func (i Info) Marshal() string {
	b, _ := json.Marshal(i)
	return string(b)
}
