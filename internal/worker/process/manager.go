package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// InstanceState 实例运行状态。
type InstanceState string

const (
	StateStopped  InstanceState = "STOPPED"
	StateStarting InstanceState = "STARTING"
	StateRunning  InstanceState = "RUNNING"
	StateStopping InstanceState = "STOPPING"
	StateCrashed  InstanceState = "CRASHED"
)

// Instance 运行中的实例记账信息。
// 策略实现（direct/daemon）持有进程/socket 句柄，这里只保留 Manager 路由与查询所需字段。
type Instance struct {
	UUID         string
	Name         string
	StartCommand string
	// StopCommand 优雅停止命令（按角色派生：MC 后端 stop / 代理 end）；空时回退默认 stop。
	StopCommand  string
	WorkDir      string
	EnvVars      map[string]string
	JDKPath      string
	JDKBinPath   string
	RCONPort     int
	RCONPassword string
	// ProbePort 是实例 ServerProbe /metrics 端口（CP 分配后随 Create 下发）。
	// 心跳采集器据此自采每实例富指标（FR-060）；0=未部署探针，心跳跳过该实例。
	ProbePort int
	// ServerPort 是实例 server-port（MC 游戏端口），用于 SLP 直探（FR-446）；0=未知。
	ServerPort int
	// QueryPort 是实例 query.port（Query/GameSpy4 端口），用于 Query 直探（FR-446）；0=未开启。
	QueryPort int
	// GracefulStopTimeoutSeconds 是优雅停止超时（秒，CP 从平台设置下发，FR-063）。daemon 启动时
	// 透传到 wrapper 做超时强杀兜底；0=未指定，wrapper 回退 env/默认。值在启动时随 spec 定型。
	GracefulStopTimeoutSeconds int
	// Image 是 docker 模式的容器镜像引用（ADR-019）；仅 docker 实例使用。
	Image string
	// PortMappings 是 docker 模式的容器端口↔宿主端口映射（ADR-019）；仅 docker 实例使用。
	PortMappings []PortMapping
	// CPULimit / MemLimitMB / DiskLimitMB 是 docker 模式的资源限额（FR-079，见 ADR-019）；
	// 仅 docker 实例使用，值在 Start 时随 spec 定型。0=不限制；DiskLimitMB v1 仅记账不注入。
	CPULimit    float64
	MemLimitMB  int64
	DiskLimitMB int64
	State       InstanceState
	AutoRestart bool
	CrashCount  int
	// StartedAt 是最近一次跨入 RUNNING 的时刻（FR-459 启动宽限期的纪元起点）。
	// 零值=未知（PID 恢复等路径），此时巡检不做宽限、保持既有判定。
	StartedAt time.Time
	// operationMu 串行同一实例的生命周期操作，不阻塞其他实例。
	operationMu sync.Mutex
	// strategy 是该实例的启动策略，按 ProcessType 选择。
	// nil 表示实例已创建但尚未启动（或已 Close）。
	strategy IProcessCommand
	// strategyStale 表示运行期间收到新启动规格；当前策略继续服务，正常停止后再丢弃重建。
	strategyStale bool
	// strategyResetting 防止 Start 在锁外关闭过期策略期间被另一启动请求并发穿透。
	strategyResetting bool
	// processType 记录构造策略时的方式，用于 StopAll 判断优雅退出路径。
	processType ProcessType
}

// CrashInfo 进程非正常退出的现场信息（FR-313）：退出码 ≠ 0，或 RUNNING/STARTING 态
// 意外退出时由策略捕获，经 Manager 崩溃回调扇出、组装崩溃快照上报 CP。
type CrashInfo struct {
	// ExitCode 进程退出码；无法获知（Wait 出错无退出状态 / 容器 Wait 错误）时为 -1。
	ExitCode int
	// Signal 终止信号名（Unix，如 killed）；Windows / 非信号退出为空。
	Signal string
	// DurationMs 本次运行时长（毫秒）。
	DurationMs int64
	// OOMKilled 容器被 cgroup OOM killer 终止（FR-467）。仅 docker 模式可判定：
	// 容器退出码恒为 137 且宿主侧无信号，只有 ContainerInspect 的 State.OOMKilled 能给出这个事实。
	OOMKilled bool
	// OccurredAt 崩溃发生时刻。
	OccurredAt time.Time
}

// Manager 进程管理器。
// 它通过 IProcessCommand 策略接口支持多种启动方式（direct/daemon/docker），
// 参见 ADR-003: 守护进程 Wrapper 模式。
type Manager struct {
	mu         sync.RWMutex
	instances  map[string]*Instance
	serversDir string
	onOutput   func(instanceID string, stream string, data []byte)
	// pidDir 存放 daemon wrapper 的 PID 文件目录。
	pidDir string
	// onStateChange 实例状态变更回调，用于 StreamInstanceEvents 推送。
	onStateChange func(instanceUUID string, oldState, newState InstanceState)
	// onCrash 进程非正常退出回调（FR-313），用于组装崩溃快照上报 CP。
	onCrash func(instanceID string, info CrashInfo)
	// memGuard 启动内存闸配置；readMem 系统内存读数器（nil=真读数，测试注入，FR-317）。
	memGuard MemGuardConfig
	readMem  readSysMem
	// recoverDial / recoverKillTree / recoverPIDAlive / recoverSleep 是接管扫描兜底路径
	// （FR-325）的可注入桩：nil=真实现（strategy.Reconnect / daemon.KillPIDTree /
	// daemon.IsPIDAlive / time.Sleep），测试注入以免真拨号、真杀进程、真等待。
	recoverDial     func(s *daemonStrategy, addr string) error
	recoverKillTree func(pid int) error
	recoverPIDAlive func(pid int) bool
	recoverSleep    func(d time.Duration)
	// recoverVerifyOwner 是「处置前置存活复核」（FR-455①）的可注入桩：nil=真实现
	// （DefaultVerifyProcessOwnership，读 cmdline/cwd）。返回 false=无法确认 PID 确属目标实例 → 不杀。
	recoverVerifyOwner func(pid int, instanceUUID, workDir string, expectWrapper bool) bool
	// recoverTermTree 是「向外来进程树发 SIGTERM」的可注入桩（FR-471 接管动作）：
	// nil=真实现（signalPIDTree），测试注入以免真向无关进程发信号。
	recoverTermTree func(pid int) error
	// onOrphanAudit 是孤儿处置/误杀拦截的审计回调（FR-455/456）：由 worker main 注入落结构化审计。
	// nil 时回退 slog（仍保证「不静默」）。FR-459 的健康/自愈审计复用同一通道（action 前缀 health.*）。
	onOrphanAudit func(action, targetID, detail string, success bool, errMsg string)
	// health FR-459 健康记账：逐实例健康故障 + 崩溃重启窗口 + 熔断锁（自有锁，见 health_scan.go）。
	health *healthState
	// healthOnce 惰性初始化 health（零值 Manager / 直接结构体构造时的防御）。
	healthOnce sync.Once
	// foreignRuntimes 是「已注册实例工作目录下存在外来活进程」的观测缓存（FR-471）：
	// 由孤儿扫描每轮经 SetForeignRuntimes 写入（60s 一拍），心跳每拍经 GetAllInstanceStates 读取
	// 填充快照（避免心跳自己每拍全机枚举进程）。key=实例 UUID；空/nil 表示无漂移。由 mu 保护。
	foreignRuntimes map[string]ForeignRuntime
}

// healthState 返回健康记账，必要时惰性初始化（对非 NewManager 构造的 Manager 亦安全）。
func (m *Manager) healthState() *healthState {
	m.healthOnce.Do(func() {
		if m.health == nil {
			m.health = newHealthState()
		}
	})
	return m.health
}

// SetOrphanAuditHandler 注入孤儿处置审计回调（FR-455/456）。
// worker main 装配时调用一次；不调用则回退 slog。
func (m *Manager) SetOrphanAuditHandler(handler func(action, targetID, detail string, success bool, errMsg string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onOrphanAudit = handler
}

// verifyProcessOwnership 判定 pid 是否确属 instanceUUID 的受管进程（FR-455①）。
// 处置任何 killTree 之前调用；false=无法确认（只告警不杀）。
func (m *Manager) verifyProcessOwnership(pid int, instanceUUID, workDir string, expectWrapper bool) bool {
	if pid <= 0 {
		return false
	}
	if m.recoverVerifyOwner != nil {
		return m.recoverVerifyOwner(pid, instanceUUID, workDir, expectWrapper)
	}
	return DefaultVerifyProcessOwnership(pid, instanceUUID, workDir, expectWrapper)
}

// auditOrphan 记录一条孤儿处置审计（FR-455/456）：优先经注入回调，缺省回退 slog。
// 保证孤儿处置/误杀拦截「不静默」。
//
// 回调经 RLock 读取（与 SetOrphanAuditHandler 的写锁配对）：装配期 SetOrphanAuditHandler 在
// 锁内写入，运行期扫描/处置路径并发读取——不加锁读取 m.onOrphanAudit 属数据竞争（FR-456）。
// 回调在锁外执行，避免回调内再触达 Manager 造成重入死锁。
func (m *Manager) auditOrphan(action, targetID, detail string, success bool, errMsg string) {
	m.mu.RLock()
	handler := m.onOrphanAudit
	m.mu.RUnlock()
	if handler != nil {
		handler(action, targetID, detail, success, errMsg)
		return
	}
	slog.Warn("孤儿处置审计", "action", action, "target", targetID, "detail", detail, "success", success, "error", errMsg)
}

// ForeignRuntime 一条「已注册实例工作目录下的外来活进程」观测（FR-471）。
//
// 背景：服务器可能被运维在平台之外启动（真机场景：/home/<user>/server 下 60 台农场由 tmux 拉起），
// 平台 DB 记 STOPPED 而磁盘进程在跑。扫描观测到这种「记账与磁盘不一致」后写入本缓存，
// 经心跳上报 CP 供面板标红与人工「接管」，本身**不处置**任何进程。
type ForeignRuntime struct {
	// PID 该外来活进程的 PID。
	PID int
	// Cmdline 该进程命令行摘要（已截断，供运维识别来源）。
	Cmdline string
}

// SetForeignRuntimes 由孤儿扫描每轮写入「实例目录下外来活进程」观测（key=实例 UUID）。
// 传 nil/空表示本轮无漂移。扫描是该缓存的唯一写入方，故整体替换而非增量合并——
// 已被接管/自然退出而消失的漂移必须随本轮快照一起清掉。
func (m *Manager) SetForeignRuntimes(byUUID map[string]ForeignRuntime) {
	if m == nil {
		return
	}
	if len(byUUID) == 0 {
		m.mu.Lock()
		m.foreignRuntimes = nil
		m.mu.Unlock()
		return
	}
	cp := make(map[string]ForeignRuntime, len(byUUID))
	for uuid, fr := range byUUID {
		cp[uuid] = fr
	}
	m.mu.Lock()
	m.foreignRuntimes = cp
	m.mu.Unlock()
}

// foreignRuntime 返回某实例当前观测到的外来活进程（未观测到则零值，PID=0）。
func (m *Manager) foreignRuntime(uuid string) ForeignRuntime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.foreignRuntimes[uuid]
}

// clearForeignRuntime 清除某实例的漂移观测（接管完成后调用；其余实例的观测不受影响）。
func (m *Manager) clearForeignRuntime(uuid string) {
	m.mu.Lock()
	if m.foreignRuntimes != nil {
		delete(m.foreignRuntimes, uuid)
	}
	m.mu.Unlock()
}

// NewManager 创建进程管理器。
func NewManager(serversDir string) *Manager {
	return &Manager{
		instances:  make(map[string]*Instance),
		serversDir: serversDir,
		pidDir:     serversDir,
		health:     newHealthState(),
	}
}

// SetOutputHandler 设置进程输出回调。
// 输出会路由到此处（用于桥接 WebSocket 终端）。
func (m *Manager) SetOutputHandler(handler func(instanceID string, stream string, data []byte)) {
	m.onOutput = handler
}

// SetStateChangeHandler 设置实例状态变更回调。
// 每次实例状态发生转换时调用，用于 StreamInstanceEvents 推送。
func (m *Manager) SetStateChangeHandler(handler func(instanceUUID string, oldState, newState InstanceState)) {
	m.onStateChange = handler
}

// SetCrashHandler 设置进程非正常退出回调（FR-313）。
// 各策略在崩溃判定路径（direct/docker waitLoop、daemon 经 wrapper 退出事件）捕获现场后
// 经此扇出；回调须快速返回（上报本身应异步），不得阻塞策略的崩溃处理。
func (m *Manager) SetCrashHandler(handler func(instanceID string, info CrashInfo)) {
	m.onCrash = handler
}

// emitCrash 触发崩溃回调（未设置则忽略）。
// FR-459：同时把本次崩溃计入熔断滚动窗口（覆盖 direct/docker waitLoop 与 daemon 退出事件三条崩溃来源）。
func (m *Manager) emitCrash(instanceID string, info CrashInfo) {
	m.noteProcessCrash(instanceID)
	if m.onCrash != nil {
		m.onCrash(instanceID, info)
	}
}

// emitStateChange 触发状态变更回调。调用方需持有或不持有锁均可（回调在锁外执行）。
func (m *Manager) emitStateChange(instanceUUID string, oldState, newState InstanceState) {
	if m.onStateChange != nil && oldState != newState {
		m.onStateChange(instanceUUID, oldState, newState)
	}
}

// markStrategyState 由策略在检测到自身异步状态变化（如 wrapper/子进程退出 = 崩溃或停止）时回调，
// 把变化同步到 Manager 的实例记账（inst.State）并扇出状态事件。调用方不得持有策略锁。
//
// 修复点：此前策略异步崩溃只更新策略内部状态、未回写 inst.State，Manager 仍记 RUNNING，
// 导致 Start() 守卫（仅允许 STOPPED/CRASHED 启动）拒绝崩溃实例重启，必须重启整个 Worker 才能恢复。
// oldState 取自 Manager 记账（而非策略内部状态），与 Start/Stop 的记账保持单一事实源。
func (m *Manager) markStrategyState(uuid string, newState InstanceState) {
	m.mu.Lock()
	inst, ok := m.instances[uuid]
	if !ok {
		m.mu.Unlock()
		return
	}
	oldState := inst.State
	if oldState == newState {
		m.mu.Unlock()
		return
	}
	// 重启（stop→start）复用同一策略对象时，上一代 wrapper 的优雅退出回报可能晚于新一轮
	// startLocked 置 STARTING（reapWrapper 抢锁先于 Start 替换 wrapperCmd，代际守卫拦不住）。
	// Stop/Start 经 operationMu 串行化，STARTING 期间不可能有真正的停止在飞；而新一代秒崩
	// 上报的是 CRASHED（启动后策略态非 Stopping）。故 STARTING 收到 STOPPED 必属旧代迟到
	// 讣告——忽略，避免把健康的新实例记成 STOPPED（进程活着、面板显停止的脱同步）。
	if newState == StateStopped && oldState == StateStarting {
		m.mu.Unlock()
		slog.Info("忽略上一代 wrapper 的迟到停止回报（新实例正在启动）", "instanceId", uuid)
		return
	}
	inst.State = newState
	m.mu.Unlock()
	m.emitStateChange(uuid, oldState, newState)
}

// InstanceSnapshot 表示单个实例的状态快照（用于心跳上报）。
type InstanceSnapshot struct {
	UUID       string
	State      string // STOPPED, STARTING, RUNNING, STOPPING, CRASHED
	ProbePort  int    // ServerProbe /metrics 端口；>0 且 RUNNING 时心跳采集器自采富指标（FR-060）
	ServerPort int    // server-port（MC 游戏端口）；>0 时心跳做 SLP 直探（FR-446）
	QueryPort  int    // query.port；>0 时心跳做 Query 直探（FR-446）
	PID        int    // 受管实例根进程 PID；>0 时心跳可采集进程 TOPN（FR-170）
	// Health 是 FR-459 健康巡检结论标识（healthy/suspected_dead/dead/crashed/circuit_broken；空=无结论）。
	// 经心跳上报 CP，供健康总览墙（FR-461）与动态基线（FR-462）消费。
	Health string
	// StatusReason 是健康巡检给出的原因说明（假死/熔断等，FR-459）；空=正常。
	StatusReason string
	// ForeignPID 是「已注册实例工作目录下存在活进程、但本实例未认作运行」时该进程 PID（0=无漂移，FR-471）。
	// 由孤儿扫描第 4 相观测、经 SetForeignRuntimes 缓存供本快照读取；仅观测，不表示平台已处置。
	ForeignPID int
	// ForeignCmdline 上述漂移进程的命令行摘要（已截断）。
	ForeignCmdline string
}

// GetAllInstanceStates 返回所有实例的状态快照（用于心跳上报）。
func (m *Manager) GetAllInstanceStates() []InstanceSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make([]InstanceSnapshot, 0, len(m.instances))
	for uuid, inst := range m.instances {
		// 以管理器记账状态为准；仅当「记账为 RUNNING 但策略已崩溃」时用策略实时状态纠正为 CRASHED。
		// （否则会把停止时 inst.State 已置的 STOPPED 被策略的瞬态 STOPPING 覆盖，导致无法再次启动。）
		state := inst.State
		if inst.State == StateRunning && inst.strategy != nil && inst.strategy.State() == StateCrashed {
			state = StateCrashed
		}
		pid := 0
		if inst.strategy != nil {
			pid = inst.strategy.GetPID()
		}
		fault, reason := m.healthFault(uuid)
		// FR-471 漂移观测：识别由扫描每轮写入本缓存，此处只做搬运（缓存无该 UUID 即零值 0/空）。
		foreign := m.foreignRuntimes[uuid]
		states = append(states, InstanceSnapshot{
			UUID:           uuid,
			State:          string(state),
			ProbePort:      inst.ProbePort,
			ServerPort:     inst.ServerPort,
			QueryPort:      inst.QueryPort,
			PID:            pid,
			Health:         fault,
			StatusReason:   reason,
			ForeignPID:     foreign.PID,
			ForeignCmdline: foreign.Cmdline,
		})
	}
	return states
}

// Create 创建实例（但不启动）。processType 决定启动方式（direct/daemon/docker/rcon）。
// jdkPath / jdkBinPath 非空时会被注入到实例启动时的环境。
// stopCommand 为优雅停止命令（按角色派生：MC 后端 stop / 代理 end），空时回退默认 stop。
// probePort 为实例 ServerProbe /metrics 端口（CP 分配），供心跳采集器自采富指标（FR-060）；0=未部署。
// gracefulStopTimeoutSeconds 为优雅停止超时（秒，CP 从平台设置下发，FR-063）；0=未指定，wrapper 回退默认。
func (m *Manager) Create(uuid, name, startCommand, stopCommand, workDir string, envVars map[string]string, autoRestart bool, processType ProcessType, jdkPath, jdkBinPath string, probePort, gracefulStopTimeoutSeconds int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.instances[uuid]; exists {
		return fmt.Errorf("实例 %s 已存在", uuid)
	}
	// 同 UUID 重新登记时回收上一代健康记账（健康故障/崩溃窗口/熔断态/自愈计数），
	// 避免历史崩溃污染新实例的熔断判定（FR-459 复审项 10）。
	m.dropHealthAccounting(uuid)

	m.instances[uuid] = &Instance{
		UUID:                       uuid,
		Name:                       name,
		StartCommand:               startCommand,
		StopCommand:                stopCommand,
		WorkDir:                    workDir,
		EnvVars:                    envVars,
		JDKPath:                    jdkPath,
		JDKBinPath:                 jdkBinPath,
		ProbePort:                  probePort,
		GracefulStopTimeoutSeconds: gracefulStopTimeoutSeconds,
		State:                      StateStopped,
		AutoRestart:                autoRestart,
		processType:                processType,
	}

	slog.Info("实例已创建", "instanceId", uuid, "name", name, "autoRestart", autoRestart, "processType", processType, "jdkPath", jdkPath, "probePort", probePort, "gracefulStopTimeoutSeconds", gracefulStopTimeoutSeconds)
	return nil
}

// SetGracefulStopTimeout 更新已登记实例的优雅停止超时（秒）。
// 供 CP 在「重新注册已存在实例」时刷新该值，使设置变更对下一次启动生效（值在 Start 时随 spec 定型）。
// 实例不存在则忽略（与 SetRCONConfig 的容错风格一致，但此处不报错以免阻塞启动路径）。
func (m *Manager) SetGracefulStopTimeout(uuid string, seconds int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[uuid]; ok {
		inst.GracefulStopTimeoutSeconds = seconds
	}
}

// SetProbePort 更新已登记实例的 ServerProbe /metrics 端口（FR-411 补口）。
// 供 CP 幂等重注册时刷新：导入/历史实例起初 probe_port=0，CP 部署探针前补分配端口后经
// 重注册下发，Worker 内存表随即生效——心跳下一拍即开始采集该实例指标，无需等待实例重启。
// 实例不存在则忽略（容错风格与 SetGracefulStopTimeout 一致）。
func (m *Manager) SetProbePort(uuid string, port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[uuid]; ok {
		inst.ProbePort = port
	}
}

// SetServerPort 更新已登记实例的 server-port（MC 游戏端口），供 SLP 直探（FR-446）。
// 供 CP 幂等重注册时刷新：端口迁移/导入实例后经重注册下发，Worker 内存表随即生效，
// 心跳下一拍即按新端口直探（无需重启进程）。实例不存在则忽略（容错风格与 SetProbePort 一致）。
func (m *Manager) SetServerPort(uuid string, port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[uuid]; ok {
		inst.ServerPort = port
	}
}

// SetQueryPort 更新已登记实例的 query.port，供 Query 直探（FR-446）。容错风格与 SetProbePort 一致。
func (m *Manager) SetQueryPort(uuid string, port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[uuid]; ok {
		inst.QueryPort = port
	}
}

// SetDockerConfig 设置已登记实例的 docker 镜像、端口映射与资源限额（ADR-019 / FR-079）。
// 由 CP 在创建/重注册 docker 实例时下发，使镜像/端口/限额对下一次启动生效（值在 Start 时随 spec 定型）。
// 实例不存在则忽略（与 SetGracefulStopTimeout 容错风格一致，不阻塞启动路径）。
func (m *Manager) SetDockerConfig(uuid, image string, mappings []PortMapping, cpuLimit float64, memLimitMB, diskLimitMB int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[uuid]; ok {
		inst.Image = image
		inst.PortMappings = mappings
		inst.CPULimit = cpuLimit
		inst.MemLimitMB = memLimitMB
		inst.DiskLimitMB = diskLimitMB
	}
}

// SetResourceLimits 只刷新实例记账里的 CPU/内存限额，其余启动配置不动（N-3）。
//
// 与 SetDockerConfig 的区别：后者要求 CP 把镜像/端口映射一并重推（一次完整重注册），
// 而启动/重启请求随附的限额是**运行期配额收紧**这一个字段的变化，不该顺带重写镜像与端口。
//
// 为什么必须标记 strategyStale：启动策略在构造时就定型了限额（见 startLocked 的 CommandSpec），
// 若不置过期标记，startLocked 会**复用旧策略**——收紧值照样不生效，只是把缺陷挪了个位置。
// 置位后由既有路径处理：运行中仅标记（cgroup 限额无法热改，停止时随旧策略一并丢弃），
// 停止/崩溃态则在下次 startLocked 入口锁外 Close 旧策略并重建（复用一条已验证的释放路径，
// 避免在此处重复实现「锁外关闭」而引入新的资源泄漏）。
//
// 仅在**值真的变化**时置过期：startLocked 每次启动都会调用本函数，无条件置位会让停止/崩溃态
// 的实例每次启停都被迫重建策略（多一次多余的构造与资源释放），且掩盖「限额未变」这一事实。
func (m *Manager) SetResourceLimits(uuid string, cpuLimit float64, memLimitMB int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[uuid]
	if !ok {
		return
	}
	if inst.CPULimit == cpuLimit && inst.MemLimitMB == memLimitMB {
		return
	}
	inst.CPULimit = cpuLimit
	inst.MemLimitMB = memLimitMB
	if inst.strategy != nil {
		inst.strategyStale = true
	}
}

// SetLaunchConfig 刷新已存在实例的启动配置，供 CP 幂等重注册时下发（FR-233）。
// 运行中的策略继续服务且仅标记过期；停止/崩溃态则立即丢弃旧策略，确保下一次启动采用新规格。
func (m *Manager) SetLaunchConfig(uuid, startCommand, jdkPath, jdkBinPath string, envVars map[string]string, autoRestart bool) {
	var stale IProcessCommand
	var resetting *Instance
	m.mu.Lock()
	if inst, ok := m.instances[uuid]; ok {
		inst.StartCommand = startCommand
		inst.JDKPath = jdkPath
		inst.JDKBinPath = jdkBinPath
		inst.EnvVars = envVars
		inst.AutoRestart = autoRestart
		if inst.strategy != nil {
			switch inst.State {
			case StateRunning, StateStarting, StateStopping:
				inst.strategyStale = true
			default:
				stale = inst.strategy
				inst.strategy = nil
				inst.strategyStale = false
				inst.strategyResetting = true
				resetting = inst
			}
		}
	}
	m.mu.Unlock()
	if stale != nil {
		_ = stale.Close()
	}
	if resetting != nil {
		m.mu.Lock()
		if current, ok := m.instances[uuid]; ok && current == resetting {
			current.strategyResetting = false
		}
		m.mu.Unlock()
	}
}

// SetRCONConfig 设置实例的 RCON 配置。
func (m *Manager) SetRCONConfig(uuid string, port int, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, exists := m.instances[uuid]
	if !exists {
		return fmt.Errorf("实例 %s 不存在", uuid)
	}

	inst.RCONPort = port
	inst.RCONPassword = password
	return nil
}

// GetRCONConfig 获取实例的 RCON 配置。
func (m *Manager) GetRCONConfig(uuid string) (port int, password string, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inst, exists := m.instances[uuid]
	if !exists {
		return 0, "", fmt.Errorf("实例 %s 不存在", uuid)
	}

	return inst.RCONPort, inst.RCONPassword, nil
}

// GetInstancePID 获取实例进程的 PID。
// 策略未启动或已退出时返回 0。
func (m *Manager) GetInstancePID(uuid string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inst, exists := m.instances[uuid]
	if !exists || inst.strategy == nil {
		return 0
	}

	return inst.strategy.GetPID()
}

// dockerStatser 由 docker 策略实现：经 Engine API 采集容器 cgroup 资源指标。
// 仅 docker 实例满足此接口，供 Manager 在宿主 PID 不可见时取容器 CPU%/内存（#6）。
type dockerStatser interface {
	Stats(ctx context.Context) (cpuPercent float64, memBytes int64, memLimitBytes int64, ok bool)
}

// GetInstanceDockerStats 采集 docker 实例的容器资源指标（CPU%/内存/内存上限，字节）。
// 非 docker 实例、未运行或采集失败返回 ok=false，调用方据此回退 OS 进程内存路径。
func (m *Manager) GetInstanceDockerStats(ctx context.Context, uuid string) (cpuPercent float64, memBytes int64, memLimitBytes int64, ok bool) {
	m.mu.RLock()
	inst, exists := m.instances[uuid]
	m.mu.RUnlock()
	if !exists || inst.strategy == nil {
		return 0, 0, 0, false
	}
	ds, isDocker := inst.strategy.(dockerStatser)
	if !isDocker {
		return 0, 0, 0, false
	}
	return ds.Stats(ctx)
}

// lockInstanceOperation 获取实例级生命周期锁，并复核等待期间实例未被移除或替换。
func (m *Manager) lockInstanceOperation(uuid string) (*Instance, bool) {
	m.mu.RLock()
	inst, exists := m.instances[uuid]
	m.mu.RUnlock()
	if !exists {
		return nil, false
	}

	inst.operationMu.Lock()
	m.mu.RLock()
	current, stillExists := m.instances[uuid]
	m.mu.RUnlock()
	if !stillExists || current != inst {
		inst.operationMu.Unlock()
		return nil, false
	}
	return inst, true
}

// Start 启动实例。按实例的 ProcessType 选择策略；首次启动时惰性构造策略。
func (m *Manager) Start(uuid string) error {
	inst, exists := m.lockInstanceOperation(uuid)
	if !exists {
		return fmt.Errorf("实例 %s 不存在", uuid)
	}
	defer inst.operationMu.Unlock()
	return m.startLocked(uuid, inst)
}

// startLocked 在已持有实例生命周期锁时启动实例。
func (m *Manager) startLocked(uuid string, inst *Instance) error {
	m.mu.Lock()
	current, exists := m.instances[uuid]
	if !exists || current != inst {
		m.mu.Unlock()
		return fmt.Errorf("实例 %s 不存在或已被替换", uuid)
	}
	if inst.State != StateStopped && inst.State != StateCrashed {
		m.mu.Unlock()
		return fmt.Errorf("实例 %s 当前状态 %s 无法启动", uuid, inst.State)
	}
	if inst.strategyResetting {
		m.mu.Unlock()
		return fmt.Errorf("实例 %s 正在刷新启动策略，请稍后重试", uuid)
	}

	// Stop 收尾与配置更新可能交错：Stop 已检查 stale 后，SetLaunchConfig 才在 STOPPING 窗口
	// 标记旧策略过期。Start 必须在构造新策略前锁外关闭旧策略，同时阻止并发启动穿透。
	if inst.strategy != nil && inst.strategyStale {
		stale := inst.strategy
		inst.strategy = nil
		inst.strategyStale = false
		inst.strategyResetting = true
		m.mu.Unlock()
		_ = stale.Close()
		m.mu.Lock()
		current, stillExists := m.instances[uuid]
		if !stillExists || current != inst {
			m.mu.Unlock()
			return fmt.Errorf("实例 %s 不存在", uuid)
		}
		inst.strategyResetting = false
		if inst.State != StateStopped && inst.State != StateCrashed {
			state := inst.State
			m.mu.Unlock()
			return fmt.Errorf("实例 %s 当前状态 %s 无法启动", uuid, state)
		}
	}

	// 启动前校验 java 版本（仅宿主进程模式；docker 的 java 在容器内不适用）。
	// 避免无绑定 JDK / 版本不符的 MC 实例以 UnsupportedClassVersionError 静默崩在
	// 游戏服自身日志、面板只见 CRASHED 无因（BUG-012）。校验失败保持原状态、返回明确错误。
	// 启动前实时内存闸（FR-317，先于 Java 预检——对所有 processType 普适）：
	// 可用内存不足以再塞下这个实例即拒绝，保持原状态返回明确错误。
	// docker 模式同样过闸（容器内存也吃宿主，按 MemLimitMB 估算）。
	if err := m.preflightMemory(inst); err != nil {
		m.mu.Unlock()
		return err
	}

	if inst.processType != ProcessTypeDocker {
		if err := preflightJavaVersion(CommandSpec{
			StartCommand: inst.StartCommand,
			JavaHome:     inst.JDKPath,
			JDKBinPath:   inst.JDKBinPath,
		}); err != nil {
			m.mu.Unlock()
			return err
		}
	}

	// 惰性构造策略：CRASHED 重启时复用已构造的策略（保留连接），首次启动则新建。
	if inst.strategy == nil {
		spec := CommandSpec{
			UUID:                       inst.UUID,
			Name:                       inst.Name,
			StartCommand:               inst.StartCommand,
			StopCommand:                inst.StopCommand,
			WorkDir:                    inst.WorkDir,
			EnvVars:                    inst.EnvVars,
			JavaHome:                   inst.JDKPath,
			JDKBinPath:                 inst.JDKBinPath,
			AutoRestart:                inst.AutoRestart,
			ProcessType:                inst.processType,
			ProbePort:                  inst.ProbePort,
			GracefulStopTimeoutSeconds: inst.GracefulStopTimeoutSeconds,
			Image:                      inst.Image,
			PortMappings:               inst.PortMappings,
			CPULimit:                   inst.CPULimit,
			MemLimitMB:                 inst.MemLimitMB,
			DiskLimitMB:                inst.DiskLimitMB,
		}
		strategy, err := m.newStrategy(spec)
		if err != nil {
			inst.State = StateCrashed
			m.mu.Unlock()
			return fmt.Errorf("构造启动策略失败: %w", err)
		}
		inst.strategy = strategy
	}
	strategy := inst.strategy
	oldState := inst.State
	inst.State = StateStarting
	m.mu.Unlock()

	// FR-459：启动即开启新的健康纪元——上一代的假死/崩溃故障标识不再适用，先清空，
	// 避免重启后在启动宽限期内继续对外暴露陈旧原因（宽限期内巡检不动故障标识）。
	m.clearHealthFault(uuid)

	m.emitStateChange(uuid, oldState, StateStarting)

	if err := strategy.Start(context.Background()); err != nil {
		m.mu.Lock()
		prevState := inst.State
		inst.State = StateCrashed
		m.mu.Unlock()
		m.emitStateChange(uuid, prevState, StateCrashed)
		return fmt.Errorf("启动实例 %s 失败: %w", uuid, err)
	}

	// 仅在记账仍为 STARTING 时才落 RUNNING（CAS）。若进程在 strategy.Start 返回前就极速崩溃，
	// 其 waitLoop 可能已通过 markStrategyState 把记账抢先置为 CRASHED；此处若无条件覆盖为 RUNNING，
	// 会把 CRASHED 改回 RUNNING，导致实例「已死却记 RUNNING」，Start 守卫从此拒绝重启，必须重启
	// 整个 Worker 才能恢复（与 markStrategyState 的修复意图一致，补上其遗留的启动窗口竞态）。
	m.mu.Lock()
	startedClean := inst.State == StateStarting
	if startedClean {
		inst.State = StateRunning
		// FR-459 启动宽限期的纪元起点：Start 返回 RUNNING 即开始计时，宽限期内不做假死判定
		// （慢启动 MC 常达 90s+，否则会被连续探针失败误判并反复重启）。
		inst.StartedAt = time.Now()
	}
	m.mu.Unlock()
	if !startedClean {
		// 启动窗口内已被异步崩溃改写（CRASHED 等），保留该状态、不再扇出 RUNNING。
		slog.Warn("实例启动后在启动窗口内即崩溃，保留崩溃记账", "instanceId", uuid)
		return nil
	}
	m.emitStateChange(uuid, StateStarting, StateRunning)
	slog.Info("实例已启动", "instanceId", uuid)
	return nil
}

// Stop 停止实例。
func (m *Manager) Stop(uuid string) error {
	inst, exists := m.lockInstanceOperation(uuid)
	if !exists {
		return fmt.Errorf("实例 %s 未运行", uuid)
	}
	defer inst.operationMu.Unlock()
	return m.stopLocked(uuid, inst)
}

// stopLocked 在已持有实例生命周期锁时停止实例。
func (m *Manager) stopLocked(uuid string, inst *Instance) error {
	m.mu.Lock()
	current, exists := m.instances[uuid]
	if !exists || current != inst || inst.strategy == nil {
		m.mu.Unlock()
		return fmt.Errorf("实例 %s 未运行", uuid)
	}
	strategy := inst.strategy
	oldState := inst.State
	inst.State = StateStopping
	m.mu.Unlock()
	m.emitStateChange(uuid, oldState, StateStopping)

	if err := strategy.Stop(); err != nil {
		return fmt.Errorf("停止实例 %s 失败: %w", uuid, err)
	}

	// 运行期间保存过新规格时，保持 STOPPING 直到旧策略资源释放，再允许下一次 Start 重建。
	m.mu.Lock()
	replaceStrategy := inst.strategy == strategy && inst.strategyStale
	if replaceStrategy {
		inst.strategy = nil
		inst.strategyStale = false
	}
	m.mu.Unlock()
	if replaceStrategy {
		_ = strategy.Close()
	}

	m.mu.Lock()
	oldState = inst.State
	inst.State = StateStopped
	inst.CrashCount = 0
	m.mu.Unlock()
	// FR-459：人工/正常停止清空崩溃重启窗口（给予一次干净的重启配额，避免历史崩溃影响后续熔断判定）。
	m.clearRestartWindow(uuid)
	m.emitStateChange(uuid, oldState, StateStopped)
	return nil
}

// Restart 优雅重启实例：运行中先发停止命令让游戏服正常关服（保存世界、释放 world/session.lock、
// 输出关服日志），等旧进程完全退出后再启动；已停止/崩溃则直接启动。整个过程持有同一实例生命周期锁。
//
// 此前用强杀（killLocked）+ 立即启动：daemon 模式强杀 wrapper 进程树时，Unix 上自成进程组的 Java
// 可能未被杀到而沦为孤儿、仍占 world 锁，新进程随即启动即撞 Paper `SessionLock$ExceptionWorldConflict`
// （真机复现）；且强杀无关服日志、跳过世界保存。改走优雅停止——stopLocked 下发 stop 控制帧由
// wrapper 优雅关服（自带超时强杀兜底、复用同一策略）；startLocked 内 strategy.Start() 的
// daemon.WaitForPriorExit 依 PID 文件等旧 wrapper/Java 全退出（best-effort 上限）再拉起新进程，
// reapWrapper 的 `d.wrapperCmd != cmd` 陈旧守卫保证旧 reaper 不误改新实例状态。
func (m *Manager) Restart(uuid string) error {
	inst, exists := m.lockInstanceOperation(uuid)
	if !exists {
		return fmt.Errorf("实例 %s 不存在", uuid)
	}
	defer inst.operationMu.Unlock()
	return m.restartLocked(uuid, inst)
}

// restartLocked 在已持有实例生命周期锁时优雅重启：运行类先优雅停止、等旧进程退出，再启动。
func (m *Manager) restartLocked(uuid string, inst *Instance) error {
	m.mu.RLock()
	state := inst.State
	m.mu.RUnlock()
	// 运行中（含启动/停止中）先优雅停止、等旧进程退出；STOPPED/CRASHED 无活跃进程，直接（重）启动。
	if state == StateRunning || state == StateStarting || state == StateStopping {
		if err := m.stopLocked(uuid, inst); err != nil {
			return err
		}
	}
	return m.startLocked(uuid, inst)
}

// RestartIfRunning 是 FR-459 假死自愈的受锁入口：与 Restart 相同地优雅重启，但**仅当**实例在
// 持锁复核时仍处 `RUNNING` 才动作。
//
// 只接受 RUNNING（spec §5：自愈不得与人工操作抢状态）：
//   - STOPPED/CRASHED 已被人工停掉或本就未运行 → 拒绝，绝不把「已被人工停止的实例」误启动；
//   - STARTING/STOPPING 是过渡态：假死判定的前提是「进程在跑且不响应」，而过渡态本身尚未定型，
//     此时重启会与人工 Start/Stop 抢生命周期（原实现允许过渡态，与 spec 意图相悖，FR-459 复审项 4）。
//
// 复用 stopLocked/startLocked 既有优雅路径，**不新增杀进程路径**（spec §1/§5）。
func (m *Manager) RestartIfRunning(uuid string) error {
	inst, exists := m.lockInstanceOperation(uuid)
	if !exists {
		return fmt.Errorf("实例 %s 不存在", uuid)
	}
	defer inst.operationMu.Unlock()

	m.mu.RLock()
	state := inst.State
	m.mu.RUnlock()
	if state != StateRunning {
		return fmt.Errorf("实例 %s 当前状态 %s 非运行中，跳过假死自愈", uuid, state)
	}
	return m.restartLocked(uuid, inst)
}

// Kill 强制终止实例。
func (m *Manager) Kill(uuid string) error {
	inst, exists := m.lockInstanceOperation(uuid)
	if !exists {
		return fmt.Errorf("实例 %s 不存在", uuid)
	}
	defer inst.operationMu.Unlock()
	return m.killLocked(uuid, inst)
}

// killLocked 在已持有实例生命周期锁时强制终止实例。
func (m *Manager) killLocked(uuid string, inst *Instance) error {
	m.mu.Lock()
	current, exists := m.instances[uuid]
	if !exists || current != inst {
		m.mu.Unlock()
		return fmt.Errorf("实例 %s 不存在或已被替换", uuid)
	}
	strategy := inst.strategy
	inst.strategy = nil
	inst.strategyStale = false
	m.mu.Unlock()

	if strategy != nil {
		if err := strategy.Kill(); err != nil {
			slog.Warn("强制终止实例策略失败，继续清理生命周期状态", "instanceId", uuid, "error", err)
		}
		if err := strategy.Close(); err != nil {
			slog.Warn("关闭实例策略失败，继续清理生命周期状态", "instanceId", uuid, "error", err)
		}
	}

	m.mu.Lock()
	oldState := inst.State
	inst.State = StateStopped
	m.mu.Unlock()
	// FR-459：强制终止同样清空崩溃重启窗口。
	m.clearRestartWindow(uuid)
	m.emitStateChange(uuid, oldState, StateStopped)
	return nil
}

// FR-471 接管：SIGTERM 之后等待进程退出的有界轮询（约 30s）。
// Paper 的 shutdown hook 需保存世界、释放 world/session.lock，秒级到十几秒属正常；
// 超时即升级强杀（进程已不响应，继续等只会拖住 CP 的同步调用）。
const (
	adoptTermWaitAttempts = 30
	adoptTermWaitInterval = time.Second
)

// AdoptForeignRuntime 接管实例工作目录下的外来活进程（FR-471）。
//
// 语义 = 「先让外来进程优雅退场，再以受管方式拉起」：对外来进程树发 SIGTERM（Paper 走 shutdown hook
// 保存世界后正常退出），有界等待其退出，超时升级 SIGKILL 强杀整棵进程树，随后按正常路径 Start，
// 使平台记账与磁盘事实对齐、此后由本平台掌管该实例生命周期。
//
// 与孤儿扫描（FR-456）的分工：那里处置的是**无主**进程（Worker 内存表已丢失该实例），默认只观测；
// 这里处置的是**已注册实例**目录下的进程，是人工确认后的显式接管动作，故允许停止进程。
//
// 返回被停止的外来进程 PID（0=接管前本无漂移，等价于一次正常启动）。
// 实例未注册返回 error；外来进程未能退出时不启动（避免双开），并落审计。
func (m *Manager) AdoptForeignRuntime(uuid string) (int, error) {
	inst, exists := m.lockInstanceOperation(uuid)
	if !exists {
		return 0, fmt.Errorf("实例 %s 不存在", uuid)
	}
	defer inst.operationMu.Unlock()

	fr := m.foreignRuntime(uuid)
	if fr.PID <= 0 {
		// 无漂移：直接走正常启动路径（幂等；已 RUNNING 时由 startLocked 的状态守卫拒绝并给出明确错误）。
		if err := m.startLocked(uuid, inst); err != nil {
			return 0, err
		}
		return 0, nil
	}
	m.mu.RLock()
	workDir := inst.WorkDir
	m.mu.RUnlock()

	if err := m.terminateForeignProcessTree(uuid, workDir, fr); err != nil {
		m.auditOrphan("orphan.foreign_runtime_adopt_blocked", uuid,
			m.foreignDriftDetail(uuid, fr, "terminate_failed"), false, err.Error())
		return fr.PID, fmt.Errorf("接管实例 %s 失败：工作目录下的外来进程 pid=%d 未能退出: %w", uuid, fr.PID, err)
	}
	// 漂移进程已退场：先清该实例的观测，避免下一次心跳仍报旧 PID（扫描下一轮也会自然收敛）。
	m.clearForeignRuntime(uuid)

	if err := m.startLocked(uuid, inst); err != nil {
		return fr.PID, fmt.Errorf("外来进程 pid=%d 已退出，但以受管方式启动实例 %s 失败: %w", fr.PID, uuid, err)
	}
	m.auditOrphan("orphan.foreign_runtime_adopted", uuid,
		m.foreignDriftDetail(uuid, fr, "adopted"), true, "")
	slog.Info("已接管实例目录下的外来运行时", "instanceId", uuid, "stoppedPid", fr.PID)
	return fr.PID, nil
}

// foreignDriftDetail 组装漂移/接管审计的 detail JSON（FR-471）。
func (m *Manager) foreignDriftDetail(uuid string, fr ForeignRuntime, phase string) string {
	return fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"cmdline":%q,"phase":%q,"policy":"adopt"}`,
		uuid, fr.PID, fr.Cmdline, phase)
}

// terminateForeignProcessTree 先对外来进程树发 SIGTERM 等其优雅退出，超时升级 SIGKILL 强杀整树（FR-471）。
// 返回 nil 表示该 PID（连同其进程组）已退出。
//
// 处置前置归属复核（与 FR-455① 同一纪律）：漂移观测与人工点击之间可能间隔数十秒（扫描 60s 一拍），
// 期间该 PID 可能已被 OS 回收给无关进程。发信号前先用 cmdline/cwd 复核它仍属该实例目录；复核不通过
// 即拒绝处置并返回错误（宁可不接管，也不误杀无关进程）。
func (m *Manager) terminateForeignProcessTree(uuid, workDir string, fr ForeignRuntime) error {
	pid := fr.PID
	if pid <= 0 || !m.pidAlive(pid) {
		return nil // 已自行退出：无需处置，直接进入受管启动
	}
	if !m.verifyProcessOwnership(pid, uuid, workDir, false) {
		return fmt.Errorf("无法确认 pid=%d 仍属实例 %s 的目录（%s），拒绝处置（PID 可能已被回收）", pid, uuid, workDir)
	}
	if err := m.signalTree(pid); err != nil {
		// 发信号失败不立即判死：进程可能恰好已退出，交由下面的存活轮询与强杀兜底。
		slog.Warn("接管：向外来进程树发送 SIGTERM 失败，继续等待其退出", "pid", pid, "error", err)
	} else {
		slog.Info("接管：已向外来进程树发送 SIGTERM，等待其优雅退出", "pid", pid)
	}
	for attempt := 0; attempt < adoptTermWaitAttempts; attempt++ {
		if !m.pidAlive(pid) {
			return nil
		}
		m.retrySleep(adoptTermWaitInterval)
	}
	slog.Warn("接管：外来进程优雅退出超时，升级强杀进程树", "pid", pid)
	if err := m.killTree(pid); err != nil {
		slog.Warn("接管：强杀外来进程树报错（以存活复核为准）", "pid", pid, "error", err)
	}
	if m.waitPIDsGone([]int{pid}) {
		return nil
	}
	return fmt.Errorf("进程 pid=%d 在强杀后仍未退出", pid)
}

// signalTree 向外来进程树发送 SIGTERM；测试经 recoverTermTree 注入假信号器。
func (m *Manager) signalTree(pid int) error {
	if m.recoverTermTree != nil {
		return m.recoverTermTree(pid)
	}
	return signalPIDTree(pid)
}

// GetState 获取实例状态。
func (m *Manager) GetState(uuid string) (InstanceState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inst, exists := m.instances[uuid]
	if !exists {
		return "", fmt.Errorf("实例 %s 不存在", uuid)
	}
	return inst.State, nil
}

// ListInstances 返回所有实例的 UUID 列表。
func (m *Manager) ListInstances() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	uuids := make([]string, 0, len(m.instances))
	for uuid := range m.instances {
		uuids = append(uuids, uuid)
	}
	return uuids
}

// GetInstance 获取实例信息。
func (m *Manager) GetInstance(uuid string) (*Instance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inst, exists := m.instances[uuid]
	return inst, exists
}

// SendCommand 向实例发送命令（通过 stdin）。
func (m *Manager) SendCommand(uuid, command string) error {
	m.mu.RLock()
	inst, exists := m.instances[uuid]
	m.mu.RUnlock()

	if !exists || inst.strategy == nil {
		return fmt.Errorf("实例 %s 未运行", uuid)
	}

	return inst.strategy.SendCommand(command)
}

// Remove 移除实例记录。
func (m *Manager) Remove(uuid string) error {
	m.mu.RLock()
	inst, exists := m.instances[uuid]
	m.mu.RUnlock()
	if !exists {
		return nil
	}

	inst.operationMu.Lock()
	defer inst.operationMu.Unlock()
	return m.removeLocked(uuid, inst)
}

// removeLocked 在已持有实例生命周期锁时移除实例记录。
func (m *Manager) removeLocked(uuid string, inst *Instance) error {
	m.mu.Lock()
	current, exists := m.instances[uuid]
	if !exists {
		m.mu.Unlock()
		return nil
	}
	if current != inst {
		m.mu.Unlock()
		return fmt.Errorf("实例 %s 已被替换，拒绝移除新实例", uuid)
	}
	strategy := inst.strategy
	inst.strategy = nil
	inst.strategyStale = false
	m.mu.Unlock()

	if strategy != nil {
		if err := strategy.Kill(); err != nil {
			slog.Warn("移除实例时强制终止策略失败，继续删除实例记录", "instanceId", uuid, "error", err)
		}
		if err := strategy.Close(); err != nil {
			slog.Warn("移除实例时关闭策略失败，继续删除实例记录", "instanceId", uuid, "error", err)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.instances[uuid]; ok && current == inst {
		delete(m.instances, uuid)
	}
	// FR-459 复审项 10：实例移除时回收健康记账，避免残留记录随 UUID 复用污染后续熔断判定。
	m.dropHealthAccounting(uuid)
	return nil
}

// StopAll 停止所有运行中的实例。
// direct 模式：终止游戏服进程（Worker 退出时一并清理）。
// daemon 模式：仅断开与 wrapper 的连接，wrapper 继续托管游戏服（ADR-003 进程隔离目标）。
func (m *Manager) StopAll() {
	m.mu.RLock()
	uuids := make([]string, 0)
	for uuid, inst := range m.instances {
		if inst.State == StateRunning {
			uuids = append(uuids, uuid)
		}
	}
	m.mu.RUnlock()

	for _, uuid := range uuids {
		inst, ok := m.GetInstance(uuid)
		if !ok {
			continue
		}
		// daemon 模式优雅退出：不杀游戏服，只断开 wrapper 连接。
		if inst.processType == ProcessTypeDaemon {
			m.mu.Lock()
			if inst.strategy != nil {
				_ = inst.strategy.Close()
				inst.strategy = nil
			}
			inst.State = StateStopped
			m.mu.Unlock()
			slog.Info("daemon 实例已断开连接（wrapper 继续运行）", "instanceId", uuid)
			continue
		}
		if err := m.Stop(uuid); err != nil {
			slog.Warn("停止实例失败", "instanceId", uuid, "error", err)
		}
	}
}

// RecoverDaemonInstances 在 Worker 重启后扫描 PID 目录，恢复仍存活的 daemon wrapper 连接。
// 对每个 PID 文件：wrapper pid 存活且 socket 可达则 reconnect 并登记实例为 RUNNING；
// 否则删除 PID 文件与残留 socket（清理）。返回成功恢复的实例数。
// 参见 ADR-003: 平台重启后通过 PID 文件重新连接已有 daemon。
func (m *Manager) RecoverDaemonInstances() (int, error) {
	entries, err := os.ReadDir(m.pidDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("读取 PID 目录失败: %w", err)
	}

	recovered := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pid") {
			continue
		}
		pidPath := filepath.Join(m.pidDir, entry.Name())
		instanceUUID := strings.TrimSuffix(entry.Name(), ".pid")

		rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
		if err != nil {
			slog.Warn("读取 PID 文件失败，清理", "path", pidPath, "error", err)
			_ = os.Remove(pidPath)
			continue
		}

		// wrapper 不存活：先看 Java 是否还在。
		if rec.WrapperPID <= 0 || !m.pidAlive(rec.WrapperPID) {
			// wrapper 已死但 Java 仍活：**启动期不再强杀**（FR-471 语义修订）。
			//
			// 旧行为（FR-325/FR-455）按 PID 记录强杀 Java 树，理由是「wrapper 一走就再没人能
			// stop 它，活着的 Java 会占着服务端口与 Paper session.lock，面板显示 STOPPED 却
			// 再也起不来」。真机事故（2026-09-23）证明该强杀会直接打断正在服务的服务器：换二进制
			// 重启 Worker 时，一条陈旧 PID 记录即触发对在跑农场的强杀（一次 54 台）。
			// 而它原本要解决的问题已有非破坏出口：FR-471 的启动冲突预检（work_dir_busy / port_free）
			// 拦住双开，漂移观测 + 接管入口把它纳入管理。
			//
			// 故与 FR-456 的 `warn` 默认策略同口径：**只观测、保留 PID 文件**，交由周期孤儿扫描
			// （按 policy 处置，默认 warn 仅告警）与 FR-471 的接管流程处理；显式 `auto` 策略仍由
			// 周期扫描负责强杀，启动路径不再有任何静默破坏。
			if rec.JavaPID > 0 && rec.JavaPID != rec.WrapperPID && m.pidAlive(rec.JavaPID) {
				m.observeOrphanAtStartup(instanceUUID, rec, "startup_wrapper_gone_java_alive")
				continue
			}
			slog.Info("daemon wrapper 已不存活，清理残留", "instanceId", rec.InstanceUUID, "wrapperPid", rec.WrapperPID)
			_ = os.Remove(pidPath)
			if rec.SocketAddr != "" {
				daemon.RemoveSocket(rec.SocketAddr)
			}
			continue
		}

		// wrapper 存活：构造 daemon 策略并 reconnect。
		// WorkDir 从 PID 记录恢复，否则文件/配置操作会因空工作目录失败（open :）。
		strategy := newDaemonStrategy(m, CommandSpec{UUID: instanceUUID, WorkDir: rec.WorkDir, ProcessType: ProcessTypeDaemon, ProbePort: rec.ProbePort})
		if err := m.reconnectWithRetry(strategy, rec.SocketAddr, instanceUUID); err != nil {
			// FR-325 / ADR-093：重试耗尽仍拨不通。
			//   - wrapper 仍存活：属「确属本实例、仅 socket 瞬时不可达」，按 ADR-093 只告警 + 落审计、
			//     保留 PID 文件，不杀（强杀会误杀一个 wrapper 与 Java 都健康、只是暂时拨不通的服务器）。
			//   - wrapper 已在重试期间死亡：旧行为按 PID 记录强杀 Java 树；FR-471 起改为**只观测不强杀**
			//     （启动路径不再有任何静默破坏），交由周期扫描按 policy 与 FR-471 接管流程处置。
			if rec.WrapperPID > 0 && m.pidAlive(rec.WrapperPID) {
				detail := fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"reason":"alive_unreachable"}`,
					instanceUUID, rec.WrapperPID, rec.JavaPID)
				m.auditOrphan("orphan.dispose_blocked", instanceUUID, detail, false,
					"接管重试耗尽但 wrapper 仍存活（仅瞬时不可达），按 ADR-093 只告警不杀")
				slog.Warn("接管重试耗尽但 wrapper 仍存活，按 ADR-093 只告警不杀，保留 PID 文件等下一轮/人工介入",
					"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID, "error", err)
				continue
			}
			m.observeOrphanAtStartup(instanceUUID, rec, "startup_reconnect_failed")
			continue
		}
		strategy.SetWrapperPID(rec.WrapperPID)

		// FR-459 终验 Low #3：若 Worker 重启前该实例已熔断，从状态文件恢复熔断态——否则 Worker
		// 会「忘掉」熔断（以为可自动重启）而 wrapper 粘性 autoRestartOff 仍在拒绝重启，形成反向 desync。
		restored := m.restorePersistedCircuit(instanceUUID)

		m.mu.Lock()
		m.instances[instanceUUID] = &Instance{
			UUID:        instanceUUID,
			State:       StateRunning,
			AutoRestart: !restored,
			WorkDir:     rec.WorkDir,
			ProbePort:   rec.ProbePort,
			strategy:    strategy,
			processType: ProcessTypeDaemon,
		}
		m.mu.Unlock()
		if restored {
			// 恢复熔断后须让 wrapper 与 Worker 记账对齐：worker 重启前 wrapper 的粘性开关可能已置位，
			// 也可能因重启窗口丢失该帧——幂等补发一次禁用帧（wrapper 已置位时无副作用）。
			m.disarmAutoRestart(instanceUUID, "恢复熔断态（Worker 重启）")
		}
		recovered++
		slog.Info("已恢复 daemon 实例", "instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "circuitRestored", restored)
	}
	return recovered, nil
}
