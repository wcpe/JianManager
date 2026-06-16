//go:build !windows

package daemon

import "os"

// IsPIDAlive 在 Linux/macOS 上用 signal 0 探测进程是否存在（不实际发送信号）。
func IsPIDAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(os.Signal(nil)) == nil
}
