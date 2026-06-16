//go:build windows

package daemon

import (
	"syscall"
)

// IsPIDAlive 在 Windows 上用 OpenProcess 探测进程是否存活。
// signal-0 在 Windows 上不可用（os.Process.Signal 返回 not supported），
// 改用 OpenProcess + PROCESS_QUERY_LIMITED_INFORMATION，失败表示进程不存在或无权限。
func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	handle, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	syscall.CloseHandle(handle)
	return true
}
