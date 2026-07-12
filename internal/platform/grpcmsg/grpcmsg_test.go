package grpcmsg

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// bigBytes 返回一个超过 MaxMessageBytes 的载荷（64MiB+1MiB）。
func bigBytes() []byte { return make([]byte, MaxMessageBytes+1<<20) }

// guardedWorker 假 Worker：WriteFile 原样成功；ReadFile 返回可控大小内容；UploadFile 计帧。
type guardedWorker struct {
	workerpb.UnimplementedWorkerServiceServer
	readSize int
}

func (w *guardedWorker) WriteFile(_ context.Context, req *workerpb.WriteFileRequest) (*workerpb.WriteFileResponse, error) {
	return &workerpb.WriteFileResponse{Success: true}, nil
}

func (w *guardedWorker) ReadFile(_ context.Context, _ *workerpb.ReadFileRequest) (*workerpb.ReadFileResponse, error) {
	return &workerpb.ReadFileResponse{Content: make([]byte, w.readSize)}, nil
}

func (w *guardedWorker) UploadFile(stream workerpb.WorkerService_UploadFileServer) error {
	for {
		if _, err := stream.Recv(); err != nil {
			if err == io.EOF {
				return stream.SendAndClose(&workerpb.UploadFileResponse{Success: true})
			}
			return err
		}
	}
}

// startGuarded 起一个「经 WrapRegistrar 注册」的进程内 server（真实生成 ServiceDesc 驱动，
// 覆盖 spec 风险项），返回带显式 CallOptions 的客户端。
func startGuarded(t *testing.T, impl workerpb.WorkerServiceServer, callOpts ...grpc.CallOption) workerpb.WorkerServiceClient {
	t.Helper()
	// server 侧不设原生尺寸选项——守卫必须完全由 WrapRegistrar 提供（模拟隧道无选项环境）。
	// 原生默认 recv 4MiB 会先于拦截器拒掉大请求，故放开到无界以逼真隧道语义。
	srv := grpc.NewServer(grpc.MaxRecvMsgSize(int(^uint32(0) >> 1)))
	workerpb.RegisterWorkerServiceServer(WrapRegistrar(srv), impl)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithDefaultCallOptions(callOpts...),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return workerpb.NewWorkerServiceClient(conn)
}

func requireExhausted(t *testing.T, err error) {
	t.Helper()
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("应 ResourceExhausted 拒收，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "64MiB") {
		t.Fatalf("错误应含上限说明，实际: %v", err)
	}
}

// TestWrapRegistrar_UnaryRequestOverCapRejected 请求超限 → 拒收且不进 handler（FR-305）。
func TestWrapRegistrar_UnaryRequestOverCapRejected(t *testing.T) {
	c := startGuarded(t, &guardedWorker{}, CallOptions()...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// 客户端显式 send 上限也会先拒——为直击服务端守卫，客户端放开 send。
	_, err := c.WriteFile(ctx, &workerpb.WriteFileRequest{Path: "a.bin", Content: bigBytes()},
		grpc.MaxCallSendMsgSize(int(^uint32(0)>>1)))
	requireExhausted(t, err)
}

// TestWrapRegistrar_UnaryResponseOverCapRejected 响应超限 → 发送前拦截拒收（FR-305）。
func TestWrapRegistrar_UnaryResponseOverCapRejected(t *testing.T) {
	c := startGuarded(t, &guardedWorker{readSize: MaxMessageBytes + 1<<20},
		grpc.MaxCallRecvMsgSize(int(^uint32(0)>>1))) // 客户端放开 recv，直击服务端守卫
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := c.ReadFile(ctx, &workerpb.ReadFileRequest{Path: "a.bin"})
	requireExhausted(t, err)
}

// TestWrapRegistrar_UnderCapPasses ≤上限照常放行（8MiB 请求 + 8MiB 响应，FR-305）。
func TestWrapRegistrar_UnderCapPasses(t *testing.T) {
	c := startGuarded(t, &guardedWorker{readSize: 8 << 20}, CallOptions()...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.WriteFile(ctx, &workerpb.WriteFileRequest{Path: "a.bin", Content: make([]byte, 8<<20)}); err != nil {
		t.Fatalf("8MiB 请求应放行: %v", err)
	}
	resp, err := c.ReadFile(ctx, &workerpb.ReadFileRequest{Path: "a.bin"})
	if err != nil {
		t.Fatalf("8MiB 响应应放行: %v", err)
	}
	if len(resp.Content) != 8<<20 {
		t.Fatalf("响应内容长度不符: %d", len(resp.Content))
	}
}

// TestWrapRegistrar_StreamNotWrapped 流式小帧照常放行（FR-304 生产帧型 64KiB×4）——
// WrapRegistrar 只守 unary、不包裹 stream（避免干扰 grpctunnel 分块流控，真机 DeployServerProbe
// DeadlineExceeded 的根因）；流的尺寸由直拨 ServerOptions / 隧道 16KiB 分块天然兜底。
func TestWrapRegistrar_StreamNotWrapped(t *testing.T) {
	c := startGuarded(t, &guardedWorker{}, CallOptions()...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	up, err := c.UploadFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := up.Send(&workerpb.UploadFileChunk{Path: "a.bin", Content: make([]byte, 64<<10)}); err != nil {
			t.Fatalf("小帧应放行: %v", err)
		}
	}
	if _, err := up.CloseAndRecv(); err != nil {
		t.Fatalf("小帧流应成功: %v", err)
	}
}

// TestCallOptions_RecvCapExplicit CP 客户端显式 recv 上限修 4MiB 默认暗礁：
// 8MiB 响应默认失败、带 CallOptions 成功（FR-305 验收项 2 的机制证明）。
func TestCallOptions_RecvCapExplicit(t *testing.T) {
	// 默认客户端（无 CallOptions）：8MiB 响应撞 4MiB 默认。
	cDefault := startGuarded(t, &guardedWorker{readSize: 8 << 20})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := cDefault.ReadFile(ctx, &workerpb.ReadFileRequest{Path: "a.bin"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("默认 4MiB 上限应拒 8MiB 响应，实际: %v", err)
	}
	// 显式 CallOptions：同响应成功（TestWrapRegistrar_UnderCapPasses 已覆盖成功侧，此处对照即可）。
}
