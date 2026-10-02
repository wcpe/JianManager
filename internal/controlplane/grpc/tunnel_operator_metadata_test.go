// 外部测试包：与 tunnel_test.go 同包（复用其 startTunnelCP/dialBuf/waitConnected 夹具）。
package grpc_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jhump/grpctunnel"
	"github.com/jhump/grpctunnel/tunnelpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 本文件为 2026-10-02 缺陷 E2（CP 不透传操作人）修复提供**传输层证据**：
// 生产节点 RPC 的唯一承载是反向隧道（FR-281/ADR-066），CP 侧只要把操作人放进 outgoing
// metadata，Worker 侧就必须能在 incoming metadata 里读到它——否则「CP 注入 x-jm-operator」
// 这个修法本身不成立（会变成「写了但到不了」）。
//
// 与 router 包的用例互补：router 包断言**CP 有没有写**，本文件断言**写了能不能到**。

// operatorMetadataTunnelWorker 记录经反向隧道到达的 incoming metadata 与请求体。
type operatorMetadataTunnelWorker struct {
	workerpb.UnimplementedWorkerServiceServer
	mu         sync.Mutex
	operators  []string
	namespaces []string
}

func (w *operatorMetadataTunnelWorker) LogResolveIngestGaps(ctx context.Context, req *workerpb.LogResolveIngestGapsRequest) (*workerpb.LogTaskResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	operator := ""
	if values := md.Get("x-jm-operator"); len(values) > 0 {
		operator = values[0]
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.operators = append(w.operators, operator)
	w.namespaces = append(w.namespaces, req.GetStorageNamespace())
	return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
}

func (w *operatorMetadataTunnelWorker) snapshot() ([]string, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.operators...), append([]string(nil), w.namespaces...)
}

// serveTunnelWorker 以给定身份开反向隧道并挂指定 WorkerService 实现
// （serveReverseTunnel 的泛化版，本文件需要能记录 metadata 的假 Worker）。
func serveTunnelWorker(t *testing.T, lis *bufconn.Listener, uuid, secret string, worker workerpb.WorkerServiceServer) {
	t.Helper()
	conn := dialBuf(t, lis)
	rts := grpctunnel.NewReverseTunnelServer(tunnelpb.NewTunnelServiceClient(conn))
	workerpb.RegisterWorkerServiceServer(rts, worker)

	ctx, cancel := context.WithCancel(context.Background())
	ctx = metadata.AppendToOutgoingContext(ctx, "node-uuid", uuid, "node-secret", secret)
	go func() { _, _ = rts.Serve(ctx) }()
	t.Cleanup(cancel)
}

// TestReverseTunnelForwardsOperatorMetadata：经反向隧道的 outgoing metadata 必须还原为
// Worker 侧的 incoming metadata（含阴性对照，确保断言不是「夹具泄漏」造成的假绿）。
func TestReverseTunnelForwardsOperatorMetadata(t *testing.T) {
	reg, db, lis := startTunnelCP(t)
	const uuid, secret = "node-operator-1", "secret-operator-1"
	require.NoError(t, db.Create(&model.Node{UUID: uuid, Name: "operator-1", Secret: secret}).Error)

	worker := &operatorMetadataTunnelWorker{}
	serveTunnelWorker(t, lis, uuid, secret, worker)
	waitConnected(t, reg, uuid, true)

	pool := cpgrpc.NewClientPool()
	pool.SetTunnelProvider(reg)
	client, ok := pool.Get(uuid)
	require.True(t, ok)

	// ① 带操作人：必须原样到达 Worker 的 incoming metadata。
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-jm-operator", "ops@example"))
	resp, err := client.Worker.LogResolveIngestGaps(ctx, &workerpb.LogResolveIngestGapsRequest{StorageNamespace: "inst:153/stderr"})
	require.NoError(t, err)
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_SUCCEEDED, resp.GetState())

	// ② 阴性对照：不带操作人时必须读不到该键——否则①的断言可能是「上一轮残留」造成的假绿。
	_, err = client.Worker.LogResolveIngestGaps(context.Background(), &workerpb.LogResolveIngestGapsRequest{StorageNamespace: "inst:153/stderr"})
	require.NoError(t, err)

	operators, namespaces := worker.snapshot()
	require.Equal(t, []string{"ops@example", ""}, operators)
	require.Equal(t, []string{"inst:153/stderr", "inst:153/stderr"}, namespaces)
}
