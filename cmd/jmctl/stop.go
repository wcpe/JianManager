package main

import (
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// runStop 实现 `jmctl stop <uuid 前缀>`：单发优雅停服命令后退出（非交互，镜像 Worker stop_command）。
func runStop(args []string) error {
	return runSingleControl("stop", daemon.ControlStop, args)
}

// runKill 实现 `jmctl kill <uuid 前缀>`：单发强制终止命令后退出（应急强杀，运营者显式选择）。
func runKill(args []string) error {
	return runSingleControl("kill", daemon.ControlKill, args)
}

// parseTargetArgs 解析 stop/kill 的参数：恰好一个位置参数（uuid 前缀）+ 可选 --pid-dir。
//
// 关键：位置参数与 flag 的先后顺序不限。Go 标准 flag 包在遇到首个非 flag token 即停止解析，
// 故 `jmctl stop <uuid> --pid-dir X` 这种自然写法会把 `--pid-dir X` 落到 fs.Args() 里。
// 这里先把 flag token（及其值）与位置参数分离、把 flag 重排到前面再交给 FlagSet，消除顺序敏感。
func parseTargetArgs(name string, args []string) (prefix, pidDir string, err error) {
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--pid-dir" || a == "-pid-dir":
			// 形如 `--pid-dir X`：连同下一个 token 作为值一起取走。
			flagArgs = append(flagArgs, a)
			if i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		case strings.HasPrefix(a, "--pid-dir=") || strings.HasPrefix(a, "-pid-dir="):
			flagArgs = append(flagArgs, a) // 形如 `--pid-dir=X`：本身即完整 flag。
		case strings.HasPrefix(a, "-"):
			flagArgs = append(flagArgs, a) // 其它 flag（如 -h）交给 FlagSet 处理/报错。
		default:
			positional = append(positional, a)
		}
	}

	fs := newFlagSet(name)
	pidDirFlag := fs.String("pid-dir", "", "daemon PID 目录（默认数据根下 var/servers）")
	if err := fs.Parse(flagArgs); err != nil {
		return "", "", err
	}
	if len(positional) != 1 {
		return "", "", fmt.Errorf("用法: jmctl %s <uuid 前缀> [--pid-dir DIR]", name)
	}
	return positional[0], *pidDirFlag, nil
}

// runSingleControl 是 stop/kill 的公共实现：解析 --pid-dir 与目标前缀，Dial socket，
// 单发一个控制命令帧后退出。不等待关服日志（非交互场景，要看输出用 emergency）。
func runSingleControl(name, control string, args []string) error {
	prefix, pidDirFlag, err := parseTargetArgs(name, args)
	if err != nil {
		return err
	}

	pidDir, err := resolvePIDDir(pidDirFlag)
	if err != nil {
		return err
	}
	insts, err := scanInstances(pidDir)
	if err != nil {
		return err
	}
	target, err := resolvePrefix(insts, prefix)
	if err != nil {
		return err
	}
	if !target.Alive {
		return fmt.Errorf("实例 %s 的 wrapper 进程（PID %d）已不存活，无法发送命令", target.UUID, target.WrapperPID)
	}

	conn, err := daemon.Dial(target.SocketAddr)
	if err != nil {
		return fmt.Errorf("连接实例 %s 的 socket 失败: %w", target.UUID, err)
	}
	defer conn.Close()

	if err := sendControl(conn, control); err != nil {
		return fmt.Errorf("发送 %s 命令失败: %w", name, err)
	}
	fmt.Printf("[jmctl] 已向实例 %s 发送 %s 命令\n", target.UUID, name)
	return nil
}
