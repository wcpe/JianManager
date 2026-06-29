//go:build windows

package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
)

// chdirToExeDirIfService 在以 Windows 服务运行时把工作目录切到可执行文件所在目录（FIX-2/3）。
//
// Windows 服务进程默认工作目录为 C:\Windows\System32（New-Service 无 WorkingDirectory 等价项），
// 导致 worker 免配置 setup 把 worker.yml 写到 System32、重启又找不到 → 节点「发蒙」。
// Linux systemd 由 unit 的 WorkingDirectory=<install-dir> 解决（见 install-worker.sh）；Windows 无此
// 机制，故由 Worker 自纠：检测到以服务身份运行即 chdir 到 exe 目录（= 安装目录），使配置/数据根的
// cwd 相对解析落在安装目录，与 Linux 行为对齐。前台/交互运行（非服务）完全不受影响。
func chdirToExeDirIfService() {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("无法定位可执行文件路径，跳过服务工作目录自纠", "error", err)
		return
	}
	dir := filepath.Dir(exe)
	if err := os.Chdir(dir); err != nil {
		slog.Warn("切换到可执行文件目录失败（Windows 服务）", "dir", dir, "error", err)
		return
	}
	slog.Info("以 Windows 服务运行，工作目录已切到安装目录", "dir", dir)
}
