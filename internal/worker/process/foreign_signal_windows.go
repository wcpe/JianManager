//go:build windows

package process

import (
	"os/exec"
	"strconv"
)

// signalPIDTree 向 pid 的进程树发送「优雅终止」请求（FR-471 接管的优雅停止第一步）。
//
// Windows 无 SIGTERM：用 taskkill /T（不带 /F）向进程树发关闭请求（WM_CLOSE 语义），
// 让控制台程序自行退出（相当于 Unix 的 SIGTERM）。若该进程无窗口消息循环、请求无人处理，
// 由调用方的超时轮询升级为 daemon.KillPIDTree（taskkill /T /F）强杀兜底。
func signalPIDTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T").Run()
}
