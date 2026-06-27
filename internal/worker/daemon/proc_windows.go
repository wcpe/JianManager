//go:build windows

package daemon

import (
	"os/exec"
	"strconv"
)

// applyProcAttr Windows 无需特殊进程组设置；进程树终止靠 taskkill /T（见 killProcessTree）。
func applyProcAttr(cmd *exec.Cmd) {}

// killProcessTree 用 taskkill /T /F 递归终止整棵进程树（含 cmd.exe 派生的子进程），
// 避免子进程继承句柄继续运行致 cmd.Wait 阻塞、wrapper 不退出。
func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
}
