package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 按进程名在本机进程列表中查找。
//
// 目标只是回答「这个组件的进程在不在」，不需要完整进程树或命令行参数，
// 因此各平台都用最轻的方式：
//   - Linux：读 /proc/<pid>/cmdline，比对 argv[0] 的文件名（不引入 ps 依赖）
//   - Windows：调 tasklist（系统自带，无需额外权限）
//   - 其它（macOS）：调 ps
//
// 一律返回第一个匹配的 PID。同名多实例时结果不唯一，但 status 只需知道
// 「在跑」与「PID 大约是谁」，不影响判断。

// scanProcessesByName 一次扫描进程表，返回「可执行名 → PID」。
//
// 只扫一次供全部组件复用：Windows 上 tasklist 单次约 0.8 秒，
// 按组件各扫一次会让 status 无谓地慢一倍。
func scanProcessesByName() map[string]int {
	switch {
	case isLinux():
		return scanProcTable()
	case isWindows():
		return scanTasklistTable()
	default:
		return scanPsTable()
	}
}

// scanProcTable 遍历 /proc，比对 cmdline 首项的文件名。
//
// 比 `ps` 更快且不依赖外部命令，但 /proc 只在 Linux 存在。
func scanProcTable() map[string]int {
	out := map[string]int{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	self := os.Getpid()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := atoiSafe(e.Name())
		if pid <= 0 || pid == self {
			continue
		}
		// cmdline 是 NUL 分隔的参数列表；首项即可执行文件路径。
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		first := raw
		if i := strings.IndexByte(string(raw), 0); i >= 0 {
			first = raw[:i]
		}
		name := binaryBase(string(first))
		// 同名多实例只记第一个：status 只需知道「在跑」与代表 PID。
		if _, exists := out[name]; !exists {
			out[name] = pid
		}
	}
	return out
}

// scanTasklistTable 解析 Windows tasklist 输出。
//
// 输出为 CSV：`"映像名称","PID","会话名",...`。中文系统列名不同，
// 故按「第一列映像名、第二列数字」解析，不依赖表头文字。
func scanTasklistTable() map[string]int {
	out := map[string]int{}
	raw, err := runCommand("tasklist", "/FO", "CSV", "/NH")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := splitCSVLine(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(fields[0]), ".exe")
		pid := atoiSafe(fields[1])
		if name == "" || pid <= 0 {
			continue
		}
		if _, exists := out[name]; !exists {
			out[name] = pid
		}
	}
	return out
}

// scanPsTable 解析 `ps -A -o pid=,comm=` 输出（macOS 等）。
func scanPsTable() map[string]int {
	out := map[string]int{}
	raw, err := runCommand("ps", "-A", "-o", "pid=,comm=")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := binaryBase(fields[1])
		pid := atoiSafe(fields[0])
		if name == "" || pid <= 0 {
			continue
		}
		if _, exists := out[name]; !exists {
			out[name] = pid
		}
	}
	return out
}

// binaryBase 取可执行文件名并去 .exe 后缀，用于跨平台比对。
func binaryBase(p string) string {
	// 统一分隔符后再取末段：/proc 里是 '/'，tasklist 是裸名。
	b := p
	if i := strings.LastIndexAny(b, `/\`); i >= 0 {
		b = b[i+1:]
	}
	return strings.TrimSuffix(b, ".exe")
}

// splitCSVLine 按 CSV 规则切分一行（仅处理引号包裹与逗号，够 tasklist 用）。
func splitCSVLine(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

// atoiSafe 解析正整数，失败返回 0。
func atoiSafe(s string) int {
	n := 0
	if s == "" {
		return 0
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// isLinux / isWindows 是编译期常量判断的薄封装，便于阅读与测试替换。
func isLinux() bool   { return runtime.GOOS == "linux" }
func isWindows() bool { return runtime.GOOS == "windows" }

// runCommand 执行外部命令并返回 stdout。
//
// 带超时：status 是应急命令，不能因为某个系统命令挂住而整体卡死。
// 超时取得很短——这些命令正常都在百毫秒内返回，慢说明本机已经不对劲，
// 此时给出「未能确认」也比一直等更有用。
func runCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// probeTimeout 是单个外部探测命令的上限。
const probeTimeout = 2 * time.Second
