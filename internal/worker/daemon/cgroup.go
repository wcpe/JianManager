package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// 本文件实现「游离 daemon 子树脱离 Worker systemd unit cgroup」的路径推导与环境开关。
//
// # 为什么需要（2026-10-01 生产事故根因）
//
// 四层模型 CP → Worker → wrapper(setsid 游离) → sh -c → java 承诺「Worker/CP 重启不牵连服务器」
// （ADR-003、ADR-093）。`setsid` 让 wrapper 逃脱 Worker 的**进程组**，但逃不出 systemd 的
// **cgroup**：`systemctl restart <worker 单元>` 时 systemd 按 KillMode（默认 `control-group`，
// KillSignal=15）向该单元 cgroup 内的**全部**进程发 SIGTERM。真机复现（本机 jm-prod-worker.service）：
// Worker 与其 11 个 wrapper、11 个 java 同处 `/user.slice/…/app.slice/jm-prod-worker.service`，
// 重启后 Worker 日志 11×`wrapper 进程退出 err="signal: terminated"`，且 11 个实例的 Java **自身日志**
// 在同一秒打出 `Stopping server`（Paper 的 SIGTERM 钩子）——即 Java 是被 cgroup 级 SIGTERM 直接打中，
// 不是被 wrapper 死亡连累。Worker 代码侧的关闭路径当时是**正确**的：`manager.StopAll()` 对 daemon 实例
// 只下发 `strategy.Close()`（`internal/worker/process/manager.go` 的 daemon 分支，日志
// `daemon 实例已断开连接（wrapper 继续运行）`），全程没有任何杀 wrapper 的调用。
//
// 部署层已有对策（`scripts/install-worker.sh` 的 `KillMode=process`，FR-341 的 K2），但它是
// **部署期**属性：单元漂移（手写单元、旧版本单元、非 systemd 托管）一旦缺该项，运行期零保障，
// 每次重启就赔上全部服务器。本文件把该承诺下沉到**运行期**：让 wrapper 把自身（以及由它 fork 的
// java 子树）迁到 Worker 单元 cgroup **之外**的同级目录，于是「重启单元」在 cgroup 维度也碰不到它们，
// 与 `setsid` 在进程组维度的语义对齐。
//
// # 迁到哪、为什么不越权
//
// 目标 = Worker 单元 cgroup 的同级目录 `jianmanager-daemons/<实例 UUID>`，即：
//
//	/user.slice/user-1000.slice/user@1000.service/app.slice/jm-prod-worker.service   ← Worker 单元
//	/user.slice/user-1000.slice/user@1000.service/app.slice/jianmanager-daemons/<uuid> ← daemon 子树
//
// 选同级而非更外层，是为了不越权：① 仍在同一 systemd 用户会话子树内，登出/`stop user@1000.service`
// 依旧能按 KillMode 回收（会话语义不变）；② 内核要求「迁移进程者对源与目标的**共同祖先** cgroup
// 目录有写权限」，同级目录的共同祖先就是 `app.slice`（属该用户，可写），越外层（如直接放
// `/sys/fs/cgroup/` 根）反而会被拒绝。
//
// # 保守边界
//
//   - 只在自身 cgroup **确为 systemd service 单元**（末段形如 `*.service`）时生效：手工前台运行
//     （session/scope）、容器内（cgroup 根或非 systemd 层级）一律不动，避免在无 systemd 的
//     环境里做无意义甚至有害的迁移。
//   - 任何一步失败（无权限、cgroup v1 无 `0::` 行、只读 cgroupfs）都只告警并**保持原状**，
//     行为与修复前完全一致，不引入新的失败面。
//   - 显式停止/删除实例的语义不变：那些路径按进程组（`Setpgid`/负 pgid）与 PID 终止
//     （`daemon.KillPIDTree`、`process.killProcessTree`），与 cgroup 归属无关，故迁出后照样能停。
const (
	// daemonCgroupDirName 是 Worker 单元 cgroup 同级下承载游离 daemon 子树的目录名。
	// 刻意不用 `.slice`/`.scope` 后缀：那会被 systemd 当成单元名去解析，而本目录不由 systemd 创建。
	daemonCgroupDirName = "jianmanager-daemons"

	// EnvCgroupIsolation 是运行期逃生开关：置为 0/false/no/off（大小写不敏感）即关闭
	// 「daemon 子树脱离 Worker 单元 cgroup」。默认开启（未设置=开启）。
	// 该环境变量由 Worker 原样传给 wrapper 子进程（`cmd.Env = append(os.Environ(), …)`），
	// 故一处设置对 Worker 与全部 wrapper 同时生效。
	EnvCgroupIsolation = "JM_DAEMON_CGROUP_ISOLATION"
)

// cgroupMountRoot 是 cgroup2 统一挂载点。本文件中的路径推导一律产出**cgroup 相对路径**
// （形如 `/user.slice/…/xxx.service`，与 `/proc/self/cgroup` 同口径），由平台实现文件在触达
// 文件系统时加上本前缀。测试可注入假挂载点，从而在不触碰真实 cgroup 层级的前提下验证写路径。
var cgroupMountRoot = "/sys/fs/cgroup"

// cgroupFSPath 把 cgroup 相对路径换算为文件系统绝对路径。
func cgroupFSPath(cgroupRelPath string) string {
	return filepath.Join(cgroupMountRoot, cgroupRelPath)
}

// ownCgroupReader 读取本进程在 cgroup2 中的路径（形如 `/user.slice/…/xxx.service`）。
// 独立成变量便于测试注入假数据，避免测试依赖运行环境的 cgroup 布局。
// 默认实现见平台文件（unix 读 /proc/self/cgroup；windows 无 cgroup2 语义，恒失败）。
var ownCgroupReader = readOwnCgroupPath

// workerUnitKillModeReader 查询「本进程所在 systemd 单元的生效 KillMode」。
// 独立成变量便于测试注入假读数（真实实现需调 systemctl，测试不应依赖宿主 systemd）。
// 默认实现见平台文件（unix 调 systemctl；windows 无该问题面，恒失败）。
var workerUnitKillModeReader = queryWorkerUnitKillMode

// CgroupIsolationEnabled 报告是否启用 daemon 子树 cgroup 隔离（默认启用）。
func CgroupIsolationEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvCgroupIsolation))) {
	case "0", "false", "no", "off", "disable", "disabled":
		return false
	default:
		return true
	}
}

// DaemonCgroupRoot 由 ownCgroup（本进程 cgroup2 路径）推导游离 daemon 子树的**根目录**（cgroup 相对路径）。
//
// 返回 ok=false 表示「不该迁移 / 无从推导」，调用方必须保持原状：
//   - 空路径或非绝对路径（解析异常）；
//   - 路径不在 cgroup 根之下（`/` 本身）；
//   - 末段不是 `*.service`（说明本进程不在 systemd service 单元内：前台运行、scope/session、
//     容器或 cgroup v1，此时不存在「重启单元连坐」的问题面）；
//   - 末段已是本目录的成员（形如 `…/jianmanager-daemons/<uuid>`，重复迁移无意义）。
//
// 纯函数：不读系统、不写系统，便于穷举边界做单测。
func DaemonCgroupRoot(ownCgroup string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(ownCgroup))
	if clean == "" || !strings.HasPrefix(clean, "/") || clean == "/" {
		return "", false
	}
	unit := filepath.Base(clean)
	if !strings.HasSuffix(unit, ".service") {
		return "", false
	}
	parent := filepath.Dir(clean)
	if parent == "/" || parent == "." {
		return "", false
	}
	if filepath.Base(parent) == daemonCgroupDirName {
		// 自身已在隔离目录内（例如 wrapper 重启后再判断）：无需再迁。
		return "", false
	}
	return filepath.Join(parent, daemonCgroupDirName), true
}

// WorkerUnitNameFromCgroup 由自身 cgroup 路径反推**承载本进程的 systemd 单元名**（如 `jm-prod-worker.service`）。
//
// 为什么需要反推而不是让调用方自己填单元名：启动自检要查该单元的 KillMode，而「查一个不存在的单元」
// 在 systemd 下**不报错**——`systemctl show <不存在>.service -p KillMode` 会返回默认值 `control-group`
// 且退出码 0（实测），于是「单元名猜错」与「单元真无保护」无法区分，自检会静默误报。由 cgroup 路径
// 取得真实单元名可消除该歧义（单元确实存在，因为本进程就在其中）。
//
// 返回 ok=false 表示本进程不在 systemd service 单元内（前台运行、scope/session、容器、cgroup v1），
// 此时不存在「重启单元连坐」的问题面，调用方应静默跳过。
func WorkerUnitNameFromCgroup(ownCgroup string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(ownCgroup))
	if clean == "" || !strings.HasPrefix(clean, "/") || clean == "/" {
		return "", false
	}
	// 已迁入隔离目录时自身是 `…/jianmanager-daemons/<uuid>`，末段不是单元名；
	// 该形态下 Worker 单元仍是隔了一层的父级，但自检只需在启动期跑一次（此时尚未迁移），
	// 故这里保守返回 false，避免把实例 UUID 误当单元名去查。
	if filepath.Base(filepath.Dir(clean)) == daemonCgroupDirName {
		return "", false
	}
	unit := filepath.Base(clean)
	if !strings.HasSuffix(unit, ".service") {
		return "", false
	}
	return unit, true
}

// KillModeProtectsDaemonSubtree 报告该 KillMode 取值能否让 daemon 子树活过「重启/停止该单元」。
//
// systemd 语义（`man systemd.kill`）：
//   - `process` / `none`：只对主进程发信号（或完全不发）⇒ wrapper 与 java 存活，**有保护**；
//   - `control-group`（默认值）：向单元 cgroup 内**全部**进程发 KillSignal ⇒ 子树被杀；
//   - `mixed`：先对主进程发 KillSignal，再对其余进程发 SIGKILL ⇒ 子树被杀。
//
// 未知取值按**最危险**处理（返回 false）：告警多报一句无害，漏报则会让「重启赔上全部服务器」
// 这一后果在毫无提示的情况下发生。空串同理。
func KillModeProtectsDaemonSubtree(killMode string) bool {
	switch strings.ToLower(strings.TrimSpace(killMode)) {
	case "process", "none":
		return true
	default:
		return false
	}
}

// DaemonCgroupDirFor 在 DaemonCgroupRoot 之上拼出该实例的隔离叶目录。
// ownCgroup 允许是「Worker 单元 cgroup」或「已隔离实例自身的 cgroup」两种形态：
// 后者（形如 `…/jianmanager-daemons/<uuid>`）直接返回自身，便于 wrapper 退出时清理自己的目录。
func DaemonCgroupDirFor(ownCgroup, instanceUUID string) (string, bool) {
	if instanceUUID == "" {
		return "", false
	}
	clean := filepath.Clean(strings.TrimSpace(ownCgroup))
	if clean == "" || !strings.HasPrefix(clean, "/") || clean == "/" {
		return "", false
	}
	if filepath.Base(clean) == instanceUUID && filepath.Base(filepath.Dir(clean)) == daemonCgroupDirName {
		return clean, true
	}
	root, ok := DaemonCgroupRoot(clean)
	if !ok {
		return "", false
	}
	return filepath.Join(root, instanceUUID), true
}
