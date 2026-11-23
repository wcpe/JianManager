package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestStartInstanceAppliesRequestLimits N-3：启动请求携带的生效限额必须在启动前落地到实例记账。
//
// 缺陷现场：限额原先只经幂等重注册（CreateInstance）刷新，而重注册失败不阻断启动 →
// Worker 沿用旧 spec 的 cgroup 限额，配额巡检登记的收紧值静默不生效。本用例直接验证
// 「随动作请求送达」这条新通路。
func TestStartInstanceAppliesRequestLimits(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	srv := NewServer(mgr, "node-n3", nil, nil, nil)
	ctx := context.Background()
	const uuid = "n3-apply-limits"

	_, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "实例",
		ProcessType:  string(process.ProcessTypeDocker),
		StartCommand: "./beacon", WorkDir: t.TempDir(),
		CpuLimit: 2, MemLimitMb: 2048,
	})
	require.NoError(t, err)

	// 巡检收紧后的生效限额随启动请求送达（比 CreateInstance 时的规格更严）。
	// 启动会因本机无 Docker 而失败，但那发生在限额落地之后——本用例只断言落地。
	_, _ = srv.StartInstance(ctx, &workerpb.InstanceActionRequest{
		InstanceUuid: uuid, WithLimits: true, CpuLimit: 1.8, MemLimitMb: 1843,
	})

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	require.Equal(t, 1.8, inst.CPULimit, "启动请求携带的 CPU 限额必须落到实例记账")
	require.EqualValues(t, 1843, inst.MemLimitMB, "启动请求携带的内存限额必须落到实例记账")
}

// TestActionLimitsNotAppliedWithoutFlag 未声明 with_limits 时不覆盖（兼容旧版 CP）：
// 0 在 cgroup 语义里是有效值「不限制」，不能用「非 0 才应用」的写法区分「未携带」与「显式解除」。
func TestActionLimitsNotAppliedWithoutFlag(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	srv := NewServer(mgr, "node-n3b", nil, nil, nil)
	ctx := context.Background()
	const uuid = "n3-no-flag"

	_, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "实例",
		ProcessType:  string(process.ProcessTypeDocker),
		StartCommand: "./beacon", WorkDir: t.TempDir(),
		CpuLimit: 2, MemLimitMb: 2048,
	})
	require.NoError(t, err)

	// 旧版 CP：不带 with_limits 且字段为零值 → 必须保持既有 spec。
	_, _ = srv.StartInstance(ctx, &workerpb.InstanceActionRequest{InstanceUuid: uuid})

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	require.Equal(t, 2.0, inst.CPULimit, "未声明携带限额时不得把既有限额清零")
	require.EqualValues(t, 2048, inst.MemLimitMB)
}

// TestActionLimitsCanClearThrottle 收紧解除必须能送达：with_limits=true 且值 0 → 覆盖为 0（不限制）。
func TestActionLimitsCanClearThrottle(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	srv := NewServer(mgr, "node-n3c", nil, nil, nil)
	ctx := context.Background()
	const uuid = "n3-clear"

	_, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "实例",
		ProcessType:  string(process.ProcessTypeDocker),
		StartCommand: "./beacon", WorkDir: t.TempDir(),
		CpuLimit: 1.8, MemLimitMb: 1843,
	})
	require.NoError(t, err)

	_, _ = srv.StartInstance(ctx, &workerpb.InstanceActionRequest{
		InstanceUuid: uuid, WithLimits: true, CpuLimit: 0, MemLimitMb: 0,
	})

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	require.Zero(t, inst.CPULimit, "显式携带 0 表示解除限制，必须生效")
	require.Zero(t, inst.MemLimitMB)
}

// TestRestartInstanceAppliesRequestLimits 重启路径同样应用（容器重建是限额落地的时机）。
func TestRestartInstanceAppliesRequestLimits(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	srv := NewServer(mgr, "node-n3d", nil, nil, nil)
	ctx := context.Background()
	const uuid = "n3-restart-limits"

	_, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "实例",
		ProcessType:  string(process.ProcessTypeDocker),
		StartCommand: "./beacon", WorkDir: t.TempDir(),
		CpuLimit: 4, MemLimitMb: 4096,
	})
	require.NoError(t, err)

	_, _ = srv.RestartInstance(ctx, &workerpb.InstanceActionRequest{
		InstanceUuid: uuid, WithLimits: true, CpuLimit: 3.6, MemLimitMb: 3686,
	})

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	require.Equal(t, 3.6, inst.CPULimit)
	require.EqualValues(t, 3686, inst.MemLimitMB)
}

// TestSetResourceLimitsIsIdempotentForUnknownInstance 实例不存在时幂等忽略（与 SetProbePort
// 等容错风格一致），不得 panic 或阻塞启动路径。
func TestSetResourceLimitsIsIdempotentForUnknownInstance(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	require.NotPanics(t, func() { mgr.SetResourceLimits("n3-nonexistent", 1, 1) })
}
