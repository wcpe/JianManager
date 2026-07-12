package grpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	wruntime "github.com/wcpe/JianManager/internal/worker/runtime"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 异步 InstallRuntime（携带 task_id，FR-299）：RPC 立即返回并登记内存任务表，
// 不阻塞下载完成；随后后台 goroutine 因无可达源最终把任务置 failed。
func TestServer_InstallRuntime_Async_ReturnsImmediatelyAndRegisters(t *testing.T) {
	s := NewServer(process.NewManager(t.TempDir()), "node-rt", nil, nil, nil)
	s.SetRuntimeManager(wruntime.NewManager(filepath.Join(t.TempDir(), "runtimes")))

	start := time.Now()
	resp, err := s.InstallRuntime(context.Background(), &workerpb.InstallRuntimeRequest{
		Type: "nodejs", Major: 22, Arch: "x64",
		MirrorBase: "http://127.0.0.1:1", // 拒连端口：后台快速失败，不真下载
		TaskId:     "task-rt-1",
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 3*time.Second, "异步路径应立即返回")
	require.True(t, resp.Success)
	require.Equal(t, "task-rt-1", resp.TaskId)

	// 后台失败后任务转 failed（轮询等待）。
	require.Eventually(t, func() bool {
		for _, sn := range s.TaskSnapshots() {
			if sn.TaskId == "task-rt-1" && sn.State == "failed" {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "后台下载失败应把任务置 failed")
	s.DropTask("task-rt-1")
}

// InstallRuntime 参数守卫：未知类型 / 缺 task_id / 管理器未启用均明确拒绝，不进任务表。
func TestServer_InstallRuntime_Rejections(t *testing.T) {
	s := NewServer(process.NewManager(t.TempDir()), "node-rt2", nil, nil, nil)

	// 管理器未启用。
	resp, err := s.InstallRuntime(context.Background(), &workerpb.InstallRuntimeRequest{Type: "nodejs", Major: 22, TaskId: "t"})
	require.NoError(t, err)
	require.False(t, resp.Success)

	s.SetRuntimeManager(wruntime.NewManager(filepath.Join(t.TempDir(), "runtimes")))

	// 未知类型。
	resp, err = s.InstallRuntime(context.Background(), &workerpb.InstallRuntimeRequest{Type: "python", Major: 3, TaskId: "t"})
	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Contains(t, resp.Error, "不支持的运行时类型")

	// 缺 task_id（仅支持异步任务路径）。
	resp, err = s.InstallRuntime(context.Background(), &workerpb.InstallRuntimeRequest{Type: "nodejs", Major: 22})
	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Contains(t, resp.Error, "task_id")
	require.Empty(t, s.TaskSnapshots(), "被拒请求不进任务表")
}

// RemoveRuntime：托管根下顶层删除成功；未知类型拒绝。
func TestServer_RemoveRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtimes")
	s := NewServer(process.NewManager(t.TempDir()), "node-rt3", nil, nil, nil)
	s.SetRuntimeManager(wruntime.NewManager(root))

	resp, err := s.RemoveRuntime(context.Background(), &workerpb.RemoveRuntimeRequest{Type: "python", Path: "/x"})
	require.NoError(t, err)
	require.False(t, resp.Success)

	// 托管根外路径：Manager 拒绝，success=false 带原因。
	resp, err = s.RemoveRuntime(context.Background(), &workerpb.RemoveRuntimeRequest{Type: "nodejs", Path: t.TempDir()})
	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Contains(t, resp.Error, "托管目录")
}
