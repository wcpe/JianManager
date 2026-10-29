package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	psproc "github.com/shirou/gopsutil/v4/process"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// 运行期周期孤儿扫描：把孤儿清理从「仅 Worker 启动时」升级为「运行期持续兜底」。
// 扫描三态孤儿并按策略处置：
//  1. wrapper 死 / Java 活（daemon 模式：wrapper 自身死亡，Java 仍占端口与 Paper session.lock）；
//  2. direct 孤儿（Worker 硬崩后 Java 未 setsid、reparent 到 init，内存表已无该实例）；
//  3. docker 残留（Worker 重启后容器仍在跑，但内存表未认作 RUNNING）。
//
// 增加第四相（只观测不处置）：已注册实例的工作目录下存在外来活进程（平台记账 STOPPED、磁盘
// 却在跑），聚合后写 Manager 缓存供心跳上报 CP，处置由人工「接管」动作触发。
//
// 与启动期 RecoverDaemonInstances 复用同一 `daemon.KillPIDTree` 基座与① 的处置前置复核。

// OrphanDisposePolicy 运行期孤儿处置策略。
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
	// OrphanKindForeignRuntime 已注册实例的工作目录下存在外来活进程。
	// 与前三类不同，它不是孤儿（实例仍在内存表中），而是「平台记账 STOPPED、磁盘却在跑」的对齐线索：
	// 本相只观测并上报，处置须由人工确认后经 AdoptForeignRuntime 接管。
	OrphanKindForeignRuntime OrphanKind = "foreign_runtime"
	// OrphanKindUnmanagedLiveRuntime PID 目录中未纳管的活进程对（wrapper 与 Java 均活、PID 文件有效，
	// 但本 Worker 尚未纳管③）。它不是孤儿——平台自有 wrapper 仍在托管服务器，只是控制连接
	// 缺失；处置是**非破坏**的：归属复核通过即自动重连并登记 RUNNING（见 adopt_orphan.go）。
	// 自动收养关闭（auto_adopt=false）时本类仍会被发现并告警，只是 Adopted 恒为 false（不接管）。
	OrphanKindUnmanagedLiveRuntime OrphanKind = "unmanaged_live_runtime"
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
	// ContainerRunning docker 残留容器是否仍处于 running（F13：running 容器 auto 档更保守）。
	ContainerRunning bool
	// ManagedLabel docker 残留容器是否带本平台受管标签（归属复核证据N5）。
	ManagedLabel bool
	// LabelInstanceUUID docker 残留容器受管标签内记录的实例 UUID（与容器名交叉核对用）。
	LabelInstanceUUID string
	// Disposed 是否已按 auto 策略实际处置。
	Disposed bool
	// Adopted 是否已被自动收养为 RUNNING（仅 OrphanKindUnmanagedLiveRuntime③）。
	// 与 Disposed 语义正交：收养是**非破坏**动作（只重连，不杀不重启进程）。
	Adopted bool
	// Detail 供审计的补充说明。
	Detail string
}

// ScannedProcess 进程枚举快照（供 direct 孤儿扫描；可由测试注入）。
type ScannedProcess struct {
	PID     int
	PPID    int // 父进程 PID（用于按进程树归属判定，如 preflight 排除实例自身进程树）
	Cmdline string
	Cwd     string
}

// ManagedContainer 本平台受管容器快照（供 docker 残留扫描；可由测试注入）。
type ManagedContainer struct {
	UUID    string
	Name    string // jianmanager-<uuid>
	Running bool
	PID     int
	// ManagedLabel 容器是否带平台受管标签（N5，见 containerManagedLabelKey）。
	ManagedLabel bool
	// LabelInstanceUUID 受管标签内记录的实例 UUID，用于与容器名交叉核对（可能为空）。
	LabelInstanceUUID string
}

// unmanagedLivePeer 一条「未纳管但存活」进程对的保护信息（本缺陷修复的降级依据）。
//
// 字段取自第 0 相的发现结果（`unmanagedLiveFinding`）：PIDs 固定为 [wrapperPID, javaPID]，
// workDir 取自 PID 记录。命中判据与命中后的降级动作见 unmanagedLiveGuardHit / scanDirectOrphans。
type unmanagedLivePeer struct {
	instanceUUID string
	wrapperPID   int
	javaPID      int
	workDir      string
}

// OrphanScanner 运行期周期孤儿扫描器。
type OrphanScanner struct {
	mgr      *Manager
	interval time.Duration
	policy   OrphanDisposePolicy
	// autoAdopt 未纳管活进程自动收养开关（③，默认 DefaultOrphanAutoAdopt=true）。
	// 关闭时第 0 相降级为「只发现 + 告警」，仍不收养（见 autoAdoptUnmanaged）。装配期写入、运行期读取。
	//
	// **本开关也是处置策略的闸门（本缺陷修复）**：关闭时不只关掉收养，「本轮发现到的未纳管活进程对」
	// 在 dispose_policy=auto 下也一并降级为 warn、不做处置（见 scanDirectOrphans / warnDowngradedDirectOrphans）。
	// 理由是语义而非偏好：`auto_adopt=false` 是运维明确表达「这些进程我来管」，自动处置若照旧执行，
	// 等于用处置策略绕过运维的明示意图，最坏后果是杀掉运维正在手动管理的服务器。
	// 已纳管实例与未能被本相发现的其它进程不受此闸门影响，仍照旧走 dispose_policy。
	autoAdopt bool

	// warnedUnmanaged 记录「开关关闭时已告警过」的未纳管进程对（key=实例 UUID，value=wrapper/java PID
	// 签名），用于降噪：同一进程对只在首次出现或 PID 变化时告警 + 落审计，其余轮次静默。
	// 仅由扫描 goroutine 读写，经 warnMu 保护（ScanOnce 是导出方法，可能与 Start 的 ticker 并发）。
	warnedUnmanaged map[string]string
	warnMu          sync.Mutex

	// unmanagedLive 是本轮「未纳管 + 活」进程对的保护快照：第 0 相发现后写入，第二相（direct 孤儿）
	// 据此把它们的处置**降级为 warn**（本缺陷修复，只有 autoAdopt=false 时填充，每轮整体替换）。
	// 由 unmanagedMu 保护（ScanOnce 是导出方法，可能与 Start 的 ticker 并发）。
	unmanagedLive []unmanagedLivePeer
	// warnedDowngraded 记录「已落过降级留痕」的进程对（key=实例 UUID，value=wrapper/java PID 签名），
	// 降噪口径与 warnedUnmanaged 相同：同一进程对只留一条 WARN + 一条审计，PID 变化才算新事件。
	// 由 unmanagedMu 保护。
	warnedDowngraded map[string]string
	unmanagedMu      sync.Mutex

	// 以下为可注入桩：nil=真实现，测试注入以免真枚举进程/真连 Docker。
	listProcesses   func() ([]ScannedProcess, error)
	listContainers  func(ctx context.Context) ([]ManagedContainer, error)
	removeContainer func(ctx context.Context, name string) error
	pidAlive        func(pid int) bool
}

// NewOrphanScanner 构造运行期孤儿扫描器。interval<=0 时取默认 60s；policy 空取 warn。
// 未纳管活进程自动收养默认启用（③，见 DefaultOrphanAutoAdopt）；可用 SetAutoAdopt(false) 关闭。
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
		autoAdopt:       DefaultOrphanAutoAdopt,
		listProcesses:   defaultListProcesses,
		listContainers:  listManagedContainers,
		removeContainer: removeManagedContainer,
		pidAlive:        daemon.IsPIDAlive,
	}
}

// SetAutoAdopt 设置未纳管活进程自动收养开关（③）：false = 只告警不收养（应急逃生口）。
func (s *OrphanScanner) SetAutoAdopt(enabled bool) {
	if s == nil {
		return
	}
	s.autoAdopt = enabled
}

// Start 启动周期扫描 goroutine（随 ctx 取消而退出）。interval<=0 或 mgr 为空时不启动。
//
// 启动后**立即执行一轮**再进入 ticker（③）：「Worker 启动」窗口与新产生的未纳管活进程
// 不能等到第一个周期才发现。
//
// 与启动接管的时序（本缺陷修复）：main 在 RecoverDaemonInstances 之后启动本扫描器，而
// RecoverDaemonInstances 现只同步完成「快速首拨」——首拨失败的记录转后台重试，故本扫描器
// 不会再被 ~127s 的重试阻塞（旧实现下一条不可达 PID 记录就把扫描器推迟 127s，N 条线性累加）。
// 代价是接管重试可能与扫描并行，故第 0 相与 direct 相都要为「在飞接管」让路
// （见 takeoverPendingHas / takeoverProtects），避免并发拨号与误判强杀。
func (s *OrphanScanner) Start(ctx context.Context) {
	if s == nil || s.mgr == nil || s.interval <= 0 {
		return
	}
	go func() {
		slog.Info("运行期孤儿扫描已启用", "interval", s.interval, "policy", string(s.policy),
			"autoAdopt", s.autoAdopt)
		s.ScanOnce()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
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

// ScanOnce 执行一轮孤儿扫描并落审计。返回本轮发现（供单测/观测）。
// 本轮耗时超过 maxScanRoundDuration 时告警（F14：扫描成本可观测）。
//
// 相位顺序（③）：第 0 相 autoAdoptUnmanaged 先跑——把本轮可收养的未纳管活进程纳入管理，
// 使同一轮后续相位的「受管 PID / 工作目录」集合立刻包含它们，避免刚收养实例的 Java 在同轮被
// direct 相误判为无主进程。开关关闭时该相降级为「只发现 + 告警」（不收养，故不会改变受管集合），
// 并把本轮发现到的进程对登记为**处置保护快照**：第二相据此把它们的 auto 处置降级为 warn
// （`auto_adopt=false` 的意图是「不要动这些进程」，不是「放弃它们的保护」，见 scanDirectOrphans）。
//
// 第四相 scanForeignInRegisteredWorkdirs 与前三相共用同一轮进程枚举快照
// （procs 只枚举一次），观测「已注册实例目录下的外来活进程」并写入 Manager 缓存供心跳上报。
func (s *OrphanScanner) ScanOnce() []OrphanFinding {
	if s == nil || s.mgr == nil {
		return nil
	}
	started := time.Now()
	findings := make([]OrphanFinding, 0)
	findings = append(findings, s.autoAdoptUnmanaged()...)
	// 本机进程只枚举一次，供第二相（无主进程）与第四相（在册实例目录下的漂移）共用；
	// 枚举失败仅告警降级，此时第四相也一并跳过（宁漏勿误）。
	procs, procErr := s.listProcesses()
	if procErr != nil {
		slog.Warn("运行期孤儿扫描：进程枚举失败，第二/四相跳过本轮", "error", procErr)
	}
	findings = append(findings, s.scanWrapperGone()...)
	findings = append(findings, s.scanDirectOrphans(procs)...)
	findings = append(findings, s.scanDockerLeftovers(context.Background())...)
	findings = append(findings, s.scanForeignInRegisteredWorkdirs(procs, procErr == nil)...)
	if elapsed := time.Since(started); elapsed > maxScanRoundDuration {
		slog.Warn("运行期孤儿扫描单轮耗时偏长", "elapsed", elapsed, "findings", len(findings))
	}
	return findings
}

// autoAdoptUnmanaged 是扫描的第 0 相（③）：把 PID 目录中未纳管的活进程对自动收养为 RUNNING。
//
// 开关关闭（autoAdopt=false）时**只关闭收养动作**（归属复核 + 重连 + 登记），不关闭「发现 + 告警」这个
// 观测能力——规格 §2.3/§4 的口径是「`false` 只关收养」「不收养（仍只告警）」。理由有两条：
//  1. 开关是**收养的逃生口**，不是观测的逃生口。连发现一并关掉会让「磁盘在跑、平台记账 STOPPED、
//     控制面完全沉默」这一缺陷形态原样复活，运维连排查线索都没有；
//  2. 开关关闭恰恰是最需要告警的时候——有人主动关掉了自愈，运维更该知道还有多少未纳管进程在跑。
//
// 开关关闭时更不杀进程（护栏五.1「校验不过只告警，不得杀进程」同口径）：本相全程不调用
// Start / killTree / signalTree，只读取 PID 记录与存活状态。
//
// 开关关闭时还会把本轮发现到的未纳管活进程对登记为**处置保护快照**（setUnmanagedLiveGuard），
// 使第二相（direct 孤儿）的 auto 处置对它们降级为 warn——见 scanDirectOrphans 与
// warnDowngradedDirectOrphans。开关开启时不登记（开关开启路径本就先把它们收养为受管，
// 或由启动接管在飞集合 takeoverPending 让路），并清空任何残留快照，保证「行为完全不变」。
func (s *OrphanScanner) autoAdoptUnmanaged() []OrphanFinding {
	if s.autoAdopt {
		s.resetUnmanagedLiveGuard()
		adopted, findings := s.mgr.AdoptUnmanagedDaemonInstances()
		if adopted > 0 {
			slog.Info("运行期扫描已自动收养未纳管的活进程", "adopted", adopted)
		}
		return findings
	}
	findings := s.mgr.DiscoverUnmanagedDaemonInstances()
	s.setUnmanagedLiveGuard(findings)
	s.warnUnmanagedNotAdopted(findings)
	return findings
}

// setUnmanagedLiveGuard 把本轮「未纳管 + 活」的发现结果登记为处置保护快照（整体替换）。
// 只收 OrphanKindUnmanagedLiveRuntime；PIDs 不足两条时按 0 处理（上界防御，发现阶段恒为两条）。
func (s *OrphanScanner) setUnmanagedLiveGuard(findings []OrphanFinding) {
	if s == nil {
		return
	}
	peers := make([]unmanagedLivePeer, 0, len(findings))
	for _, f := range findings {
		if f.Kind != OrphanKindUnmanagedLiveRuntime {
			continue
		}
		wrapperPID, javaPID := 0, 0
		if len(f.PIDs) >= 2 {
			wrapperPID, javaPID = f.PIDs[0], f.PIDs[1]
		}
		peers = append(peers, unmanagedLivePeer{
			instanceUUID: f.InstanceUUID,
			wrapperPID:   wrapperPID,
			javaPID:      javaPID,
			workDir:      f.WorkDir,
		})
	}
	s.unmanagedMu.Lock()
	s.unmanagedLive = peers
	s.unmanagedMu.Unlock()
}

// resetUnmanagedLiveGuard 清空处置保护快照与降级留痕去重表（开关开启时的每轮收尾）。
//
// 必须每轮清空：开关若在运行期由 false 翻回 true，上一轮的快照会让 direct 相继续放过真孤儿，
// 使「auto_adopt=true 行为完全不变」失效。清空后继起名条目，等于让下一次降级重新留痕。
func (s *OrphanScanner) resetUnmanagedLiveGuard() {
	if s == nil {
		return
	}
	s.unmanagedMu.Lock()
	s.unmanagedLive = nil
	s.warnedDowngraded = nil
	s.unmanagedMu.Unlock()
}

// unmanagedLiveGuardHit 报告 pid / workDir 是否属本轮「未纳管 + 活」的进程对，命中则返回该对。
//
// 口径与 handleDirectOrphan 的 takeoverProtects 一致（PID 与工作目录双口径，任一命中）：
//   - PID 口径覆盖「wrapper 的 cwd 与实例工作目录不一致」的真实形态（wrapper 由 Worker 拉起、
//     不设 cmd.Dir，其 cwd 常不是 workDir，见 auto-adopt-orphans spec §5）；
//   - workDir 口径覆盖「同目录下由该进程对派生出的子进程」（如 sh -c 中间层）。
func (s *OrphanScanner) unmanagedLiveGuardHit(pid int, workDir string) (unmanagedLivePeer, bool) {
	if s == nil {
		return unmanagedLivePeer{}, false
	}
	s.unmanagedMu.Lock()
	defer s.unmanagedMu.Unlock()
	if len(s.unmanagedLive) == 0 {
		return unmanagedLivePeer{}, false
	}
	cleanWorkDir := ""
	if workDir != "" {
		cleanWorkDir = filepath.Clean(workDir)
	}
	for _, peer := range s.unmanagedLive {
		if pid > 0 && (pid == peer.wrapperPID || pid == peer.javaPID) {
			return peer, true
		}
		if cleanWorkDir != "" && peer.workDir != "" && filepath.Clean(peer.workDir) == cleanWorkDir {
			return peer, true
		}
	}
	return unmanagedLivePeer{}, false
}

// warnUnmanagedNotAdopted 对「开关关闭时发现的未纳管活进程」告警 + 落审计，并按变化去重。
//
// 降噪口径与第四相（scanForeignInRegisteredWorkdirs）同一手法：同一 (实例 UUID, wrapper/java PID
// 签名) 只在本轮签名与上一轮**不同**时（首次出现，或 PID 变了＝确实是新事件）才 WARN + 落审计；签名未变
// 的轮次完全静默。扫描默认 60s 一拍，故一个长期驻留的未纳管进程整段生命周期只产生**一条** WARN 与
// **一条**审计，不会每轮刷屏，也不会把审计表撑大。
//
// 缓存整体替换（而非逐条更新）：进程消失后条目自然过期，下次再出现会当作新事件重新告警一次。
//
// 只告警、不处置——开关关闭时绝不杀进程（护栏五.1）。
func (s *OrphanScanner) warnUnmanagedNotAdopted(findings []OrphanFinding) {
	if s == nil || s.mgr == nil {
		return
	}
	fresh := s.newlyWarnedUnmanaged(findings)
	for _, f := range fresh {
		// 发现阶段固定产出 [wrapperPID, javaPID]；此处仍做长度防御，避免上游形态变化时越界。
		wrapperPID, javaPID := 0, 0
		if len(f.PIDs) >= 2 {
			wrapperPID, javaPID = f.PIDs[0], f.PIDs[1]
		}
		slog.Warn("运行期扫描发现未纳管的活进程（自动收养已关闭，只告警不接管）",
			"instanceId", f.InstanceUUID, "wrapperPid", wrapperPID, "javaPid", javaPID, "workDir", f.WorkDir)
		s.mgr.auditOrphan("orphan.scan_detected", f.InstanceUUID,
			fmt.Sprintf(`{"kind":"unmanaged_live_runtime","instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"workDir":%q,"policy":"warn","reason":"auto_adopt_disabled"}`,
				f.InstanceUUID, wrapperPID, javaPID, f.WorkDir),
			true, "")
	}
}

// newlyWarnedUnmanaged 返回本轮「签名相对上一轮发生变化」的发现，并把缓存整体替换为本轮观测。
// 单个临界区内完成比对 + 替换，且**锁内不做日志与审计**（避免持锁做 I/O）。
// 缓存整体替换（而非逐条更新）：进程消失后条目自然过期，下次再出现会当作新事件重新告警一次。
func (s *OrphanScanner) newlyWarnedUnmanaged(findings []OrphanFinding) []OrphanFinding {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()

	seen := make(map[string]string, len(findings))
	fresh := make([]OrphanFinding, 0, len(findings))
	for _, f := range findings {
		sig := pidSignature(f.PIDs)
		seen[f.InstanceUUID] = sig
		if prev, warned := s.warnedUnmanaged[f.InstanceUUID]; warned && prev == sig {
			continue // 与上一轮同一进程对：本轮不重复告警、不重复审计
		}
		fresh = append(fresh, f)
	}
	s.warnedUnmanaged = seen
	return fresh
}

// pidSignature 生成进程对的稳定签名（告警去重用）：PID 任一变化即视为新事件。
func pidSignature(pids []int) string {
	return fmt.Sprint(pids)
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
// auto 走同源处置（reapOrphanWrapper，经① 复核）；warn 只告警 + 落审计。
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

// scanDirectOrphans 枚举本机进程（procs 由 ScanOnce 统一枚举一次后传入），识别工作目录落在受管
// 服务器根下、却无对应受管实例的无主进程。枚举失败仅告警降级（spec §5：全机进程枚举成本/权限风险）。
//
// **未纳管活进程对的处置降级（本缺陷修复）**：`dispose_policy=auto` + `auto_adopt=false` 时，第 0 相
// 发现到的未纳管进程对（wrapper 与 Java 均活、PID 记录有效）也在受管服务器根下运行、且不在受管集合中，
// 会被本相判为 direct 孤儿并在 auto 档 killTree——这等于用处置策略绕过 `auto_adopt=false` 表达的
// 「这些进程我来管」，最坏后果是杀掉运维正在手动管理的服务器。故命中保护快照者一律降级为 warn：
// 只落 WARN + 审计，不进入 handleDirectOrphan。开关开启时快照为空，本分支恒不命中（行为不变）。
func (s *OrphanScanner) scanDirectOrphans(procs []ScannedProcess) []OrphanFinding {
	if len(procs) == 0 {
		return nil
	}
	serversDir := filepath.Clean(s.mgr.serversDir)
	managedPIDs, managedWorkDirs := s.mgr.managedInstanceRuntime()

	var out []OrphanFinding
	// 本轮被降级的未纳管进程对（key=实例 UUID）：wrapper 与 Java 各产出**一条**进程级 finding，
	// 但整对只留**一条**降级痕迹——循环内只收集，循环后统一留痕（不在循环里加锁/写审计）。
	downgraded := make(map[string]unmanagedLivePeer)
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
		// 降级判据只在 auto 档生效：warn 档本就只告警不处置，无须（也不该）多落一条「降级」审计，
		// 否则 dispose_policy=warn 的既有行为与审计形态会变（硬约束：warn 档行为不变）。
		if s.policy == OrphanPolicyAuto {
			if peer, guarded := s.unmanagedLiveGuardHit(proc.PID, workDir); guarded {
				finding := OrphanFinding{
					Kind:         OrphanKindDirect,
					InstanceUUID: peer.instanceUUID,
					WorkDir:      workDir,
					PIDs:         []int{proc.PID},
					Detail: fmt.Sprintf("无主进程在受管服务器目录运行，但属未纳管活进程对（auto_adopt=false，已降级为 warn 不处置）：%s",
						truncateCmdline(proc.Cmdline)),
				}
				downgraded[peer.instanceUUID] = peer
				out = append(out, finding)
				continue
			}
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
	if len(downgraded) > 0 {
		s.warnDowngradedDirectOrphans(downgraded)
	}
	return out
}

// warnDowngradedDirectOrphans 对「按 dispose_policy=auto 本应处置、但因 auto_adopt=false 降级为 warn」
// 的未纳管进程对留痕：WARN + 审计，并按变化去重（同一进程对只留一条，PID 变化才算新事件）。
//
// 为什么必须留痕：降级是**有意的行为差异**，不是静默跳过。若不留痕，运维既看不到「配置的处置策略
// 在这一类上没生效」，也无法解释「扫描报了 direct 孤儿却没杀」——那正是「策略静默失效」这类最危险
// 的事故形态（与护栏五.1「不静默」同源）。审计复用既有 action，不新增审计输入面：
// `orphan.scan_dispose_blocked`（success=false），detail 用 `reason=auto_adopt_disabled` 与
// `effectivePolicy=warn` 与「归属复核不过 / 启动接管在飞」两种拦截区分开。
func (s *OrphanScanner) warnDowngradedDirectOrphans(peers map[string]unmanagedLivePeer) {
	if s == nil || s.mgr == nil {
		return
	}
	for _, peer := range s.newlyDowngraded(peers) {
		slog.Warn("运行期扫描：未纳管活进程对在 dispose_policy=auto 下本应处置，因 auto_adopt=false 降级为 warn，不做处置",
			"instanceId", peer.instanceUUID, "wrapperPid", peer.wrapperPID, "javaPid", peer.javaPID, "workDir", peer.workDir)
		s.mgr.auditOrphan("orphan.scan_dispose_blocked", peer.instanceUUID,
			fmt.Sprintf(`{"kind":"direct_orphan","instanceUuid":%q,"workDir":%q,"wrapperPid":%d,"javaPid":%d,"policy":"auto","effectivePolicy":"warn","reason":"auto_adopt_disabled"}`,
				peer.instanceUUID, peer.workDir, peer.wrapperPID, peer.javaPID),
			false, "未纳管活进程对因 auto_adopt=false 降级为 warn，不做处置")
	}
}

// newlyDowngraded 返回本轮「签名相对上一轮发生变化」的降级进程对（按 UUID 序，保证审计顺序可复现），
// 并把去重表整体替换为本轮集合。单个临界区内完成比对 + 替换，**锁内不做日志与审计**（避免持锁做 I/O）。
func (s *OrphanScanner) newlyDowngraded(peers map[string]unmanagedLivePeer) []unmanagedLivePeer {
	s.unmanagedMu.Lock()
	defer s.unmanagedMu.Unlock()

	seen := make(map[string]string, len(peers))
	fresh := make([]unmanagedLivePeer, 0, len(peers))
	for uuid, peer := range peers {
		sig := pidSignature([]int{peer.wrapperPID, peer.javaPID})
		seen[uuid] = sig
		if prev, warned := s.warnedDowngraded[uuid]; warned && prev == sig {
			continue // 与上一轮同一进程对：本轮不重复告警、不重复审计
		}
		fresh = append(fresh, peer)
	}
	s.warnedDowngraded = seen
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].instanceUUID < fresh[j].instanceUUID })
	return fresh
}

// handleDirectOrphan 按策略处置 direct 孤儿。
//
// （F1/F6）：auto 档每个 PID 在 killTree 前先复用① 的归属复核入口
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
			// 处置前最后一道并发复核（本缺陷修复）：受管集合在轮首取一次，若启动接管的后台重试在此
			// 期间登记成功，集合就是陈旧视图。此时唯一正确的动作是放弃处置——宁可少杀一个真孤儿
			// （下一轮 60s 后仍会抓到），也绝不因视图陈旧杀掉一台正在服务的服务器。
			// 注意：本分支**不**置 killAttempted——它没有发起任何杀树尝试，不应参与「已处置」审计。
			if s.mgr.takeoverProtects(pid, finding.WorkDir) {
				blocked++
				allDisposed = false
				s.mgr.auditOrphan("orphan.scan_dispose_blocked", finding.InstanceUUID,
					fmt.Sprintf(`{"kind":"direct_orphan","workDir":%q,"pid":%d,"reason":"startup_takeover_inflight"}`, finding.WorkDir, pid),
					false, "该 PID 属启动接管在飞实例，扫描让路不处置")
				slog.Info("direct 孤儿候选属启动接管在飞实例，本轮放弃处置（让路给启动接管）",
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
		// 仅在确有杀树尝试时落「已处置」审计（其 success 依逐 PID 实际结果判定F6）；
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
// （F1/F13）：auto 档不再无条件 Force 删除容器——
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
		// 归属复核（F1 / N5 修复）：显式两级证据，不再是恒真的伪复核。
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

// scanForeignInRegisteredWorkdirs 是运行期扫描的第四相：识别「已注册实例的工作目录下存在
// 活进程、但该实例并未认作在跑」的运行态漂移（真机场景：/home/<user>/server 下的农场由 tmux 外部拉起，
// 平台 DB 记 STOPPED 而磁盘进程仍在跑）。**本相只观测、不杀任何进程**，处置由人工「接管」动作触发
// （Manager.AdoptForeignRuntime）。
//
// 与 scanDirectOrphans 的三点关键区别：
//   - 判据是「**实例目录**下有活进程」，且工作目录限**任意路径**（不限 serversDir）——外来启动的
//     服务器目录常落在受管服务器根之外；direct 相只管「无主进程」，方向不同。
//   - 对象是**已注册实例**（内存表有该 UUID），而非内存表已丢失的孤儿。
//   - 只写观测缓存 + 落审计，不处置。
//
// 状态判据复用 mayOwnLiveProcess 的取反：RUNNING/STARTING/STOPPING 的实例本就有受管活进程，不属漂移；
// STOPPED/CRASHED 的实例目录下若仍有活进程，即「记账与磁盘不一致」。
//
// 结果整体写入 Manager 缓存（含「本轮无漂移」时的清空），供心跳上报 CP 写 runtime_drift_*；
// 审计只在漂移集**变化**时落（每 60s 一轮，同一漂移不重复刷审计）。
//
// listOK=false 表示本轮进程枚举失败：此时无权断言「无漂移」，保留上一轮观测，
// 宁让面板暂时显示陈旧漂移，也不把真实漂移误清成「已消解」。
func (s *OrphanScanner) scanForeignInRegisteredWorkdirs(procs []ScannedProcess, listOK bool) []OrphanFinding {
	if !listOK {
		return nil
	}
	m := s.mgr

	// 待查实例：已注册、有工作目录、且当前状态不属「可能仍有活进程」三态。
	type registeredDir struct {
		uuid    string
		workDir string
	}
	m.mu.RLock()
	targets := make([]registeredDir, 0, len(m.instances))
	for uuid, inst := range m.instances {
		if inst.WorkDir == "" || mayOwnLiveProcess(inst.State) {
			continue
		}
		targets = append(targets, registeredDir{uuid: uuid, workDir: filepath.Clean(inst.WorkDir)})
	}
	// 上一轮观测（用于审计去重，避免同一漂移每轮刷一条）。
	prev := make(map[string]ForeignRuntime, len(m.foreignRuntimes))
	for uuid, fr := range m.foreignRuntimes {
		prev[uuid] = fr
	}
	m.mu.RUnlock()
	// 遍历顺序固定为 UUID 序，保证 findings 顺序可复现（单测与观测日志友好）。
	sort.Slice(targets, func(i, j int) bool { return targets[i].uuid < targets[j].uuid })

	managedPIDs, _ := m.managedInstanceRuntime()
	// 本进程（Worker 自身）排除：若某实例的工作目录恰是 Worker 数据根或其在磁盘上的父目录，
	// Worker 自己会被自己的 cwd 判为「目录下的活进程」——那是自指噪声，且会让接管动作去杀自己。
	self := os.Getpid()

	observed := make(map[string]ForeignRuntime, len(targets))
	findings := make([]OrphanFinding, 0)
	for _, tgt := range targets {
		// 取最小 PID 作为该实例的稳定代表：同目录多进程时避免每轮代表抖动。
		hitPID := 0
		hitCmdline := ""
		for _, proc := range procs {
			if proc.PID <= 0 || proc.PID == self {
				continue
			}
			if _, ok := managedPIDs[proc.PID]; ok {
				continue // 受管进程（本实例或其它实例的）：非漂移
			}
			if !procInWorkDir(proc, tgt.workDir) {
				continue
			}
			if hitPID == 0 || proc.PID < hitPID {
				hitPID = proc.PID
				hitCmdline = truncateCmdline(proc.Cmdline)
			}
		}
		if hitPID == 0 {
			continue
		}
		observed[tgt.uuid] = ForeignRuntime{PID: hitPID, Cmdline: hitCmdline}
		findings = append(findings, OrphanFinding{
			Kind:         OrphanKindForeignRuntime,
			InstanceUUID: tgt.uuid,
			WorkDir:      tgt.workDir,
			PIDs:         []int{hitPID},
			Detail:       fmt.Sprintf("实例工作目录下存在未纳管活进程（pid=%d）：%s", hitPID, hitCmdline),
		})
	}

	// 整体替换观测缓存：本轮无漂移即清空（心跳据此把已消解的漂移清零）。
	m.SetForeignRuntimes(observed)

	for _, f := range findings {
		fr := observed[f.InstanceUUID]
		if old, ok := prev[f.InstanceUUID]; ok && old.PID == fr.PID {
			continue // 与上一轮同一 PID：漂移未变化，不重复落审计
		}
		m.auditOrphan("orphan.foreign_runtime_detected", f.InstanceUUID,
			fmt.Sprintf(`{"kind":"foreign_runtime","instanceUuid":%q,"workDir":%q,"pid":%d,"cmdline":%q,"policy":"observe"}`,
				f.InstanceUUID, f.WorkDir, fr.PID, fr.Cmdline),
			true, "")
		slog.Warn("运行期扫描发现实例目录下的外来活进程（只观测不杀，待人工接管）",
			"instanceId", f.InstanceUUID, "pid", fr.PID, "workDir", f.WorkDir)
	}
	return findings
}

// procInWorkDir 报告进程是否「落在」给定工作目录下：cwd 位于该目录之下，或其命令行引用了该目录。
// 判据与 directOrphanWorkDir 同源（cmdline/cwd），但**不限定 serversDir**——要求覆盖任意路径
// 的实例工作目录（真机农场目录在 /home/<user>/server 下，不在受管服务器根内）。
func procInWorkDir(proc ScannedProcess, workDir string) bool {
	if workDir == "" || workDir == "." {
		return false
	}
	if proc.Cwd != "" && pathUnder(proc.Cwd, workDir) {
		return true
	}
	return strings.Contains(proc.Cmdline, workDir)
}

// managedInstanceRuntime 返回本节点「内存表中已知实例」的根 PID 集合与工作目录集合。
// （F2 + 回归修复）：两集合的判据刻意不对称——
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
// **启动接管在飞集合（本缺陷修复）**：还需把「首拨失败、仍由启动接管后台重试」的记录并进来。
// 它们尚未登记进 instances（拨号没通，无法登记），但磁盘上确有属于本平台的活进程对；若不保护，
// auto 档会把在跑的服务器当无主进程强杀——这正是「扫描器提前启动」若不配并发隔离会引入的事故。
// 保护范围取 PID（wrapper/Java）与工作目录双口径，与上表两集合的语义一致。
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
	for _, peer := range m.takeoverPending {
		if peer.workDir != "" {
			workDirs[filepath.Clean(peer.workDir)] = struct{}{}
		}
		if peer.wrapperPID > 0 {
			pids[peer.wrapperPID] = struct{}{}
		}
		if peer.javaPID > 0 {
			pids[peer.javaPID] = struct{}{}
		}
	}
	return
}

// mayOwnLiveProcess 报告该状态是否「可能仍有活进程」，据此决定其工作目录是否受运行期扫描保护。
//
// RUNNING/STARTING/STOPPING 三态可能仍有活着的 Java（运行中 / 启动中 / 优雅停止尚未退净），
// 其工作目录须受保护，避免被误判为 direct 孤儿后强杀（F2）。STOPPED/CRASHED 的进程已死，
// 不保护——否则 Worker 硬崩重启后按 STOPPED 重登记的直接实例会「保护」掉目录下真实的残留 Java，
// 恰是 scanner 要抓的场景（N1 回归修复）。
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
// 实例生命周期掌管，容器不应被扫描强删（F1/F13）。
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

// 运行期孤儿扫描的每轮规模上限（F14；spec §5：全机进程/容器枚举有 CPU/权限开销，
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
// 枚举数量受 maxScannedProcesses 限制：超限取前 N 个并告警降级（F14）。
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
		ppid, _ := p.Ppid()
		out = append(out, ScannedProcess{PID: int(p.Pid), PPID: int(ppid), Cmdline: cmdline, Cwd: cwd})
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
				// 受管标签证据（N5）：供归属复核把强归属与「仅名字形似」区分开。
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

// verifyContainerOwnership 复核容器是否确为本平台受管容器（N5）。
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
