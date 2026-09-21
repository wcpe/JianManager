package process

import (
	"path"
	"path/filepath"
	"strings"

	psproc "github.com/shirou/gopsutil/v4/process"
)

// DefaultVerifyProcessOwnership 以进程侧证据判定 pid 是否确属 instanceUUID 对应的受管进程（FR-455①）。
//
// 处置前置存活复核的核心：任何强杀（killTree）之前都必须先确认这个 PID「确属目标实例」，
// 否则若 PID 已被 OS 复用给无关进程（机器负载高时真实存在），就会误杀无关进程。
// 与「是否还活着」（daemon.IsPIDAlive）正交：本函数回答的是「是否确属目标实例」。
//
// 保守取向（「宁可多等一轮，不可误杀」）：任何无法确认（进程不在、cmdline 读不到、权限不足）
// 一律返回 false，调用方据此只告警不处置。判定采用「多特征任一命中」的宽口径，兼顾 wrapper 与 Java：
//   - cmdline 含实例 UUID（部分部署把 UUID 写进启动参数）；
//   - cmdline 含实例工作目录绝对路径，或进程 cwd 等于工作目录（Java 常以工作目录为 cwd）；
//   - expectWrapper 时，cmdline 形如 jianmanager worker 的 `... daemon`（wrapper 标识）。
//
// 匹配口径的误判率需真机标定（spec §5）：过宽会放过真孤儿，过窄会误拦截。
func DefaultVerifyProcessOwnership(pid int, instanceUUID, workDir string, expectWrapper bool) bool {
	if pid <= 0 {
		return false
	}
	proc, err := psproc.NewProcess(int32(pid))
	if err != nil {
		// 进程不存在 / 无权限读取 → 无法确认归属 → 不通过（不杀）。
		return false
	}
	cmdline, err := proc.Cmdline()
	if err != nil {
		return false
	}
	if instanceUUID != "" && strings.Contains(cmdline, instanceUUID) {
		return true
	}
	if workDir != "" {
		if strings.Contains(cmdline, workDir) {
			return true
		}
		if cwd, cwdErr := proc.Cwd(); cwdErr == nil && samePath(cwd, workDir) {
			return true
		}
	}
	if expectWrapper && looksLikeJianManagerDaemon(cmdline) {
		return true
	}
	return false
}

// looksLikeJianManagerDaemon 判定 cmdline 是否为 JianManager worker 的 daemon 子命令（wrapper 标识）。
// wrapper 由 daemonStrategy 以当前 worker 二进制 spawn，argv = `<worker-exe> daemon`。
func looksLikeJianManagerDaemon(cmdline string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) < 2 {
		return false
	}
	// 同时兼容 POSIX（/）与 Windows（\）分隔符：Linux 上 filepath.Base 不切分反斜杠。
	exe := strings.ReplaceAll(fields[0], "\\", "/")
	base := strings.ToLower(path.Base(exe))
	switch base {
	case "worker", "worker.exe", "jianmanager", "jianmanager.exe":
	default:
		if !strings.Contains(base, "jianmanager") {
			return false
		}
	}
	return fields[1] == "daemon"
}

// samePath 判定两个路径是否指向同一位置（容忍相对路径与符号链接，如 macOS /tmp → /private/tmp）。
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, ea := filepath.Abs(a)
	cb, eb := filepath.Abs(b)
	if ea != nil || eb != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	if ca == cb {
		return true
	}
	ra, raErr := filepath.EvalSymlinks(ca)
	rb, rbErr := filepath.EvalSymlinks(cb)
	return raErr == nil && rbErr == nil && ra == rb
}
