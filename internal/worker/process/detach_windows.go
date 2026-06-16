//go:build windows

package process

import (
	"os/exec"
	"syscall"
)

// setDetachedAttrPlatform 在 Windows 上用 CREATE_NEW_PROCESS_GROUP 让子进程脱离进程组。
// 这样 Worker 收到 Ctrl 事件时不会传递给 wrapper 子进程。
func setDetachedAttrPlatform(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
