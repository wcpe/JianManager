package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	psproc "github.com/shirou/gopsutil/v4/process"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-456 运行期周期孤儿扫描：把孤儿清理从「仅 Worker 启动时」升级为「运行期持续兜底」。
// 扫描三态孤儿并按策略处置：
//  1. wrapper 死 / Java 活（daemon 模式：wrapper 自身死亡，Java 仍占端口与 Paper session.lock）；
//  2. direct 孤儿（Worker 硬崩后 Java 未 setsid、reparent 到 init，内存表已无该实例）；
//  3. docker 残留（Worker 重启后容器仍在跑，但内存表未认作 RUNNING）。
//
// 与启动期 RecoverDaemonInstances 复用同一 `daemon.KillPIDTree` 基座与 FR-455① 的处置前置复核。

// OrphanDisposePolicy 运行期孤儿处置策略（FR-456）。
type OrphanDisposePolicy string

const (
	// OrphanPolicyWarn 只告警 + 落审计，不自动清理（默认；管理员据审计介入）。
	OrphanPolicyWarn OrphanDisposePolicy = "warn"
	// OrphanPolicyAuto 按扫描结果自动清理孤儿（强杀进程树 / 删除容器）。
	OrphanPolicyAuto OrphanDisposePolicy = "auto"
)

// NormalizeOrphanDisposePolicy 归一化策略字符串：空/未知一律回退 warn（安全默认）。
func NormalizeOrphanDisposePolicy(raw string) OrphanDisposePolicy {
	if OrphanDisposePolicy(strings.TrimSpace(strings.ToLower(raw))) == OrphanPolicyAuto {
		return OrphanPolicyAuto
	}
	return OrphanPolicyWarn
}

// OrphanKind 孤儿分类。
type OrphanKind string

const (
	// OrphanKindWrapperGoneJavaAlive daemon wrapper 已死、其托管的 Java 仍活。
	OrphanKindWrapperGoneJavaAlive OrphanKind = "wrapper_gone_java_alive"
	// OrphanKindDirect direct 实例的 Java 孤儿（内存表已丢失该实例）。
	OrphanKindDirect OrphanKind = "direct_orphan"
	// OrphanKindDockerLeftover docker 容器残留（内存表未认作 RUNNING 却容器在跑/已退出）。
	OrphanKindDockerLeftover OrphanKind = "docker_leftover"
)

// OrphanFinding 一次扫描发现的一条孤儿。
type OrphanFinding struct {
	Kind OrphanKind
	// InstanceUUID 相关实例 UUID；direct 孤儿无法解析时为空（以 WorkDir 标识）。
	InstanceUUID string
	// WorkDir 孤儿的工作目录（direct 孤儿的主标识）。
	WorkDir string
	// PIDs 相关进程 PID（docker 残留为空，改用 ContainerName）。
	PIDs []int
	// ContainerName docker 残留的容器名（其余为空）。
	ContainerName string
	// Disposed 是否已按 auto 策略实际处置。
	Disposed bool
	// Detail 供审计的补充说明。
	Detail string
}

// ScannedProcess 进程枚举快照（供 direct 孤儿扫描；可由测试注入）。
type ScannedProcess struct {
	PID     int
	Cmdline string
	Cwd     string
}

// ManagedContainer 本平台受管容器快照（供 docker 残留扫描；可由测试注入）。
type ManagedContainer struct {
	UUID    string
	Name    string // jianmanager-<uuid>
	Running bool
	PID     int
}

// OrphanScanner 运行期周期孤儿扫描器（FR-456）。
type OrphanScanner struct {
	mgr      *Manager
	interval time.Duration
	policy   OrphanDisposePolicy

	// 以下为可注入桩：nil=真实现，测试注入以免真枚举进程/真连 Docker。
	listProcesses   func() ([]ScannedProcess, error)
	listContainers  func(ctx context.Context) ([]ManagedContainer, error)
	removeContainer func(ctx context.Context, name string) error
	pidAlive        func(pid int) bool
}

// NewOrphanScanner 构造运行期孤儿扫描器。interval<=0 时取默认 60s；policy 空取 warn。
func NewOrphanScanner(mgr *Manager, interval time.Duration, policy OrphanDisposePolicy) *OrphanScanner {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if policy != OrphanPolicyAuto {
		policy = OrphanPolicyWarn
	}
	return &OrphanScanner{
		mgr:             mgr,
		interval:        interval,
		policy:          policy,
		listProcesses:   defaultListProcesses,
		listContainers:  listManagedContainers,
		removeContainer: removeManagedContainer,
		pidAlive:        daemon.IsPIDAlive,
	}
}

// Start 启动周期扫描 goroutine（随 ctx 取消而退出）。interval<=0 或 mgr 为空时不启动。
func (s *OrphanScanner) Start(ctx context.Context) {
	if s == nil || s.mgr == nil || s.interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		slog.Info("运行期孤儿扫描已启用", "interval", s.interval, "policy", string(s.policy))
		for {
			select {
			case <-ctx.Done():
				slog.Info("运行期孤儿扫描已停止")
				return
			case <-ticker.C:
				s.ScanOnce()
			}
		}
	}()
}

// ScanOnce 执行一轮三态孤儿扫描并落审计。返回本轮发现（供单测/观测）。
func (s *OrphanScanner) ScanOnce() []OrphanFinding {
	if s == nil || s.mgr == nil {
		return nil
	}
	findings := make([]OrphanFinding, 0)
	findings = append(findings, s.scanWrapperGone()...)
	findings = append(findings, s.scanDirectOrphans()...)
	findings = append(findings, s.scanDockerLeftovers(context.Background())...)
	return findings
}

// scanWrapperGone 遍历 PID 目录，识别「wrapper 已死 / Java 仍活」的 daemon 孤儿。
func (s *OrphanScanner) scanWrapperGone() []OrphanFinding {
	entries, err := os.ReadDir(s.mgr.pidDir)
	if err != nil {
		return nil
	}
	var out []OrphanFinding
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pid") {
			continue
		}
		pidPath := filepath.Join(s.mgr.pidDir, entry.Name())
		uuid := strings.TrimSuffix(entry.Name(), ".pid")
		rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
		if err != nil {
			continue
		}
		if rec.WrapperPID > 0 && s.pidAlive(rec.WrapperPID) {
			continue // wrapper 存活：非本类（由接管扫描负责）
		}
		if rec.JavaPID <= 0 || rec.JavaPID == rec.WrapperPID || !s.pidAlive(rec.JavaPID) {
			continue // Java 也不活：无孤儿（清理由接管扫描/删除路径负责）
		}
		finding := OrphanFinding{
			Kind:         OrphanKindWrapperGoneJavaAlive,
			InstanceUUID: uuid,
			WorkDir:      rec.WorkDir,
			PIDs:         []int{rec.JavaPID},
			Detail:       fmt.Sprintf("wrapper(pid=%d) 已死，Java(pid=%d) 仍活", rec.WrapperPID, rec.JavaPID),
		}
		s.handleWrapperGone(pidPath, rec, &finding)
		out = append(out, finding)
	}
	return out
}

// handleWrapperGone 按策略处置 wrapper 死/Java 活孤儿。
// auto 走 FR-325 同源处置（reapOrphanWrapper，经 FR-455① 复核）；warn 只告警 + 落审计。
func (s *OrphanScanner) handleWrapperGone(pidPath string, rec *daemon.PIDRecord, finding *OrphanFinding) {
	if s.policy == OrphanPolicyAuto {
		s.mgr.reapOrphanWrapper(rec.InstanceUUID, pidPath, rec, errOrphanedWrapperGone)
		finding.Disposed = !pidFileExists(pidPath)
		return
	}
	slog.Warn("运行期扫描发现 wrapper 死/Java 活孤儿（warn 档仅告警）",
		"instanceId", rec.InstanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
	s.mgr.auditOrphan("orphan.scan_detected", rec.InstanceUUID,
		fmt.Sprintf(`{"kind":"wrapper_gone_java_alive","instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"policy":"warn"}`,
			rec.InstanceUUID, rec.WrapperPID, rec.JavaPID),
		true, "")
}

// scanDirectOrphans 枚举本机进程，识别工作目录落在受管服务器根下、却无对应受管实例的无主进程。
// 枚举失败仅告警降级（spec §5：全机进程枚举成本/权限风险）。
func (s *OrphanScanner) scanDirectOrphans() []OrphanFinding {
	procs, err := s.listProcesses()
	if err != nil {
		slog.Warn("direct 孤儿扫描：进程枚举失败，跳过本轮", "error", err)
		return nil
	}
	serversDir := filepath.Clean(s.mgr.serversDir)
	managedPIDs, managedWorkDirs := s.mgr.managedInstanceRuntime()

	var out []OrphanFinding
	for _, proc := range procs {
		if proc.PID <= 0 {
			continue
		}
		workDir := directOrphanWorkDir(proc, serversDir)
		if workDir == "" {
			continue // 不属于本节点受管服务器根
		}
		if _, ok := managedPIDs[proc.PID]; ok {
			continue // 受管进程
		}
		if _, ok := managedWorkDirs[filepath.Clean(workDir)]; ok {
			continue // 其工作目录有受管实例在跑
		}
		finding := OrphanFinding{
			Kind:    OrphanKindDirect,
			WorkDir: workDir,
			PIDs:    []int{proc.PID},
			Detail:  fmt.Sprintf("无主进程在受管服务器目录运行：%s", truncateCmdline(proc.Cmdline)),
		}
		s.handleDirectOrphan(&finding)
		out = append(out, finding)
	}
	return out
}

// handleDirectOrphan 按策略处置 direct 孤儿。
func (s *OrphanScanner) handleDirectOrphan(finding *OrphanFinding) {
	if s.policy == OrphanPolicyAuto {
		for _, pid := range finding.PIDs {
			if err := s.mgr.killTree(pid); err != nil {
				slog.Warn("运行期扫描处置 direct 孤儿失败", "pid", pid, "workDir", finding.WorkDir, "error", err)
				continue
			}
			finding.Disposed = true
		}
		s.mgr.auditOrphan("orphan.scan_disposed", finding.WorkDir,
			fmt.Sprintf(`{"kind":"direct_orphan","workDir":%q,"pids":%v,"policy":"auto"}`, finding.WorkDir, finding.PIDs),
			true, "")
		return
	}
	slog.Warn("运行期扫描发现 direct 孤儿（warn 档仅告警）", "pid", finding.PIDs, "workDir", finding.WorkDir)
	s.mgr.auditOrphan("orphan.scan_detected", finding.WorkDir,
		fmt.Sprintf(`{"kind":"direct_orphan","workDir":%q,"pids":%v,"policy":"warn"}`, finding.WorkDir, finding.PIDs),
		true, "")
}

// scanDockerLeftovers 列出本平台容器，识别内存表未认作 RUNNING 却仍存在的容器（含已退出）。
func (s *OrphanScanner) scanDockerLeftovers(ctx context.Context) []OrphanFinding {
	containers, err := s.listContainers(ctx)
	if err != nil {
		slog.Warn("docker 残留扫描：容器枚举失败，跳过本轮", "error", err)
		return nil
	}
	var out []OrphanFinding
	for _, c := range containers {
		if c.UUID == "" {
			continue
		}
		if s.mgr.instanceRunning(c.UUID) {
			continue // 内存表认作在跑：非残留
		}
		finding := OrphanFinding{
			Kind:          OrphanKindDockerLeftover,
			InstanceUUID:  c.UUID,
			ContainerName: c.Name,
			PIDs:          pidList(c.PID),
			Detail:        fmt.Sprintf("容器 %s 存在（running=%v）但内存表未认作 RUNNING", c.Name, c.Running),
		}
		s.handleDockerLeftover(ctx, &finding)
		out = append(out, finding)
	}
	return out
}

// handleDockerLeftover 按策略处置 docker 残留。
func (s *OrphanScanner) handleDockerLeftover(ctx context.Context, finding *OrphanFinding) {
	if s.policy == OrphanPolicyAuto {
		if err := s.removeContainer(ctx, finding.ContainerName); err != nil {
			slog.Warn("运行期扫描处置 docker 残留失败", "container", finding.ContainerName, "error", err)
		} else {
			finding.Disposed = true
		}
		s.mgr.auditOrphan("orphan.scan_disposed", finding.InstanceUUID,
			fmt.Sprintf(`{"kind":"docker_leftover","instanceUuid":%q,"container":%q,"policy":"auto"}`, finding.InstanceUUID, finding.ContainerName),
			finding.Disposed, disposeErr(finding))
		return
	}
	slog.Warn("运行期扫描发现 docker 残留（warn 档仅告警）",
		"instanceId", finding.InstanceUUID, "container", finding.ContainerName)
	s.mgr.auditOrphan("orphan.scan_detected", finding.InstanceUUID,
		fmt.Sprintf(`{"kind":"docker_leftover","instanceUuid":%q,"container":%q,"policy":"warn"}`, finding.InstanceUUID, finding.ContainerName),
		true, "")
}

// managedInstanceRuntime 返回「运行/启动中」受管实例的根 PID 集合与工作目录集合。
func (m *Manager) managedInstanceRuntime() (pids map[int]struct{}, workDirs map[string]struct{}) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pids = make(map[int]struct{}, len(m.instances))
	workDirs = make(map[string]struct{}, len(m.instances))
	for _, inst := range m.instances {
		if inst.State != StateRunning && inst.State != StateStarting {
			continue
		}
		if inst.WorkDir != "" {
			workDirs[filepath.Clean(inst.WorkDir)] = struct{}{}
		}
		if inst.strategy != nil {
			if pid := inst.strategy.GetPID(); pid > 0 {
				pids[pid] = struct{}{}
			}
		}
	}
	return
}

// instanceRunning 报告该实例在内存表中是否认作「在跑」（RUNNING/STARTING）。
func (m *Manager) instanceRunning(uuid string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[uuid]
	if !ok {
		return false
	}
	return inst.State == StateRunning || inst.State == StateStarting
}

// directOrphanWorkDir 返回进程若落在受管服务器根下则给出其工作目录，否则空串。
// 以「cwd 或 cmdline 引用 serversDir 前缀」为判据（direct 实例工作目录均在 serversDir 下）。
func directOrphanWorkDir(proc ScannedProcess, serversDir string) string {
	if serversDir == "" || serversDir == "." {
		return ""
	}
	if proc.Cwd != "" && pathUnder(proc.Cwd, serversDir) {
		return filepath.Clean(proc.Cwd)
	}
	if strings.Contains(proc.Cmdline, serversDir) {
		if proc.Cwd != "" {
			return filepath.Clean(proc.Cwd)
		}
		return serversDir
	}
	return ""
}

// pathUnder 报告路径 p 是否位于 root 之下。
func pathUnder(p, root string) bool {
	cp := filepath.Clean(p)
	cr := filepath.Clean(root)
	if cp == cr {
		return true
	}
	return strings.HasPrefix(cp, cr+string(os.PathSeparator))
}

// defaultListProcesses 用 gopsutil 枚举本机进程（cmdline/cwd 快照）。
func defaultListProcesses() ([]ScannedProcess, error) {
	procs, err := psproc.Processes()
	if err != nil {
		return nil, err
	}
	out := make([]ScannedProcess, 0, len(procs))
	for _, p := range procs {
		if p == nil {
			continue
		}
		cmdline, cmdErr := p.Cmdline()
		if cmdErr != nil {
			continue
		}
		cwd, _ := p.Cwd()
		out = append(out, ScannedProcess{PID: int(p.Pid), Cmdline: cmdline, Cwd: cwd})
	}
	return out, nil
}

// listManagedContainers 列出本机全部 jianmanager-<uuid> 容器（含已退出）。
func listManagedContainers(ctx context.Context) ([]ManagedContainer, error) {
	cli, err := dockerClientFromEnv()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	containers, err := cli.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]ManagedContainer, 0, len(containers))
	for _, c := range containers {
		for _, name := range c.Names {
			trimmed := strings.TrimPrefix(name, "/")
			uuid, ok := containerUUID(trimmed)
			if !ok {
				continue
			}
			out = append(out, ManagedContainer{
				UUID:    uuid,
				Name:    trimmed,
				Running: strings.EqualFold(c.State, "running"),
			})
		}
	}
	return out, nil
}

// removeManagedContainer 强制删除指定容器。
func removeManagedContainer(ctx context.Context, name string) error {
	cli, err := dockerClientFromEnv()
	if err != nil {
		return err
	}
	defer cli.Close()
	containers, err := cli.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range containers {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == name {
				return cli.ContainerRemove(ctx, c.ID, containertypes.RemoveOptions{Force: true})
			}
		}
	}
	return nil
}

// containerNamePrefix 是 docker 策略容器名前缀（见 dockerStrategy.containerName）。
const containerNamePrefix = "jianmanager-"

// containerUUID 从容器名解析实例 UUID；非本平台容器返回 false。
func containerUUID(name string) (string, bool) {
	if !strings.HasPrefix(name, containerNamePrefix) {
		return "", false
	}
	uuid := strings.TrimPrefix(name, containerNamePrefix)
	if uuid == "" {
		return "", false
	}
	return uuid, true
}

// pidList 把 PID 包成切片（0 视为无）。
func pidList(pid int) []int {
	if pid <= 0 {
		return nil
	}
	return []int{pid}
}

// pidFileExists 报告 PID 文件是否仍存在（reapOrphanWrapper 死透后会删除它）。
func pidFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// disposeErr 返回处置失败时的错误说明（成功为空）。
func disposeErr(f *OrphanFinding) string {
	if f.Disposed {
		return ""
	}
	return "处置失败"
}

// truncateCmdline 截断命令行供审计展示。
func truncateCmdline(cmdline string) string {
	cmdline = strings.TrimSpace(cmdline)
	if len(cmdline) <= 240 {
		return cmdline
	}
	return cmdline[:240]
}
