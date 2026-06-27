//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
)

// applyProcAttr 让被托管进程独立成进程组（Setpgid），便于停止时按进程组整组终止——
// 含 sh -c 派生的孙进程。否则孙进程残留并继承 stdout 管道写端，cmd.Wait 永等管道 EOF
// 而永久阻塞，wrapper 在 stop 后不退出（Windows 同类问题靠 taskkill /T 解，Linux 靠进程组）。
func applyProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree 强制终止整棵进程树：杀子进程所在进程组（负 pgid，SIGKILL），
// 取不到 pgid 时回退杀单进程。对应 Windows 的 taskkill /T，杜绝孙进程孤儿残留。
func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		if syscall.Kill(-pgid, syscall.SIGKILL) == nil {
			return
		}
	}
	_ = cmd.Process.Kill()
}
