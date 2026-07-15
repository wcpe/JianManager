package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestCreateInstance_RefreshesExistingLaunchSpec 验证 FR-233 的 Worker 注册入口：
// 同 UUID 幂等重注册必须刷新启动命令、JDK、环境变量与 autoRestart，供下一次生命周期采用。
func TestCreateInstance_RefreshesExistingLaunchSpec(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	srv := NewServer(mgr, "test-node", nil, nil, nil)
	ctx := context.Background()
	const uuid = "fr233-refresh"

	first, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "实例",
		ProcessType:  string(process.ProcessTypeDirect),
		StartCommand: "old-command",
		WorkDir:      t.TempDir(),
		EnvVars:      map[string]string{"OLD": "1"},
		JdkPath:      "old-jdk",
		JdkBinPath:   "old-bin",
		AutoRestart:  false,
	})
	require.NoError(t, err)
	require.True(t, first.Success)

	second, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "实例",
		ProcessType:  string(process.ProcessTypeDirect),
		StartCommand: "new-command",
		WorkDir:      t.TempDir(),
		EnvVars:      map[string]string{"NEW": "2"},
		JdkPath:      "new-jdk",
		JdkBinPath:   "new-bin",
		AutoRestart:  true,
	})
	require.NoError(t, err)
	require.True(t, second.Success)

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	require.Equal(t, "new-command", inst.StartCommand)
	require.Equal(t, "new-jdk", inst.JDKPath)
	require.Equal(t, "new-bin", inst.JDKBinPath)
	require.Equal(t, map[string]string{"NEW": "2"}, inst.EnvVars)
	require.True(t, inst.AutoRestart)
}
