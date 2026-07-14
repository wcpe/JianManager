package crashreport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// stubCP 是只实现 ReportCrashSnapshot 的假 CP：记录收到的请求供断言。
type stubCP struct {
	workerpb.UnimplementedWorkerServiceServer
	got chan *workerpb.ReportCrashSnapshotRequest
}

func (s *stubCP) ReportCrashSnapshot(_ context.Context, req *workerpb.ReportCrashSnapshotRequest) (*workerpb.ReportCrashSnapshotResponse, error) {
	s.got <- req
	return &workerpb.ReportCrashSnapshotResponse{}, nil
}

// startStubCP 在回环地址起一个假 CP gRPC 服务器，返回地址与请求接收通道。
func startStubCP(t *testing.T, srv workerpb.WorkerServiceServer) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(gs, srv)
	go func() { _ = gs.Serve(ln) }()
	t.Cleanup(gs.Stop)
	return ln.Addr().String()
}

// TestReporter_Report 快照组装上报 happy path：请求字段与快照一一对应，且携带节点身份。
func TestReporter_Report(t *testing.T) {
	stub := &stubCP{got: make(chan *workerpb.ReportCrashSnapshotRequest, 1)}
	addr := startStubCP(t, stub)

	r := New(addr)
	r.SetIdentity("node-1", "secret-1")
	occurred := time.Now()
	r.report(Snapshot{
		InstanceUUID: "inst-1",
		OccurredAt:   occurred,
		ExitCode:     1,
		Signal:       "killed",
		DurationMs:   1234,
		TailOutput:   "boom\n",
	})

	select {
	case req := <-stub.got:
		assert.Equal(t, "node-1", req.NodeUuid)
		assert.Equal(t, "secret-1", req.NodeSecret)
		assert.Equal(t, "inst-1", req.InstanceUuid)
		assert.Equal(t, occurred.UnixMilli(), req.OccurredAtUnixMs)
		assert.Equal(t, int32(1), req.ExitCode)
		assert.Equal(t, "killed", req.Signal)
		assert.Equal(t, int64(1234), req.DurationMs)
		assert.Equal(t, "boom\n", req.TailOutput)
	case <-time.After(5 * time.Second):
		t.Fatal("等待假 CP 收到崩溃快照超时")
	}
}

// TestReporter_UnimplementedCPDoesNotPanic 老 CP 兜底（spec §5 新老互不炸的单测面）：
// 服务端未实现本 RPC（UnimplementedWorkerServiceServer 返回 Unimplemented），
// 上报安静丢弃、不 panic 不重试。
func TestReporter_UnimplementedCPDoesNotPanic(t *testing.T) {
	addr := startStubCP(t, &workerpb.UnimplementedWorkerServiceServer{})

	r := New(addr)
	r.SetIdentity("node-1", "secret-1")
	assert.NotPanics(t, func() {
		r.report(Snapshot{InstanceUUID: "inst-1", OccurredAt: time.Now(), ExitCode: 1})
	})
}

// TestReporter_NoIdentityDropped 身份未注入（注册前）时直接丢弃，不尝试建连。
func TestReporter_NoIdentityDropped(t *testing.T) {
	r := New("127.0.0.1:1") // 不可达地址：若误尝试建连并等待会显著变慢/报错
	start := time.Now()
	assert.NotPanics(t, func() {
		r.report(Snapshot{InstanceUUID: "inst-1", OccurredAt: time.Now()})
	})
	assert.Less(t, time.Since(start), time.Second, "无身份应立即丢弃，不等待网络")
}
