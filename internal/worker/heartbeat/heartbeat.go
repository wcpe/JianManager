package heartbeat

import (
	"log/slog"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/wxys233/JianManager/internal/worker/process"
	"github.com/wxys233/JianManager/internal/worker/register"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// nodeSecretHeader gRPC metadata 中携带 node_secret 的 header 名。
// 心跳鉴权不放进 proto 字段，改用 gRPC metadata（HTTP/2 header），
// 避免改动 proto 与重新生成代码。
const nodeSecretHeader = "node-secret"

// InstanceStateProvider 提供所有实例的状态快照。
type InstanceStateProvider interface {
	GetAllInstanceStates() []process.InstanceSnapshot
}

// Heartbeat 心跳上报器。
type Heartbeat struct {
	controlPlaneAddr string
	nodeUUID         string
	nodeSecret       string
	interval         time.Duration
	stopCh           chan struct{}
	instanceProvider InstanceStateProvider
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

	// 附加实例状态快照
	if h.instanceProvider != nil {
		states := h.instanceProvider.GetAllInstanceStates()
		for _, s := range states {
			req.Instances = append(req.Instances, &workerpb.InstanceState{
				InstanceUuid: s.UUID,
				State:        s.State,
			})
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

	slog.Debug("心跳已发送", "timestamp", reply.Timestamp,
		"cpu", req.CpuUsage, "memory", req.MemoryUsage)
	return nil
}
