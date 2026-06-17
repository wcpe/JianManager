package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/wxys233/JianManager/internal/worker/daemon"
	"github.com/wxys233/JianManager/internal/worker/heartbeat"
	jdks "github.com/wxys233/JianManager/internal/worker/jdk"
	wgrpc "github.com/wxys233/JianManager/internal/worker/grpc"
	"github.com/wxys233/JianManager/internal/worker/metrics"
	"github.com/wxys233/JianManager/internal/worker/process"
	"github.com/wxys233/JianManager/internal/worker/register"
	"github.com/wxys233/JianManager/internal/worker/ws"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// main 是 Worker Node 入口。
// 若以 `daemon` 子命令模式启动（由 daemonStrategy spawn），则运行 wrapper 而非 Worker 主进程。
// 见 ADR-003: 守护进程 Wrapper 模式。
func main() {
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		runDaemonWrapper()
		return
	}
	runWorker()
}

// runDaemonWrapper 以 wrapper 子进程模式运行。
// 配置通过环境变量 JM_DAEMON_WRAPPER_CONFIG 传递（JSON）。
func runDaemonWrapper() {
	cfg, err := daemon.ParseWrapperConfigFromEnv()
	if err != nil {
		slog.Error("daemon wrapper 配置解析失败", "error", err)
		os.Exit(1)
	}
	if err := daemon.Run(cfg); err != nil {
		slog.Error("daemon wrapper 退出", "instanceId", cfg.InstanceUUID, "error", err)
		os.Exit(1)
	}
}

func runWorker() {
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

	host := os.Getenv("JIANMANAGER_HOST") // 留空自动检测本机 IP

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

	// 初始化 JDK 管理器：托管根 <workDir>/jdks；可选追加系统 JDK 探测目录。
	var systemJDKDirs []string
	if v := os.Getenv("JIANMANAGER_JDK_SYSTEM_DIRS"); v != "" {
		for _, d := range strings.Split(v, string(os.PathListSeparator)) {
			d = strings.TrimSpace(d)
			if d != "" {
				systemJDKDirs = append(systemJDKDirs, d)
			}
		}
	}
	jdkMgr := jdks.NewManager(filepath.Join(workDir, "jdks"), systemJDKDirs)
	// 这是 ADR-003「平台重启不杀游戏服」的关键路径。
	recovered, recoverErr := manager.RecoverDaemonInstances()
	if recoverErr != nil {
		slog.Warn("恢复 daemon 实例失败", "error", recoverErr)
	}
	if recovered > 0 {
		slog.Info("已恢复 daemon 实例连接", "count", recovered)
	}

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
	workerServer := wgrpc.NewServer(manager, nodeUUID, collector, jdkMgr)
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

	// 桥接进程输出到 WebSocket 终端
	manager.SetOutputHandler(func(instanceID string, stream string, data []byte) {
		terminalServer.Broadcast(instanceID, stream, string(data))
	})

	// 桥接终端输入到进程 stdin
	terminalServer.SetStdinHandler(func(instanceID, data string) {
		if err := manager.SendCommand(instanceID, data); err != nil {
			slog.Warn("终端输入发送失败", "instanceId", instanceID, "error", err)
		}
	})

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

	// 注册到 Control Plane
	// Control Plane 未启动时 Worker 不退出，按指数退避重试直到注册成功。
	cpAddr := os.Getenv("JIANMANAGER_CONTROL_PLANE_GRPC")
	if cpAddr == "" {
		cpAddr = "localhost:9100"
	}

	regResult, err := register.RegisterWithRetry(context.Background(), register.Config{
		ControlPlaneAddr: cpAddr,
		NodeName:         nodeName,
		WsPort:           wsPort,
		GrpcPort:         grpcPort,
		Host:             host,
	}, 2*time.Second, 60*time.Second)
	if err != nil {
		slog.Error("注册到 Control Plane 失败", "error", err)
		os.Exit(1)
	}

	nodeUUID = regResult.NodeUUID
	slog.Info("已注册到 Control Plane", "nodeUUID", nodeUUID)

	// 启动心跳上报（携带注册获得的 node_secret 供 Control Plane 鉴权）
	hb := heartbeat.New(cpAddr, nodeUUID, regResult.NodeSecret, 30*time.Second, manager)
	hb.Start()
	defer hb.Stop()

	// 等待信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	slog.Info("收到退出信号，正在关闭", "signal", sig)
	grpcServer.GracefulStop()
	manager.StopAll()
	slog.Info("Worker Node 已停止")
}
