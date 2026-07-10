package grpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newRemoveTestServer 构造带数据根的 Server，并在托管区 var/servers 下布好一个实例工作目录。
func newRemoveTestServer(t *testing.T) (*Server, *dataroot.Root, string) {
	t.Helper()
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	srv := NewServer(process.NewManager(t.TempDir()), "test-node", nil, nil, root)

	workDir := filepath.Join(root.ServersDir(), "paper-a-8b4cb747")
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "world"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "server.jar"), []byte("jar"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "world", "level.dat"), []byte("dat"), 0o644))
	return srv, root, workDir
}

func registerRemoveInstance(t *testing.T, srv *Server, uuid, workDir string) {
	t.Helper()
	require.NoError(t, srv.manager.Create(uuid, "paper-a", "noop", "", workDir, nil, false, process.ProcessTypeDirect, "", "", 0, 0))
}

// 已注册的停机实例：删除工作目录 + 派生索引 + 注册表条目（复现真机走查缺陷：删实例后目录残留）。
func TestRemoveInstance_DeletesWorkDirIndexAndRegistry(t *testing.T) {
	srv, root, workDir := newRemoveTestServer(t)
	const uuid = "22222222-2222-2222-2222-222222222222"
	registerRemoveInstance(t, srv, uuid, workDir)

	indexDir := filepath.Join(root.IndexDir(), uuid)
	require.NoError(t, os.MkdirAll(indexDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(indexDir, "postings.json"), []byte("{}"), 0o644))

	resp, err := srv.RemoveInstance(context.Background(), &workerpb.RemoveInstanceRequest{InstanceUuid: uuid})
	require.NoError(t, err)
	require.True(t, resp.Success, "RemoveInstance 应成功: %s", resp.Error)
	assert.False(t, resp.WorkDirSkipped)

	assert.NoDirExists(t, workDir, "工作目录必须被删除")
	assert.NoDirExists(t, indexDir, "派生搜索索引必须一并清理")
	_, ok := srv.manager.GetInstance(uuid)
	assert.False(t, ok, "注册表条目必须移除")
}

// 未注册实例（Worker 重启后未重推）：按 CP 下发的相对工作目录兜底解析并删除。
func TestRemoveInstance_FallsBackToRequestWorkDir(t *testing.T) {
	srv, _, workDir := newRemoveTestServer(t)

	resp, err := srv.RemoveInstance(context.Background(), &workerpb.RemoveInstanceRequest{
		InstanceUuid: "33333333-3333-3333-3333-333333333333",
		WorkDir:      "var/servers/paper-a-8b4cb747",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, "未注册实例应据 work_dir 兜底删除: %s", resp.Error)
	assert.NoDirExists(t, workDir)
}

// 运行中实例拒绝删除（防 CP/Worker 状态漂移把在跑的服务器连目录端掉）。
func TestRemoveInstance_RefusesRunningInstance(t *testing.T) {
	srv, _, workDir := newRemoveTestServer(t)
	const uuid = "44444444-4444-4444-4444-444444444444"
	registerRemoveInstance(t, srv, uuid, workDir)
	inst, ok := srv.manager.GetInstance(uuid)
	require.True(t, ok)
	inst.State = process.StateRunning

	resp, err := srv.RemoveInstance(context.Background(), &workerpb.RemoveInstanceRequest{InstanceUuid: uuid})
	require.NoError(t, err)
	require.False(t, resp.Success)
	assert.Contains(t, resp.Error, "运行")
	assert.DirExists(t, workDir, "运行中实例的目录不得被删除")
}

// 托管区（var/servers）之外的工作目录不删文件（历史手填绝对路径），
// 但不阻断实例删除：移除注册并回报 work_dir_skipped。
func TestRemoveInstance_SkipsWorkDirOutsideServersRoot(t *testing.T) {
	srv, _, _ := newRemoveTestServer(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("x"), 0o644))
	const uuid = "55555555-5555-5555-5555-555555555555"
	registerRemoveInstance(t, srv, uuid, outside)

	resp, err := srv.RemoveInstance(context.Background(), &workerpb.RemoveInstanceRequest{InstanceUuid: uuid})
	require.NoError(t, err)
	require.True(t, resp.Success)
	assert.True(t, resp.WorkDirSkipped, "托管区外目录应跳过删除而非越界 RemoveAll")
	assert.NotEmpty(t, resp.SkipReason)
	assert.FileExists(t, filepath.Join(outside, "keep.txt"))
	_, ok := srv.manager.GetInstance(uuid)
	assert.False(t, ok, "注册表条目仍应移除")
}

// 幂等：目录已不存在（重试路径）仍返回成功。
func TestRemoveInstance_IdempotentWhenDirAlreadyGone(t *testing.T) {
	srv, _, _ := newRemoveTestServer(t)

	resp, err := srv.RemoveInstance(context.Background(), &workerpb.RemoveInstanceRequest{
		InstanceUuid: "66666666-6666-6666-6666-666666666666",
		WorkDir:      "var/servers/never-existed-deadbeef",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}
