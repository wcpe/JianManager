// Package runtimescan 扫描节点常见安装路径发现运行时候选（节点运行时库）。
//
// 设计约束（见 docs/specs/node-runtime-library/spec.md §3.2）：
//   - 路径表按 GOOS 内置（jdk / nodejs 两类），路径不存在或探测失败**静默跳过**，
//     不阻断整体扫描（Windows 权限差异 / 空目录皆常态）；
//   - jdk 探测复用 internal/worker/jdk 的 detectAt 语义（bin/java -XshowSettings）；
//   - nodejs 探测跑 `node --version` + `node -p process.arch`；
//   - 托管根（jdk 托管目录等）下的候选标 AlreadyRegistered（已在库），
//     CP 侧另按 DB 已登记路径补标。
package runtimescan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/jdk"
)

// probeExecTimeout 单次探测外部可执行文件的最长等待时间，避免卡死进程泄漏 Worker 协程。
const probeExecTimeout = 10 * time.Second

// 支持的运行时类型（python 仅 CP 侧预留枚举，无扫描器）。
const (
	TypeJDK    = "jdk"
	TypeNodeJS = "nodejs"
)

// Candidate 一条扫描发现的运行时候选（对齐 workerpb.RuntimeCandidate）。
type Candidate struct {
	Type              string `json:"type"`
	Vendor            string `json:"vendor"` // jdk=归一厂商；nodejs=展示名（Node.js）
	Version           string `json:"version"`
	Major             int    `json:"major"`
	Arch              string `json:"arch"`
	Path              string `json:"path"` // jdk=home 目录；nodejs=node 可执行文件
	AlreadyRegistered bool   `json:"alreadyRegistered"`
}

// Scanner 按内置路径表扫描运行时候选。探测器可注入（测试用伪目录布局时替换真探测）。
type Scanner struct {
	jdkGlobs     []string
	nodeGlobs    []string
	managedRoots []string
	detectJDK    func(dir string) (Candidate, bool)
	probeNode    func(exe string) (Candidate, bool)
	probeNodeCtx func(ctx context.Context, exe string) (Candidate, bool)
}

// New 创建扫描器：路径表按 GOOS 内置，探测器用真实现。
// managedRoots 是 Worker 托管根（如 <数据根>/opt/jdks），其下候选标 AlreadyRegistered。
func New(managedRoots []string) *Scanner {
	return &Scanner{
		jdkGlobs:     defaultJDKGlobs(),
		nodeGlobs:    defaultNodeGlobs(),
		managedRoots: managedRoots,
		detectJDK:    detectJDKAt,
		probeNode:    probeNodeExecutable,
		probeNodeCtx: probeNodeExecutableContext,
	}
}

// AddNodeGlobs 追加 Node 可执行扫描 glob（×胶合）：默认路径表只含系统常见位，
// 托管安装根（<数据根>/opt/runtimes/nodejs-*）由 main 据 dataroot 动态追加——否则一键安装的
// 托管 Node 对扫描发现与 Bot spawn 解析器（NodeResolver）不可见。
func (s *Scanner) AddNodeGlobs(globs ...string) {
	s.nodeGlobs = append(s.nodeGlobs, globs...)
}

// Scan 扫描并返回候选列表。types 过滤类型（空=全部支持类型）；未知类型静默忽略
// （CP 侧负责校验拒绝）。结果按 type、major 降序、path 升序稳定排序，按 path 去重。
func (s *Scanner) Scan(types []string) []Candidate {
	want := map[string]bool{}
	if len(types) == 0 {
		want[TypeJDK], want[TypeNodeJS] = true, true
	} else {
		for _, t := range types {
			want[strings.ToLower(strings.TrimSpace(t))] = true
		}
	}

	seen := map[string]bool{}
	var out []Candidate
	add := func(c Candidate) {
		if seen[c.Path] {
			return
		}
		seen[c.Path] = true
		c.AlreadyRegistered = c.AlreadyRegistered || s.underManagedRoot(c.Path)
		out = append(out, c)
	}

	if want[TypeJDK] {
		for _, dir := range expandGlobs(s.jdkGlobs) {
			st, err := os.Stat(dir)
			if err != nil || !st.IsDir() {
				continue // 不存在/非目录：静默跳过
			}
			if c, ok := s.detectJDK(dir); ok {
				add(c)
			}
		}
	}
	if want[TypeNodeJS] {
		for _, exe := range expandGlobs(s.nodeGlobs) {
			st, err := os.Stat(exe)
			if err != nil || st.IsDir() {
				continue
			}
			if c, ok := s.probeNode(exe); ok {
				add(c)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].Major != out[j].Major {
			return out[i].Major > out[j].Major
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// underManagedRoot 报告 path 是否位于任一托管根之下。
func (s *Scanner) underManagedRoot(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range s.managedRoots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootAbs, abs)
		if err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}

// expandGlobs 展开 glob 列表（glob 语法错误/无匹配静默跳过）。
func expandGlobs(globs []string) []string {
	var out []string
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			continue
		}
		out = append(out, matches...)
	}
	return out
}

// detectJDKAt 真 JDK 探测：复用 jdk 包 detectAt 语义（bin/java -XshowSettings:properties）。
func detectJDKAt(dir string) (Candidate, bool) {
	info, ok := jdk.Detect(dir)
	if !ok {
		return Candidate{}, false
	}
	return Candidate{
		Type:    TypeJDK,
		Vendor:  info.Vendor,
		Version: info.Version,
		Major:   info.MajorVersion,
		Arch:    info.Arch,
		Path:    info.Path,
	}, true
}

// ProbePath 探测**指定路径**上的某个运行时（供 Worker 的 ProbeRuntime RPC 用）。
//
// 与 Scan 的区别：Scan 在硬编码的常见目录里找，本方法只认用户给的那一个路径。
// 用户把运行时装在 /opt/my-runtime 这类位置时 Scan 扫不到，但他自己知道路径——
// 让他指着路径说"就这个"，比让他手填版本号靠谱得多。
//
// path 的形态按类型归一：
//   - nodejs：可以是 node 可执行文件本身，也可以是其所在目录或 ../（本方法按 bin/node、node 依次尝试）；
//   - jdk：可以是 JDK home，也可以是 bin/java 或 java 本身（沿用 jdk.Manager 的归一逻辑）。
//
// 找不到/不是该类型时返回 (Candidate{}, false)，由调用方决定怎么描述错误。
// 这里不返回 error：路径不对是常规输入错误，不是异常。
func (s *Scanner) ProbePath(typ, path string) (Candidate, bool) {
	return s.ProbePathContext(context.Background(), typ, path)
}

// ProbePathContext 带上下文取消与超时控制的指定路径探测。
// 仅接受绝对路径，且仅尝试基名为 node/node.exe 的常规文件，防止执行任意调用方路径。
func (s *Scanner) ProbePathContext(ctx context.Context, typ, path string) (Candidate, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	p := strings.TrimSpace(path)
	if p == "" || !filepath.IsAbs(p) {
		return Candidate{}, false
	}
	clean := filepath.Clean(p)
	probe := s.probeNodeCtx
	if probe == nil {
		probe = func(_ context.Context, exe string) (Candidate, bool) {
			return s.probeNode(exe)
		}
	}
	switch strings.ToLower(typ) {
	case TypeNodeJS:
		for _, exe := range nodeExecutableCandidates(clean) {
			if ctx.Err() != nil {
				return Candidate{}, false
			}
			if c, ok := probe(ctx, exe); ok {
				return c, true
			}
		}
		return Candidate{}, false
	default:
		return Candidate{}, false
	}
}

// nodeExecutableCandidates 把用户给的路径展开成可能的 node 可执行文件路径。
//
// 【安全边界】仅当路径基名本身为 node / node.exe 时才把 p 当成可执行文件尝试；
// 其它路径一律视作目录展开 <dir>/bin/node 与 <dir>/node（及 .exe），绝不把任意
// 非 node 文件（如 /bin/cat 或任意脚本）直接传给 exec.Command。
func nodeExecutableCandidates(p string) []string {
	base := strings.ToLower(filepath.Base(p))
	if base == "node" || base == "node.exe" {
		return []string{p}
	}
	return []string{
		filepath.Join(p, "bin", "node"),
		filepath.Join(p, "node"),
		filepath.Join(p, "bin", "node.exe"),
		filepath.Join(p, "node.exe"),
	}
}

// probeNodeExecutable 真 Node.js 探测：`node --version`（vX.Y.Z）+ `node -p process.arch`。
// 任一步失败视为无效候选（静默跳过）。
func probeNodeExecutable(exe string) (Candidate, bool) {
	return probeNodeExecutableContext(context.Background(), exe)
}

func probeNodeExecutableContext(ctx context.Context, exe string) (Candidate, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := os.Stat(exe)
	if err != nil || !st.Mode().IsRegular() {
		return Candidate{}, false
	}
	execCtx, cancel := context.WithTimeout(ctx, probeExecTimeout)
	defer cancel()
	verCmd := exec.CommandContext(execCtx, exe, "--version")
	verCmd.WaitDelay = 500 * time.Millisecond
	verOut, err := verCmd.Output()
	if err != nil {
		return Candidate{}, false
	}
	version, major, ok := parseNodeVersion(string(verOut))
	if !ok {
		return Candidate{}, false
	}
	arch := ""
	archCmd := exec.CommandContext(execCtx, exe, "-p", "process.arch")
	archCmd.WaitDelay = 500 * time.Millisecond
	if archOut, err := archCmd.Output(); err == nil {
		arch = strings.TrimSpace(string(archOut)) // x64 / arm64（nodejs 命名，勿与 adoptium aarch64 混归一）
	}
	return Candidate{
		Type:    TypeNodeJS,
		Vendor:  "Node.js",
		Version: version,
		Major:   major,
		Arch:    arch,
		Path:    exe,
	}, true
}

// parseNodeVersion 解析 `node --version` 输出（"v22.17.0"）为 (version, major)。
func parseNodeVersion(raw string) (string, int, bool) {
	v := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if v == "" {
		return "", 0, false
	}
	head := strings.SplitN(v, ".", 2)[0]
	major := 0
	for _, c := range head {
		if c < '0' || c > '9' {
			return "", 0, false
		}
		major = major*10 + int(c-'0')
	}
	if major == 0 {
		return "", 0, false
	}
	return v, major, true
}

// defaultJDKGlobs 按 GOOS 返回 JDK 常见安装路径 glob 表（spec §3.2）。
func defaultJDKGlobs() []string {
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return []string{
			filepath.Join(pf, "Java", "*"),
			filepath.Join(pf, "Eclipse Adoptium", "*"),
			filepath.Join(pf, "Microsoft", "jdk*"),
		}
	}
	globs := []string{
		"/usr/lib/jvm/*",
		"/opt/java*",
		"/opt/jdk*",
	}
	if home, err := os.UserHomeDir(); err == nil {
		globs = append(globs, filepath.Join(home, ".sdkman", "candidates", "java", "*"))
	}
	return globs
}

// defaultNodeGlobs 按 GOOS 返回 Node.js 常见安装路径 glob 表（spec §3.2）。
func defaultNodeGlobs() []string {
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		globs := []string{filepath.Join(pf, "nodejs", "node.exe")}
		if appData := os.Getenv("APPDATA"); appData != "" {
			globs = append(globs, filepath.Join(appData, "nvm", "v*", "node.exe"))
		}
		return globs
	}
	globs := []string{
		"/usr/local/bin/node",
		"/usr/bin/node",
		"/opt/node*/bin/node",
	}
	if home, err := os.UserHomeDir(); err == nil {
		globs = append(globs, filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "node"))
	}
	return globs
}
