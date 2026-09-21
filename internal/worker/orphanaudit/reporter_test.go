package orphanaudit

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

// stubCP 是只实现 ReportOrphanAudit 的假 CP：记录收到的请求供断言。
type stubCP struct {
	workerpb.UnimplementedWorkerServiceServer
	got chan *workerpb.ReportOrphanAuditRequest
}

func (s *stubCP) ReportOrphanAudit(_ context.Context, req *workerpb.ReportOrphanAuditRequest) (*workerpb.ReportOrphanAuditResponse, error) {
	s.got <- req
	return &workerpb.ReportOrphanAuditResponse{}, nil
}

// startStubCP 在回环地址起一个假 CP gRPC 服务器，返回地址。
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

// TestReporter_Report 上报 happy path：请求字段与事件一一对应，且携带节点身份。
func TestReporter_Report(t *testing.T) {
	stub := &stubCP{got: make(chan *workerpb.ReportOrphanAuditRequest, 1)}
	addr := startStubCP(t, stub)

	r := New(addr)
	r.SetIdentity("node-1", "secret-1")
	r.report(Event{
		Action:   "orphan.scan_dispose_blocked",
		TargetID: "inst-1",
		Detail:   `{"kind":"docker_leftover"}`,
		Success:  false,
		ErrMsg:   "容器仍 running",
	})

	select {
	case req := <-stub.got:
		assert.Equal(t, "node-1", req.NodeUuid)
		assert.Equal(t, "secret-1", req.NodeSecret)
		assert.Equal(t, "orphan.scan_dispose_blocked", req.Action)
		assert.Equal(t, "inst-1", req.TargetId)
		assert.Equal(t, `{"kind":"docker_leftover"}`, req.Detail)
		assert.False(t, req.Success)
		assert.Equal(t, "容器仍 running", req.Error)
	case <-time.After(5 * time.Second):
		t.Fatal("等待假 CP 收到孤儿审计超时")
	}
}

// TestReporter_UnimplementedCPDoesNotPanic 老 CP 兜底：服务端未实现本 RPC 时安静丢弃、不 panic。
func TestReporter_UnimplementedCPDoesNotPanic(t *testing.T) {
	addr := startStubCP(t, &workerpb.UnimplementedWorkerServiceServer{})
	r := New(addr)
	r.SetIdentity("node-1", "secret-1")
	assert.NotPanics(t, func() {
		r.report(Event{Action: "orphan.scan_detected", TargetID: "wd"})
	})
}

// TestReporter_NoIdentityDropped 身份未注入时直接丢弃，不尝试建连。
func TestReporter_NoIdentityDropped(t *testing.T) {
	r := New("127.0.0.1:1") // 不可达地址：若误尝试建连并等待会显著变慢
	start := time.Now()
	assert.NotPanics(t, func() { r.report(Event{Action: "orphan.scan_detected"}) })
	assert.Less(t, time.Since(start), time.Second, "无身份应立即丢弃，不等待网络")
}

// TestReporter_ReportQueueDelivers FR-456 N4：Report 经有界队列 + 单派发 goroutine 送达假 CP。
func TestReporter_ReportQueueDelivers(t *testing.T) {
	stub := &stubCP{got: make(chan *workerpb.ReportOrphanAuditRequest, 4)}
	addr := startStubCP(t, stub)

	r := New(addr)
	defer r.Close()
	r.SetIdentity("node-1", "secret-1")
	r.Report(Event{Action: "orphan.scan_detected", TargetID: "wd", Detail: `{"kind":"direct_orphan"}`})

	select {
	case req := <-stub.got:
		assert.Equal(t, "node-1", req.NodeUuid)
		assert.Equal(t, "orphan.scan_detected", req.Action)
		assert.Equal(t, "wd", req.TargetId)
	case <-time.After(5 * time.Second):
		t.Fatal("队列上报未送达")
	}
}

// TestReporter_ReportNeverBlocks FR-456 N4：队列满时 Report 立即丢弃、绝不阻塞处置路径
// （防止单轮上千 findings 拖住扫描/处置线程）。
func TestReporter_ReportNeverBlocks(t *testing.T) {
	r := New("127.0.0.1:1") // 不可达：派发侧会失败但不影响 Report 入队语义
	defer r.Close()
	r.SetIdentity("node-1", "secret-1")

	start := time.Now()
	for i := 0; i < reportQueueSize*4; i++ {
		r.Report(Event{Action: "orphan.scan_detected", TargetID: "wd"})
	}
	assert.Less(t, time.Since(start), 2*time.Second, "队列满应丢弃而非阻塞")
}

// TestReporter_CloseIdempotent Close 可重复调用且关闭后 Report 不 panic。
func TestReporter_CloseIdempotent(t *testing.T) {
	r := New("127.0.0.1:1")
	r.Close()
	assert.NotPanics(t, func() {
		r.Close()
		r.Report(Event{Action: "orphan.scan_detected"})
	})
}
