//go:build !windows

package process

import (
	"os"
	"syscall"
)

// signalPIDTree 向 pid 所在进程组发送 SIGTERM（FR-471 接管的优雅停止第一步）。
//
// 与 daemon.KillPIDTree 的区别只在信号：这里发 SIGTERM，让被接管的游戏服走正常的关闭流程
// （Paper 的 shutdown hook 保存世界、释放 world/session.lock），而不是 SIGKILL 直接断电。
// 先杀进程组（负 pgid）以覆盖 sh -c 派生的孙进程；取不到 pgid 时退化为杀单进程。
func signalPIDTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	if pgid, err := syscall.Getpgid(pid); err == nil {
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err == nil {
			return nil
		}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
