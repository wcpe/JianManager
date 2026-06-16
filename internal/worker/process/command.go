package process

import (
	"context"
	"errors"
)

// ErrNotImplemented 表示该启动方式尚未实现。
// docker/rcon 等未落地的策略统一返回此错误，避免误用。
var ErrNotImplemented = errors.New("该启动方式尚未实现")

// ProcessType 启动方式，与 model.ProcessType 对齐。
// 这里独立定义为字符串以避免 process 包反向依赖 controlplane/model。
type ProcessType string

const (
	ProcessTypeDirect ProcessType = "direct"
	ProcessTypeDaemon ProcessType = "daemon"
	ProcessTypeDocker ProcessType = "docker"
	ProcessTypeRCON   ProcessType = "rcon"
)

// CommandSpec 是启动一个实例所需的全部配置。
// 各策略实现（direct/daemon/docker）从同一份配置派生各自所需参数。
type CommandSpec struct {
	UUID         string
	Name         string
	StartCommand string
	WorkDir      string
	EnvVars      map[string]string
	AutoRestart  bool
	ProcessType  ProcessType
}

// IProcessCommand 进程启动策略接口。
// Manager 按 instance.ProcessType 选择具体实现（direct/daemon/docker），
// 把「如何启动/停止/发送命令」的差异收敛到策略内部，Manager 只负责路由和生命周期记账。
// 参见 ADR-003: 守护进程 Wrapper 模式。
type IProcessCommand interface {
	// Start 启动实例进程。ctx 用于取消启动阶段（如连接 wrapper 超时）。
	Start(ctx context.Context) error
	// Stop 优雅停止实例。
	Stop() error
	// Kill 强制终止实例。
	Kill() error
	// SendCommand 向实例 stdin 发送一行命令。
	SendCommand(command string) error
	// State 返回策略当前记录的实例状态。
	State() InstanceState
	// Close 释放策略持有的资源（socket 连接、goroutine 等），
	// 不影响被管理的游戏服进程本身（daemon 模式下 wrapper 继续运行）。
	Close() error
	// GetPID 返回实例进程的 PID，用于从 OS 层采集进程内存等指标。
	// 未启动或已退出时返回 0。
	GetPID() int
}

// newStrategy 按 ProcessType 构造对应策略。
// direct 走 Worker 直连子进程；daemon 走 wrapper 子进程隔离；
// docker/rcon 暂未实现，返回 ErrNotImplemented，避免误用未落地能力。
func (m *Manager) newStrategy(spec CommandSpec) (IProcessCommand, error) {
	switch spec.ProcessType {
	case ProcessTypeDirect, "":
		return newDirectStrategy(m, spec), nil
	case ProcessTypeDaemon:
		return newDaemonStrategy(m, spec), nil
	case ProcessTypeDocker, ProcessTypeRCON:
		return nil, ErrNotImplemented
	default:
		return nil, ErrNotImplemented
	}
}
