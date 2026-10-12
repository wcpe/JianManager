package grpc

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func lifecycleRPCClient(t *testing.T, srv *Server, options ...gogrpc.ServerOption) workerpb.WorkerServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	rpc := gogrpc.NewServer(options...)
	workerpb.RegisterWorkerServiceServer(rpc, srv)
	go func() { _ = rpc.Serve(lis) }()
	conn, err := gogrpc.NewClient("passthrough:///lifecycle", gogrpc.WithTransportCredentials(insecure.NewCredentials()), gogrpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(); rpc.Stop(); _ = lis.Close() })
	return workerpb.NewWorkerServiceClient(conn)
}

// 真实本地 RPC 取消必须传到上一代退出等待；不能等预算耗尽才写 CRASHED。
func TestRV016RPCStartAndRestartCancellation(t *testing.T) {
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "600ms")
	for _, action := range []string{"start", "restart"} {
		t.Run(action, func(t *testing.T) {
			pidDir := t.TempDir()
			m := process.NewManager(pidDir)
			const uuid = "rv016-rpc"
			require.NoError(t, m.Create(uuid, "实例", "sleep 60", "", t.TempDir(), nil, false, process.ProcessTypeDaemon, "", "", 0, 0))
			require.NoError(t, daemon.NewPIDFile(daemon.PIDFileName(pidDir, uuid)).WriteRecord(daemon.PIDRecord{WrapperPID: os.Getpid(), JavaPID: os.Getpid(), InstanceUUID: uuid}))
			// 即使负例转红，也先等等待阶段收尾再删除 PID 文件，避免误放行真实启动。
			t.Cleanup(func() {
				require.Eventually(t, func() bool { state, _ := m.GetState(uuid); return state != process.StateStarting }, time.Second, 5*time.Millisecond)
			})
			client := lifecycleRPCClient(t, NewServer(m, "node", nil, nil, nil))
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			if action == "start" {
				_, _ = client.StartInstance(ctx, &workerpb.InstanceActionRequest{InstanceUuid: uuid})
			} else {
				_, _ = client.RestartInstance(ctx, &workerpb.InstanceActionRequest{InstanceUuid: uuid})
			}
			require.Eventually(t, func() bool { state, _ := m.GetState(uuid); return state == process.StateStopped }, 200*time.Millisecond, 5*time.Millisecond, "RPC 取消后 Worker 应立即恢复未启动状态")
			time.Sleep(650 * time.Millisecond)
			state, err := m.GetState(uuid)
			require.NoError(t, err)
			require.Equal(t, process.StateStopped, state, "不能迟到崩溃或迟到启动")
		})
	}
}

// 既有重推入口应补足收养实例的完整规格，而不是只补端口。
func TestRV015RPCResyncFillsCompleteSpec(t *testing.T) {
	m := process.NewManager(t.TempDir())
	const uuid = "rv015-spec"
	require.NoError(t, m.Create(uuid, "未知实例", "", "", t.TempDir(), nil, false, process.ProcessTypeDaemon, "", "", 0, 0))
	srv := NewServer(m, "node", nil, nil, nil)
	spec := &workerpb.CreateInstanceRequest{InstanceUuid: uuid, Name: "实例", StartCommand: "sleep 60", StopCommand: "save-stop", EnvVars: map[string]string{"K": "V"}, AutoRestart: true, JdkPath: "home", JdkBinPath: "bin", ProcessType: "daemon", GracefulStopTimeoutSeconds: 120}
	_, err := srv.ResyncInstances(context.Background(), &workerpb.ResyncInstancesRequest{Instances: []*workerpb.CreateInstanceRequest{spec}})
	require.NoError(t, err)
	inst, _ := m.GetInstance(uuid)
	require.Equal(t, spec.StartCommand, inst.StartCommand)
	require.Equal(t, spec.StopCommand, inst.StopCommand)
	require.Equal(t, spec.EnvVars, inst.EnvVars)
	require.Equal(t, spec.JdkPath, inst.JDKPath)
	require.Equal(t, spec.JdkBinPath, inst.JDKBinPath)
	require.True(t, inst.AutoRestart)
	require.Equal(t, 120, inst.GracefulStopTimeoutSeconds)
	require.Equal(t, process.StateStopped, inst.State)
}
