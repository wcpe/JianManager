package tunnel

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/jhump/grpctunnel"
	"github.com/jhump/grpctunnel/tunnelpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeWorker 隧道上挂的最小 WorkerService 实现。
type fakeWorker struct {
	workerpb.UnimplementedWorkerServiceServer
}

func (f *fakeWorker) GetVersion(ctx context.Context, req *workerpb.GetVersionRequest) (*workerpb.GetVersionResponse, error) {
	return &workerpb.GetVersionResponse{Version: "runner-test"}, nil
}

// startFakeCP 起一个进程内「CP」：只挂 TunnelService，开/关事件经 channel 上报。
func startFakeCP(t *testing.T) (*grpctunnel.TunnelServiceHandler, *bufconn.Listener, chan string) {
	t.Helper()
	events := make(chan string, 16)
	handler := grpctunnel.NewTunnelServiceHandler(grpctunnel.TunnelServiceHandlerOptions{
		OnReverseTunnelOpen:  func(tc grpctunnel.TunnelChannel) { events <- "open" },
		OnReverseTunnelClose: func(tc grpctunnel.TunnelChannel) { events <- "close" },
	})
	server := grpc.NewServer()
	tunnelpb.RegisterTunnelServiceServer(server, handler.Service())
	lis := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	return handler, lis, events
}

// newTestRunner 建一个拨 bufconn 的 Runner（退避缩短加速测试）。
func newTestRunner(lis *bufconn.Listener) *Runner {
	r := New("passthrough:///bufnet", "node-x", "secret-x", func(reg grpc.ServiceRegistrar) {
		workerpb.RegisterWorkerServiceServer(reg, &fakeWorker{})
	})
	r.initialBackoff = 50 * time.Millisecond
	r.maxBackoff = 200 * time.Millisecond
	r.dialOpts = []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	}
	return r
}

func waitEvent(t *testing.T, events chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		require.Equal(t, want, got)
	case <-time.After(5 * time.Second):
		t.Fatalf("等待隧道事件 %q 超时", want)
	}
}

// 隧道建立 → RPC 经隧道可达 → CP 侧踢断 → Runner 自动重连 → Stop 干净退出。
func TestRunner_ConnectServeReconnectStop(t *testing.T) {
	handler, lis, events := startFakeCP(t)
	r := newTestRunner(lis)
	r.Start()
	defer r.Stop()

	waitEvent(t, events, "open")

	// RPC 经隧道可达（无亲和键的假 CP 用 AsChannel 轮询全部隧道）。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := workerpb.NewWorkerServiceClient(handler.AsChannel()).GetVersion(ctx, &workerpb.GetVersionRequest{})
	require.NoError(t, err)
	require.Equal(t, "runner-test", resp.Version)

	// CP 侧踢断当前隧道 → Runner 退避后自动重建。
	tunnels := handler.AllReverseTunnels()
	require.Len(t, tunnels, 1)
	tunnels[0].Close()
	waitEvent(t, events, "close")
	waitEvent(t, events, "open")

	// Stop 后隧道关闭且不再重连。
	r.Stop()
	waitEvent(t, events, "close")
	select {
	case e := <-events:
		t.Fatalf("Stop 后不应再有隧道事件，收到 %q", e)
	case <-time.After(400 * time.Millisecond): // > maxBackoff，足以证明未重连
	}
}
