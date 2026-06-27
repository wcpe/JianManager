//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// IsPIDAlive 在 Linux/macOS 上用 signal 0 探测进程是否存在（不实际发送信号）。
func IsPIDAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// 必须用 syscall.Signal(0)：signal 0 不投递、仅探测存活/权限。
	// 不能用 os.Signal(nil)——其类型断言为 syscall.Signal 必失败、恒返回「unsupported signal type」错误，
	// 导致本函数在 Linux/macOS 上恒为 false（jmctl list/attach、daemon 恢复据此误判进程全死）。
	return proc.Signal(syscall.Signal(0)) == nil
}
