package grpc

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestDeployServerProbe_OverGRPCAllowsLargePayload 复现并回归：DeployServerProbe 经真实 gRPC
// 传输下发探针 jar + 运行库缓存（合计可超 gRPC 默认 4MiB 单消息上限）时，不得被 Worker
// 服务端以 ResourceExhausted 拒收。
//
// 既有 TestDeployServerProbe 直接在进程内调用 handler，不经 gRPC 编解码，因此覆盖不到该上限——
// 这正是真机建服/建代理时内嵌探针部署报 ResourceExhausted（7.6MB vs 4MB）的验证盲区。
// 本用例走 bufconn + 生产 ServerOptions() 打通真实传输，把该盲区下沉为自动化回归（FR-010/FR-114）。
func TestDeployServerProbe_OverGRPCAllowsLargePayload(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)

	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer(ServerOptions()...)
	workerpb.RegisterWorkerServiceServer(grpcServer, srv)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := workerpb.NewWorkerServiceClient(conn)

	ctx := context.Background()
	const uuid = "33333333-3333-3333-3333-333333333333"
	workDir := filepath.Join(tmp, "inst")
	createResp, err := client.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "probe-large",
		StartCommand: "noop",
		WorkDir:      workDir,
		ProcessType:  "direct",
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)

	// 构造 >4MiB 的探针 jar 载荷：protobuf 默认不压缩，序列化后仍稳定超过默认 4MiB 上限。
	// 非法 zip 会被 readProbeRuntimeMeta 静默跳过依赖预置，故 DeployServerProbe 仍应成功落盘。
	jar := make([]byte, 6*1024*1024)
	for i := range jar {
		jar[i] = byte(i*31 + 7)
	}

	resp, err := client.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
		InstanceUuid: uuid,
		Jar:          jar,
		ConfigYaml:   "metrics:\n  enabled: true\n",
	})
	if status.Code(err) == codes.ResourceExhausted {
		t.Fatalf("DeployServerProbe 大载荷被 gRPC 单消息上限拒收（应放宽 MaxRecvMsgSize）: %v", err)
	}
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
}
