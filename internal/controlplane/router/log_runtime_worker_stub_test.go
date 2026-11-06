package router

// 本文件放 log-runtime 用例共用的假 Worker 桩。
//
// # 为什么单独一个文件
//
// 它们原本住在 `log_resolve_gaps_operator_test.go`。那个文件整份验证「解缺口」授权，
// 随旧采集链路的缺口机制一起删除；但桩被多个**其他**用例复用（授权审计等），
// 删文件时若不把它们移出来，那些用例会一起编译失败。

import (
	"context"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// recordingResolveGapsWorker 记录 CP 转发的 metadata 与请求体。
//
// 采用「嵌入 nil 接口 + 只实现被调用方法」的桩形态（与包内既有假 Worker 同型）：
// 未实现的方法在这些用例中不会被触达，因此不需要为整个 WorkerService 写桩。
type recordingResolveGapsWorker struct {
	workerpb.WorkerServiceClient
	mu       sync.Mutex
	calls    int
	hasMD    bool
	operator string
	reason   string
	request  *workerpb.LogResolveIngestGapsRequest
}

func (w *recordingResolveGapsWorker) LogResolveIngestGaps(ctx context.Context, in *workerpb.LogResolveIngestGapsRequest, _ ...grpc.CallOption) (*workerpb.LogTaskResponse, error) {
	md, ok := metadata.FromOutgoingContext(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	w.hasMD = ok
	if values := md.Get(operatorMetadataKey); len(values) > 0 {
		w.operator = values[0]
	}
	if values := md.Get("x-jm-reason"); len(values) > 0 {
		w.reason = values[0]
	}
	w.request = in
	return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
}

type resolveGapsCall struct {
	calls    int
	hasMD    bool
	operator string
	reason   string
	request  *workerpb.LogResolveIngestGapsRequest
}

func (w *recordingResolveGapsWorker) snapshot() resolveGapsCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return resolveGapsCall{calls: w.calls, hasMD: w.hasMD, operator: w.operator, reason: w.reason, request: w.request}
}
