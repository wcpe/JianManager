//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// setDetachedAttrPlatform 在 Linux/macOS 上用 Setsid 让子进程新建会话、脱离进程组。
func setDetachedAttrPlatform(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
