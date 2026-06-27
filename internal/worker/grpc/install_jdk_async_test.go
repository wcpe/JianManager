package grpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/jdk"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 异步 InstallJDK（携带 task_id）：RPC 立即返回 task_id 并登记内存任务表为 running，
// 不阻塞下载完成。随后后台 goroutine 因无可达源最终把任务置 failed。
func TestServer_InstallJDK_Async_ReturnsImmediatelyAndRegisters(t *testing.T) {
	// 指向一个不可达的镜像源，确保后台下载快速失败（不真正下载）。
	t.Setenv("JIANMANAGER_JDK_TEMURIN_BASE", "http://127.0.0.1:1") // 拒连端口
	jdkMgr := jdk.NewManager(filepath.Join(t.TempDir(), "jdks"), nil)
	s := NewServer(process.NewManager(t.TempDir()), "node-x", nil, jdkMgr, nil)

	start := time.Now()
	resp, err := s.InstallJDK(context.Background(), &workerpb.InstallJDKRequest{
		Vendor: "Temurin", MajorVersion: 21, Arch: "x64", TaskId: "task-async-1",
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 3*time.Second, "异步路径应立即返回")
	require.True(t, resp.Success)
	require.Equal(t, "task-async-1", resp.TaskId)
	require.Nil(t, resp.Jdk, "异步路径不回 JDK 详情")

	// 立即可在内存任务表见到 running 任务。
	snaps := s.TaskSnapshots()
	require.Len(t, snaps, 1)
	require.Equal(t, "task-async-1", snaps[0].TaskId)

	// 后台失败后任务转 failed（轮询等待，最长 5s）。
	require.Eventually(t, func() bool {
		for _, sn := range s.TaskSnapshots() {
			if sn.TaskId == "task-async-1" && sn.State == "failed" {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "后台下载失败应把任务置 failed")

	// Drop 后任务从表内移除。
	s.DropTask("task-async-1")
	require.Empty(t, s.TaskSnapshots())
}

// 同步 InstallJDK（无 task_id）：保持原行为——下载失败返回 success=false + error（不进任务表）。
func TestServer_InstallJDK_Sync_BackwardCompat(t *testing.T) {
	t.Setenv("JIANMANAGER_JDK_TEMURIN_BASE", "http://127.0.0.1:1")
	jdkMgr := jdk.NewManager(filepath.Join(t.TempDir(), "jdks"), nil)
	s := NewServer(process.NewManager(t.TempDir()), "node-y", nil, jdkMgr, nil)

	resp, err := s.InstallJDK(context.Background(), &workerpb.InstallJDKRequest{
		Vendor: "Temurin", MajorVersion: 21, Arch: "x64", // 无 TaskId
	})
	require.NoError(t, err)
	require.False(t, resp.Success)
	require.NotEmpty(t, resp.Error)
	require.Empty(t, s.TaskSnapshots(), "同步路径不进任务表")
}
