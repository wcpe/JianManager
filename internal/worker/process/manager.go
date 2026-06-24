package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	AutoRestart                bool
	CrashCount                 int
	// strategy 是该实例的启动策略，按 ProcessType 选择。
	// nil 表示实例已创建但尚未启动（或已 Close）。
	strategy IProcessCommand
	// processType 记录构造策略时的方式，用于 StopAll 判断优雅退出路径。
	processType ProcessType
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
}

// NewManager 创建进程管理器。
func NewManager(serversDir string) *Manager {
	return &Manager{
		instances:  make(map[string]*Instance),
		serversDir: serversDir,
		pidDir:     serversDir,
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
	inst.State = newState
	m.mu.Unlock()
	m.emitStateChange(uuid, oldState, newState)
}

// InstanceSnapshot 表示单个实例的状态快照（用于心跳上报）。
type InstanceSnapshot struct {
	UUID      string
	State     string // STOPPED, STARTING, RUNNING, STOPPING, CRASHED
	ProbePort int    // ServerProbe /metrics 端口；>0 且 RUNNING 时心跳采集器自采富指标（FR-060）
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
		states = append(states, InstanceSnapshot{
			UUID:      uuid,
			State:     string(state),
			ProbePort: inst.ProbePort,
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

// Start 启动实例。按实例的 ProcessType 选择策略；首次启动时惰性构造策略。
func (m *Manager) Start(uuid string) error {
	m.mu.Lock()
	inst, exists := m.instances[uuid]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("实例 %s 不存在", uuid)
	}
	if inst.State != StateStopped && inst.State != StateCrashed {
		m.mu.Unlock()
		return fmt.Errorf("实例 %s 当前状态 %s 无法启动", uuid, inst.State)
	}

	// 启动前校验 java 版本（仅宿主进程模式；docker 的 java 在容器内不适用）。
	// 避免无绑定 JDK / 版本不符的 MC 实例以 UnsupportedClassVersionError 静默崩在
	// 游戏服自身日志、面板只见 CRASHED 无因（BUG-012）。校验失败保持原状态、返回明确错误。
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

	m.emitStateChange(uuid, oldState, StateStarting)

	if err := strategy.Start(context.Background()); err != nil {
		m.mu.Lock()
		prevState := inst.State
		inst.State = StateCrashed
		m.mu.Unlock()
		m.emitStateChange(uuid, prevState, StateCrashed)
		return fmt.Errorf("启动实例 %s 失败: %w", uuid, err)
	}

	m.mu.Lock()
	prevState := inst.State
	inst.State = StateRunning
	m.mu.Unlock()
	m.emitStateChange(uuid, prevState, StateRunning)
	slog.Info("实例已启动", "instanceId", uuid)
	return nil
}

// Stop 停止实例。
func (m *Manager) Stop(uuid string) error {
	m.mu.RLock()
	inst, exists := m.instances[uuid]
	m.mu.RUnlock()
	if !exists || inst.strategy == nil {
		return fmt.Errorf("实例 %s 未运行", uuid)
	}

	m.mu.Lock()
	oldState := inst.State
	inst.State = StateStopping
	m.mu.Unlock()
	m.emitStateChange(uuid, oldState, StateStopping)

	if err := inst.strategy.Stop(); err != nil {
		return fmt.Errorf("停止实例 %s 失败: %w", uuid, err)
	}

	m.mu.Lock()
	oldState = inst.State
	inst.State = StateStopped
	inst.CrashCount = 0
	m.mu.Unlock()
	m.emitStateChange(uuid, oldState, StateStopped)
	return nil
}

// Kill 强制终止实例。
func (m *Manager) Kill(uuid string) error {
	m.mu.RLock()
	inst, exists := m.instances[uuid]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("实例 %s 不存在", uuid)
	}

	if inst.strategy != nil {
		_ = inst.strategy.Kill()
		_ = inst.strategy.Close()
		inst.strategy = nil
	}

	m.mu.Lock()
	oldState := inst.State
	inst.State = StateStopped
	m.mu.Unlock()
	m.emitStateChange(uuid, oldState, StateStopped)

	return nil
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
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, exists := m.instances[uuid]
	if !exists {
		return nil
	}

	if inst.strategy != nil {
		_ = inst.strategy.Kill()
		_ = inst.strategy.Close()
	}

	delete(m.instances, uuid)
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

		// wrapper 进程不存活：清理 PID 文件 + 残留 socket
		if rec.WrapperPID <= 0 || !daemon.IsPIDAlive(rec.WrapperPID) {
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
		if err := strategy.Reconnect(rec.SocketAddr); err != nil {
			slog.Warn("reconnect wrapper 失败，清理", "instanceId", instanceUUID, "error", err)
			_ = os.Remove(pidPath)
			continue
		}
		strategy.SetWrapperPID(rec.WrapperPID)

		m.mu.Lock()
		m.instances[instanceUUID] = &Instance{
			UUID:        instanceUUID,
			State:       StateRunning,
			AutoRestart: true,
			WorkDir:     rec.WorkDir,
			ProbePort:   rec.ProbePort,
			strategy:    strategy,
			processType: ProcessTypeDaemon,
		}
		m.mu.Unlock()
		recovered++
		slog.Info("已恢复 daemon 实例", "instanceId", instanceUUID, "wrapperPid", rec.WrapperPID)
	}
	return recovered, nil
}
