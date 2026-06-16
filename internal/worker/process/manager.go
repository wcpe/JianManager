package process

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
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

// Instance 运行中的实例。
type Instance struct {
	UUID         string
	Name         string
	StartCommand string
	WorkDir      string
	EnvVars      map[string]string
	RCONPort     int
	RCONPassword string
	State        InstanceState
	Cmd          *exec.Cmd
	AutoRestart  bool
	CrashCount   int
}

// Manager 进程管理器。
type Manager struct {
	mu        sync.RWMutex
	instances map[string]*Instance
	serversDir string
}

// NewManager 创建进程管理器。
func NewManager(serversDir string) *Manager {
	return &Manager{
		instances:  make(map[string]*Instance),
		serversDir: serversDir,
	}
}

// Create 创建实例（但不启动）。
func (m *Manager) Create(uuid, name, startCommand, workDir string, envVars map[string]string, autoRestart bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.instances[uuid]; exists {
		return fmt.Errorf("实例 %s 已存在", uuid)
	}

	m.instances[uuid] = &Instance{
		UUID:         uuid,
		Name:         name,
		StartCommand: startCommand,
		WorkDir:      workDir,
		EnvVars:      envVars,
		State:        StateStopped,
		AutoRestart:  autoRestart,
	}

	slog.Info("实例已创建", "instanceId", uuid, "name", name, "autoRestart", autoRestart)
	return nil
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

// Start 启动实例。
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

	inst.State = StateStarting
	m.mu.Unlock()

	cmd := exec.Command("sh", "-c", inst.StartCommand)
	cmd.Dir = inst.WorkDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	for k, v := range inst.EnvVars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		inst.State = StateCrashed
		m.mu.Unlock()
		return fmt.Errorf("启动实例 %s 失败: %w", uuid, err)
	}

	m.mu.Lock()
	inst.Cmd = cmd
	inst.State = StateRunning
	m.mu.Unlock()

	slog.Info("实例已启动", "instanceId", uuid, "pid", cmd.Process.Pid)

	// 异步等待进程结束
	go func() {
		err := cmd.Wait()
		m.mu.Lock()

		if inst.State == StateStopping {
			inst.State = StateStopped
			inst.CrashCount = 0
			m.mu.Unlock()
			slog.Info("实例已停止", "instanceId", uuid)
			return
		}

		inst.State = StateCrashed
		inst.Cmd = nil
		inst.CrashCount++
		crashCount := inst.CrashCount
		autoRestart := inst.AutoRestart
		m.mu.Unlock()

		slog.Warn("实例崩溃", "instanceId", uuid, "err", err, "crashCount", crashCount)

		// 指数退避自动重启
		if autoRestart {
			delay := backoffDelay(crashCount)
			slog.Info("将在延迟后自动重启", "instanceId", uuid, "delay", delay, "crashCount", crashCount)
			time.Sleep(delay)

			m.mu.RLock()
			currentState := inst.State
			m.mu.RUnlock()

			if currentState == StateCrashed {
				if restartErr := m.Start(uuid); restartErr != nil {
					slog.Error("自动重启失败", "instanceId", uuid, "error", restartErr)
				}
			}
		}
	}()

	return nil
}

// Stop 停止实例。
func (m *Manager) Stop(uuid string) error {
	m.mu.RLock()
	inst, exists := m.instances[uuid]
	m.mu.RUnlock()
	if !exists || inst.Cmd == nil {
		return fmt.Errorf("实例 %s 未运行", uuid)
	}

	m.mu.Lock()
	inst.State = StateStopping
	m.mu.Unlock()

	return inst.Cmd.Process.Signal(os.Interrupt)
}

// Kill 强制终止实例。
func (m *Manager) Kill(uuid string) error {
	m.mu.RLock()
	inst, exists := m.instances[uuid]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("实例 %s 不存在", uuid)
	}

	if inst.Cmd != nil {
		if err := inst.Cmd.Process.Kill(); err != nil {
			return fmt.Errorf("终止实例 %s 失败: %w", uuid, err)
		}
	}

	m.mu.Lock()
	inst.State = StateStopped
	inst.Cmd = nil
	m.mu.Unlock()

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

	if !exists || inst.Cmd == nil || inst.Cmd.Process == nil {
		return fmt.Errorf("实例 %s 未运行", uuid)
	}

	stdin, err := inst.Cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("获取 stdin 失败: %w", err)
	}

	_, err = fmt.Fprintln(stdin, command)
	return err
}

// Remove 移除实例记录。
func (m *Manager) Remove(uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, exists := m.instances[uuid]
	if !exists {
		return nil
	}

	if inst.Cmd != nil {
		_ = inst.Cmd.Process.Kill()
	}

	delete(m.instances, uuid)
	return nil
}

// StopAll 停止所有运行中的实例。
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
		if err := m.Stop(uuid); err != nil {
			slog.Warn("停止实例失败", "instanceId", uuid, "error", err)
		}
	}
}

// backoffDelay 计算指数退避延迟。
// 1s → 2s → 4s → 8s → 16s → 30s (上限)。
func backoffDelay(crashCount int) time.Duration {
	delay := time.Second * time.Duration(1<<uint(crashCount-1))
	maxDelay := 30 * time.Second
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}
