package register

import (
	"fmt"
	"log/slog"
	"net"
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
	GrpcPort         int    // gRPC 端口（供 Control Plane 反向连接）
	Host             string // 本机 IP（留空自动检测，优先 127.0.0.1）
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

// RegisterWithRetry 带指数退避重试的注册。
// Control Plane 未启动或暂时不可达时，Worker 不退出，按指数退避重试直到成功或 ctx 取消。
// 退避区间 [initialDelay, maxDelay]，每次 ×2，上限 maxDelay。
func RegisterWithRetry(ctx context.Context, cfg Config, initialDelay, maxDelay time.Duration) (*Result, error) {
	delay := initialDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 60 * time.Second
	}

	var lastErr error
	for {
		result, err := Register(ctx, cfg)
		if err == nil {
			return result, nil
		}
		lastErr = err
		slog.Warn("注册 Control Plane 失败，稍后重试",
			"addr", cfg.ControlPlaneAddr, "error", err, "retryIn", delay)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("注册中止: %w (最后错误: %v)", ctx.Err(), lastErr)
		case <-time.After(delay):
		}

		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// collectSystemInfo 采集系统信息用于注册。
func collectSystemInfo(cfg Config) *workerpb.RegisterRequest {
	host := cfg.Host
	if host == "" {
		host = getOutboundIP()
	}

	info := &workerpb.RegisterRequest{
		Name:     cfg.NodeName,
		Host:     host,
		GrpcPort: int32(cfg.GrpcPort),
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

// getOutboundIP 获取本机出口 IP（通过 UDP 探测，不实际发送数据）。
// 失败时回退到 127.0.0.1。
func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
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
