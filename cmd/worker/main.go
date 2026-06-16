package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	wgrpc "github.com/wxys233/JianManager/internal/worker/grpc"
	"github.com/wxys233/JianManager/internal/worker/metrics"
	"github.com/wxys233/JianManager/internal/worker/process"
	"github.com/wxys233/JianManager/internal/worker/ws"
	"github.com/wxys233/JianManager/proto/workerpb"
)

func main() {
	// 配置
	nodeName := "node-01"
	if v := os.Getenv("JIANMANAGER_NODE_NAME"); v != "" {
		nodeName = v
	}

	grpcPort := 9101
	if v := os.Getenv("JIANMANAGER_GRPC_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &grpcPort)
	}

	wsPort := 9102
	if v := os.Getenv("JIANMANAGER_WS_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &wsPort)
	}

	jwtSecret := os.Getenv("JIANMANAGER_JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "dev-secret-change-me"
	}

	nodeUUID := os.Getenv("JIANMANAGER_NODE_UUID")
	if nodeUUID == "" {
		nodeUUID = "local-dev"
	}

	workDir := os.Getenv("JIANMANAGER_WORK_DIR")
	if workDir == "" {
		workDir = "./servers"
	}

	slog.Info("Worker Node 启动", "name", nodeName, "grpcPort", grpcPort, "wsPort", wsPort)

	// 初始化进程管理器
	manager := process.NewManager(workDir)

	// 初始化指标采集器
	collector := metrics.NewCollector(30 * time.Second)
	collector.StartPeriodic(func(m metrics.NodeMetrics) {
		slog.Debug("指标采集",
			"cpu", fmt.Sprintf("%.1f%%", m.CPUUsage*100),
			"memory", fmt.Sprintf("%.1f%%", m.MemoryUsage*100),
			"goroutines", m.Goroutines,
		)
	})
	defer collector.Stop()

	// 启动 gRPC 服务器
	grpcServer := grpc.NewServer()
	workerServer := wgrpc.NewServer(manager, nodeUUID, collector)
	workerpb.RegisterWorkerServiceServer(grpcServer, workerServer)

	grpcAddr := fmt.Sprintf(":%d", grpcPort)
	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		slog.Error("监听 gRPC 端口失败", "addr", grpcAddr, "error", err)
		os.Exit(1)
	}

	go func() {
		slog.Info("gRPC 服务器就绪", "addr", grpcAddr)
		if err := grpcServer.Serve(grpcListener); err != nil {
			slog.Error("gRPC 服务器退出", "error", err)
		}
	}()

	// 启动 WS 终端服务器
	terminalServer := ws.NewTerminalServer(jwtSecret)

	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/ws/terminal", terminalServer.Handler())

	wsAddr := fmt.Sprintf(":%d", wsPort)
	wsServer := &http.Server{Addr: wsAddr, Handler: wsMux}

	go func() {
		slog.Info("WebSocket 终端服务器就绪", "addr", wsAddr)
		if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("WS 服务器退出", "error", err)
		}
	}()

	// TODO: 连接 Control Plane 进行注册（需要 protoc 生成的客户端代码）
	// TODO: 启动心跳上报（需要 Control Plane 连接）

	// 等待信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	slog.Info("收到退出信号，正在关闭", "signal", sig)
	grpcServer.GracefulStop()
	manager.StopAll()
	slog.Info("Worker Node 已停止")
}
