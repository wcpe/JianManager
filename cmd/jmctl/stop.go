package main

import (
	"fmt"

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

// runSingleControl 是 stop/kill 的公共实现：解析 --pid-dir 与目标前缀，Dial socket，
// 单发一个控制命令帧后退出。不等待关服日志（非交互场景，要看输出用 emergency）。
func runSingleControl(name, control string, args []string) error {
	fs := newFlagSet(name)
	pidDirFlag := fs.String("pid-dir", "", "daemon PID 目录（默认数据根下 var/servers）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("用法: jmctl %s <uuid 前缀> [--pid-dir DIR]", name)
	}
	prefix := rest[0]

	pidDir, err := resolvePIDDir(*pidDirFlag)
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
