//go:build windows

package acquire

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// makeUnreadable 构造「归档存在但读不了」的场景，返回还原函数。
//
// Windows 没有 POSIX 权限位：os.Chmod(0o000) 只置只读属性，文件仍可正常读取，
// 「不可读」这一路径在 Windows 上构造不出来（用例会在 Linux 上通过、在 Windows 上假绿变红）。
// 等价构造是用**独占句柄**（dwShareMode=0，不共享读/写/删）占住文件：期间任何其它
// 打开尝试都会拿到 sharing violation，对采集器而言与 Unix 权限拒绝同因——
// os.Open 失败，必须记 ARCHIVE_UNREADABLE 缺口并标记失败，而不是静默跳过。
//
// 句柄必须在 t.TempDir 清理前关闭，否则目录删不掉：调用方用 t.Cleanup 登记还原即可
// （t.Cleanup 后进先出，晚登记的还原先执行）。
func makeUnreadable(t *testing.T, path string) func() {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	require.NoError(t, err, "路径转 UTF-16 失败: %s", path)
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ,
		0, // 不共享：后续任何打开尝试都得到 sharing violation
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	require.NoError(t, err, "以独占句柄占住归档失败: %s", path)

	return func() { _ = syscall.CloseHandle(h) }
}
