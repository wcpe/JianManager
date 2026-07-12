//go:build !windows

package main

// chdirToExeDirIfService 非 Windows 平台为空操作：Linux/macOS 服务由 systemd/launchd 的
// WorkingDirectory 指定工作目录（见 install-worker.sh 的 systemd unit），无需进程自纠。
func chdirToExeDirIfService() {}
