//go:build !windows

package acquire

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// makeUnreadable 构造「归档存在但读不了」的场景（清掉全部权限位），返回还原函数。
//
// 以 root 运行时权限位对 root 不生效（文件仍可读），该场景在本进程下无法构造 → 跳过用例
// （注意 os.Geteuid 在 Windows 返回 -1，不能用它做跨平台的「非 root」判据）。
func makeUnreadable(t *testing.T, path string) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时权限位不生效，跳过权限路径")
	}
	require.NoError(t, os.Chmod(path, 0o000))

	return func() { _ = os.Chmod(path, 0o600) }
}
