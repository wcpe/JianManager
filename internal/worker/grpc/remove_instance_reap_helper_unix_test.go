//go:build !windows

package grpc

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startDetachedSleeper 起一个独立进程组的存活进程（sleep），模拟 wrapper / Java 被托管进程。
// Setpgid 使其自成进程组，与真实 wrapper.applyProcAttr 一致：KillPIDTree 才能按 PID 各杀其组，
// 不误伤测试进程所在的进程组。
func startDetachedSleeper() (*exec.Cmd, error) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// writeSocketPlaceholder 在 Unix 上建一个占位 socket 文件（真实 socket 由 wrapper 建，此处仅验清理）。
func writeSocketPlaceholder(t *testing.T, addr string) {
	t.Helper()
	require.NoError(t, os.WriteFile(addr, []byte{}, 0o600))
}

// assertSocketGone 断言 Unix socket 文件已清理。
func assertSocketGone(t *testing.T, addr string) {
	t.Helper()
	assert.NoFileExists(t, addr, "遗留 socket 必须清理")
}
