package process

import (
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	psproc "github.com/shirou/gopsutil/v4/process"
)

// readProcessEnviron 读取进程环境块（Linux: /proc/<pid>/environ），NUL 分隔转空格便于 contains。
// 平台不支持（如 Windows/macOS 无 /proc）或权限不足时返回错误，调用方保守处理。
func readProcessEnviron(pid int) (string, error) {
	if pid <= 0 {
		return "", os.ErrInvalid
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(b), "\x00", " "), nil
}

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
//   - expectWrapper 时，进程命令行形如 jianmanager worker 的 `... daemon`，**且**该进程环境
//     （/proc/<pid>/environ）确携带本实例 UUID（FR-456 F11）。
//
// F11：wrapper 由 `exec.Command(exe, "daemon")` 启动，实例 UUID 只在 env
// （daemon.EnvWrapperConfig → JM_DAEMON_WRAPPER_CONFIG JSON）而不在 argv——argv 对**任何**实例
// 的 wrapper 都相同。若仅按 argv「... daemon」判真，则节点上所有 wrapper 都会被判为「本实例」，
// 一旦 PID 被复用/串号即误杀他人 wrapper。故 wrapper 分支必须再校验进程环境的实例 UUID，
// 单靠 argv 不再单独判真；环境读不到（权限/平台不支持）时保守返回 false（不杀）。
//
// 匹配口径的误判率需真机标定（spec §5）：过宽会放过真孤儿，过窄会误拦截。
//
// **不要**把本函数用于自动收养：收养有 cgroup 迁移与控制权副作用，只能按强特征判真，见
// DefaultVerifyAdoptOwnership。本函数的宽口径是强杀路径的刻意取舍（放过真孤儿只多等一轮）。
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
	if expectWrapper && looksLikeJianManagerDaemon(cmdline) && processEnvMatchesInstance(pid, instanceUUID) {
		return true
	}
	return false
}

// DefaultVerifyAdoptOwnership 是「自动收养」路径专用的归属复核（FR-497③）。
//
// 为什么不能直接复用 DefaultVerifyProcessOwnership（缺陷修复 2026-11-03）：
// 那个函数服务于**强杀前**的复核，采「多特征任一命中」的宽口径——那里放过一个真孤儿只是多等一轮，
// 代价可接受。收养却有两个不可忽略的副作用：① `isolateAdoptedWrapper` 会把该 wrapper 子树迁进
// 实例 cgroup（此后停 cgroup 即连带终止）；② 实例获得该 PID 的控制权（后续 Stop / Kill 会向它发
// 信号）。因此收养只接受**强特征**，弱特征单独不足以判真：
//
//   - 强特征（二选一即可）：实例 UUID 出现在进程 cmdline；或出现在进程环境（/proc/<pid>/environ）。
//     环境之所以是强特征，是因为 UUID 只经 `JM_DAEMON_WRAPPER_CONFIG` 注入：wrapper 由 Worker 以
//     `cmd.Env = append(os.Environ(), …)` 拉起，Java 子进程再由 wrapper 经 `composeEnv(os.Environ(), cfg)`
//     继承（internal/worker/daemon/wrapper.go），故两者都携带且只携带本实例的 UUID。
//   - 弱特征（单独不判真）：cmdline 含工作目录绝对路径、进程 cwd 等于工作目录。
//
// 弱特征必须被降级的直接理由：PID 被 OS 复用后，一个 cwd **恰好**落在该实例 work_dir 下的无关进程
// （典型：管理员在同一目录里手工拉起的脚本、备份进程、tail 之类）即可凭 cwd 单特征通过宽口径复核，
// 进而被误收养、被迁进实例 cgroup，并在其后的 Stop / Kill 中被误杀——这正是本函数要闭合的误收养风险。
//
// wrapper 分支额外要求 argv 形如 `<worker 二进制> daemon`，与 ADR-093 决策 1 及
// docs/specs/auto-adopt-orphans/spec.md §2.1 的口径一致（wrapper 必须「argv 形如 daemon **且** 环境
// 携带实例 UUID」）。真实 wrapper 的 argv 只有 `<worker> daemon`、UUID 仅在 env，故该分支事实上只有
// 「argv 为 daemon + 环境携带 UUID」一条通过路径，单凭 argv 不判真（argv 对任何实例的 wrapper 都相同）。
//
// 平台口径：环境依赖 /proc，Windows/macOS 读不到 → 强特征不成立 → fail-closed（不收养，仍只告警）。
// 与既有强杀路径同一取向：**宁可不收养，绝不误纳管**。
//
// 第二个返回值是复核口径（通过或拦截的具体原因），落进审计 detail 供运维核查，避免「为什么没收养」
// 变成不可解释的黑箱。
func DefaultVerifyAdoptOwnership(pid int, instanceUUID, workDir string, expectWrapper bool) (bool, string) {
	if pid <= 0 {
		return false, "invalid_pid"
	}
	if instanceUUID == "" {
		// 没有实例 UUID 就没有强特征可比对：无强特征即不判真，杜绝「按目录猜归属」。
		return false, "missing_instance_uuid"
	}
	proc, err := psproc.NewProcess(int32(pid))
	if err != nil {
		// 进程不存在 / 无权限读取 → 无法确认归属 → 不通过（不收养）。
		return false, "process_unreadable"
	}
	cmdline, err := proc.Cmdline()
	if err != nil {
		return false, "cmdline_unreadable"
	}
	envHasUUID := processEnvMatchesInstance(pid, instanceUUID)

	if expectWrapper {
		if !looksLikeJianManagerDaemon(cmdline) {
			return false, "wrapper_argv_mismatch"
		}
		if !envHasUUID {
			// argv 形如 daemon 但环境无本实例 UUID：argv 对所有 wrapper 都相同，不能据此判真。
			return false, "env_uuid_missing"
		}
		return true, "daemon_argv+env_uuid"
	}

	if envHasUUID || strings.Contains(cmdline, instanceUUID) {
		return true, "uuid_evidence"
	}
	// 仅剩弱特征（cmdline 含工作目录 / cwd 等于工作目录）或毫无特征：一律不判真。
	if workDir != "" {
		if strings.Contains(cmdline, workDir) {
			return false, "weak_evidence_only"
		}
		if cwd, cwdErr := proc.Cwd(); cwdErr == nil && samePath(cwd, workDir) {
			return false, "weak_evidence_only"
		}
	}
	return false, "uuid_evidence_missing"
}

// processEnvMatchesInstance 校验 pid 的进程环境是否携带该实例 UUID（FR-456 F11）。
//
// wrapper 把实例 UUID 放在 env（JM_DAEMON_WRAPPER_CONFIG JSON 内），argv 不含。读
// /proc/<pid>/environ 做精确归属确认；instanceUUID 为空（无从比对）或环境读不到一律返回 false
// （保守：无法确认即不判属，杜绝按 argv 空判误杀）。
func processEnvMatchesInstance(pid int, instanceUUID string) bool {
	if instanceUUID == "" {
		return false
	}
	environ, err := readProcessEnviron(pid)
	if err != nil || environ == "" {
		return false
	}
	return strings.Contains(environ, instanceUUID)
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
