package grpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestResyncInstances_FillsRegistryForFileOps 验证 bug #2 修复（ADR-050）：
// Worker 重启后内存注册表为空，对既有 STOPPED 实例做文件 op 本会报「实例不存在」；
// CP 经 ResyncInstances 重推规格后，注册表被填充（STOPPED），同一文件 op 即成功。
func TestResyncInstances_FillsRegistryForFileOps(t *testing.T) {
	tmp := t.TempDir()
	// root=nil → WorkDir 按绝对路径处理（与生产 root.Abs 解析后等价），便于用 TempDir 绝对目录。
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()

	const uuid = "11111111-1111-1111-1111-111111111111"
	workDir := filepath.Join(tmp, "inst-stopped")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "server.properties"), []byte("server-port=25565\n"), 0o644))

	// 重推前：实例不在注册表，文件 op 报「实例不存在」（重启后的 bug 现象）。
	_, err := srv.ListFiles(ctx, &workerpb.ListFilesRequest{InstanceUuid: uuid})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "不存在")

	// CP 重推该实例规格（STOPPED，不启动）。
	resp, err := srv.ResyncInstances(ctx, &workerpb.ResyncInstancesRequest{
		Instances: []*workerpb.CreateInstanceRequest{
			{
				InstanceUuid: uuid,
				Name:         "stopped-server",
				ProcessType:  "direct",
				StartCommand: "noop",
				WorkDir:      workDir,
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), resp.Registered)
	assert.Equal(t, int32(0), resp.Skipped)

	// 重推后：实例已登记为 STOPPED，且不被启动。
	inst, ok := srv.manager.GetInstance(uuid)
	require.True(t, ok)
	assert.Equal(t, process.StateStopped, inst.State)
	assert.Equal(t, workDir, inst.WorkDir)

	// 文件 op 现在能定位工作目录并成功。
	listResp, err := srv.ListFiles(ctx, &workerpb.ListFilesRequest{InstanceUuid: uuid})
	require.NoError(t, err)
	names := make([]string, 0, len(listResp.Files))
	for _, f := range listResp.Files {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, "server.properties")
}

// TestResyncInstances_SkipsAlreadyRegistered 验证「只补不覆盖」（ADR-050）：
// 已在注册表的实例（模拟 RecoverDaemonInstances 恢复的 RUNNING 实例）在重推中按 UUID 命中被跳过，
// 其状态与工作目录不被重推覆盖。
func TestResyncInstances_SkipsAlreadyRegistered(t *testing.T) {
	tmp := t.TempDir()
	mgr := process.NewManager(tmp)
	srv := NewServer(mgr, "test-node", nil, nil, nil)
	ctx := context.Background()

	const uuid = "22222222-2222-2222-2222-222222222222"
	origWorkDir := filepath.Join(tmp, "running-orig")
	require.NoError(t, os.MkdirAll(origWorkDir, 0o755))
	// 预置一个已登记实例（模拟 worker 已通过其它路径持有它）。
	require.NoError(t, mgr.Create(uuid, "running-server", "noop", "", origWorkDir, nil, true, process.ProcessTypeDirect, "", "", 0, 0))

	// CP 重推带不同工作目录的同 UUID 规格——应被跳过、不覆盖既有登记。
	resp, err := srv.ResyncInstances(ctx, &workerpb.ResyncInstancesRequest{
		Instances: []*workerpb.CreateInstanceRequest{
			{
				InstanceUuid: uuid,
				Name:         "running-server-renamed",
				ProcessType:  "direct",
				StartCommand: "noop",
				WorkDir:      filepath.Join(tmp, "should-not-apply"),
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.Registered)
	assert.Equal(t, int32(1), resp.Skipped)

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	assert.Equal(t, origWorkDir, inst.WorkDir, "已登记实例的工作目录不应被重推覆盖")
}

// TestResyncInstances_MixedAndIdempotent 验证混合批次（部分新增 + 部分已存在）与幂等重推：
// 第一次重推新增全部，第二次重推全部命中跳过（registered 归零）。
func TestResyncInstances_MixedAndIdempotent(t *testing.T) {
	tmp := t.TempDir()
	mgr := process.NewManager(tmp)
	srv := NewServer(mgr, "test-node", nil, nil, nil)
	ctx := context.Background()

	const known = "33333333-3333-3333-3333-333333333333"
	const fresh = "44444444-4444-4444-4444-444444444444"
	require.NoError(t, mgr.Create(known, "known", "noop", "", filepath.Join(tmp, "known"), nil, false, process.ProcessTypeDirect, "", "", 0, 0))

	specs := []*workerpb.CreateInstanceRequest{
		{InstanceUuid: known, Name: "known", ProcessType: "direct", StartCommand: "noop", WorkDir: filepath.Join(tmp, "known")},
		{InstanceUuid: fresh, Name: "fresh", ProcessType: "direct", StartCommand: "noop", WorkDir: filepath.Join(tmp, "fresh")},
		{InstanceUuid: "", Name: "empty-uuid-ignored"}, // 空 UUID 应被忽略，不计数
	}

	first, err := srv.ResyncInstances(ctx, &workerpb.ResyncInstancesRequest{Instances: specs})
	require.NoError(t, err)
	assert.Equal(t, int32(1), first.Registered, "仅 fresh 新增")
	assert.Equal(t, int32(1), first.Skipped, "known 跳过；空 UUID 不计数")

	// 幂等：再次重推同一批，全部命中跳过。
	second, err := srv.ResyncInstances(ctx, &workerpb.ResyncInstancesRequest{Instances: specs})
	require.NoError(t, err)
	assert.Equal(t, int32(0), second.Registered)
	assert.Equal(t, int32(2), second.Skipped)
}
