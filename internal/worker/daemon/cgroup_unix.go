//go:build !windows

package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
