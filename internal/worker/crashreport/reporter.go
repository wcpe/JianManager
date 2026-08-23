package crashreport

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// reportTimeout 单次上报的超时上限（与心跳单次发送同量级）。
const reportTimeout = 10 * time.Second

// Snapshot 一条待上报的崩溃快照（FR-313）。
type Snapshot struct {
	// InstanceUUID 崩溃实例。
	InstanceUUID string
	// OccurredAt 崩溃发生时刻。
	OccurredAt time.Time
	// ExitCode 进程退出码；无法获知时为 -1。
	ExitCode int
	// Signal 终止信号名（Unix）；Windows / 非信号退出为空。
	Signal string
	// DurationMs 本次运行时长（毫秒）。
	DurationMs int64
	// TailOutput 崩溃前终端尾部输出（≤DefaultTailLines 行 / DefaultTailBytes 字节）。
	TailOutput string
}

// Reporter 崩溃快照上报器：走 Worker→CP 既有出站信道（与注册/心跳同址，
// NAT 节点同样可达），上报失败（网络 / 老 CP Unimplemented）记日志丢弃，
// 不重试不排队——快照是尽力而为的诊断增强，不得反向影响进程状态机。
type Reporter struct {
	cpAddr string

	mu         sync.RWMutex
	nodeUUID   string
	nodeSecret string
}

// New 创建上报器。节点身份注册成功后经 SetIdentity 注入；注入前的上报直接丢弃
// （实例启动依赖 CP 下发，注册前不存在受管进程崩溃，实际不触达）。
func New(cpAddr string) *Reporter {
	return &Reporter{cpAddr: cpAddr}
}

// SetIdentity 注入节点身份（注册成功后由 main 装配调用），供上报鉴权。
func (r *Reporter) SetIdentity(nodeUUID, nodeSecret string) {
	r.mu.Lock()
	r.nodeUUID = nodeUUID
	r.nodeSecret = nodeSecret
	r.mu.Unlock()
}

// Report 异步上报一条崩溃快照：立即返回，不阻塞调用方（进程崩溃处理路径）。
func (r *Reporter) Report(snap Snapshot) {
	go r.report(snap)
}

// report 同步执行一次上报（拆出便于单测直接调用）。
func (r *Reporter) report(snap Snapshot) {
	r.mu.RLock()
	nodeUUID, nodeSecret := r.nodeUUID, r.nodeSecret
	r.mu.RUnlock()
	if nodeUUID == "" || nodeSecret == "" {
		slog.Debug("节点身份未就绪，崩溃快照丢弃", "instanceId", snap.InstanceUUID)
		return
	}

	conn, err := grpc.NewClient(r.cpAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Warn("崩溃快照上报连接 Control Plane 失败，丢弃", "instanceId", snap.InstanceUUID, "error", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	_, err = workerpb.NewWorkerServiceClient(conn).ReportCrashSnapshot(ctx, &workerpb.ReportCrashSnapshotRequest{
		NodeUuid:         nodeUUID,
		NodeSecret:       nodeSecret,
		InstanceUuid:     snap.InstanceUUID,
		OccurredAtUnixMs: snap.OccurredAt.UnixMilli(),
		ExitCode:         int32(snap.ExitCode),
		Signal:           snap.Signal,
		DurationMs:       snap.DurationMs,
		TailOutput:       snap.TailOutput,
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			// 老 CP 不认识本 RPC（新 Worker 对老 CP 兜底，spec §3）：降噪为 Info，不告警。
			slog.Info("Control Plane 不支持崩溃快照上报（老版本），本条丢弃", "instanceId", snap.InstanceUUID)
			return
		}
		slog.Warn("崩溃快照上报失败，丢弃", "instanceId", snap.InstanceUUID, "error", err)
		return
	}
	slog.Info("崩溃快照已上报", "instanceId", snap.InstanceUUID, "exitCode", snap.ExitCode, "durationMs", snap.DurationMs)
}
