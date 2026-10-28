package grpc

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestInspectManagedProcessRejectsPidOutsideInstanceTree(t *testing.T) {
	srv, _, uuid := startFR343RunningInstance(t)

	resp, err := srv.InspectManagedProcess(context.Background(), &workerpb.ManagedProcessInspectRequest{
		InstanceUuid: uuid,
		Pid:          int32(os.Getpid()),
	})
	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Equal(t, "PID_NOT_MANAGED", resp.Code)
}

/*
  根进程允许 PID 级处置（用户要求：卡死时必须能清理）。

  这条用例锁住的是「放开后行为正确」：处置成功，且**实例状态随根进程退出被同步**——
  原先靠拒绝根进程来保证状态一致性，现在改为由 waitLoop 的退出监听保证，
  所以这里必须验证状态确实变了，否则放开就退化成「杀了但平台还记着 RUNNING」。
*/
func TestTerminateManagedProcessAllowsRootPID(t *testing.T) {
	srv, mgr, uuid := startFR343RunningInstance(t)
	rootPID := mgr.GetInstancePID(uuid)
	require.Greater(t, rootPID, 0)

	resp, err := srv.TerminateManagedProcess(context.Background(), &workerpb.ManagedProcessActionRequest{
		InstanceUuid: uuid,
		Pid:          int32(rootPID),
		Mode:         "kill_tree",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, "根进程应当允许处置：%s", resp.Message)

	// 退出监听把实例同步为 CRASHED/STOPPED，不会停在 RUNNING。
	require.Eventually(t, func() bool {
		st, err := mgr.GetState(uuid)
		if err != nil {
			return true
		}
		return st != process.StateRunning
	}, 5*time.Second, 50*time.Millisecond)
}

func TestInspectManagedProcessReturnsTreeScopedDetail(t *testing.T) {
	srv, mgr, uuid := startFR343RunningInstance(t)
	rootPID := int32(mgr.GetInstancePID(uuid))
	require.Greater(t, rootPID, int32(0))

	var resp *workerpb.ManagedProcessInspectResponse
	require.Eventually(t, func() bool {
		var err error
		resp, err = srv.InspectManagedProcess(context.Background(), &workerpb.ManagedProcessInspectRequest{
			InstanceUuid: uuid,
			Pid:          rootPID,
		})
		return err == nil && resp.Success && resp.Target != nil && len(resp.Children) > 0
	}, 5*time.Second, 100*time.Millisecond)

	require.Equal(t, int64(rootPID), resp.RootPid)
	require.Equal(t, rootPID, resp.Target.Pid)
	require.True(t, resp.Target.IsRoot)
	require.NotZero(t, resp.SampledAtUnixMs)
	for _, child := range resp.Children {
		require.NotEqual(t, int32(os.Getpid()), child.Pid, "不得泄露实例树外的测试进程")
	}

	childResp, err := srv.InspectManagedProcess(context.Background(), &workerpb.ManagedProcessInspectRequest{InstanceUuid: uuid, Pid: resp.Children[0].Pid})
	require.NoError(t, err)
	require.True(t, childResp.Success)
	require.NotEmpty(t, childResp.Ancestors)
	require.Equal(t, rootPID, childResp.Ancestors[0].Pid)
}

func TestTerminateManagedProcessRejectsUnsupportedMode(t *testing.T) {
	srv, mgr, uuid := startFR343RunningInstance(t)
	rootPID := int32(mgr.GetInstancePID(uuid))
	require.Greater(t, rootPID, int32(0))

	var detail *workerpb.ManagedProcessInspectResponse
	require.Eventually(t, func() bool {
		var err error
		detail, err = srv.InspectManagedProcess(context.Background(), &workerpb.ManagedProcessInspectRequest{InstanceUuid: uuid, Pid: rootPID})
		return err == nil && detail.Success && len(detail.Children) > 0
	}, 5*time.Second, 100*time.Millisecond)

	resp, err := srv.TerminateManagedProcess(context.Background(), &workerpb.ManagedProcessActionRequest{
		InstanceUuid: uuid,
		Pid:          detail.Children[0].Pid,
		Mode:         "kill",
	})
	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Equal(t, "INVALID_REQUEST", resp.Code)
}

func TestManagedProcessCommandSummaryMasksSensitiveArgs(t *testing.T) {
	summary := truncateCommandSummary("java -jar server.jar --token abc123 --password=secret --api-key:raw --name lobby")

	require.NotContains(t, summary, "abc123")
	require.NotContains(t, summary, "secret")
	require.NotContains(t, summary, "raw")
	require.Contains(t, summary, "--token ******")
	require.Contains(t, summary, "--password=******")
	require.Contains(t, summary, "--api-key:******")
	require.Contains(t, summary, "--name lobby")
}
