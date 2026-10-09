//go:build !windows

package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxCgroupDescendantScan 限制单次后代枚举的 PID 上限（防止 /proc 异常膨胀时无界循环）。
const maxCgroupDescendantScan = 4096

// readOwnCgroupPath 读取本进程在 cgroup2 统一层级中的路径（`/proc/self/cgroup` 的 `0::<路径>` 行）。
// cgroup v1 没有该行，返回错误——调用方据此保守放弃迁移（v1 上 systemd 的单元终止同样按 cgroup，
// 但路径语义与推导规则不同，宁可不做也不做错）。
func readOwnCgroupPath() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("读取 /proc/self/cgroup 失败: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("未找到 cgroup2 层级行（0::），疑为 cgroup v1")
}

// EscapeWorkerUnitCgroup 把**调用进程自身**迁出 Worker 的 systemd 单元 cgroup，返回迁入的目录
// （未迁移时为 ""）。
//
// 必须在 fork 游戏服进程**之前**调用：子进程在 fork 时继承父进程当时的 cgroup，先迁后生才能让
// java 及其 sh -c 包裹一并落在隔离目录内、一起免疫单元级 SIGTERM。
//
// 不返回错误即视为「无需迁移」（非 service 单元、隔离被开关关闭等）；返回错误表示尝试过但失败，
// 调用方只告警、保持原状。
func EscapeWorkerUnitCgroup(instanceUUID string) (string, error) {
	if !CgroupIsolationEnabled() {
		slog.Debug("daemon 子树 cgroup 隔离已由环境开关关闭", "env", EnvCgroupIsolation)
		return "", nil
	}
	own, err := ownCgroupReader()
	if err != nil {
		return "", err
	}
	target, ok := DaemonCgroupDirFor(own, instanceUUID)
	if !ok {
		// 常见形态：手工前台运行 / 容器内 / cgroup v1——此时不存在「重启单元连坐」的问题面。
		slog.Debug("当前 cgroup 非 systemd service 单元，跳过 daemon 子树隔离", "ownCgroup", own)
		return "", nil
	}
	dir := cgroupFSPath(target)
	if err := ensureCgroupDir(dir); err != nil {
		return "", err
	}
	if err := movePIDToCgroup(os.Getpid(), dir); err != nil {
		return "", err
	}
	return dir, nil
}

// MigratePIDTreeToCgroup 把 rootPID 及其全部后代迁入该实例的隔离目录，返回成功迁移的进程数。
// 供 Worker 接管**已在运行**的 daemon wrapper 时使用：那些 wrapper 由旧二进制 spawn，仍留在
// Worker 单元 cgroup 内，若不迁移，下一次重启依旧会连坐杀掉它们（部署首个重启即验收场景）。
func MigratePIDTreeToCgroup(rootPID int, instanceUUID string) (int, error) {
	if rootPID <= 0 {
		return 0, nil
	}
	if !CgroupIsolationEnabled() {
		return 0, nil
	}
	own, err := ownCgroupReader()
	if err != nil {
		return 0, err
	}
	target, ok := DaemonCgroupDirFor(own, instanceUUID)
	if !ok {
		return 0, nil
	}
	dir := cgroupFSPath(target)
	if err := ensureCgroupDir(dir); err != nil {
		return 0, err
	}
	moved := 0
	for _, pid := range append([]int{rootPID}, descendantsOf(rootPID)...) {
		if pid <= 0 || !IsPIDAlive(pid) {
			continue
		}
		if err := movePIDToCgroup(pid, dir); err != nil {
			// 单个进程迁移失败（已退出/权限）不阻断其余：wrapper 或 java 至少一侧迁成功仍有收益，
			// 且失败只告警——绝不因此改动进程状态。
			slog.Warn("迁移进程至 daemon 隔离 cgroup 失败",
				"instanceId", instanceUUID, "pid", pid, "cgroup", dir, "error", err)
			continue
		}
		moved++
	}
	return moved, nil
}

// ensureCgroupDir 幂等创建 cgroup 目录（cgroupfs 上 mkdir 即建 cgroup）。
func ensureCgroupDir(dir string) error {
	if st, err := os.Stat(dir); err == nil {
		if st.IsDir() {
			return nil
		}
		return fmt.Errorf("%s 已存在且不是目录", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 cgroup 目录 %s 失败: %w", dir, err)
	}
	return nil
}

// movePIDToCgroup 把 pid 迁入 dir（写 cgroup.procs；内核按线程组整体迁移）。
//
// 用 O_CREATE 打开：真实 cgroupfs 上 cgroup.procs 由内核提供，O_CREATE 对已存在文件是无操作；
// 而单测用普通目录伪造挂载点时，该标志让写路径可在不触碰真实 cgroup 层级的前提下被验证。
// 生产路径的挂载点是编译期常量 `/sys/fs/cgroup`，不存在「误写到普通目录」的可能。
func movePIDToCgroup(pid int, dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, "cgroup.procs"), os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("打开 %s/cgroup.procs 失败: %w", dir, err)
	}
	defer f.Close()
	if _, err := f.WriteString(strconv.Itoa(pid) + "\n"); err != nil {
		return fmt.Errorf("写入 %s/cgroup.procs 失败: %w", dir, err)
	}
	return nil
}

// descendantsOf 广度优先枚举 rootPID 的全部后代 PID（读 /proc/<pid>/stat 的 PPID 字段）。
// 用于接管路径：wrapper 与其 java 是两棵独立进程组，只迁 wrapper 会漏掉 java。
func descendantsOf(rootPID int) []int {
	children := make(map[int][]int)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	scanned := 0
	for _, e := range entries {
		if scanned >= maxCgroupDescendantScan {
			break
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		ppid, ok := readPPID(pid)
		if !ok {
			continue
		}
		scanned++
		children[ppid] = append(children[ppid], pid)
	}

	var out []int
	queue := []int{rootPID}
	seen := map[int]bool{rootPID: true}
	for len(queue) > 0 && len(out) < maxCgroupDescendantScan {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range children[cur] {
			if seen[child] {
				continue
			}
			seen[child] = true
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

// readPPID 读 /proc/<pid>/stat 的第 4 个字段（PPID）。
// comm 字段（第 2 个）可含空格与括号，故按**最后一个** ')' 之后切分。
func readPPID(pid int) (int, bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	idx := strings.LastIndexByte(string(raw), ')')
	if idx < 0 || idx+2 >= len(raw) {
		return 0, false
	}
	fields := strings.Fields(string(raw)[idx+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// RemoveDaemonCgroupDir 尽最大努力移除本实例的隔离目录（wrapper 正常退出时调用）。
// 目录非空（java 仍在其中）时 Remove 会失败，属预期——留给下一次启动的清扫兜底。
func RemoveDaemonCgroupDir(instanceUUID string) {
	if instanceUUID == "" {
		return
	}
	own, err := ownCgroupReader()
	if err != nil {
		return
	}
	rel, ok := DaemonCgroupDirFor(own, instanceUUID)
	if !ok {
		return
	}
	dir := cgroupFSPath(rel)
	if err := os.Remove(dir); err != nil {
		slog.Debug("移除 daemon 隔离 cgroup 目录未成功（非空或已不存在）", "dir", dir, "error", err)
		return
	}
	// 根目录已空则一并收掉；仍有其它实例在跑时 Remove 失败，属正常。
	_ = os.Remove(filepath.Dir(dir))
}

// SweepEmptyDaemonCgroupDirs 清扫历史遗留的**空**隔离目录（Worker 启动时调用）。
// 只删空目录：被 kill -9 的 wrapper 来不及自清理会留下空壳，长期累积会污染 cgroup 层级。
func SweepEmptyDaemonCgroupDirs() {
	if !CgroupIsolationEnabled() {
		return
	}
	own, err := ownCgroupReader()
	if err != nil {
		return
	}
	relRoot, ok := daemonCgroupRootOf(own)
	if !ok {
		return
	}
	root := cgroupFSPath(relRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		return // 目录不存在=无需清扫
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if os.Remove(filepath.Join(root, e.Name())) == nil {
			removed++
		}
	}
	if removed > 0 {
		slog.Info("已清扫空的 daemon 隔离 cgroup 目录", "dir", root, "removed", removed)
	}
	_ = os.Remove(root)
}

// killModeQueryTimeout 限制单次 KillMode 查询耗时。启动自检不得拖慢 Worker 启动：
// systemd 不可达/D-Bus 卡住时最多等这么久，随后放弃自检（只告警，不阻断）。
const killModeQueryTimeout = 3 * time.Second

// systemctlPath 是查询单元属性用的 systemctl 路径。独立成变量便于测试替换。
var systemctlPath = "systemctl"

// queryWorkerUnitKillMode 查询指定单元的生效 KillMode。
//
// 为什么调 systemctl 而不是读文件：KillMode 是 systemd 的**单元属性**，cgroupfs 不暴露它
// （实测 /sys/fs/cgroup/<unit>/ 只有 cgroup.* 与资源控制文件），D-Bus 或 systemctl 是唯一来源；
// 本仓库未引入 D-Bus 依赖，故用 systemctl——它随 systemd 必然存在（本函数的前提就是「跑在
// systemd 单元里」）。
//
// 必须校验 LoadState：`systemctl show <不存在的单元> -p KillMode` 会返回默认值 `control-group`
// 且**退出码 0**（实测），若不校验就会把「单元名猜错」误报成「单元无保护」。LoadState=loaded
// 才认为读数有效。
//
// isUserScope：cgroup 在 /user.slice/ 下说明是本用户的 systemd --user 实例托管，须用 `--user`
// 查询；否则是系统档单元，用不带 --user 的查询（非 root 用户通常仍可只读查询）。
func queryWorkerUnitKillMode(unit string, isUserScope bool) (string, bool) {
	if unit == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), killModeQueryTimeout)
	defer cancel()

	// 刻意**不用** `--value`：该选项下 systemctl 的输出顺序与 `-p` 书写顺序无关（实测按属性名
	// 排序，如请求 `-p LoadState -p KillMode` 会返回 `process\nloaded`），按位置取值会静默取错。
	// 用 `Key=Value` 形态按名解析，顺序无关。
	args := []string{"show", unit, "-p", "LoadState", "-p", "KillMode"}
	if isUserScope {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.CommandContext(ctx, systemctlPath, args...).Output()
	if err != nil {
		// systemctl 缺失、D-Bus 不可达、无权限——一律放弃自检（返回 ok=false 由调用方静默跳过）。
		return "", false
	}
	props := parseSystemctlProps(string(out))
	if props["LoadState"] != "loaded" {
		// 单元不存在（not-found）或未加载：读数不可信（否则会拿到默认值而误判）。
		return "", false
	}
	mode, ok := props["KillMode"]
	if !ok {
		return "", false
	}
	return mode, true
}

// parseSystemctlProps 解析 `systemctl show` 的 `Key=Value` 输出（每行一项），返回键值映射。
// 忽略空行与不含 `=` 的行；值保留原样（仅去首尾空白），故 `KillMode=control-group` → `control-group`。
func parseSystemctlProps(out string) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key == "" {
			continue
		}
		props[key] = strings.TrimSpace(val)
	}
	return props
}

// WarnIfUnitKillModeUnprotected 启动自检：若本 Worker 所在 systemd 单元的 KillMode 会让
// `systemctl restart <单元>` 连坐杀掉全部 daemon 子树（wrapper + 游戏服 java），则显式告警。
//
// 为什么要做这个自检（2026-10-09 真机事故）：ADR-097 的运行期隔离在「源与目标的共同祖先不可写」
// 时必然失败（真机 13 个 wrapper 位于 root 所属的 /system.slice/ssh.service，非特权用户在任何
// cgroup 祖先下都无可写位置），此时**唯一**防线就是单元自身的 `KillMode=process`。而该属性是
// 部署期属性：安装脚本（scripts/install-worker.sh）生成的单元带它，但**手工 systemd-run / 手写
// 单元的部署会漏掉**，落到默认 control-group——真机正是如此（实际运行的是仓库外手工创建的
// transient 单元）。代码无法在运行期改变别人创建的单元属性，但可以**把沉默的代价变成一句告警**：
// 让部署者在上线当天就知道「重启这台 Worker 会打死全部服务器」。
//
// 只告警、不阻断启动：单元属性属运维决策，且该风险只在「重启单元」时才兑现，不应因此拒绝服务。
// 任何查询失败（非 systemd 环境、systemctl 缺失、超时）都静默跳过，不引入新的失败面。
func WarnIfUnitKillModeUnprotected() {
	if !CgroupIsolationEnabled() {
		// 隔离被显式关闭时，本自检的告警前提（「唯一防线是 KillMode」）不再成立，避免误导。
		return
	}
	own, err := ownCgroupReader()
	if err != nil {
		return
	}
	unit, ok := WorkerUnitNameFromCgroup(own)
	if !ok {
		// 前台运行 / 容器 / scope / cgroup v1：不存在「重启单元连坐」的问题面。
		slog.Debug("当前非 systemd service 单元，跳过 KillMode 防护自检", "ownCgroup", own)
		return
	}
	killMode, ok := workerUnitKillModeReader(unit, strings.HasPrefix(own, "/user.slice/"))
	if !ok {
		return
	}
	if KillModeProtectsDaemonSubtree(killMode) {
		slog.Info("单元 KillMode 已保护 daemon 子树（重启单元不连坐实例）",
			"unit", unit, "killMode", killMode)
		return
	}
	slog.Warn("Worker 单元缺少 KillMode=process 防护："+
		"重启或停止该单元会向整个 cgroup 发信号，连坐杀掉全部游戏服（wrapper + java）。"+
		"请为单元补 KillMode=process（安装脚本 scripts/install-worker.sh 已含该项；"+
		"对已部署单元可用 drop-in：systemctl --user edit "+unit+"）。"+
		"运行期隔离在源单元归 root 时无法生效，故此项是该场景下的唯一防线。",
		"unit", unit, "killMode", killMode, "hint", "KillMode=process")
}

// daemonCgroupRootOf 兼容两种自身形态：位于 Worker 单元 cgroup，或已被隔离（`…/jianmanager-daemons/<uuid>`）。
func daemonCgroupRootOf(ownCgroup string) (string, bool) {
	if root, ok := DaemonCgroupRoot(ownCgroup); ok {
		return root, true
	}
	clean := filepath.Clean(strings.TrimSpace(ownCgroup))
	if parent := filepath.Dir(clean); filepath.Base(parent) == daemonCgroupDirName {
		return parent, true
	}
	return "", false
}
