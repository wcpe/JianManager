package tunnel

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/jhump/grpctunnel"
	"github.com/jhump/grpctunnel/tunnelpb"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// startFakeCPTCP 与 startFakeCP 同构，但走真实 TCP（127.0.0.1 随机端口）——
// 排查 bufconn 与真实传输在大消息/流控上的行为差异（FR-305 真机 DeadlineExceeded 定位）。
func startFakeCPTCP(t *testing.T) (*grpctunnel.TunnelServiceHandler, string, chan string) {
	t.Helper()
	events := make(chan string, 16)
	handler := grpctunnel.NewTunnelServiceHandler(grpctunnel.TunnelServiceHandlerOptions{
		OnReverseTunnelOpen:  func(tc grpctunnel.TunnelChannel) { events <- "open" },
		OnReverseTunnelClose: func(tc grpctunnel.TunnelChannel) { events <- "close" },
	})
	server := grpc.NewServer() // 与生产 CP 同：外层 server 默认选项
	tunnelpb.RegisterTunnelServiceServer(server, handler.Service())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	return handler, lis.Addr().String(), events
}

// 注：并发常驻流 + 大 unary 的死锁（WrapRegistrar 包裹 stream 干扰 grpctunnel 分块流控）
// 在进程内 bufconn/TCP 回环下无法复现（回环无真实流控饱和），故该回归以**真机实验**为准：
// 撤 WrapRegistrar 的 worker 探针部署 3.6s 成功、带 stream 包裹的 30s DeadlineExceeded。
// 修复后 WrapRegistrar 只守 unary、不再包裹 stream（见 grpcmsg.WrapRegistrar 注释）。

// TestTunnelTCP_ProbeSizedUnary 7.6MB unary（DeployServerProbe 量级）经真实 TCP 反向隧道
// 必须在 30s 内完成（真机复现：DeadlineExceeded）。
func TestTunnelTCP_ProbeSizedUnary(t *testing.T) {
	handler, addr, events := startFakeCPTCP(t)
	r := New(addr, "node-tcp", "secret-tcp", func(reg grpc.ServiceRegistrar) {
		workerpb.RegisterWorkerServiceServer(reg, &bigPayloadWorker{})
	})
	r.initialBackoff = 50 * time.Millisecond
	r.maxBackoff = 200 * time.Millisecond
	r.Start()
	defer r.Stop()
	waitEvent(t, events, "open")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := workerpb.NewWorkerServiceClient(handler.AsChannel())
	start := time.Now()
	resp, err := client.WriteFile(ctx, &workerpb.WriteFileRequest{Path: "probe.jar", Content: make([]byte, 7600*1024)})
	if err != nil || !resp.Success {
		t.Fatalf("7.6MB unary 经 TCP 隧道应成功: resp=%v err=%v (耗时 %v)", resp, err, time.Since(start))
	}
	t.Logf("7.6MB 经 TCP 隧道耗时 %v", time.Since(start))
}
