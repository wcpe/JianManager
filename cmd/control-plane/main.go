package main

import (
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/database"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/router"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func main() {
	cfgPath := ""
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	initLogger(cfg.Log)

	db, err := database.New(cfg.Database)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}

	if err := database.AutoMigrate(db); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}

	// 解析并初始化项目自包含数据根（CP 拥有制品库 var/artifacts）。参见 ADR-010/011。
	root, err := dataroot.Init(os.Getenv(dataroot.EnvVar))
	if err != nil {
		log.Fatalf("初始化数据根失败: %v", err)
	}
	slog.Info("数据根就绪", "dataDir", root.Base())

	authSvc := service.NewAuthService(db, cfg.JWT)
	userSvc := service.NewUserService(db)
	groupSvc := service.NewGroupService(db)
	nodeSvc := service.NewNodeService(db)
	pool := cpgrpc.NewClientPool()
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	// 优雅关闭：停止接受新的后台 Worker 委托并等待在途异步状态回写收尾，避免泄漏 goroutine。
	defer instanceSvc.Shutdown()
	instanceBatchSvc := service.NewInstanceBatchService(db, pool)
	// 回填实例服务，供节点排空（drain）复用实例停止逻辑（FR-048）。
	nodeSvc.SetInstanceService(instanceSvc)
	jdkSvc := service.NewJDKService(db, pool)
	terminalSvc := service.NewTerminalService(db, cfg.JWT.Secret, fmt.Sprintf("ws://localhost:%d", cfg.Server.Port))
	fileSvc := service.NewFileService(db, pool)
	fileVersionSvc := service.NewFileVersionService(db, pool, service.FileVersionConfig{
		MaxPerFile:   cfg.FileVersion.MaxPerFile,
		MaxSizeBytes: cfg.FileVersion.MaxSizeBytes,
	})
	configSvc := service.NewConfigService(db, pool)
	botSvc := service.NewBotService(db, pool)
	alertSvc := service.NewAlertService(db)
	scheduleSvc := service.NewScheduleService(db)
	backupSvc := service.NewBackupService(db, pool)
	// 备份远程存储后端（FR-057）：注入备份服务，凭证经 ${ENV_VAR} 解析后下发 Worker。
	backupStorageSvc := service.NewBackupStorageService(db)
	backupSvc.SetStorageService(backupStorageSvc)
	templateSvc := service.NewTemplateService(db)
	auditSvc := service.NewAuditService(db)
	authzSvc := service.NewAuthzService(db)
	eventSvc := service.NewEventService(pool)
	// 日志中心：采集实例输出与平台日志入库、归档到数据根 var/log、按策略保留（FR-049）。
	logSvc := service.NewLogService(db, root, cfg.LogStore)
	logSvc.Start()
	defer logSvc.Stop()
	// 实例 stdout/stderr 经事件流采集落库。
	eventSvc.SetLogSink(logSvc)
	// 平台结构化日志在输出 stdout 之外同时落库（持久化开关由 log_store.persist_platform 控制）。
	if persist := service.NewPersistSlogHandler(slog.Default().Handler(), logSvc); persist != slog.Default().Handler() {
		slog.SetDefault(slog.New(persist))
	}
	assetSvc := service.NewAssetService(db, root)
	// 插件服务：上传先入制品库（type=plugin 去重）再经 file gRPC 部署到实例（FR-052）。
	pluginSvc := service.NewPluginService(db, pool, assetSvc)
	coreSvc := service.NewCoreService()
	// 插件桥服务（FR-065，见 ADR-016）：建服时为实例签发插件桥 token 并写入探针 config 的 bridge 段。
	pluginBridgeSvc := service.NewPluginBridgeService(cfg.JWT.Secret)
	provisionSvc := service.NewProvisionService(db, pool, instanceSvc, coreSvc, pluginBridgeSvc)
	registrationSvc := service.NewRegistrationService(db)
	networkSvc := service.NewNetworkService(db, instanceSvc)
	// 代理服务实现 RegistrationSyncer：注册变更后写代理配置 + 下发 Velocity secret（FR-035）。
	proxySvc := service.NewProxyService(db, pool, instanceSvc, coreSvc, registrationSvc)
	registrationSvc.SetSyncer(proxySvc)
	cloneSvc := service.NewCloneService(db, pool, instanceSvc, registrationSvc)
	playerSvc := service.NewPlayerService(db, pool)

	// 告警评估器：每 60s 检测节点指标，触发 Webhook 通知
	alertEvaluator := service.NewAlertEvaluator(db)
	alertEvaluator.Start()
	defer alertEvaluator.Stop()

	// 实例事件服务：订阅 Worker 状态变更流并推送给前端 SSE
	defer eventSvc.Stop()

	// 定时任务调度器：每分钟检查到期任务并执行
	scheduleExecutor := service.NewScheduleExecutorImpl(db, instanceSvc, backupSvc, pool)
	scheduler := service.NewScheduler(db, scheduleExecutor)
	scheduler.Start()
	defer scheduler.Stop()

	// 时序指标卷积器：周期卷积 raw→5m→1h 并按 TTL 清理（FR-060/ADR-013）。
	metricSvc := service.NewMetricService(db)
	metricSvc.Start()
	defer metricSvc.Stop()

	// 平台配置：在 YAML+env 基线上叠加 DB 覆盖层，白名单项可运行时调整（FR-063/ADR-015）。
	// 构造时重放已落库的可即时生效覆盖（如日志级别），保证重启后覆盖仍生效。
	settingsSvc := service.NewSettingsService(db, cfg)
	// 把设置读取器注入消费方，使覆盖项真生效（FR-063）：
	//   JDK 安装读 jdk.mirror.<vendor>；实例启动读 graceful_stop.timeout 随启动下发；
	//   备份裁剪读 backup.retention_days 定期回收旧备份。
	jdkSvc.SetSettingsReader(settingsSvc)
	instanceSvc.SetSettingsReader(settingsSvc)
	backupSvc.SetSettingsReader(settingsSvc)
	backupSvc.Start()
	defer backupSvc.Stop()

	r := router.Setup(&router.Services{
		Auth:          authSvc,
		User:          userSvc,
		Group:         groupSvc,
		Node:          nodeSvc,
		Instance:      instanceSvc,
		InstanceBatch: instanceBatchSvc,
		JDK:           jdkSvc,
		Terminal:      terminalSvc,
		File:          fileSvc,
		FileVersion:   fileVersionSvc,
		Plugin:        pluginSvc,
		Player:        playerSvc,
		Config:        configSvc,
		Bot:           botSvc,
		Alert:         alertSvc,
		Schedule:      scheduleSvc,
		Backup:        backupSvc,
		BackupStorage: backupStorageSvc,
		Template:      templateSvc,
		Audit:         auditSvc,
		Authz:         authzSvc,
		Event:         eventSvc,
		Asset:         assetSvc,
		Core:          coreSvc,
		Provision:     provisionSvc,
		Proxy:         proxySvc,
		Clone:         cloneSvc,
		Registration:  registrationSvc,
		Network:       networkSvc,
		Log:           logSvc,
		Metric:        metricSvc,
		Settings:      settingsSvc,
	}, cfg.JWT.Secret)

	// 注册 WebSocket 终端代理（浏览器 → CP → Worker）
	terminalProxy := service.NewTerminalProxy(cfg.JWT.Secret, terminalSvc)
	r.GET("/ws/terminal", gin.WrapF(terminalProxy.Handler()))

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	slog.Info("Control Plane 启动", "addr", addr)

	// 启动 gRPC 服务器（用于 Worker Node 注册和心跳）
	grpcHandler := cpgrpc.NewControlPlaneHandler(db, pool)
	grpcHandler.SetOnWorkerConnect(func(nodeUUID string) {
		eventSvc.StartWorkerStream(nodeUUID)
	})
	// 心跳负载落库为时序样本（节点指标 + 每实例 ServerProbe 快照，FR-060）。
	grpcHandler.SetMetricIngester(metricSvc)
	grpcServer := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(grpcServer, grpcHandler)

	grpcAddr := fmt.Sprintf(":%d", cfg.GRPC.Port)
	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("监听 gRPC 端口失败: %v", err)
	}

	go func() {
		slog.Info("gRPC 服务器就绪", "addr", grpcAddr)
		if err := grpcServer.Serve(grpcListener); err != nil {
			slog.Error("gRPC 服务器退出", "error", err)
		}
	}()

	// 启动离线检测器
	cpgrpc.StartOfflineDetector(db)

	if err := r.Run(addr); err != nil {
		log.Fatalf("启动服务器失败: %v", err)
	}
}

func initLogger(cfg config.LogConfig) {
	// 用动态 LevelVar 而非静态 Level，使日志级别可经平台设置运行时切换（FR-063 / ADR-015）。
	config.LogLevelVar.Set(config.ParseLogLevel(cfg.Level))

	opts := &slog.HandlerOptions{Level: config.LogLevelVar}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}
