package tunnel

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wcpe/JianManager/internal/platform/grpcmsg"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// bigPayloadWorker 假 Worker：WriteFile 永远成功——用于证明超限载荷是否真进到 handler。
type bigPayloadWorker struct {
	workerpb.UnimplementedWorkerServiceServer
}

func (w *bigPayloadWorker) WriteFile(context.Context, *workerpb.WriteFileRequest) (*workerpb.WriteFileResponse, error) {
	return &workerpb.WriteFileResponse{Success: true}, nil
}

// newMsgsizeRunner 普通 Runner（register 回调不自带任何包装）：守卫必须由 Runner 的
// serveOnce 统一施加（FR-305）——本测试锁死该不变量。修前复现：serveOnce 裸传注册器，
// 65MiB unary 一路吃进 grpctunnel 的 4GB 天花板，与直拨 64MiB 拒收行为双轨。
func newMsgsizeRunner(lis *bufconn.Listener) *Runner {
	r := New("passthrough:///bufnet", "node-msg", "secret-msg", func(reg grpc.ServiceRegistrar) {
		workerpb.RegisterWorkerServiceServer(reg, &bigPayloadWorker{})
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

// TestTunnel_OversizeUnaryRejected 65MiB unary 经反向隧道 → 必须 ResourceExhausted
// 拒收（与直拨 64MiB 上限同语义，FR-305）。修前：一路吃进、handler 返回 success。
func TestTunnel_OversizeUnaryRejected(t *testing.T) {
	handler, lis, events := startFakeCP(t)
	r := newMsgsizeRunner(lis)
	r.Start()
	defer r.Stop()
	waitEvent(t, events, "open")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := workerpb.NewWorkerServiceClient(handler.AsChannel())
	_, err := client.WriteFile(ctx, &workerpb.WriteFileRequest{
		Path:    "big.bin",
		Content: make([]byte, grpcmsg.MaxMessageBytes+1<<20), // 64MiB+1MiB
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("超限 unary 经隧道应 ResourceExhausted 拒收（直拨同语义），实际: err=%v", err)
	}
}

// TestTunnel_UnderCapUnaryPasses ≤上限载荷经隧道照常放行（8MiB，回归守护）。
func TestTunnel_UnderCapUnaryPasses(t *testing.T) {
	handler, lis, events := startFakeCP(t)
	r := newMsgsizeRunner(lis)
	r.Start()
	defer r.Stop()
	waitEvent(t, events, "open")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := workerpb.NewWorkerServiceClient(handler.AsChannel())
	resp, err := client.WriteFile(ctx, &workerpb.WriteFileRequest{Path: "ok.bin", Content: make([]byte, 8<<20)})
	if err != nil || !resp.Success {
		t.Fatalf("8MiB unary 经隧道应放行: resp=%v err=%v", resp, err)
	}
}
