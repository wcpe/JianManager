// Package orphanaudit 把 Worker 侧的孤儿处置/误杀拦截审计经既有 Worker→CP 出站 gRPC 信道
// 上报 CP 审计库（FR-455/456）。
//
// 背景：运行期周期孤儿扫描与接管兜底的处置动作（scan_detected / scan_disposed /
// scan_dispose_blocked / dispose_blocked / dispose_reaped）此前只落 Worker 结构化日志，
// 未进 CP 审计库，违反 spec §2.2「处置动作一律落审计（reviewer 可查）」。本包复用与崩溃快照
// 上报（FR-313）相同的「Worker→CP 同址出站信道 + 节点身份鉴权」模式，把审计送进
// internal/controlplane/service/audit.go 的 RecordResultSafe。
//
// 上报为尽力而为：失败（网络 / 老 CP Unimplemented）记日志丢弃，不重试不排队——审计增强
// 不得反向影响进程处置路径。
//
// 投递形态（FR-456 N4）：有界队列 + **单条复用的 gRPC 连接** + 单个派发 goroutine。
// 单轮扫描可产上千 findings，此前「每事件一个 goroutine + 每条新建连接」会造成 fd/连接风暴；
// 现改为：Report 非阻塞入队（队列满即丢弃并告警），派发 goroutine 串行复用同一 client 上报。
package orphanaudit

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/proto/workerpb"
)

const (
	// reportTimeout 单次上报的超时上限（与崩溃快照上报同量级）。
	reportTimeout = 10 * time.Second
	// reportQueueSize 上报队列容量上限（FR-456 N4）。单轮扫描可产上千 findings，超出即丢弃并告警：
	// 审计是尽力而为，宁可丢尾部几条，也不阻塞处置路径或把连接/fd 打满。
	reportQueueSize = 256
)

// Event 一条待上报的孤儿处置审计（字段与 process.Manager 的审计回调一致）。
type Event struct {
	Action   string
	TargetID string
	Detail   string
	Success  bool
	ErrMsg   string
}

// Reporter 孤儿审计上报器：走 Worker→CP 既有出站信道（与注册/心跳同址，NAT 节点天然可达）。
//
// 连接经 connMu 惰性创建并**全局复用**（FR-456 N4）：单轮上千 findings 不再各建一条连接。
// Reporter 生命周期由 main 持有，进程退出前应调用 Close 释放连接与派发 goroutine。
type Reporter struct {
	cpAddr string

	// connMu 同时守护惰性建连与关闭，避免 Close 与在建上报竞争。
	connMu  sync.Mutex
	conn    *grpc.ClientConn
	client  workerpb.WorkerServiceClient
	connErr error
	closed  bool

	mu         sync.RWMutex
	nodeUUID   string
	nodeSecret string

	// 有界队列 + 单派发 goroutine（首次 Report 时惰性启动）。
	startOnce sync.Once
	queue     chan Event
	stopCh    chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
}

// New 创建上报器。节点身份注册成功后经 SetIdentity 注入；注入前的上报直接丢弃。
func New(cpAddr string) *Reporter {
	return &Reporter{
		cpAddr: cpAddr,
		queue:  make(chan Event, reportQueueSize),
		stopCh: make(chan struct{}),
	}
}

// SetIdentity 注入节点身份（注册成功后由 main 装配调用），供上报鉴权。
func (r *Reporter) SetIdentity(nodeUUID, nodeSecret string) {
	r.mu.Lock()
	r.nodeUUID = nodeUUID
	r.nodeSecret = nodeSecret
	r.mu.Unlock()
}

// Report 异步上报一条孤儿审计：立即返回，不阻塞调用方（扫描/处置路径）。
//
// 入队即返回；队列满时丢弃本条并告警（FR-456 N4）。派发由单个后台 goroutine 串行完成。
func (r *Reporter) Report(ev Event) {
	if r == nil {
		return
	}
	select {
	case <-r.stopCh:
		return // 已关闭：丢弃。
	default:
	}
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go r.loop()
	})
	select {
	case r.queue <- ev:
	default:
		slog.Warn("孤儿审计上报队列已满，丢弃本条（审计尽力而为，不阻塞处置路径）",
			"action", ev.Action, "target", ev.TargetID, "queueSize", reportQueueSize)
	}
}

// Close 停止派发 goroutine 并关闭复用的 gRPC 连接。可重复调用。
func (r *Reporter) Close() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.wg.Wait()
	r.connMu.Lock()
	defer r.connMu.Unlock()
	r.closed = true
	if r.conn != nil {
		_ = r.conn.Close()
		r.conn = nil
		r.client = nil
	}
}

// loop 单派发 goroutine：串行消费队列并以复用连接上报（FR-456 N4）。
func (r *Reporter) loop() {
	defer r.wg.Done()
	for {
		// 先非阻塞查停止信号，使关闭后尽快退出（避免 select 随机仍取队列）。
		select {
		case <-r.stopCh:
			return
		default:
		}
		select {
		case <-r.stopCh:
			return
		case ev := <-r.queue:
			r.report(ev)
		}
	}
}

// serviceClient 返回复用的 gRPC 客户端，仅首次调用建连（FR-456 N4）。
func (r *Reporter) serviceClient() (workerpb.WorkerServiceClient, error) {
	r.connMu.Lock()
	defer r.connMu.Unlock()
	if r.closed {
		return nil, errors.New("孤儿审计上报器已关闭")
	}
	if r.client != nil || r.connErr != nil {
		return r.client, r.connErr
	}
	conn, err := grpc.NewClient(r.cpAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		r.connErr = err
		return nil, err
	}
	r.conn = conn
	r.client = workerpb.NewWorkerServiceClient(conn)
	return r.client, nil
}

// report 同步执行一次上报（拆出便于单测直接调用）。
func (r *Reporter) report(ev Event) {
	r.mu.RLock()
	nodeUUID, nodeSecret := r.nodeUUID, r.nodeSecret
	r.mu.RUnlock()
	if nodeUUID == "" || nodeSecret == "" {
		slog.Debug("节点身份未就绪，孤儿审计丢弃", "action", ev.Action, "target", ev.TargetID)
		return
	}

	client, err := r.serviceClient()
	if err != nil {
		slog.Warn("孤儿审计上报连接 Control Plane 失败，丢弃", "action", ev.Action, "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	_, err = client.ReportOrphanAudit(ctx, &workerpb.ReportOrphanAuditRequest{
		NodeUuid:   nodeUUID,
		NodeSecret: nodeSecret,
		Action:     ev.Action,
		TargetId:   ev.TargetID,
		Detail:     ev.Detail,
		Success:    ev.Success,
		Error:      ev.ErrMsg,
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			// 老 CP 不认识本 RPC：降噪为 Info，不告警。
			slog.Info("Control Plane 不支持孤儿审计上报（老版本），本条丢弃", "action", ev.Action)
			return
		}
		slog.Warn("孤儿审计上报失败，丢弃", "action", ev.Action, "error", err)
		return
	}
	slog.Debug("孤儿审计已上报", "action", ev.Action, "target", ev.TargetID, "success", ev.Success)
}
