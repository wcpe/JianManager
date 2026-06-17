package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	// JavaHome 显式指定 JAVA_HOME（来自实例绑定的 JDK），非空时 Worker 会
	// 把它注入到进程环境并把 JavaHome/bin 接入 PATH。
	JavaHome string
	// JDKBinPath 显式指定要前置到 PATH 的目录；空时由 JavaHome 派生。
	JDKBinPath string
	AutoRestart bool
	ProcessType ProcessType
}

// pathKey 当前平台的 PATH 变量名：Windows 使用 Path，Unix 使用 PATH。
// 这里不依赖 build tag：Worker 进程作为托管环境的子进程直接运行，
// 实际生效的是子进程自身的 PATH 取名。
var pathKey = func() string {
	if runtime.GOOS == "windows" {
		return "Path"
	}
	return "PATH"
}()

// ComposeEnv 合成进程最终环境：
// 1) Worker 自身环境（os.Environ）作为基线，保留系统 PATH/编码等；
// 2) 注入 JAVA_HOME（如果提供）和对应 PATH 前缀；
// 3) 叠加实例自定义 EnvVars（覆盖基线同名键）。
// 始终不修改调用方的 map。
func ComposeEnv(base []string, spec CommandSpec) []string {
	out := append([]string(nil), base...)

	javaBin := spec.JDKBinPath
	if javaBin == "" && spec.JavaHome != "" {
		javaBin = joinPath(spec.JavaHome, "bin")
	}
	if spec.JavaHome != "" {
		out = append(out, "JAVA_HOME="+spec.JavaHome)
	}
	if javaBin != "" {
		// 找到原 PATH/Path，将其前置 javaBin；找不到则追加。
		prefix := javaBin
		replaced := false
		for i, kv := range out {
			if k, v, ok := splitEnvKey(kv); ok && k == pathKey {
				out[i] = k + "=" + prefix + string(os.PathListSeparator) + v
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, pathKey+"="+prefix)
		}
	}

	for k, v := range spec.EnvVars {
		// 同名键移除 base 中的旧值，确保子进程实际生效的是实例 env。
		removed := out[:0]
		for _, kv := range out {
			if name, _, ok := splitEnvKey(kv); !ok || name != k {
				removed = append(removed, kv)
			}
		}
		out = append(removed, k+"="+v)
	}
	return out
}

func joinPath(dir, leaf string) string {
	return filepath.Join(dir, leaf)
}

func splitEnvKey(kv string) (key, value string, ok bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
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
