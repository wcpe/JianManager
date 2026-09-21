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
	// ContainerRunning docker 残留容器是否仍处于 running（FR-456 F13：running 容器 auto 档更保守）。
	ContainerRunning bool
	// ManagedLabel docker 残留容器是否带本平台受管标签（归属复核证据，FR-456 N5）。
	ManagedLabel bool
	// LabelInstanceUUID docker 残留容器受管标签内记录的实例 UUID（与容器名交叉核对用）。
	LabelInstanceUUID string
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
	// ManagedLabel 容器是否带平台受管标签（FR-456 N5，见 containerManagedLabelKey）。
	ManagedLabel bool
	// LabelInstanceUUID 受管标签内记录的实例 UUID，用于与容器名交叉核对（可能为空）。
	LabelInstanceUUID string
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
// 本轮耗时超过 maxScanRoundDuration 时告警（FR-456 F14：扫描成本可观测）。
func (s *OrphanScanner) ScanOnce() []OrphanFinding {
	if s == nil || s.mgr == nil {
		return nil
	}
	started := time.Now()
	findings := make([]OrphanFinding, 0)
	findings = append(findings, s.scanWrapperGone()...)
	findings = append(findings, s.scanDirectOrphans()...)
	findings = append(findings, s.scanDockerLeftovers(context.Background())...)
	if elapsed := time.Since(started); elapsed > maxScanRoundDuration {
		slog.Warn("运行期孤儿扫描单轮耗时偏长", "elapsed", elapsed, "findings", len(findings))
	}
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
//
// FR-456（F1/F6）：auto 档每个 PID 在 killTree 前先复用 FR-455① 的归属复核入口
// （verifyProcessOwnership）——复核不通过（PID 可能已被 OS 复用给无关进程，或已不在该工作目录）
// 只告警 + 落审计、不杀。审计 success 依逐 PID 实际处置结果判定（此前恒 true，误报成功）。
func (s *OrphanScanner) handleDirectOrphan(finding *OrphanFinding) {
	if s.policy == OrphanPolicyAuto {
		allDisposed := len(finding.PIDs) > 0
		blocked := 0
		killAttempted := false
		for _, pid := range finding.PIDs {
			// 归属复核：确认该 PID 仍确属本工作目录下的进程，再强杀（杜绝 PID 复用误杀）。
			if !s.mgr.verifyProcessOwnership(pid, finding.InstanceUUID, finding.WorkDir, false) {
				blocked++
				allDisposed = false
				s.mgr.auditOrphan("orphan.scan_dispose_blocked", finding.WorkDir,
					fmt.Sprintf(`{"kind":"direct_orphan","workDir":%q,"pid":%d,"reason":"ownership_unverified"}`, finding.WorkDir, pid),
					false, "direct 孤儿处置前置归属复核不通过，拒绝强杀")
				slog.Warn("direct 孤儿处置前置归属复核不通过，只告警不杀",
					"pid", pid, "workDir", finding.WorkDir)
				continue
			}
			killAttempted = true
			if err := s.mgr.killTree(pid); err != nil {
				allDisposed = false
				slog.Warn("运行期扫描处置 direct 孤儿失败", "pid", pid, "workDir", finding.WorkDir, "error", err)
				continue
			}
		}
		finding.Disposed = allDisposed
		// 仅在确有杀树尝试时落「已处置」审计（其 success 依逐 PID 实际结果判定，FR-456 F6）；
		// 全部被归属复核拦截时只留 dispose_blocked，避免同一处置落两条矛盾审计。
		if killAttempted {
			s.mgr.auditOrphan("orphan.scan_disposed", finding.WorkDir,
				fmt.Sprintf(`{"kind":"direct_orphan","workDir":%q,"pids":%v,"policy":"auto","blocked":%d}`, finding.WorkDir, finding.PIDs, blocked),
				finding.Disposed, disposeErr(finding))
		}
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
			Kind:              OrphanKindDockerLeftover,
			InstanceUUID:      c.UUID,
			ContainerName:     c.Name,
			ContainerRunning:  c.Running,
			ManagedLabel:      c.ManagedLabel,
			LabelInstanceUUID: c.LabelInstanceUUID,
			PIDs:              pidList(c.PID),
			Detail:            fmt.Sprintf("容器 %s 存在（running=%v）但内存表未认作 RUNNING", c.Name, c.Running),
		}
		s.handleDockerLeftover(ctx, &finding)
		out = append(out, finding)
	}
	return out
}

// handleDockerLeftover 按策略处置 docker 残留。
//
// FR-456（F1/F13）：auto 档不再无条件 Force 删除容器——
//   - Running=true 的容器更保守：仅告警不删（可能正是启动中/优雅停止中而内存表暂未认作 RUNNING
//     的实例，强删=误杀运行中服务；待其自然退出成「已退出容器」后再由下一轮清理）。
//   - 内存表已登记该实例（任何状态）时也仅告警：其运行态由实例生命周期掌管，扫描不插手（F1，与
//     direct 兜底的「内存表已有则不判孤儿」口径一致）。
//   - 仅对「已退出（Running=false）且内存表无该实例」的平台容器才执行删除。
func (s *OrphanScanner) handleDockerLeftover(ctx context.Context, finding *OrphanFinding) {
	if s.policy == OrphanPolicyAuto {
		if finding.ContainerRunning {
			slog.Warn("docker 残留为 running 容器，auto 档保守不删（避免误杀启动中/停止中实例）",
				"instanceId", finding.InstanceUUID, "container", finding.ContainerName)
			s.mgr.auditOrphan("orphan.scan_dispose_blocked", finding.InstanceUUID,
				fmt.Sprintf(`{"kind":"docker_leftover","instanceUuid":%q,"container":%q,"policy":"auto","reason":"container_running"}`, finding.InstanceUUID, finding.ContainerName),
				false, "容器仍 running，保守不删，待其退出后再清理")
			return
		}
		if s.mgr.instanceKnown(finding.InstanceUUID) {
			slog.Warn("docker 残留对应的实例仍在内存表中，auto 档不删（运行态由实例生命周期掌管）",
				"instanceId", finding.InstanceUUID, "container", finding.ContainerName)
			s.mgr.auditOrphan("orphan.scan_dispose_blocked", finding.InstanceUUID,
				fmt.Sprintf(`{"kind":"docker_leftover","instanceUuid":%q,"container":%q,"policy":"auto","reason":"instance_registered"}`, finding.InstanceUUID, finding.ContainerName),
				false, "实例仍在内存表中，保守不删")
			return
		}
		// 归属复核（FR-456 F1 / N5 修复）：显式两级证据，不再是恒真的伪复核。
		//
		// 此前这里对 Name 再跑一次 containerUUID——但 Name 在 listManagedContainers 里正是用同一个
		// containerUUID 过滤出来的，判据恒真，称不上归属证明。现改为：
		//   · 名字必须解析出实例 UUID（格式校验，必要条件）；
		//   · 带平台受管标签且标签内实例 UUID 与容器名一致 → 强归属确证（managed_label）；
		//     标签与容器名不一致 → 判为归属不通过并拒绝删除（真判据，可失败）；
		//   · 无标签（本 FR 之前创建的存量容器）→ 仅名字格式校验（name_format_only），
		//     **不构成强归属证明**，仍有「同名容器」残余风险；该降级如实写进审计 detail 供运维核查。
		ownership, ok := verifyContainerOwnership(finding.ContainerName, finding.ManagedLabel, finding.LabelInstanceUUID)
		if !ok {
			slog.Warn("docker 残留归属复核不通过，否定其为平台容器，只告警不删",
				"container", finding.ContainerName, "reason", ownership)
			s.mgr.auditOrphan("orphan.scan_dispose_blocked", finding.InstanceUUID,
				fmt.Sprintf(`{"kind":"docker_leftover","container":%q,"policy":"auto","reason":"ownership_unverified","evidence":%q}`, finding.ContainerName, ownership),
				false, "docker 残留归属复核不通过，拒绝删除")
			return
		}
		if err := s.removeContainer(ctx, finding.ContainerName); err != nil {
			slog.Warn("运行期扫描处置 docker 残留失败", "container", finding.ContainerName, "error", err)
		} else {
			finding.Disposed = true
		}
		s.mgr.auditOrphan("orphan.scan_disposed", finding.InstanceUUID,
			fmt.Sprintf(`{"kind":"docker_leftover","instanceUuid":%q,"container":%q,"policy":"auto","evidence":%q}`, finding.InstanceUUID, finding.ContainerName, ownership),
			finding.Disposed, disposeErr(finding))
		return
	}
	slog.Warn("运行期扫描发现 docker 残留（warn 档仅告警）",
		"instanceId", finding.InstanceUUID, "container", finding.ContainerName)
	s.mgr.auditOrphan("orphan.scan_detected", finding.InstanceUUID,
		fmt.Sprintf(`{"kind":"docker_leftover","instanceUuid":%q,"container":%q,"policy":"warn"}`, finding.InstanceUUID, finding.ContainerName),
		true, "")
}

// managedInstanceRuntime 返回本节点「内存表中已知实例」的根 PID 集合与工作目录集合。
//
// FR-456（F2 + 回归修复）：两集合的判据刻意不对称——
//
//   - 工作目录集合只收「可能仍有活进程」的状态（RUNNING/STARTING/STOPPING，见 mayOwnLiveProcess）。
//     优雅停止期（STOPPING）必须在内，否则 auto 档会 killTree 其 Java、**绕过优雅关服**
//     （世界未保存、session.lock 未释放，F2 原意）。但 STOPPED/CRASHED 的进程已死，其工作目录
//     **不可**保护：Worker 硬崩重启后 CP ResyncInstances 会把 direct 实例按 STOPPED + WorkDir 重登记，
//     残留 Java 的 cwd 正是该 WorkDir——若一并保护，scanner 的目标场景（残留 Java 占 session.lock）
//     反被漏报，等于把兜底扫描废掉（召回回归）。
//   - 根 PID 集合按「管理器记名」收：凡实例的 strategy 明确报出一个 PID，该 PID 即视为受管，不受状态
//     影响。PID 是精确身份，且处置前仍有 verifyProcessOwnership 归属复核兜底；重登记路径的 STOPPED
//     实例 strategy 为 nil（无 PID），故此集合的宽口径不会复活上述漏报。
//
// 语义：运行期扫描是「Worker 内存表已丢失该实例（Worker 曾硬崩）」的兜底——实例仍登记且可能拥有
// 活进程时，其进程属受管，交由各自生命周期操作（stop/start/kill）负责，扫描不插手。
func (m *Manager) managedInstanceRuntime() (pids map[int]struct{}, workDirs map[string]struct{}) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pids = make(map[int]struct{}, len(m.instances))
	workDirs = make(map[string]struct{}, len(m.instances))
	for _, inst := range m.instances {
		if inst.WorkDir != "" && mayOwnLiveProcess(inst.State) {
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

// mayOwnLiveProcess 报告该状态是否「可能仍有活进程」，据此决定其工作目录是否受运行期扫描保护。
//
// RUNNING/STARTING/STOPPING 三态可能仍有活着的 Java（运行中 / 启动中 / 优雅停止尚未退净），
// 其工作目录须受保护，避免被误判为 direct 孤儿后强杀（FR-456 F2）。STOPPED/CRASHED 的进程已死，
// 不保护——否则 Worker 硬崩重启后按 STOPPED 重登记的直接实例会「保护」掉目录下真实的残留 Java，
// 恰是 scanner 要抓的场景（FR-456 N1 回归修复）。
func mayOwnLiveProcess(state InstanceState) bool {
	switch state {
	case StateRunning, StateStarting, StateStopping:
		return true
	default:
		return false
	}
}

// instanceKnown 报告该实例是否登记在本节点内存表中（任何状态）。
// docker 残留 auto 处置前用它复核：内存表已知（哪怕 STOPPING/STOPPED）说明其运行态由
// 实例生命周期掌管，容器不应被扫描强删（FR-456 F1/F13）。
func (m *Manager) instanceKnown(uuid string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.instances[uuid]
	return ok
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

// 运行期孤儿扫描的每轮规模上限（FR-456 F14；spec §5：全机进程/容器枚举有 CPU/权限开销，
// 须设上限并在超限时降级告警，避免单轮扫描拖垮节点）。
const (
	// maxScannedProcesses 单轮 direct 孤儿扫描枚举的进程数上限；超限仅取前 N 个并告警降级。
	maxScannedProcesses = 8192
	// maxScannedContainers 单轮 docker 残留扫描枚举的容器数上限；超限仅取前 N 个并告警降级。
	maxScannedContainers = 2048
	// maxScanRoundDuration 单轮扫描耗时告警阈值；超过仅告警（不中断），提示扫描成本异常。
	maxScanRoundDuration = 30 * time.Second
)

// defaultListProcesses 用 gopsutil 枚举本机进程（cmdline/cwd 快照）。
// 枚举数量受 maxScannedProcesses 限制：超限取前 N 个并告警降级（FR-456 F14）。
func defaultListProcesses() ([]ScannedProcess, error) {
	procs, err := psproc.Processes()
	if err != nil {
		return nil, err
	}
	if len(procs) > maxScannedProcesses {
		slog.Warn("direct 孤儿扫描：进程数超过单轮上限，仅枚举前 N 个（降级）",
			"total", len(procs), "limit", maxScannedProcesses)
		procs = procs[:maxScannedProcesses]
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
	if len(containers) > maxScannedContainers {
		slog.Warn("docker 残留扫描：容器数超过单轮上限，仅枚举前 N 个（降级）",
			"total", len(containers), "limit", maxScannedContainers)
		containers = containers[:maxScannedContainers]
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
				// 受管标签证据（FR-456 N5）：供归属复核把强归属与「仅名字形似」区分开。
				ManagedLabel:      c.Labels[containerManagedLabelKey] == containerManagedLabelValue,
				LabelInstanceUUID: c.Labels[containerInstanceLabelKey],
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

// containerUUID 从容器名解析实例 UUID；非本平台容器返回 false。
// containerNamePrefix 定义在 docker.go（受管容器命名与标签的单一真源）。
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

// verifyContainerOwnership 复核容器是否确为本平台受管容器（FR-456 N5）。
//
// 返回 (evidence, ok)：ok=false 表示归属不通过（不得删除）；evidence 是本次复核所依证据档位，
// 随审计 detail 落库供运维核查。与容器名同源的「格式校验」不再是唯一判据：
//
//   - managed_label：容器带平台受管标签，且标签内实例 UUID 与容器名解析出的 UUID 一致 → 强归属确证。
//   - label_mismatch：容器带受管标签但标签实例与容器名不一致（标签/命名被篡改或串号）→ 不通过。
//   - name_format_only：无受管标签（本 FR 之前创建的存量容器）→ 退化为容器名格式校验，
//     **不构成强归属证明**，存在同名容器被误删的残余风险；但容器名由本平台按 UUID 生成，
//     误删面因此极窄，为兼容存量仍放行（不静默：证据档位如实入审计）。
func verifyContainerOwnership(containerName string, managedLabel bool, labelInstanceUUID string) (string, bool) {
	name := strings.TrimPrefix(containerName, "/")
	uuid, ok := containerUUID(name)
	if !ok {
		// 名字连格式都不符：无论标签如何都不认（listManagedContainers 理应已滤掉，防御性兜底）。
		return "name_format_invalid", false
	}
	if !managedLabel {
		return "name_format_only", true
	}
	if labelInstanceUUID != "" && labelInstanceUUID != uuid {
		return "label_mismatch", false
	}
	return "managed_label", true
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
