package grpc

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// startFakeTerminalWS 起本机假终端 WS 服务：token=good 升级并 echo（回写 "echo:"+payload），否则 401。
func startFakeTerminalWS(t *testing.T) string {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "good" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			msgType, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(msgType, append([]byte("echo:"), payload...)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/terminal"
}

// dialTerminalSession 经 bufconn 真 gRPC 开一条 TerminalSession 流。
func dialTerminalSession(t *testing.T, wsAddr string) workerpb.WorkerService_TerminalSessionClient {
	t.Helper()
	s := &Server{}
	s.SetTerminalWSAddr(wsAddr)

	grpcServer := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(grpcServer, s)
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	stream, err := workerpb.NewWorkerServiceClient(conn).TerminalSession(ctx)
	require.NoError(t, err)
	return stream
}

func openFrame(token string) *workerpb.TerminalFrame {
	return &workerpb.TerminalFrame{Kind: &workerpb.TerminalFrame_Open{Open: &workerpb.TerminalOpen{Token: token}}}
}

func dataFrame(payload string) *workerpb.TerminalFrame {
	return &workerpb.TerminalFrame{Kind: &workerpb.TerminalFrame_Frame{Frame: &workerpb.TerminalWSFrame{
		MsgType: websocket.TextMessage, Payload: []byte(payload),
	}}}
}

// 有效令牌：就绪 ack → 数据帧 echo 往返。
func TestTerminalSession_ReadyAndEcho(t *testing.T) {
	stream := dialTerminalSession(t, startFakeTerminalWS(t))
	require.NoError(t, stream.Send(openFrame("good")))

	first, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, first.GetOpen(), "首个响应帧应为就绪 ack")

	require.NoError(t, stream.Send(dataFrame("hello")))
	resp, err := stream.Recv()
	require.NoError(t, err)
	f := resp.GetFrame()
	require.NotNil(t, f)
	require.Equal(t, "echo:hello", string(f.Payload))
}

// 令牌被本机 WS 拒绝（401）→ PermissionDenied 透传（FR-276 诊断语义）。
func TestTerminalSession_TokenRejected(t *testing.T) {
	stream := dialTerminalSession(t, startFakeTerminalWS(t))
	require.NoError(t, stream.Send(openFrame("bad")))

	_, err := stream.Recv()
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// 首帧不是 open → InvalidArgument。
func TestTerminalSession_MissingOpen(t *testing.T) {
	stream := dialTerminalSession(t, startFakeTerminalWS(t))
	require.NoError(t, stream.Send(dataFrame("no-open")))

	_, err := stream.Recv()
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// 未装配 WS 地址 → Unavailable（防御性兜底）。
func TestTerminalSession_NotConfigured(t *testing.T) {
	stream := dialTerminalSession(t, "")
	require.NoError(t, stream.Send(openFrame("good")))

	_, err := stream.Recv()
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
}
