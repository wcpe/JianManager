package service

// FR-281 M2（见 ADR-066）终端 gRPC 桥路测试：
// 浏览器 WS → CP 代理（bridgeViaGRPC）→ bufconn 真 gRPC → 真 worker TerminalSession
// → 回环拨真 workerws.TerminalServer——全真链路，不 mock 会话层。

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	wgrpc "github.com/wcpe/JianManager/internal/worker/grpc"
	workerws "github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// startTerminalWorkerGRPC 起 bufconn 真 gRPC server 挂给定 WorkerService 实现，返回其客户端。
func startTerminalWorkerGRPC(t *testing.T, srv workerpb.WorkerServiceServer) workerpb.WorkerServiceClient {
	t.Helper()
	grpcServer := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(grpcServer, srv)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return workerpb.NewWorkerServiceClient(conn)
}

// startWorkerTerminalWS 起真 workerws.TerminalServer（给定 WS 令牌密钥），返回其 ws:// 地址。
func startWorkerTerminalWS(t *testing.T, secret string) string {
	t.Helper()
	workerServer := workerws.NewTerminalServer(secret)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/terminal", workerServer.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/terminal"
}

// gRPC 桥全链路：DB 里的直拨地址指向黑洞端口——welcome 能到达即证明走的是 gRPC 桥。
func TestTerminalProxy_GRPCBridge_EndToEnd(t *testing.T) {
	secret := "terminal-proxy-secret"

	// DB 直拨地址黑洞（端口 1 不可达）：回退路径必失败。
	db, instanceID := newTerminalTestDB(t, "http://127.0.0.1:1")
	svc := NewTerminalService(db, secret, "ws://fallback.invalid")
	proxy := NewTerminalProxy(secret, svc)

	wsrv := &wgrpc.Server{}
	wsrv.SetTerminalWSAddr(startWorkerTerminalWS(t, secret))
	client := startTerminalWorkerGRPC(t, wsrv)
	proxy.SetWorkerClients(func(nodeUUID string) (workerpb.WorkerServiceClient, bool) {
		require.Equal(t, "node-terminal-test", nodeUUID, "gRPC 桥必须按实例所在节点取客户端")
		return client, true
	})

	cpHTTP := httptest.NewServer(proxy.Handler())
	defer cpHTTP.Close()

	token, err := svc.IssueToken(instanceID, "write", mustHost(t, cpHTTP.URL), false)
	require.NoError(t, err)

	conn := dialTerminalProxy(t, token.WSURL, token.Token)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	var welcome map[string]any
	require.NoError(t, conn.ReadJSON(&welcome), "welcome 须经 gRPC 桥到达（直拨地址是黑洞端口）")
}

// 老 Worker 兼容：TerminalSession 返回 Unimplemented → 回退直拨 WS，行为与引入前一致。
func TestTerminalProxy_FallbackToDirectWSOnUnimplemented(t *testing.T) {
	secret := "terminal-proxy-secret"
	workerServer := workerws.NewTerminalServer(secret)
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("/ws/terminal", workerServer.Handler())
	workerHTTP := httptest.NewServer(workerMux)
	defer workerHTTP.Close()

	// 直拨地址指向真 worker WS；gRPC 客户端是老 Worker（全部 RPC Unimplemented）。
	db, instanceID := newTerminalTestDB(t, workerHTTP.URL)
	svc := NewTerminalService(db, secret, "ws://fallback.invalid")
	proxy := NewTerminalProxy(secret, svc)
	proxy.SetWorkerClients(func(string) (workerpb.WorkerServiceClient, bool) {
		return startTerminalWorkerGRPC(t, &workerpb.UnimplementedWorkerServiceServer{}), true
	})

	cpHTTP := httptest.NewServer(proxy.Handler())
	defer cpHTTP.Close()

	token, err := svc.IssueToken(instanceID, "write", mustHost(t, cpHTTP.URL), false)
	require.NoError(t, err)

	conn := dialTerminalProxy(t, token.WSURL, token.Token)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	var welcome map[string]any
	require.NoError(t, conn.ReadJSON(&welcome), "老 Worker 须经直拨 WS 回退路径连通")
}

// 令牌被拒诊断：worker 侧密钥与平台不一致 → gRPC 桥路给出 FR-276 同款定向诊断。
func TestTerminalProxy_GRPCBridge_TokenRejectedDiagnostic(t *testing.T) {
	// 直拨黑洞，强制走 gRPC 桥；worker 终端用不同密钥 → 回环 401 → PermissionDenied。
	db, instanceID := newTerminalTestDB(t, "http://127.0.0.1:1")
	svc := NewTerminalService(db, "terminal-proxy-secret", "ws://fallback.invalid")
	proxy := NewTerminalProxy("terminal-proxy-secret", svc)

	wsrv := &wgrpc.Server{}
	wsrv.SetTerminalWSAddr(startWorkerTerminalWS(t, "a-different-secret"))
	client := startTerminalWorkerGRPC(t, wsrv)
	proxy.SetWorkerClients(func(string) (workerpb.WorkerServiceClient, bool) { return client, true })

	cpHTTP := httptest.NewServer(proxy.Handler())
	defer cpHTTP.Close()

	token, err := svc.IssueToken(instanceID, "write", mustHost(t, cpHTTP.URL), false)
	require.NoError(t, err)

	conn := dialTerminalProxy(t, token.WSURL, token.Token)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	var state map[string]any
	require.NoError(t, conn.ReadJSON(&state))
	require.Equal(t, "state", state["type"])
	require.Equal(t, TerminalProxyCodeWorkerTokenRejected, state["code"],
		"gRPC 桥路的令牌拒绝必须给出 FR-276 定向诊断码")
}
