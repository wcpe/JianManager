// 外部测试包：与 register_identity_test.go 同理（service 已 import grpc，避免循环）。
// 进程内集成测试：真 grpc.Server + bufconn + 真反向隧道，验证 FR-281 M1 的
// 「隧道建立→登记→pool 隧道优先→RPC 可达」与「鉴权拒绝」「无隧道回退直拨」。
package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/jhump/grpctunnel"
	"github.com/jhump/grpctunnel/tunnelpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeTunnelWorker 挂到反向隧道上的最小 WorkerService 实现，GetVersion 返回哨兵值。
type fakeTunnelWorker struct {
	workerpb.UnimplementedWorkerServiceServer
}

func (f *fakeTunnelWorker) GetVersion(ctx context.Context, req *workerpb.GetVersionRequest) (*workerpb.GetVersionResponse, error) {
	return &workerpb.GetVersionResponse{Version: "tunnel-test"}, nil
}

// startTunnelCP 起一个带隧道注册表的进程内 CP gRPC server（bufconn）。
func startTunnelCP(t *testing.T) (*cpgrpc.TunnelRegistry, *gorm.DB, *bufconn.Listener) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}))

	reg := cpgrpc.NewTunnelRegistry(db)
	server := grpc.NewServer(grpc.ChainStreamInterceptor(reg.StreamAuthInterceptor()))
	tunnelpb.RegisterTunnelServiceServer(server, reg.Service())

	lis := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	return reg, db, lis
}

// dialBuf 建立到 bufconn server 的客户端连接。
func dialBuf(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// serveReverseTunnel 以给定身份开反向隧道并挂假 WorkerService；返回停止函数。
func serveReverseTunnel(t *testing.T, lis *bufconn.Listener, uuid, secret string) context.CancelFunc {
	t.Helper()
	conn := dialBuf(t, lis)
	rts := grpctunnel.NewReverseTunnelServer(tunnelpb.NewTunnelServiceClient(conn))
	workerpb.RegisterWorkerServiceServer(rts, &fakeTunnelWorker{})

	ctx, cancel := context.WithCancel(context.Background())
	ctx = metadata.AppendToOutgoingContext(ctx, "node-uuid", uuid, "node-secret", secret)
	go func() { _, _ = rts.Serve(ctx) }()
	t.Cleanup(cancel)
	return cancel
}

// waitConnected 轮询等待隧道登记达到期望状态。
func waitConnected(t *testing.T, reg *cpgrpc.TunnelRegistry, uuid string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reg.Connected(uuid) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Equal(t, want, reg.Connected(uuid), "隧道登记状态未在时限内达到期望")
}

// 隧道建立→pool 隧道优先→RPC 经隧道可达→隧道断开→登记消失。
func TestReverseTunnel_EndToEnd(t *testing.T) {
	reg, db, lis := startTunnelCP(t)
	const uuid, secret = "node-tunnel-1", "secret-1"
	require.NoError(t, db.Create(&model.Node{UUID: uuid, Name: "n1", Secret: secret}).Error)

	cancel := serveReverseTunnel(t, lis, uuid, secret)
	waitConnected(t, reg, uuid, true)

	// pool 两级取连接：有隧道 → 隧道客户端；RPC 真跑通。
	pool := cpgrpc.NewClientPool()
	pool.SetTunnelProvider(reg)
	client, ok := pool.Get(uuid)
	require.True(t, ok)
	ctx, cancelRPC := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRPC()
	resp, err := client.Worker.GetVersion(ctx, &workerpb.GetVersionRequest{})
	require.NoError(t, err)
	require.Equal(t, "tunnel-test", resp.Version)

	// 隧道断开 → 登记消失 → pool 回退（无直拨连接则取不到）。
	cancel()
	waitConnected(t, reg, uuid, false)
	_, ok = pool.Get(uuid)
	require.False(t, ok)
}

// 鉴权：secret 不匹配 / 节点不存在 / 缺身份的隧道一律拒绝，不产生登记。
func TestReverseTunnel_AuthRejected(t *testing.T) {
	reg, db, lis := startTunnelCP(t)
	const uuid, secret = "node-tunnel-2", "secret-2"
	require.NoError(t, db.Create(&model.Node{UUID: uuid, Name: "n2", Secret: secret}).Error)

	// secret 错误
	serveReverseTunnel(t, lis, uuid, "wrong-secret")
	// 节点不存在
	serveReverseTunnel(t, lis, "ghost-node", "whatever")
	// 缺身份（空 uuid/secret 不注入 metadata）
	conn := dialBuf(t, lis)
	rts := grpctunnel.NewReverseTunnelServer(tunnelpb.NewTunnelServiceClient(conn))
	workerpb.RegisterWorkerServiceServer(rts, &fakeTunnelWorker{})
	go func() { _, _ = rts.Serve(context.Background()) }()

	// 给足窗口后确认无任何登记。
	time.Sleep(300 * time.Millisecond)
	require.False(t, reg.Connected(uuid))
	require.False(t, reg.Connected("ghost-node"))
	require.False(t, reg.Connected(""))
}

// 双模式回退：无隧道时 Get 走既有直拨路径（测试注入客户端可取到）。
func TestPool_FallbackToDirectWithoutTunnel(t *testing.T) {
	reg, _, _ := startTunnelCP(t)
	pool := cpgrpc.NewClientPool()
	pool.SetTunnelProvider(reg)

	const uuid = "node-direct-1"
	pool.SetWorkerClientForTest(uuid, workerpb.NewWorkerServiceClient(nil))
	client, ok := pool.Get(uuid)
	require.True(t, ok)
	require.Equal(t, uuid, client.NodeUUID)
}
