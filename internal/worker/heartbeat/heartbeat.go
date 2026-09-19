package heartbeat

import (
	"context"
	"log/slog"
	"sync"
	"time"

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

// probeScrapeState 记录各实例上一次探针抓取的结果，用于「错误变化才告警」的降噪：
// 同一错误每拍重刷会淹没日志中心（探针未部署的实例会永久失败），而完全静默又会让
// 「探针桥已连接但 /metrics 不通」（FR-411：导入实例 probe_port=0、端口被占、探针降级）无从排障。
var probeScrapeState = struct {
	sync.Mutex
	lastErr map[string]string
}{lastErr: map[string]string{}}

// nodeSecretHeader gRPC metadata 中携带 node_secret 的 header 名。
// 心跳鉴权不放进 proto 字段，改用 gRPC metadata（HTTP/2 header），
// 避免改动 proto 与重新生成代码。
const nodeSecretHeader = "node-secret"

// InstanceStateProvider 提供所有实例的状态快照。
type InstanceStateProvider interface {
	GetAllInstanceStates() []process.InstanceSnapshot
}

// TaskSnapshotProvider 提供运行中长任务的心跳快照（FR-183，见 ADR-040）。
// 由 Worker gRPC 服务实现持有的内存任务表实现。终态任务上报后由本心跳调 Drop 移除。
type TaskSnapshotProvider interface {
	TaskSnapshots() []*workerpb.TaskSnapshot
	DropTask(taskID string)
	// CancelTask 强制停止运行中任务（FR-227）：真中断 Worker 操作（如下载）并置 canceled 终态。
	CancelTask(taskID string) bool
}

// ManagedRuntimeProvider 提供 Worker 与已运行 Bot Worker 的只读运行时快照。
// 采集端不得因调用本接口启动、重启或扫描任意非受管进程。
type ManagedRuntimeProvider interface {
	ManagedRuntimeSnapshot() *workerpb.ManagedRuntimeSnapshot
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
	// managedRuntimeProvider 由 Worker 主进程注入；nil 时保持旧 Worker 的 Heartbeat 兼容形状。
	managedRuntimeProvider ManagedRuntimeProvider
	// proxyApplier 据心跳响应里 CP 下发的期望代理运行时重建 Worker 出站 client（FR-185，见 ADR-043）；
	// 为 nil 时心跳不应用下发代理（Worker 仅用本地 yaml/env，向后兼容旧 CP）。
	proxyApplier *proxyApplier
	// wsSecretApplier 据心跳响应里 CP 下发的 WS 令牌密钥热应用（FR-275，见 ADR-061）；
	// 为 nil 时不应用（向后兼容旧 CP：Worker 沿用启动时生效值）。
	wsSecretApplier *wsSecretApplier
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
// 由 main 装配（传入 Worker gRPC 服务实现）；不调用则心跳不携带任务进度。
func (h *Heartbeat) SetTaskProvider(p TaskSnapshotProvider) {
	h.taskProvider = p
}

// SetManagedRuntimeProvider 注入受管运行时快照来源（FR-400）。
func (h *Heartbeat) SetManagedRuntimeProvider(p ManagedRuntimeProvider) {
	h.managedRuntimeProvider = p
}

// SetProxyRebuilder 注入「据心跳下发代理重建出站 client」的回调（FR-185，见 ADR-043）。
// 由 main 装配（包裹 httpclient.Provider.Rebuild）；不调用则忽略 CP 下发的代理（向后兼容）。
// 内部用 generation 比较，仅在期望代理变化时才重建（避免每拍重建）。
func (h *Heartbeat) SetProxyRebuilder(rebuild func(httpclient.Config) error) {
	h.proxyApplier = newProxyApplier(rebuild)
}

// SetWSSecretApplier 注入「据心跳下发 WS 令牌密钥热应用」的回调（FR-275，见 ADR-061）。
// current 为启动时已生效的密钥（首拍据此去重）；apply 由 main 装配（热更新终端/插件桥 +
// 持久化身份文件）。不调用则忽略 CP 下发的密钥（向后兼容）。
func (h *Heartbeat) SetWSSecretApplier(current string, apply func(secret string) error) {
	h.wsSecretApplier = newWSSecretApplier(current, apply)
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
		// 显式空切片（非 nil）：新 Worker 无在管实例时仍启用反向对账「清单已空」语义（FR-326）；
		// 老路径 instanceProvider==nil 才保持 Instances=nil，CP 不启用反向对账。
		req.Instances = make([]*workerpb.InstanceState, 0, len(states))
		for _, s := range states {
			// pid 可选字段（FR-326）：供 CP 反向对账诊断；老 CP 忽略，零值兼容。
			req.Instances = append(req.Instances, &workerpb.InstanceState{
				InstanceUuid: s.UUID,
				State:        s.State,
				Pid:          int32(s.PID),
			})
		}
		req.InstanceMetrics = collectInstanceMetrics(states)
		req.ProcessMetrics = collectProcessMetrics(states)
	}
	if h.managedRuntimeProvider != nil {
		req.ManagedRuntime = h.managedRuntimeProvider.ManagedRuntimeSnapshot()
	}

	// 附加运行中长任务进度快照（FR-183，见 ADR-040）。
	var terminalTaskIDs []string
	if h.taskProvider != nil {
		req.Tasks = h.taskProvider.TaskSnapshots()
		for _, t := range req.Tasks {
			if t.State == "succeeded" || t.State == "failed" || t.State == "canceled" {
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

	// 应用 CP 下发的 WS 令牌密钥（FR-275，见 ADR-061）：值变化才热更新终端/插件桥校验 + 持久化。
	h.wsSecretApplier.applyReply(reply)

	// 应用 CP 下发的取消请求（FR-227）：真中断对应运行中任务（如下载），canceled 经下一拍心跳上报。
	if h.taskProvider != nil {
		for _, id := range reply.GetCancelTaskIds() {
			h.taskProvider.CancelTask(id)
		}
	}

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
				recordProbeScrapeResult(t.UUID, "")
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
			} else {
				// 探针抓取失败（未部署/端口不对/探针降级/桥通而 HTTP 端点挂）：本拍缺测，
				// CP 落 NULL 断点。同一错误只告警一次，错误变化或恢复时再报，兼顾排障与降噪。
				reason := "未知错误"
				if err != nil {
					reason = err.Error()
				}
				recordProbeScrapeResult(t.UUID, reason)
			}
			out[i] = sample
		}(i, t)
	}
	wg.Wait()
	return out
}

// recordProbeScrapeResult 记录一次探针抓取结果并按变化告警：新错误/错误变化 → WARN，
// 恢复 → INFO；同一错误持续存在则保持静默（避免每拍刷屏）。
func recordProbeScrapeResult(instanceUUID, errText string) {
	probeScrapeState.Lock()
	prev, had := probeScrapeState.lastErr[instanceUUID]
	if errText == "" {
		delete(probeScrapeState.lastErr, instanceUUID)
	} else {
		probeScrapeState.lastErr[instanceUUID] = errText
	}
	probeScrapeState.Unlock()

	switch {
	case errText == "":
		if had {
			slog.Info("实例探针 /metrics 抓取已恢复", "instanceId", instanceUUID)
		}
	case !had || prev != errText:
		slog.Warn("实例探针 /metrics 抓取失败（监控时序将缺测；请检查探针是否部署、probe_port 与探针 config 是否一致、探针日志是否降级）",
			"instanceId", instanceUUID, "error", errText)
	}
}
