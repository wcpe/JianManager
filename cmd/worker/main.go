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

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
	workercfg "github.com/wcpe/JianManager/internal/worker"
	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/internal/worker/daemon"
	"github.com/wcpe/JianManager/internal/worker/decompiler"
	wembed "github.com/wcpe/JianManager/internal/worker/embed"
	wgrpc "github.com/wcpe/JianManager/internal/worker/grpc"
	"github.com/wcpe/JianManager/internal/worker/heartbeat"
	jdks "github.com/wcpe/JianManager/internal/worker/jdk"
	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/internal/worker/register"
	"github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
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
	// 加载配置：worker.yaml + JIANMANAGER_ 环境变量覆盖（FR-080，见 ADR-020）。
	// 配置可选参数从命令行第 1 个参数取（如 `worker /path/worker.yaml`），缺省自动查找。
	cfgPath := ""
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := workercfg.Load(cfgPath)
	if err != nil {
		slog.Error("加载 Worker 配置失败", "error", err)
		os.Exit(1)
	}

	// 出站 HTTP 客户端工厂（FR-174，见 ADR-037）：所有出站下载（自更新/JDK/CFR/服务端 jar）
	// 经此进程级代理 client。proxy.url 留空=直连（沿用环境变量代理）。非法代理 URL 启动即 fail-fast。
	outboundClient, err := httpclient.New(cfg.Proxy)
	if err != nil {
		slog.Error("初始化出站代理客户端失败", "proxy", httpclient.Sanitize(cfg.Proxy.URL), "error", err)
		os.Exit(1)
	}
	if cfg.Proxy.URL != "" {
		slog.Info("出站代理已启用", "proxy", httpclient.Sanitize(cfg.Proxy.URL), "noProxy", cfg.Proxy.NoProxy)
	}

	nodeName := cfg.Name
	grpcPort := cfg.GRPC.Port
	wsPort := cfg.WS.Port
	host := cfg.Host // 留空自动检测本机 IP
	jwtSecret := cfg.JWTSecret

	// 节点 UUID 在首次注册后由 CP 签发并落本地身份文件复用（FR-080）；启动时先占位。
	nodeUUID := "local-dev"

	// 解析并初始化项目自包含数据根（配置 data_dir，缺省 ./data，可经 JIANMANAGER_DATA_DIR 覆盖）。
	// 运行态数据全部收口到此根，整体可迁移。参见 ADR-010。
	root, err := dataroot.Init(cfg.DataDir)
	if err != nil {
		slog.Error("初始化数据根失败", "error", err)
		os.Exit(1)
	}

	// 服务器工作目录根：默认数据根下 var/servers；配置 servers_dir 显式覆盖（兼容旧部署）。
	serversDir := root.ServersDir()
	if cfg.ServersDir != "" {
		serversDir = cfg.ServersDir
	}

	slog.Info("Worker Node 启动", "name", nodeName, "grpcPort", grpcPort, "wsPort", wsPort, "dataDir", root.Base(), "serversDir", serversDir)
	// 初始化进程管理器
	manager := process.NewManager(serversDir)

	// 初始化 JDK 管理器：托管根 <dataRoot>/opt/jdks；可选追加系统 JDK 探测目录。
	// 参见 ADR-010：JDK 从旧的 <serversDir>/jdks 迁移到 opt/jdks。
	var systemJDKDirs []string
	if v := os.Getenv("JIANMANAGER_JDK_SYSTEM_DIRS"); v != "" {
		for _, d := range strings.Split(v, string(os.PathListSeparator)) {
			d = strings.TrimSpace(d)
			if d != "" {
				systemJDKDirs = append(systemJDKDirs, d)
			}
		}
	}
	var jdkMgr *jdks.Manager
	if os.Getenv("JIANMANAGER_DISABLE_JDK") != "1" {
		jdkMgr = jdks.NewManager(root.JDKsDir(), systemJDKDirs)
		jdkMgr.SetHTTPClient(outboundClient) // JDK 下载经进程级出站代理（FR-174）。
		slog.Info("JDK manager enabled", "rootDir", root.JDKsDir())
	} else {
		slog.Info("JDK manager disabled by JIANMANAGER_DISABLE_JDK=1")
	}
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
	workerServer := wgrpc.NewServer(manager, nodeUUID, collector, jdkMgr, root)
	// Worker 升级二进制下载与服务端 jar 下载经进程级出站代理（FR-174，见 ADR-037）。
	workerServer.SetHTTPClient(outboundClient)
	// 全文搜索追加忽略规则（worker.yaml search.ignore，叠加内置默认集，FR-074）。
	workerServer.SetSearchIgnore(cfg.Search.Ignore)

	// Bot 管理器：按需 spawn bot-worker(Node) 子进程，经 stdin/stdout IPC 管理 Mineflayer Bot。
	// 入口脚本默认 bot-worker/dist/index.js（相对 cwd），可经 JIANMANAGER_BOT_WORKER_PATH 覆盖。参见 ADR-006。
	botWorkerPath := os.Getenv("JIANMANAGER_BOT_WORKER_PATH")
	if botWorkerPath == "" {
		botWorkerPath = filepath.Join("bot-worker", "dist", "index.js")
	}
	botMgr := bot.NewManager(bot.ManagerConfig{BotWorkerPath: botWorkerPath})
	defer botMgr.Stop()
	workerServer.SetBotManager(botMgr)

	// 反编译器（FR-075，见 ADR-018）：解析 CFR jar（配置路径>内嵌>数据根缓存>按需下载 sha256 pin），
	// 缓存落数据根 cache/tools；反编译经实例/系统 JDK 受控调起 CFR，只读+超时+体积上限+失败降级。
	decompProvider := decompiler.NewProvider(decompiler.Config{
		ConfigPath:    cfg.Decompiler.CFRPath,
		CacheDir:      filepath.Join(root.CacheDir(), "tools"),
		Embedded:      wembed.CFRJar,
		AllowDownload: cfg.Decompiler.AllowDownload,
		HTTPClient:    outboundClient, // CFR 按需下载经进程级出站代理（FR-174）。
	})
	workerServer.SetDecompiler(decompProvider)

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

	// 桥接进程输出：一份给 WebSocket 终端（交互），一份给 StreamInstanceEvents 事件流（CP 采集落库，FR-049）。
	// 两条路径相互独立，从同一份进程输出分流，互不阻塞。
	manager.SetOutputHandler(func(instanceID string, stream string, data []byte) {
		text := string(data)
		terminalServer.Broadcast(instanceID, stream, text)
		workerServer.EmitOutput(instanceID, stream, text)
	})

	// 桥接终端输入到进程 stdin
	terminalServer.SetStdinHandler(func(instanceID, data string) {
		if err := manager.SendCommand(instanceID, data); err != nil {
			slog.Warn("终端输入发送失败", "instanceId", instanceID, "error", err)
		}
	})

	// 插件桥服务端（ServerProbe 反向 WS，FR-065，见 ADR-016）：与终端 WS 并列、同一监听端口。
	// 探针主动连入 /ws/plugin-bridge，事件经 gRPC StreamPluginEvents 冒泡到 CP；token 校验复用 JWT secret。
	pluginBridge := ws.NewPluginBridgeServer(jwtSecret)
	workerServer.SetPluginBridge(pluginBridge)

	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/ws/terminal", terminalServer.Handler())
	wsMux.HandleFunc("/ws/plugin-bridge", pluginBridge.Handler())

	wsAddr := fmt.Sprintf(":%d", wsPort)
	wsServer := &http.Server{Addr: wsAddr, Handler: wsMux}

	go func() {
		slog.Info("WebSocket 终端服务器就绪", "addr", wsAddr)
		if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("WS 服务器退出", "error", err)
		}
	}()

	// 注册到 Control Plane（FR-080，见 ADR-020）。
	// Control Plane 未启动时 Worker 不退出，按指数退避重试直到注册成功。
	cpAddr := cfg.ControlPlane
	if cpAddr == "" {
		cpAddr = "localhost:9100"
	}

	// 优先读本地身份文件复用既有 node_uuid/secret（重注册，不带 token，不重复消费一次性 token）；
	// 无身份文件则为首次安装，必须携带 enrollment token 首注册。
	etcDir := root.EtcDir()
	identity, err := register.LoadIdentity(etcDir)
	if err != nil {
		slog.Error("读取本地节点身份失败", "error", err)
		os.Exit(1)
	}

	regCfg := register.Config{
		ControlPlaneAddr: cpAddr,
		NodeName:         nodeName,
		WsPort:           wsPort,
		GrpcPort:         grpcPort,
		Host:             host,
	}
	if identity != nil {
		// 重注册：沿用既有身份的节点名，并经 metadata 出示 node_uuid + node_secret，
		// CP 据此按 UUID 匹配既有节点（而非可重复的 name），杜绝重名覆写（见 ADR-039）。
		regCfg.NodeName = identity.NodeName
		regCfg.NodeUUID = identity.NodeUUID
		regCfg.NodeSecret = identity.NodeSecret
		slog.Info("发现本地节点身份，复用既有身份重注册", "nodeUUID", identity.NodeUUID, "name", identity.NodeName)
	} else {
		// 首次注册：携带一次性 enrollment token。缺 token 直接退出（避免无效注册无限重试刷日志）。
		if cfg.EnrollToken == "" {
			slog.Error("首次注册缺少 enrollment token：请在面板「添加节点」生成一键命令，" +
				"或经 JIANMANAGER_ENROLL_TOKEN 提供。已有节点请确认本地身份文件 etc/node-identity.json 是否存在")
			os.Exit(1)
		}
		regCfg.EnrollToken = cfg.EnrollToken
	}

	regResult, err := register.RegisterWithRetry(context.Background(), regCfg, 2*time.Second, 60*time.Second)
	if err != nil {
		slog.Error("注册到 Control Plane 失败", "error", err)
		os.Exit(1)
	}

	nodeUUID = regResult.NodeUUID
	slog.Info("已注册到 Control Plane", "nodeUUID", nodeUUID)

	// 首次注册成功后持久化身份（含 node_secret，0600），重启复用、不重复消费 token（FR-080）。
	if identity == nil {
		if err := register.SaveIdentity(etcDir, &register.Identity{
			NodeUUID:   regResult.NodeUUID,
			NodeSecret: regResult.NodeSecret,
			NodeName:   regCfg.NodeName,
		}); err != nil {
			// 持久化失败不致命（本次仍在线），但重启会因无身份且 token 已失效而首注册失败，需告警。
			slog.Warn("持久化节点身份失败，重启可能需重新签发 enrollment token", "error", err)
		}
	}

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
