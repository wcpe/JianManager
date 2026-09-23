package grpc

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fr471SleepCommand 返回一个可控的长驻启动命令（跨平台）：
// 用进程自身作为被托管的「游戏服」，仅用于验证 RPC 的接线与错误范式。
func fr471StartCommand() string {
	if runtime.GOOS == "windows" {
		return "timeout /t 60 /nobreak"
	}
	return "sleep 60"
}

// TestAdoptForeignRuntime_Unregistered 未注册实例：RPC 不返回 gRPC 错误，而是
// Success=false + Error（与 StopInstance 的错误范式一致，由 CP 回写 statusReason）。
func TestAdoptForeignRuntime_Unregistered(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	srv := NewServer(mgr, "node-fr471", nil, nil, nil)

	resp, err := srv.AdoptForeignRuntime(context.Background(), &workerpb.InstanceActionRequest{InstanceUuid: "missing-uuid"})
	require.NoError(t, err, "业务失败不应是 gRPC 传输错误")
	require.False(t, resp.Success)
	assert.Contains(t, resp.Error, "不存在")
}

// TestAdoptForeignRuntime_NoDriftStartsInstance 无漂移时接管等价于一次受管启动：
// RPC 成功、实例转 RUNNING（true 端到端接线：鉴权拦截器之外的 RPC → Manager → 真实进程）。
func TestAdoptForeignRuntime_NoDriftStartsInstance(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	mgr.SetMemGuard(process.MemGuardConfig{Disabled: true})
	uuid := "fr471-adopt-e2e"
	require.NoError(t, mgr.Create(uuid, "fr471", fr471StartCommand(), "stop", t.TempDir(), nil, false, process.ProcessTypeDirect, "", "", 0, 0))
	t.Cleanup(func() { _ = mgr.Kill(uuid) })

	srv := NewServer(mgr, "node-fr471", nil, nil, nil)
	resp, err := srv.AdoptForeignRuntime(context.Background(), &workerpb.InstanceActionRequest{InstanceUuid: uuid})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)

	state, err := mgr.GetState(uuid)
	require.NoError(t, err)
	assert.Equal(t, process.StateRunning, state)
}
