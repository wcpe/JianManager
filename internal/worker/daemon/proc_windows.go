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

// KillPIDTree 按 PID 用 taskkill /T /F 递归终止整棵进程树（FR-325 接管兜底：
// 只有 PID 记录、无 *exec.Cmd 句柄），与 killProcessTree 同源。
func KillPIDTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}
