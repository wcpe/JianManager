//go:build windows

package grpc

import (
	"os/exec"
	"testing"
)

// startDetachedSleeper 起一个存活进程（ping 计数）模拟被托管进程。Windows 进程树终止靠 taskkill /T，
// 无需进程组设置。
func startDetachedSleeper() (*exec.Cmd, error) {
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// writeSocketPlaceholder 在 Windows 上为空操作：daemon 通信走命名管道，无对应文件。
func writeSocketPlaceholder(t *testing.T, addr string) { t.Helper() }

// assertSocketGone 在 Windows 上为空操作：命名管道随进程退出由 OS 回收，无文件可断言。
func assertSocketGone(t *testing.T, addr string) { t.Helper() }
