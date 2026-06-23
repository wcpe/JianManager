package decompiler

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 反编译安全上限（ADR-018 决策 4）。
const (
	// DefaultTimeout 是单次反编译的默认超时。超时 kill 子进程并降级。
	DefaultTimeout = 30 * time.Second
	// MaxOutputBytes 是捕获 CFR stdout 源码的上限，超出截断。
	MaxOutputBytes = 4 * 1024 * 1024
)

// Result 是一次反编译的结果。
type Result struct {
	// Source 是反编译出的 Java 源码（截断到 MaxOutputBytes）。
	Source string
	// Truncated 表示源码因超上限被截断。
	Truncated bool
	// Decompiler 是反编译器标识（如 "CFR 0.152"）。
	Decompiler string
}

// Options 是一次反编译调用的输入。
type Options struct {
	// JavaBin 是用于运行 CFR 的 java 可执行文件路径（实例绑定 JDK 优先，否则系统 JDK）。
	JavaBin string
	// CFRJar 是已解析的 CFR jar 路径。
	CFRJar string
	// Target 是反编译目标的绝对路径（工作目录内 .class 或 .jar，或从 jar 抽出的临时 .class）。
	Target string
	// JarEntry 非空时表示只反编译 jar 内某 class 条目（传给 CFR 作为「仅该类」过滤）。
	// 当 Target 本身是单个 .class（已从 jar 抽出）时留空。
	JarEntry string
	// Timeout 覆盖默认超时（<=0 用 DefaultTimeout）。
	Timeout time.Duration
}

// Run 受控调起 CFR 反编译 opts.Target，返回 Java 源码。
// CFR 仅静态分析字节码并把源码输出到 stdout：不加载/运行目标代码、不写工作目录、不联网。
// 超时 / CFR 非 0 退出 / 无输出一律返回错误，由调用方降级为结构化失败。
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.JavaBin == "" {
		return nil, fmt.Errorf("无可用 JDK，反编译降级")
	}
	if opts.CFRJar == "" {
		return nil, fmt.Errorf("无可用 CFR 反编译器")
	}
	if opts.Target == "" {
		return nil, fmt.Errorf("未指定反编译目标")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := cfrArgs(opts.CFRJar, opts.Target, opts.JarEntry)
	cmd := exec.CommandContext(runCtx, opts.JavaBin, args...)
	// 反编译不依赖工作目录，置于 CFR jar 所在目录，避免误用实例工作目录。
	cmd.Dir = filepath.Dir(opts.CFRJar)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("反编译超时（>%s）", timeout)
	}
	if err != nil {
		// CFR 即便部分失败也可能输出内容；但退出非 0 视为失败，回传 stderr 摘要。
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("CFR 反编译失败: %s", msg)
	}

	out := stdout.Bytes()
	truncated := false
	if len(out) > MaxOutputBytes {
		out = out[:MaxOutputBytes]
		truncated = true
	}
	source := string(out)
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("CFR 未产出源码（目标可能非有效 class/jar）")
	}

	return &Result{
		Source:     source,
		Truncated:  truncated,
		Decompiler: "CFR " + CFRVersion,
	}, nil
}

// cfrArgs 组装 CFR 命令行参数（只读、输出到 stdout、关闭联网/反混淆噪声）。
//   - 输出走 stdout（不传 --outputdir，避免写盘）。
//   - --silent true 抑制进度噪声到 stderr。
//   - jarEntry 非空：用 --jarfilter 仅反编译该类（CFR 接受类名/路径前缀正则）。
func cfrArgs(cfrJar, target, jarEntry string) []string {
	args := []string{"-jar", cfrJar, target, "--silent", "true"}
	if jarEntry != "" {
		// 把 jar 内条目路径转为类名前缀（去 .class、正斜杠转点）作为过滤，
		// 限定 CFR 仅输出该类，避免整 jar 全量反编译。
		filter := strings.TrimSuffix(filepath.ToSlash(jarEntry), ".class")
		args = append(args, "--jarfilter", filter)
	}
	return args
}

// ResolveJavaBin 解析用于运行 CFR 的 java 可执行文件路径。
// 优先级：实例绑定 JDK 的 bin（jdkBinPath 或 jdkPath/bin）> 系统候选（systemCandidates）> PATH 上的 java。
// 返回空串表示无可用 JDK（调用方据此降级）。
func ResolveJavaBin(jdkPath, jdkBinPath string, systemCandidates []string) string {
	exeName := "java"
	if runtime.GOOS == "windows" {
		exeName = "java.exe"
	}

	// 实例显式 bin 目录。
	if jdkBinPath != "" {
		cand := filepath.Join(jdkBinPath, exeName)
		if fileExists(cand) {
			return cand
		}
	}
	// 实例 JDK 根的 bin。
	if jdkPath != "" {
		cand := filepath.Join(jdkPath, "bin", exeName)
		if fileExists(cand) {
			return cand
		}
	}
	// 系统候选 JDK（如 jdkMgr 探测到的各 JDK 路径）。
	for _, root := range systemCandidates {
		if root == "" {
			continue
		}
		cand := filepath.Join(root, "bin", exeName)
		if fileExists(cand) {
			return cand
		}
	}
	// PATH 上的 java（最后兜底）。
	if p, err := exec.LookPath(exeName); err == nil {
		return p
	}
	return ""
}
