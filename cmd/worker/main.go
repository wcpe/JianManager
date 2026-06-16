package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wxys233/JianManager/internal/worker/process"
	"github.com/wxys233/JianManager/internal/worker/metrics"

	"github.com/wxys233/JianManager/internal/worker/grpc"
)

func main() {
	// TODO: 加载配置文件
	nodeName := "node-01"
	if v := os.Getenv("JIANMANAGER_NODE_NAME"); v != "" {
		nodeName = v
	}

	grpcPort := 9101
	if v := os.Getenv("JIANMANAGER_GRPC_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &grpcPort)
	}

	slog.Info("Worker Node 启动", "name", nodeName, "grpcPort", grpcPort)

	// 初始化进程管理器
	manager := process.NewManager("./servers")

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
	nodeUUID := os.Getenv("JIANMANAGER_NODE_UUID")
	if nodeUUID == "" {
		nodeUUID = "local-dev"
	}

	server := grpc.NewServer(manager, nodeUUID, collector)

	addr := fmt.Sprintf(":%d", grpcPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("监听端口失败", "addr", addr, "error", err)
		os.Exit(1)
	}

	// TODO: 使用 grpc.NewServer() 注册服务并启动
	slog.Info("gRPC 服务器就绪", "addr", addr)
	_ = server
	_ = listener

	// TODO: 连接 Control Plane 进行注册
	// TODO: 启动心跳上报
	// TODO: 启动 WS 终端服务器

	// 等待信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	slog.Info("收到退出信号，正在关闭", "signal", sig)
	manager.StopAll()
	slog.Info("Worker Node 已停止")
}
