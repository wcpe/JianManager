package register

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/wxys233/JianManager/proto/workerpb"
)

// Config 注册配置。
type Config struct {
	ControlPlaneAddr string // Control Plane gRPC 地址
	NodeName         string // 节点名称
	WsPort           int    // WebSocket 端口
}

// Result 注册结果。
type Result struct {
	NodeUUID   string
	NodeSecret string
}

// Register 向 Control Plane 注册当前 Worker Node。
func Register(ctx context.Context, cfg Config) (*Result, error) {
	conn, err := grpc.NewClient(cfg.ControlPlaneAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("连接 Control Plane 失败: %w", err)
	}
	defer conn.Close()

	client := workerpb.NewWorkerServiceClient(conn)

	// 采集系统信息
	info := collectSystemInfo(cfg)

	slog.Info("正在向 Control Plane 注册", "addr", cfg.ControlPlaneAddr, "name", cfg.NodeName)

	resp, err := client.Register(ctx, info)
	if err != nil {
		return nil, fmt.Errorf("注册失败: %w", err)
	}

	slog.Info("注册成功", "nodeUUID", resp.NodeUuid)
	return &Result{
		NodeUUID:   resp.NodeUuid,
		NodeSecret: resp.NodeSecret,
	}, nil
}

// collectSystemInfo 采集系统信息用于注册。
func collectSystemInfo(cfg Config) *workerpb.RegisterRequest {
	info := &workerpb.RegisterRequest{
		Name:     cfg.NodeName,
		Host:     getOutboundIP(),
		WsPort:   int32(cfg.WsPort),
		Os:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CpuCores: int32(runtime.NumCPU()),
	}

	if vmem, err := mem.VirtualMemory(); err == nil {
		info.MemoryMb = int64(vmem.Total / 1024 / 1024)
	}

	if usage, err := disk.Usage("/"); err == nil {
		info.DiskTotalMb = int64(usage.Total / 1024 / 1024)
	}

	return info
}

// getOutboundIP 获取本机出口 IP。
func getOutboundIP() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "127.0.0.1"
	}
	return hostname
}

// WaitForControlPlane 等待 Control Plane 可用。
func WaitForControlPlane(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		conn, err := grpc.NewClient(addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err == nil {
			// 尝试调用
			client := workerpb.NewWorkerServiceClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, err = client.Register(ctx, &workerpb.RegisterRequest{Name: "probe"})
			cancel()
			conn.Close()

			if err == nil {
				return nil
			}
		}

		slog.Info("等待 Control Plane 就绪...", "addr", addr)
		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("等待 Control Plane 超时: %s", addr)
}

// CollectHeartbeatData 采集心跳数据。
func CollectHeartbeatData(nodeUUID string) *workerpb.HeartbeatRequest {
	req := &workerpb.HeartbeatRequest{
		NodeUuid: nodeUUID,
	}

	if vmem, err := mem.VirtualMemory(); err == nil {
		req.MemoryUsage = float32(vmem.UsedPercent / 100.0)
		req.MemoryUsedMb = int64(vmem.Used / 1024 / 1024)
	}

	if percents, err := cpu.Percent(time.Second, false); err == nil && len(percents) > 0 {
		req.CpuUsage = float32(percents[0] / 100.0)
	}

	if usage, err := disk.Usage("/"); err == nil {
		req.DiskUsage = float32(usage.UsedPercent / 100.0)
		req.DiskUsedMb = int64(usage.Used / 1024 / 1024)
	}

	return req
}
