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
	// 实例组织分组树（FR-165，见 ADR-XXXX）：文件夹式多级嵌套归类 + 实例 M:N，仅 CP 读写。
	instanceGroupSvc := service.NewInstanceGroupService(db)
	// 回填实例服务，供节点排空（drain）复用实例停止逻辑（FR-048）。
	nodeSvc.SetInstanceService(instanceSvc)
	jdkSvc := service.NewJDKService(db, pool)
	dockerImageSvc := service.NewDockerImageService(db, pool)
	terminalSvc := service.NewTerminalService(db, cfg.JWT.Secret, fmt.Sprintf("ws://localhost:%d", cfg.Server.Port))
	fileSvc := service.NewFileService(db, pool)
	fileVersionSvc := service.NewFileVersionService(db, pool, service.FileVersionConfig{
		MaxPerFile:   cfg.FileVersion.MaxPerFile,
		MaxSizeBytes: cfg.FileVersion.MaxSizeBytes,
	})
	configSvc := service.NewConfigService(db, pool)
	botSvc := service.NewBotService(db, pool)
	alertSvc := service.NewAlertService(db)
	alertChannelSvc := service.NewAlertChannelService(db)
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
	// 运行时与制品全局页只读聚合（FR-082）：跨节点 JDK 矩阵 + 引用实例 + 制品占用/去重/冷热。
	runtimeAssetsSvc := service.NewRuntimeAssetsService(db)
	// 节点 enrollment token（一键安装 / 傻瓜部署，FR-080，见 ADR-020）：
	// 一次性、限时的新节点准入凭据，落库只存哈希、明文签发时一次性返回。
	enrollTokenSvc := service.NewEnrollTokenService(db)
	// 平台存储资源管理器（FR-083）：CP 侧数据根 FHS 只读浏览 + 占用统计 + cache 受控清理。
	storageSvc := service.NewStorageService(db, root)
	// 数据库资源管理器只读浏览（FR-084）：CP 独有数据源，仅平台管理员；只读 + 敏感列脱敏。
	dbBrowseSvc := service.NewDBBrowseService(db)
	// 面板自更新（FR-081，见 ADR-020 §4）：可配更新源 + sha256 校验，CP 统一编排
	// CP 自升级与经 gRPC 全网 Worker 升级；CP 自身下载落数据根 cache/。
	selfUpdateSvc := service.NewSelfUpdateService(db, pool, service.SelfUpdateConfig{
		FeedURL:       cfg.Update.FeedURL,
		BinaryBaseURL: cfg.Update.BinaryBaseURL,
		AllowInsecure: cfg.Update.AllowInsecure,
	}, root)
	// 客户端分发频道与拉取密钥（FR-086，见 ADR-022）：密钥落库只存哈希、明文一次性返回。
	clientChannelSvc := service.NewClientChannelService(db)
	// 客户端分发版本与签名 manifest（FR-087，见 ADR-022、contract §2/§3）。
	// 签名私钥：优先 env 注入的生产私钥（config.client_dist.sign_priv_key ← JIANMANAGER_CLIENT_SIGN_PRIVKEY）；
	// 未配置则回退内置开发密钥（仅零配置开发，公钥已回填客户端 updater-core）。
	clientSignPriv := cfg.ClientDist.SignPrivKey
	clientSignKeyID := cfg.ClientDist.SignKeyID
	if clientSignPriv == "" {
		clientSignPriv = service.DevSignPrivateKeyPKCS8Base64
		clientSignKeyID = service.DefaultSignKeyID
		slog.Warn("客户端分发签名使用内置开发密钥，生产务必经 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入独立私钥")
	}
	clientSigner, err := service.NewManifestSigner(clientSignPriv, clientSignKeyID)
	if err != nil {
		log.Fatalf("初始化客户端分发签名器失败: %v", err)
	}
	clientVersionSvc := service.NewClientVersionService(db, assetSvc, clientChannelSvc, clientSigner)
	// 客户端机器码登记（FR-092）：manifest 拉取时 best-effort upsert，弱一致、不阻断。
	clientMachineSvc := service.NewClientMachineService(db)
	// 客户端分发拉取/下载追踪（FR-093）：明细短保留 + 写时增量聚合 + 后台滚动清理。
	clientDistTrackingSvc := service.NewClientDistTrackingService(db)
	clientDistTrackingSvc.Start()
	defer clientDistTrackingSvc.Stop()
	// 客户端分发端点 L7 防护（FR-096，见 ADR-023）：IP 黑白名单 + per-IP 限流 + 并发限制，规则运行时可改入审计。
	clientIPGuardSvc := service.NewClientIPGuardService(db)
	// 客户端分发 .jmpack 打包（FR-097，见 ADR-021/022）：复用已存制品 + Ed25519 签名，入库 type=client-pack。
	jmPackSvc := service.NewJmPackService(assetSvc, clientVersionSvc, clientSigner)
	// 客户端遥测（FR-094）：明细短保留 + 按 result 日聚合 + 后台滚动清理；端点 best-effort 202。
	clientTelemetrySvc := service.NewClientTelemetryService(db)
	clientTelemetrySvc.Start()
	defer clientTelemetrySvc.Stop()
	// 分发统计后台（FR-095）：只读聚合 FR-093/094/092 数据，供管理台看板。
	clientDistStatsSvc := service.NewClientDistStatsService(db)
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
	// 服务器状态查询服务（FR-076，见 ADR-016）：按需经探针反向 WS 桥的 QueryServerState
	// 取回某实例全量 Bukkit 内部状态（前端「服务器状态」tab 开/刷新才查，FR-077）。
	serverStateSvc := service.NewServerStateService(db, pool)
	// 业务对接编排服务（FR-116，见 ADR-026/027）：经探针桥下发业务命令（domain.action+payload）
	// 并透传结果，CP 插件无关、降级即默认。JBIS 业务对接平台 M1 脊柱。
	businessSvc := service.NewBusinessService(db, pool)
	// 业务事件汇聚服务（FR-116 底座 / FR-122 经济，见 ADR-027/028）：消费同一条插件事件流中
	// domain 非空的 JBIS 业务事件，按 (domain,dedupKey) 去重落通用 envelope，经济域再维护
	// node→zone 结构化镜像 + 变更审计（跨区同名玩家不串味/不重复计数）。CP 插件无关。
	businessEventSvc := service.NewBusinessEventService(db)
	// 玩家事件服务（FR-066，见 ADR-016）：订阅各 Worker 的插件事件流（StreamPluginEvents），
	// 维护实时在线名册并经 SSE 推送给前端（join/quit/chat/cross_server）。
	playerEventSvc := service.NewPlayerEventService(pool, db)
	defer playerEventSvc.Stop()
	// 业务事件分流：同一上行流中 domain 非空的事件交业务汇聚（FR-122），玩家事件不受影响。
	playerEventSvc.SetBusinessSink(businessEventSvc.Ingest)

	// 探针在线更新服务（FR-068，见 ADR-016）：复用 gRPC DeployServerProbe 把内嵌探针 jar
	// 推到实例（下次重启生效）。复用 pluginBridgeSvc 重新生成探针 config 的 bridge 段（实例级 token）；
	// 探针连接状态取 FR-066 在线名册（IsProbeConnected）。
	probeUpdateSvc := service.NewProbeUpdateService(db, pool, pluginBridgeSvc)
	probeUpdateSvc.SetConnChecker(playerEventSvc.IsProbeConnected)

	// 告警分发器（FR-085）：所有触发源经此统一去抖聚合 / 静默 / 分级路由 / 落库 / 通知。
	alertDispatcher := service.NewAlertDispatcher(db)
	// 轮询型告警评估器：每 60s 评估指标阈值（FR-011）与节点离线（FR-085）。
	alertEvaluator := service.NewAlertEvaluator(db, alertDispatcher)
	alertEvaluator.Start()
	defer alertEvaluator.Stop()
	// 事件驱动告警触发器（FR-085）：实例崩溃 / 日志关键字 / 玩家事件 / 备份失败。
	alertTriggers := service.NewAlertEventTriggers(db, alertDispatcher, eventSvc, playerEventSvc)
	alertTriggers.Start()
	defer alertTriggers.Stop()
	// 备份失败转入告警体系（FR-085）。
	backupSvc.SetBackupFailedHook(alertTriggers.OnBackupFailed)

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
		Auth:               authSvc,
		User:               userSvc,
		Group:              groupSvc,
		Node:               nodeSvc,
		Instance:           instanceSvc,
		InstanceBatch:      instanceBatchSvc,
		InstanceGroup:      instanceGroupSvc,
		JDK:                jdkSvc,
		DockerImage:        dockerImageSvc,
		Terminal:           terminalSvc,
		File:               fileSvc,
		FileVersion:        fileVersionSvc,
		Plugin:             pluginSvc,
		Player:             playerSvc,
		PlayerEvent:        playerEventSvc,
		ServerState:        serverStateSvc,
		Business:           businessSvc,
		BusinessEvent:      businessEventSvc,
		Config:             configSvc,
		Bot:                botSvc,
		Alert:              alertSvc,
		AlertChannel:       alertChannelSvc,
		Schedule:           scheduleSvc,
		Backup:             backupSvc,
		BackupStorage:      backupStorageSvc,
		Template:           templateSvc,
		Audit:              auditSvc,
		Authz:              authzSvc,
		Event:              eventSvc,
		Asset:              assetSvc,
		Core:               coreSvc,
		Provision:          provisionSvc,
		Proxy:              proxySvc,
		Clone:              cloneSvc,
		Registration:       registrationSvc,
		Network:            networkSvc,
		Log:                logSvc,
		Metric:             metricSvc,
		Settings:           settingsSvc,
		ProbeUpdate:        probeUpdateSvc,
		ClientChannel:      clientChannelSvc,
		ClientVersion:      clientVersionSvc,
		ClientMachine:      clientMachineSvc,
		ClientDistTracking: clientDistTrackingSvc,
		ClientIPGuard:      clientIPGuardSvc,
		ClientTelemetry:    clientTelemetrySvc,
		ClientDistStats:    clientDistStatsSvc,
		JmPack:             jmPackSvc,
		RuntimeAssets:      runtimeAssetsSvc,
		EnrollToken:        enrollTokenSvc,
		EnrollInstall: router.EnrollInstallConfig{
			AdvertiseGRPC: cfg.Enroll.AdvertiseGRPC,
			GRPCPort:      cfg.GRPC.Port,
			ScriptBaseURL: cfg.Enroll.ScriptBaseURL,
			BinaryURL:     cfg.Enroll.BinaryURL,
		},
		Storage:    storageSvc,
		DBBrowse:   dbBrowseSvc,
		SelfUpdate: selfUpdateSvc,
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
		// 玩家事件流（探针经反向 WS 上报）同步订阅（FR-066）。
		playerEventSvc.StartWorkerStream(nodeUUID)
	})
	// 心跳负载落库为时序样本（节点指标 + 每实例 ServerProbe 快照，FR-060）。
	grpcHandler.SetMetricIngester(metricSvc)
	// 注入 enrollment token 校验器（FR-080，见 ADR-020）：新节点首次注册必须凭有效一次性 token，
	// 老节点（name 命中）重注册不强制 token，避免在网节点重启掉线。
	grpcHandler.SetEnrollmentValidator(enrollTokenSvc)
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
