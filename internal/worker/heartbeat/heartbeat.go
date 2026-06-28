package heartbeat

import (
	"log/slog"
	"sync"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/internal/worker/register"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// maxConcurrentProbeScrapes 单次心跳并发抓取 ServerProbe 的上限，避免实例多时一拍抓爆。
// 抓取本身有 5s 超时（见 metrics.ScrapeServerProbe）；实例规模化下的进一步优化见 spec 开放问题。
const maxConcurrentProbeScrapes = 8

// nodeSecretHeader gRPC metadata 中携带 node_secret 的 header 名。
// 心跳鉴权不放进 proto 字段，改用 gRPC metadata（HTTP/2 header），
// 避免改动 proto 与重新生成代码。
const nodeSecretHeader = "node-secret"

// InstanceStateProvider 提供所有实例的状态快照。
type InstanceStateProvider interface {
	GetAllInstanceStates() []process.InstanceSnapshot
}

// TaskSnapshotProvider 提供运行中长任务的心跳快照（FR-183，见 ADR-040）。
// 由 Worker gRPC Server 实现（其内存任务登记表）。终态任务上报后由本心跳调 Drop 移除。
type TaskSnapshotProvider interface {
	TaskSnapshots() []*workerpb.TaskSnapshot
	DropTask(taskID string)
}

// Heartbeat 心跳上报器。
type Heartbeat struct {
	controlPlaneAddr string
	nodeUUID         string
	nodeSecret       string
	interval         time.Duration
	stopCh           chan struct{}
	instanceProvider InstanceStateProvider
	// taskProvider 运行中任务快照来源（FR-183）；为 nil 时心跳不带任务字段（向后兼容）。
	taskProvider TaskSnapshotProvider
	// proxyApplier 据心跳响应里 CP 下发的期望代理运行时重建 Worker 出站 client（FR-185，见 ADR-043）；
	// 为 nil 时心跳不应用下发代理（Worker 仅用本地 yaml/env，向后兼容旧 CP）。
	proxyApplier *proxyApplier
}

// New 创建心跳上报器。
// nodeSecret 由注册阶段从 Control Plane 获得，用于心跳鉴权。
func New(controlPlaneAddr, nodeUUID, nodeSecret string, interval time.Duration, provider InstanceStateProvider) *Heartbeat {
	return &Heartbeat{
		controlPlaneAddr: controlPlaneAddr,
		nodeUUID:         nodeUUID,
		nodeSecret:       nodeSecret,
		interval:         interval,
		stopCh:           make(chan struct{}),
		instanceProvider: provider,
	}
}

// SetTaskProvider 注入运行中任务快照来源（FR-183，见 ADR-040）。
// 由 main 装配（传入 Worker gRPC Server）；不调用则心跳不携带任务进度。
func (h *Heartbeat) SetTaskProvider(p TaskSnapshotProvider) {
	h.taskProvider = p
}

// SetProxyRebuilder 注入「据心跳下发代理重建出站 client」的回调（FR-185，见 ADR-043）。
// 由 main 装配（包裹 httpclient.Provider.Rebuild）；不调用则忽略 CP 下发的代理（向后兼容）。
// 内部用 generation 比较，仅在期望代理变化时才重建（避免每拍重建）。
func (h *Heartbeat) SetProxyRebuilder(rebuild func(httpclient.Config) error) {
	h.proxyApplier = newProxyApplier(rebuild)
}

// Start 启动心跳上报。
func (h *Heartbeat) Start() {
	go h.loop()
	slog.Info("心跳上报已启动", "interval", h.interval, "nodeUUID", h.nodeUUID)
}

// Stop 停止心跳上报。
func (h *Heartbeat) Stop() {
	close(h.stopCh)
	slog.Info("心跳上报已停止")
}

func (h *Heartbeat) loop() {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.sendHeartbeatWithRetry()
		}
	}
}

// sendHeartbeatWithRetry 发送一次心跳，失败时按指数退避重试，
// 直到成功或到达单轮最大重试时长（不阻塞 ticker 过久）。
// Control Plane 不可达时 Worker 不 panic，仅记录日志并等待下一周期。
func (h *Heartbeat) sendHeartbeatWithRetry() {
	const maxBackoff = 30 * time.Second
	backoff := 2 * time.Second
	deadline := time.Now().Add(h.interval)

	for {
		if err := h.sendHeartbeat(); err == nil {
			return
		}

		if time.Now().After(deadline) {
			slog.Warn("本周期心跳重试已达上限，等待下一周期", "nodeUUID", h.nodeUUID)
			return
		}

		select {
		case <-h.stopCh:
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (h *Heartbeat) sendHeartbeat() error {
	conn, err := grpc.NewClient(h.controlPlaneAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		slog.Warn("心跳连接 Control Plane 失败", "error", err)
		return err
	}
	defer conn.Close()

	client := workerpb.NewWorkerServiceClient(conn)

	// 采集心跳数据
	req := register.CollectHeartbeatData(h.nodeUUID)

	// 附加实例状态快照 + 每实例 ServerProbe 富指标快照（FR-060 时序留存）
	if h.instanceProvider != nil {
		states := h.instanceProvider.GetAllInstanceStates()
		for _, s := range states {
			req.Instances = append(req.Instances, &workerpb.InstanceState{
				InstanceUuid: s.UUID,
				State:        s.State,
			})
		}
		req.InstanceMetrics = collectInstanceMetrics(states)
	}

	// 附加运行中长任务进度快照（FR-183，见 ADR-040）。
	var terminalTaskIDs []string
	if h.taskProvider != nil {
		req.Tasks = h.taskProvider.TaskSnapshots()
		for _, t := range req.Tasks {
			if t.State == "succeeded" || t.State == "failed" {
				terminalTaskIDs = append(terminalTaskIDs, t.TaskId)
			}
		}
	}

	// 通过 gRPC metadata 携带 node_secret 供 Control Plane 鉴权
	ctx := metadata.AppendToOutgoingContext(context.Background(), nodeSecretHeader, h.nodeSecret)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := client.Heartbeat(ctx)
	if err != nil {
		slog.Warn("心跳发送失败", "error", err)
		return err
	}

	if err := resp.Send(req); err != nil {
		slog.Warn("心跳发送失败", "error", err)
		return err
	}

	reply, err := resp.Recv()
	if err != nil {
		slog.Warn("心跳响应接收失败", "error", err)
		return err
	}

	// 应用 CP 下发的期望出站代理（FR-185，见 ADR-043）：generation 变化才重建出站 client。
	// 重连/重启天然由后续心跳重发，无需 Worker 落盘。
	h.proxyApplier.apply(reply)

	// 终态任务已随本次心跳上报且 CP 已确认接收，从内存表移除避免重复上报（FR-183，见 ADR-040）。
	// 仅在心跳成功确认后才 Drop，确保 CP 至少收到一次终态快照。
	if h.taskProvider != nil {
		for _, id := range terminalTaskIDs {
			h.taskProvider.DropTask(id)
		}
	}

	slog.Debug("心跳已发送", "timestamp", reply.Timestamp,
		"cpu", req.CpuUsage, "memory", req.MemoryUsage)
	return nil
}

// collectInstanceMetrics 对 RUNNING 且部署了探针的实例并发抓取本机 ServerProbe /metrics，
// 构造心跳负载里的每实例富指标快照（FR-060 时序）。抓取失败时该实例 probe_available=false（缺测，
// CP 落库为 NULL，曲线断点），不阻塞其他实例采集。无可采实例时返回 nil。
func collectInstanceMetrics(snaps []process.InstanceSnapshot) []*workerpb.InstanceMetricSample {
	targets := make([]process.InstanceSnapshot, 0, len(snaps))
	for _, s := range snaps {
		if s.State == string(process.StateRunning) && s.ProbePort > 0 {
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		return nil
	}

	out := make([]*workerpb.InstanceMetricSample, len(targets))
	sem := make(chan struct{}, maxConcurrentProbeScrapes)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t process.InstanceSnapshot) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sample := &workerpb.InstanceMetricSample{InstanceUuid: t.UUID}
			// 探针与实例同机，抓 localhost:probe_port；本机白名单放行，无需 token。
			if snap, err := metrics.ScrapeServerProbe("localhost", t.ProbePort, ""); err == nil && snap != nil {
				sample.ProbeAvailable = true
				sample.Tps = snap.TPS
				sample.MsptMillis = snap.MSPTAvgMillis
				sample.PlayersOnline = snap.PlayersOnline
				sample.HeapUsedBytes = snap.HeapUsedBytes
				sample.HeapMaxBytes = snap.HeapMaxBytes
				sample.Threads = snap.Threads
				sample.CpuLoad = snap.SystemCPULoad
				sample.UptimeSeconds = snap.UptimeSeconds
				for name, w := range snap.Worlds {
					sample.Worlds = append(sample.Worlds, &workerpb.WorldMetric{
						Name:         name,
						LoadedChunks: w.LoadedChunks,
						Entities:     w.Entities,
						TileEntities: w.TileEntities,
					})
				}
			}
			out[i] = sample
		}(i, t)
	}
	wg.Wait()
	return out
}
