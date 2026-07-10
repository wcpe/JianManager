//go:build windows

package grpc

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// lockFileShareRead 以「允许他人读、禁止删除/写」的共享模式独占打开 path，
// 模拟运行中 JVM 对 classpath 依赖 jar 的占用：他人仍可读取校验，但无法删除/替换（Windows 覆盖会 Access denied）。
func lockFileShareRead(t *testing.T, path string) {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	require.NoError(t, err)
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.CloseHandle(h) })
}

// relocatorEntry 是一条典型的 classpath 依赖 jar（运行中会被 JVM 锁定）。
const relocatorEntry = "libraries/me/lucko/jar-relocator/1.7/jar-relocator-1.7.jar"

// TestDeployServerProbe_RunningInstanceLockedLibrary 复现 FR-068：运行中实例点「更新探针」时，
// libraries 依赖 jar 被 JVM 独占锁定，覆盖会 Access denied——修复前整个请求 422，修复后应绕锁成功。
func TestDeployServerProbe_RunningInstanceLockedLibrary(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()
	const uuid = "66666666-6666-6666-6666-666666666666"
	workDir := filepath.Join(tmp, "inst")
	requireCreatedProbeInstance(t, srv, ctx, uuid, workDir)

	relocator := filepath.Join(workDir, filepath.FromSlash(relocatorEntry))

	// 首次部署：把依赖 jar 落地（模拟建服时已预置）。
	first := makeProbeLibrariesZip(t, map[string]string{relocatorEntry: "relocator-v1"})
	resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{InstanceUuid: uuid, LibrariesZip: first})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)

	// 模拟实例 RUNNING：JVM 独占锁定该 classpath 依赖 jar。
	lockFileShareRead(t, relocator)

	t.Run("同内容依赖跳过覆盖不报错", func(t *testing.T) {
		z := makeProbeLibrariesZip(t, map[string]string{
			relocatorEntry:        "relocator-v1", // 同内容，应命中跳过
			"libraries/new-a.jar": "aaa",          // 新条目应正常写入
		})
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{InstanceUuid: uuid, LibrariesZip: z})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)

		got, e := os.ReadFile(relocator)
		require.NoError(t, e)
		assert.Equal(t, "relocator-v1", string(got))
		newA, e := os.ReadFile(filepath.Join(workDir, "libraries", "new-a.jar"))
		require.NoError(t, e)
		assert.Equal(t, "aaa", string(newA))
	})

	t.Run("变更内容依赖被锁降级不整体失败", func(t *testing.T) {
		z := makeProbeLibrariesZip(t, map[string]string{
			relocatorEntry:        "relocator-v2-changed", // 内容变化但文件被锁，无法覆盖
			"libraries/new-b.jar": "bbb",                  // 其余条目仍应正常写入
		})
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{InstanceUuid: uuid, LibrariesZip: z})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error) // 降级：锁定条目跳过，整体仍成功

		got, e := os.ReadFile(relocator)
		require.NoError(t, e)
		assert.Equal(t, "relocator-v1", string(got)) // 旧内容留存（被锁无法覆盖）
		newB, e := os.ReadFile(filepath.Join(workDir, "libraries", "new-b.jar"))
		require.NoError(t, e)
		assert.Equal(t, "bbb", string(newB))
	})
}
