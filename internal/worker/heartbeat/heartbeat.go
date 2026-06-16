package heartbeat

import (
	"log/slog"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/wxys233/JianManager/internal/worker/register"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// Heartbeat 心跳上报器。
type Heartbeat struct {
	controlPlaneAddr string
	nodeUUID         string
	interval         time.Duration
	stopCh           chan struct{}
}

// New 创建心跳上报器。
func New(controlPlaneAddr, nodeUUID string, interval time.Duration) *Heartbeat {
	return &Heartbeat{
		controlPlaneAddr: controlPlaneAddr,
		nodeUUID:         nodeUUID,
		interval:         interval,
		stopCh:           make(chan struct{}),
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
			h.sendHeartbeat()
		}
	}
}

func (h *Heartbeat) sendHeartbeat() {
	conn, err := grpc.NewClient(h.controlPlaneAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		slog.Warn("心跳连接 Control Plane 失败", "error", err)
		return
	}
	defer conn.Close()

	client := workerpb.NewWorkerServiceClient(conn)

	// 采集心跳数据
	req := register.CollectHeartbeatData(h.nodeUUID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Heartbeat(ctx)
	if err != nil {
		slog.Warn("心跳发送失败", "error", err)
		return
	}

	if err := resp.Send(req); err != nil {
		slog.Warn("心跳发送失败", "error", err)
		return
	}

	reply, err := resp.Recv()
	if err != nil {
		slog.Warn("心跳响应接收失败", "error", err)
		return
	}

	slog.Debug("心跳已发送", "timestamp", reply.Timestamp,
		"cpu", req.CpuUsage, "memory", req.MemoryUsage)
}
