package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jhump/grpctunnel/tunnelpb"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/database"
	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/router"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type botLoadServiceBundle struct {
	capacity      *service.BotLoadCapacityDirectory
	reservations  *service.BotLoadReservationStore
	signer        *service.BotLoadPlanTokenSigner
	preflight     *service.BotLoadPreflightService
	execution     *service.BotLoadExecutionService
	actionResults *service.ActionResultService
	coordinator   *service.BotFleetRuntimeCoordinator
	subscriptions *service.BotFleetSubscriptionManager
}

// assembleBotLoadServices 创建进程级共享的容量目录、软预留、签名器与执行服务。
func assembleBotLoadServices(db *gorm.DB, pool *cpgrpc.ClientPool, stableSecret string) (*botLoadServiceBundle, error) {
	reservations := service.NewBotLoadReservationStore(nil, 0)
	capacity := service.NewGRPCBotLoadCapacityDirectory(db, pool, reservations, nil)
	planSecret := service.DeriveBotLoadPlanTokenSecret([]byte(stableSecret))
	signer, err := service.NewBotLoadPlanTokenSigner(planSecret, nil)
	if err != nil {
		return nil, err
	}
	preflight := service.NewBotLoadPreflightService(db, capacity, reservations, signer, nil)
	execution := service.NewGRPCBotLoadExecutionService(db, capacity, reservations, signer, pool, nil, nil)
	actionResults := service.NewActionResultService(db, nil)
	coordinator := service.NewGRPCBotFleetRuntimeCoordinator(db, pool, nil, nil)
	coordinator.SetActionEventHandler(actionResults)
	coordinator.SetSnapshotReconciler(execution)
	coordinator.SetRuntimeObserver(execution)
	subscriptions := service.NewBotFleetSubscriptionManager(coordinator)
	execution.SetFleetSubscriptionManager(subscriptions)
	return &botLoadServiceBundle{
		capacity: capacity, reservations: reservations, signer: signer,
		preflight: preflight, execution: execution, actionResults: actionResults,
		coordinator: coordinator, subscriptions: subscriptions,
	}, nil
}

func recoverConnectedBotFleetSubscriptions(ctx context.Context, execution *service.BotLoadExecutionService, pool *cpgrpc.ClientPool, nodeUUIDs ...string) error {
	connected := pool.ConnectedNodes()
	if len(nodeUUIDs) > 0 {
		connected = connected[:0]
		for _, nodeUUID := range nodeUUIDs {
			if _, ok := pool.Get(nodeUUID); ok {
				connected = append(connected, nodeUUID)
			}
		}
	}
	return execution.RecoverFleetSubscriptions(ctx, connected)
}

func runControlPlaneServer(run func() error, closeSubscriptions func()) error {
	err := run()
	if err != nil && closeSubscriptions != nil {
		closeSubscriptions()
	}
	return err
}

func main() {
	if version.Requested(os.Args[1:]) {
		fmt.Println(version.Version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		runResetPassword(os.Args[2:])
		return
	}
	cfgPath := ""
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	initLogger(cfg.Log)

	// 出站 HTTP 客户端持有者（FR-174/FR-185，见 ADR-037/043）：CP 所有出站下载（自更新 feed/二进制、
	// 服务端 jar、客户端制品入库等）经此进程级代理 client。proxy.url 留空=直连（沿用环境变量代理）。
	// 非法代理 URL 启动即 fail-fast。持有者可运行时重建：设置面板改全局代理后即时生效（FR-185）。
	// 启动时按 yaml/env 基线构造；settings 服务就绪后再据 DB 覆盖重建（优先级 DB > yaml > env）。
	outboundProvider, err := httpclient.NewProvider(cfg.Proxy)
	if err != nil {
		log.Fatalf("初始化出站代理客户端失败 (proxy=%s): %v", httpclient.Sanitize(cfg.Proxy.URL), err)
	}
	if cfg.Proxy.URL != "" {
		slog.Info("出站代理已启用", "proxy", httpclient.Sanitize(cfg.Proxy.URL), "noProxy", cfg.Proxy.NoProxy)
	}

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

	// CP↔Worker 专用 WS 令牌密钥（FR-275，见 ADR-061）：只签终端/插件桥令牌，与签用户会话的
	// jwt.secret 隔离（Worker 永不持有 jwt.secret）。三轨：显式 jwt.ws_secret > 生产 autogen
	// 持久化 <dataRoot>/etc/ws-token-secret.key > dev 回退。经注册/心跳自动下发 Worker。
	// 解析失败 fail-fast：WS 密钥不可用则终端/监控必坏，且绝不回退 jwt.secret 掩盖问题。
	wsTokenSecret, wsSecretSource, err := service.ResolveWSTokenSecret(cfg.JWT.WSSecret, cfg.Server.DevMode, root.Abs("etc/ws-token-secret.key"))
	if err != nil {
		log.Fatalf("初始化 WS 令牌密钥失败: %v", err)
	}
	switch wsSecretSource {
	case service.WSTokenSecretSourceGenerated:
		slog.Info("WS 令牌密钥已就绪（生产自动生成并持久化，经注册自动下发 Worker，勿删密钥文件）", "file", "etc/ws-token-secret.key")
	case service.WSTokenSecretSourceDev:
		slog.Info("WS 令牌密钥使用 dev 回退值（dev_mode）")
	default:
		slog.Info("WS 令牌密钥使用显式配置（jwt.ws_secret）")
	}

	authSvc := service.NewAuthService(db, cfg.JWT)
	userSvc := service.NewUserService(db)
	groupSvc := service.NewGroupService(db)
	nodeSvc := service.NewNodeService(db)
	// 坏节点检测/修复（见 ADR-039 §2）：检测重名/被串改节点、重新 enroll、清理孤立 JDK/实例。
	nodeRepairSvc := service.NewNodeRepairService(db)
	pool := cpgrpc.NewClientPool()
	botLoadSvcs, err := assembleBotLoadServices(db, pool, cfg.JWT.Secret)
	if err != nil {
		log.Fatalf("初始化 Bot 分布式负载服务失败: %v", err)
	}
	defer botLoadSvcs.subscriptions.Close()
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	// 优雅关闭：停止接受新的后台 Worker 委托并等待在途异步状态回写收尾，避免泄漏 goroutine。
	defer instanceSvc.Shutdown()
	instanceBatchSvc := service.NewInstanceBatchService(db, pool)
	instanceBatchSvc.SetInstanceService(instanceSvc)
	// 实例组织分组树（FR-165，见 ADR-033）：文件夹式多级嵌套归类 + 实例 M:N，仅 CP 读写。
	instanceGroupSvc := service.NewInstanceGroupService(db)
	// 回填实例服务与 Worker 连接池，供节点排空（drain）和实时指标拉取复用。
	nodeSvc.SetInstanceService(instanceSvc)
	nodeSvc.SetClientPool(pool)
	jdkSvc := service.NewJDKService(db, pool)
	// 节点运行时管理（FR-178）：制品缓存 + JDK 版本目录（foojay）+ 目录浏览，经 gRPC 委托 Worker。
	nodeRuntimeSvc := service.NewNodeRuntimeService(db, pool)
	// 节点运行时库（FR-298）：统一 Runtime 视图 + 扫描发现 + 泛化登记（JDK 写路径委托 jdkSvc）。
	runtimeLibrarySvc := service.NewRuntimeLibraryService(db, pool, jdkSvc)
	pmConfigSvc := service.NewPMConfigService(db, pool)
	// 连通性测试（FR-229）：出站 HTTP 经当前出站代理客户端、节点存活经 gRPC GetVersion。
	diagnosticsSvc := service.NewDiagnosticsService(db, pool)
	diagnosticsSvc.SetHTTPClientProvider(outboundProvider.Client)
	dockerImageSvc := service.NewDockerImageService(db, pool)
	terminalSvc := service.NewTerminalService(db, wsTokenSecret, fmt.Sprintf("ws://localhost:%d", cfg.Server.Port))
	fileSvc := service.NewFileService(db, pool)
	fileVersionSvc := service.NewFileVersionService(db, pool, service.FileVersionConfig{
		MaxPerFile:   cfg.FileVersion.MaxPerFile,
		MaxSizeBytes: cfg.FileVersion.MaxSizeBytes,
	})
	configSvc := service.NewConfigService(db, pool)
	botSvc := service.NewBotService(db, pool)
	botStressSessionSvc := service.NewBotStressSessionService(db, botSvc)
	alertSvc := service.NewAlertService(db)
	alertChannelSvc := service.NewAlertChannelService(db)
	scheduleSvc := service.NewScheduleService(db)
	backupSvc := service.NewBackupService(db, pool)
	// 备份远程存储后端（FR-057/FR-152）：注入备份服务与 Worker 池，凭证经 ${ENV_VAR} 解析后下发 Worker。
	backupStorageSvc := service.NewBackupStorageService(db, pool)
	backupStorageSvc.SetDataRoot(root)
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
	// 制品入库下载（如服务端核心 IngestFromURL）经进程级出站代理（FR-174，见 ADR-037）。
	// 用持有者注入，使全局代理改动运行时即时生效（FR-185，见 ADR-043）。
	assetSvc.SetHTTPClientProvider(outboundProvider.Client)
	// 运行时与制品全局页聚合（FR-082）：跨节点 JDK 矩阵 + 引用实例 + 制品占用/去重/冷热。
	runtimeAssetsSvc := service.NewRuntimeAssetsService(db)
	// FR-301 手动刷新：强制全节点库存 syncFromWorker（失败容忍显旧数据）。
	runtimeAssetsSvc.SetJDKSync(jdkSvc)
	// 节点 enrollment token（一键安装 / 傻瓜部署，FR-080，见 ADR-020）：
	// 一次性、限时的新节点准入凭据，落库只存哈希、明文签发时一次性返回。
	enrollTokenSvc := service.NewEnrollTokenService(db)
	// 平台存储资源管理器（FR-083）：CP 侧数据根 FHS 只读浏览 + 占用统计 + cache 受控清理。
	storageSvc := service.NewStorageService(db, root)
	// 数据库资源管理器只读浏览（FR-084）：CP 独有数据源，仅平台管理员；只读 + 敏感列脱敏。
	dbBrowseSvc := service.NewDBBrowseService(db)
	// 面板自更新（FR-081 / FR-175，见 ADR-036 §7）：默认读 GitHub Releases 源（feed 为可选回退）
	// + sha256 校验，CP 统一编排 CP 自升级与经 gRPC 全网 Worker 升级；CP 自身下载落数据根 cache/。
	selfUpdateSvc := service.NewSelfUpdateService(db, pool, service.SelfUpdateConfig{
		GitHubRepo:    cfg.Update.GitHubRepo,
		Channel:       cfg.Update.Channel,
		GitHubToken:   cfg.Update.GitHubToken,
		FeedURL:       cfg.Update.FeedURL,
		BinaryBaseURL: cfg.Update.BinaryBaseURL,
		AllowInsecure: cfg.Update.AllowInsecure,
	}, root)
	// 拉取 feed 与 CP 自身二进制下载经进程级出站代理（FR-174，见 ADR-037）。
	// 用持有者注入，使全局代理改动运行时即时生效（CP「检查更新」立即走新代理，FR-185）。
	selfUpdateSvc.SetHTTPClientProvider(outboundProvider.Client)
	// 内嵌 Worker 资产装配（FR-278，见 ADR-062）：安装/升级同版本 Worker 优先取内嵌、不出网。
	// service 不隐式读 go:embed（测试与构建环境解耦），真实现由此处装配；
	// 未注入（make embed-worker 未跑）时回退 缓存/远程 链路，打点便于排障。
	selfUpdateSvc.SetEmbeddedWorkerSource(cpembed.EmbeddedWorkerManifest, cpembed.EmbeddedWorkerBinary)
	if wm := cpembed.EmbeddedWorkerManifest(); wm != nil {
		platforms := make([]string, 0, len(wm.Assets))
		for _, a := range wm.Assets {
			platforms = append(platforms, a.OS+"/"+a.Arch)
		}
		slog.Info("CP 内嵌 Worker 资产在位", "version", wm.Version, "platforms", strings.Join(platforms, ","))
	} else {
		slog.Info("未内嵌 Worker 资产（make embed-worker 未注入），worker-assets 走缓存/远程链路")
	}
	// 客户端分发频道与拉取密钥（FR-086，见 ADR-022）：鉴权只用哈希比对。
	clientChannelSvc := service.NewClientChannelService(db)
	// 拉取密钥可逆加密 + 管理员可查看（FR-192，见 ADR-044）：另存 AES-256-GCM 加密副本供查看明文。
	// 加密密钥来源三轨（FR-263，优先级 env 注入 > 生产自动生成 > dev 回退）：env 注入优先；
	// 生产未注入则自动生成并持久化到 <dataRoot>/etc/client-key-enc.key（0600，跨重启用同一密钥）；
	// dev_mode 回退内置开发密钥；自动生成失败优雅降级（密钥不可查看但不崩，与 ADR-038 降级哲学一致）。
	keyEncPath := root.Abs("etc/client-key-enc.key")
	keyEncryptor, encSource, err := service.ResolveKeyEncryptor(cfg.ClientDist.KeyEncSecret, cfg.Server.DevMode, keyEncPath)
	if err != nil {
		if strings.TrimSpace(cfg.ClientDist.KeyEncSecret) != "" {
			// 注入了非法 env 密钥：配错快失败，让运维即时修正。
			log.Fatalf("初始化拉取密钥加密器失败: %v", err)
		}
		// 自动生成/持久化失败：密钥不可查看，其余功能正常；记录真实原因便于排障。
		slog.Warn("拉取密钥加密密钥自动生成/持久化失败，降级为不可查看；可检查数据根 etc/ 目录权限或经 JIANMANAGER_CLIENT_KEY_ENC_SECRET 注入密钥", "path", keyEncPath, "error", err)
	}
	switch encSource {
	case service.KeyEncSourceGenerated:
		slog.Info("已自动生成拉取密钥加密密钥并持久化（生产未注入环境变量），密钥可查看")
	case service.KeyEncSourceDev:
		slog.Warn("拉取密钥加密使用内置开发密钥（仅 dev_mode 生效），生产务必经 JIANMANAGER_CLIENT_KEY_ENC_SECRET 注入独立密钥")
	case "":
		// 降级日志已在 ResolveKeyEncryptor 返回错误时带真实原因输出。
	}
	clientChannelSvc.SetKeyEncryptor(keyEncryptor)
	// 制品存储渠道（FR-347，见 ADR-073）：client-file 制品外置对象存储的落点路由配置。
	// 凭证复用拉取密钥加密器（同一份主密钥，运维口径一致，ADR-073 决策 4）；
	// EnsureBuiltin 幂等 seed 内置「本机存储」渠道并兜底活跃（local 恒可用）。
	artifactStorageSvc := service.NewArtifactStorageChannelService(db, root)
	artifactStorageSvc.SetKeyEncryptor(keyEncryptor)
	if err := artifactStorageSvc.EnsureBuiltin(); err != nil {
		slog.Warn("内置本机存储渠道初始化失败，制品上传按本地兜底", "error", err)
	}
	// 制品对账（FR-349）：索引 ↔ S3 对象清单一致性——手动/定期对账产差异报告，
	// 处置走显式按钮（标记失效/清理孤儿），不做全自动修复。Start 含启动清障 + 定期调度。
	artifactReconcileSvc := service.NewArtifactReconcileService(db, artifactStorageSvc)
	artifactReconcileSvc.SetAudit(auditSvc)
	artifactReconcileSvc.Start()
	defer artifactReconcileSvc.Stop()
	// 客户端分发版本与 manifest 组装（FR-087 / FR-256 简化后：不再签名 manifest，信任靠 HTTPS + 拉取密钥鉴权）。
	clientVersionSvc := service.NewClientVersionService(db, assetSvc, clientChannelSvc)
	// 制品外置存储接线（FR-347，见 ADR-073）：写路径 client-file 按活跃渠道路由落点；
	// 读路径 s3 制品经渠道 BlobStore（玩家端点 302 预签名 / 管理面代理直流 / 预览 / 补丁物化）。
	assetSvc.SetStorageChannels(artifactStorageSvc)
	clientVersionSvc.SetStorageChannels(artifactStorageSvc)
	// updater-core 版本归档（FR-259，见 updater-arch-simplification spec §D）：
	// 内嵌 core jar 入库为 client-updater-core 类型（内容寻址去重——不同版本 sha256 不同即天然归档多版本不覆盖）。
	// 频道选定版本经 coreEndpoint 端点返回给楔子（gradle-wrapper 模式，FR-258）；运营可一键切换回滚。
	// manifest agent.core 段仍由内嵌 core 自动产出（信息性保留，见 applyEmbeddedCore）。
	if coreJar := cpembed.UpdaterCoreJar(); len(coreJar) > 0 {
		embeddedCore := service.NewEmbeddedCoreFromJar(coreJar, cpembed.ClientUpdaterEmbeddedCoreVersion)
		clientVersionSvc.SetEmbeddedCore(embeddedCore)
		// 归档入库为 client-updater-core（FR-259）：Version 字段存整数版本号，供 coreEndpoint 返回。
		if _, ierr := clientVersionSvc.ArchiveCoreJar(bytes.NewReader(coreJar), cpembed.ClientUpdaterEmbeddedCoreVersion); ierr != nil {
			slog.Warn("内嵌 updater-core 归档入库失败，coreEndpoint 将无可用版本", "error", ierr)
		}
	} else {
		slog.Warn("未内嵌 updater-core jar（make embed-client-updater 未注入），manifest 将省略 agent.core、coreEndpoint 无可用版本")
	}
	// 客户端分发大文件分块上传（FR-251，增强 FR-088）：init→chunk→complete，临时分片进
	// cache/client-uploads/<id>/，complete 拼装喂 clientVersionSvc.PublishFile 落同一 CAS。
	// Start 启动 TTL 清理并清残留分片（会话内存态，CP 重启即弃单）。
	clientChunkUploadSvc := service.NewChunkedUploadService(root, clientVersionSvc)
	clientChunkUploadSvc.Start()
	defer clientChunkUploadSvc.Stop()
	// 客户端分发上传增效（FR-346，增强 FR-250/251）：秒传预查（批量 sha256 查 CAS 命中免传）
	// + 小文件聚合上传（一请求多小文件），复用 clientVersionSvc.PublishFile 落同一 CAS。
	clientUploadEffSvc := service.NewClientUploadEfficiencyService(db, clientVersionSvc)
	// 客户端机器码登记（FR-092）：manifest 拉取时 best-effort upsert，弱一致、不阻断。
	clientMachineSvc := service.NewClientMachineService(db)
	// 客户端分发拉取/下载追踪（FR-093）：明细短保留 + 写时增量聚合 + 后台滚动清理。
	clientDistTrackingSvc := service.NewClientDistTrackingService(db)
	clientDistTrackingSvc.Start()
	defer clientDistTrackingSvc.Stop()
	// 客户端分发端点 L7 防护（FR-096，见 ADR-023）：IP 黑白名单 + per-IP 限流 + 并发限制，规则运行时可改入审计。
	clientIPGuardSvc := service.NewClientIPGuardService(db)
	// 客户端遥测（FR-094）：明细短保留 + 按 result 日聚合 + 后台滚动清理；端点 best-effort 202。
	clientTelemetrySvc := service.NewClientTelemetryService(db)
	clientTelemetrySvc.Start()
	defer clientTelemetrySvc.Stop()
	// 分发统计后台（FR-095）：只读聚合 FR-093/094/092 数据，供管理台看板。
	clientDistStatsSvc := service.NewClientDistStatsService(db)
	// 客户端启动运行态（FR-265）：独立表记录心跳，不污染分发请求事件与遥测明细。
	clientRuntimeStateSvc := service.NewClientRuntimeStateService(db)
	// 客户端分发安全防护（FR-264）：画像、处置、key/channel 防护与制品授权。
	clientDistSecuritySvc := service.NewClientDistSecurityService(db, clientChannelSvc, clientVersionSvc)
	// 客户端分发观测数据底座（FR-217，见 ADR-049）：离线把 events/telemetry 卷积为按频道×小时桶的
	// 时序快照，供观测·分发监控页跨频道/平台时序。聚合落 CP、复用 scheduler 式后台 goroutine。
	clientDistObsSvc := service.NewClientDistObservabilityService(db)
	clientDistObsSvc.Start()
	defer clientDistObsSvc.Stop()
	// 插件服务：上传先入制品库（type=plugin 去重）再经 file gRPC 部署到实例（FR-052）。
	pluginSvc := service.NewPluginService(db, pool, assetSvc)
	coreSvc := service.NewCoreService()
	// 解析核心版本/构建的 PaperMC API 请求经进程级出站代理（FR-174，见 ADR-037）。
	// 用持有者注入，使全局代理改动运行时即时生效（FR-185）。
	coreSvc.SetHTTPClientProvider(outboundProvider.Client)
	// 插件桥服务（FR-065，见 ADR-016）：建服时为实例签发插件桥 token 并写入探针 config 的 bridge 段。
	pluginBridgeSvc := service.NewPluginBridgeService(wsTokenSecret)
	provisionSvc := service.NewProvisionService(db, pool, instanceSvc, coreSvc, pluginBridgeSvc)
	registrationSvc := service.NewRegistrationService(db)
	networkSvc := service.NewNetworkService(db, instanceSvc)
	// 代理服务实现 RegistrationSyncer：注册变更后写代理配置 + 下发 Velocity secret（FR-035）。
	proxySvc := service.NewProxyService(db, pool, instanceSvc, coreSvc, registrationSvc)
	registrationSvc.SetSyncer(proxySvc)
	cloneSvc := service.NewCloneService(db, pool, instanceSvc, registrationSvc)
	// 导入现有服务器（FR-302，见 ADR-069）：就地接管 / 搬迁托管区。
	importServerSvc := service.NewImportServerService(db, pool, instanceSvc)
	playerSvc := service.NewPlayerService(db, pool)
	// 服务器状态查询服务（FR-076，见 ADR-016）：按需经探针反向 WS 桥的 QueryServerState
	// 取回某实例全量 Bukkit 内部状态（前端「服务器状态」tab 开/刷新才查，FR-077）。
	serverStateSvc := service.NewServerStateService(db, pool)

	// 崩溃快照只读查询（FR-313）：写入在 gRPC 层（Worker 上报），REST 只读供实例控制台「崩溃诊断」卡。
	crashSnapshotSvc := service.NewCrashSnapshotService(db)
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

	// 全局任务中心 + 站内信（FR-183，见 ADR-040）：长任务进度汇聚 + 终态发站内信。
	// TaskService 在心跳路径处理任务快照、终态落 NodeJDK 并经 NotificationService 发信；
	// JDKService 据此把 JDK 安装改为异步（建任务→Worker 启动即返回 taskId）。
	notificationSvc := service.NewNotificationService(db)
	taskSvc := service.NewTaskService(db)
	taskSvc.SetNotificationService(notificationSvc)
	jdkSvc.SetTaskService(taskSvc)
	pmConfigSvc.SetTaskService(taskSvc) // FR-307 全局包异步安装走任务中心
	// 运行时库安装异步化（FR-299）：Node.js 安装复用 jdk_install 的任务模式（kind=runtime_install）。
	runtimeLibrarySvc.SetTaskService(taskSvc)
	// 一键搭建异步化：后端服与代理的下载/配置在后台任务推进，不再受 HTTP 请求取消影响。
	provisionSvc.SetTaskService(taskSvc)
	proxySvc.SetTaskService(taskSvc)
	// 长操作任务化（FR-323）：导入 migrate 搬迁 / 克隆拷贝 / 备份创建恢复 纳入任务中心。
	importServerSvc.SetTaskService(taskSvc)
	cloneSvc.SetTaskService(taskSvc)
	backupSvc.SetTaskService(taskSvc)
	// 制品存量迁移（FR-348）：渠道间搬运后台任务（先改记录再删源，幂等续跑）。
	// RecoverOrphans 清扫 CP 重启滞留的非终态迁移任务，保证在途判定即真相。
	artifactMigrationSvc := service.NewArtifactMigrationService(db, root, artifactStorageSvc, taskSvc)
	if err := artifactMigrationSvc.RecoverOrphans(); err != nil {
		slog.Warn("清扫制品迁移孤儿任务失败", "error", err)
	}

	// 统一通知中心（FR-216，见 ADR-048）：只读聚合站内信（定向消息）+ 告警事件（系统警报）
	// 为一条通知流，页眉单铃铛 + 通知中心页消费。不新建表，标记已读下推到各源既有服务。
	notificationFeedSvc := service.NewNotificationFeedService(db, notificationSvc, alertSvc)

	// 平台配置：在 YAML+env 基线上叠加 DB 覆盖层，白名单项可运行时调整（FR-063/ADR-015）。
	// 构造时重放已落库的可即时生效覆盖（如日志级别），保证重启后覆盖仍生效。
	settingsSvc := service.NewSettingsService(db, cfg)
	// 把设置读取器注入消费方，使覆盖项真生效（FR-063）：
	//   JDK 安装读 jdk.mirror.<vendor>；实例启动读 graceful_stop.timeout 随启动下发；
	//   备份裁剪读 backup.retention_days 定期回收旧备份。
	jdkSvc.SetSettingsReader(settingsSvc)
	// Node.js 安装读 runtime.mirror.nodejs 随安装下发（FR-299）。
	runtimeLibrarySvc.SetSettingsReader(settingsSvc)
	instanceSvc.SetSettingsReader(settingsSvc)
	backupSvc.SetSettingsReader(settingsSvc)
	backupSvc.Start()
	defer backupSvc.Stop()

	// 出站代理可视化配置（FR-185，见 ADR-043）：
	//   - settings 保存 proxy.* 后重建 CP 出站持有者（优先级 settings DB > yaml > env）；
	//   - 启动时若 DB 已有代理覆盖，按当前生效代理重建一次（保证重启后覆盖仍生效）；
	//   - 节点代理服务以 settings.EffectiveProxy 作全局默认，供心跳按节点算期望代理下发。
	settingsSvc.SetProxyRebuilder(func(c httpclient.Config) {
		if err := outboundProvider.Rebuild(c); err != nil {
			// 已在保存前校验过，理论不达；保险起见记录而不中断（保留旧 client）。
			slog.Warn("重建 CP 出站代理客户端失败", "proxy", httpclient.Sanitize(c.URL), "error", err)
			return
		}
		slog.Info("CP 出站代理已运行时更新", "proxy", httpclient.Sanitize(c.URL), "noProxy", c.NoProxy)
	})
	if eff := settingsSvc.EffectiveProxy(); eff.URL != cfg.Proxy.URL || eff.NoProxy != cfg.Proxy.NoProxy {
		if err := outboundProvider.Rebuild(eff); err != nil {
			slog.Warn("启动时按 DB 覆盖重建出站代理失败，沿用 yaml/env", "proxy", httpclient.Sanitize(eff.URL), "error", err)
		} else {
			slog.Info("启动时按 settings DB 覆盖应用出站代理", "proxy", httpclient.Sanitize(eff.URL))
		}
	}
	// 节点级出站代理（FR-185）：全局默认取自 settings（inherit 节点用之），custom 节点用自身值。
	nodeProxySvc := service.NewNodeProxyService(db, settingsSvc.EffectiveProxy)

	// 调试模式（FR-225，增强 FR-063）：默认 Gin release 静默（杀启动 [GIN-debug] 路由噪音）；
	// debug.mode 开关运行时切 Gin 模式 + 日志级别（gin 依赖留在入口层、不渗入 service）。
	// 必须在 router.Setup（注册路由触发 [GIN-debug] 输出）之前设好 Gin 模式。
	settingsSvc.SetGinModeApplier(func(debug bool) {
		if debug {
			gin.SetMode(gin.DebugMode)
		} else {
			gin.SetMode(gin.ReleaseMode)
		}
	})
	settingsSvc.ApplyDebugBaseline()

	r := router.Setup(&router.Services{
		Auth:                    authSvc,
		User:                    userSvc,
		Group:                   groupSvc,
		Node:                    nodeSvc,
		NodeRepair:              nodeRepairSvc,
		NodeProxy:               nodeProxySvc,
		Instance:                instanceSvc,
		InstanceBatch:           instanceBatchSvc,
		InstanceGroup:           instanceGroupSvc,
		JDK:                     jdkSvc,
		NodeRuntime:             nodeRuntimeSvc,
		RuntimeLibrary:          runtimeLibrarySvc,
		PMConfig:                pmConfigSvc,
		Diagnostics:             diagnosticsSvc,
		DockerImage:             dockerImageSvc,
		Terminal:                terminalSvc,
		File:                    fileSvc,
		FileVersion:             fileVersionSvc,
		Plugin:                  pluginSvc,
		Player:                  playerSvc,
		PlayerEvent:             playerEventSvc,
		ServerState:             serverStateSvc,
		CrashSnapshot:           crashSnapshotSvc,
		Business:                businessSvc,
		BusinessEvent:           businessEventSvc,
		Config:                  configSvc,
		Bot:                     botSvc,
		BotStressSession:        botStressSessionSvc,
		BotLoadCapacity:         botLoadSvcs.capacity,
		BotLoadPreflight:        botLoadSvcs.preflight,
		BotLoadExecution:        botLoadSvcs.execution,
		Alert:                   alertSvc,
		AlertChannel:            alertChannelSvc,
		Schedule:                scheduleSvc,
		Backup:                  backupSvc,
		BackupStorage:           backupStorageSvc,
		ArtifactStorage:         artifactStorageSvc,
		ArtifactMigration:       artifactMigrationSvc,
		ArtifactReconcile:       artifactReconcileSvc,
		Template:                templateSvc,
		Audit:                   auditSvc,
		Authz:                   authzSvc,
		Event:                   eventSvc,
		Asset:                   assetSvc,
		Core:                    coreSvc,
		Provision:               provisionSvc,
		Proxy:                   proxySvc,
		Clone:                   cloneSvc,
		ImportServer:            importServerSvc,
		Registration:            registrationSvc,
		Network:                 networkSvc,
		Log:                     logSvc,
		Metric:                  metricSvc,
		Settings:                settingsSvc,
		ProbeUpdate:             probeUpdateSvc,
		ClientChannel:           clientChannelSvc,
		ClientVersion:           clientVersionSvc,
		ClientChunkUpload:       clientChunkUploadSvc,
		ClientUploadEfficiency:  clientUploadEffSvc,
		ClientMachine:           clientMachineSvc,
		ClientDistTracking:      clientDistTrackingSvc,
		ClientIPGuard:           clientIPGuardSvc,
		ClientTelemetry:         clientTelemetrySvc,
		ClientDistStats:         clientDistStatsSvc,
		ClientRuntimeState:      clientRuntimeStateSvc,
		ClientDistSecurity:      clientDistSecuritySvc,
		ClientDistObservability: clientDistObsSvc,
		RuntimeAssets:           runtimeAssetsSvc,
		EnrollToken:             enrollTokenSvc,
		EnrollInstall: router.EnrollInstallConfig{
			AdvertiseGRPC: cfg.Enroll.AdvertiseGRPC,
			GRPCPort:      cfg.GRPC.Port,
			ScriptBaseURL: cfg.Enroll.ScriptBaseURL,
			BinaryURL:     cfg.Enroll.BinaryURL,
		},
		Storage:          storageSvc,
		DBBrowse:         dbBrowseSvc,
		SelfUpdate:       selfUpdateSvc,
		Task:             taskSvc,
		Notification:     notificationSvc,
		NotificationFeed: notificationFeedSvc,
	}, cfg.JWT.Secret)

	// 注册 WebSocket 终端代理（浏览器 → CP → Worker）
	terminalProxy := service.NewTerminalProxy(wsTokenSecret, terminalSvc)
	// 终端优先经 gRPC TerminalSession 桥接（FR-281 M2，见 ADR-066）：池取客户端隧道优先/
	// 直拨回退，NAT/内网节点终端可达；老 Worker（Unimplemented）回退直拨 WS。
	terminalProxy.SetWorkerClients(func(nodeUUID string) (workerpb.WorkerServiceClient, bool) {
		client, ok := pool.Get(nodeUUID)
		if !ok {
			return nil, false
		}
		return client.Worker, true
	})
	r.GET("/ws/terminal", gin.WrapF(terminalProxy.Handler()))

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	slog.Info("Control Plane 启动", "addr", addr)

	// 启动 gRPC 服务器（用于 Worker Node 注册和心跳）
	grpcHandler := cpgrpc.NewControlPlaneHandler(db, pool)
	// WS 令牌密钥经注册/心跳下发 Worker（FR-275，见 ADR-061）。
	grpcHandler.SetWSTokenSecret(wsTokenSecret)
	grpcHandler.SetOnWorkerConnect(func(nodeUUID string) {
		eventSvc.StartWorkerStream(nodeUUID)
		// 玩家事件流（探针经反向 WS 上报）同步订阅（FR-066）。
		playerEventSvc.StartWorkerStream(nodeUUID)
		if err := recoverConnectedBotFleetSubscriptions(context.Background(), botLoadSvcs.execution, pool, nodeUUID); err != nil {
			slog.Warn("恢复 Worker 的 Bot Fleet 订阅失败", "nodeUUID", nodeUUID, "error", err)
		}
		// Worker 重连/重注册后重推该节点全部实例规格，让重启后丢失的 STOPPED 实例
		// 在 Worker 侧重新可被文件/配置/归档 op 定位（修 bug #2，见 ADR-050）。
		// 异步执行：该回调可能在心跳处理路径内触发，重推不应阻塞心跳应答。
		go instanceSvc.ResyncNode(nodeUUID)
	})
	// 心跳负载落库为时序样本（节点指标 + 每实例 ServerProbe 快照，FR-060）。
	grpcHandler.SetMetricIngester(metricSvc)
	// 心跳负载里的运行中任务快照汇聚落库 + 终态副作用（落 NodeJDK / 发站内信，FR-183，见 ADR-040）。
	grpcHandler.SetTaskIngester(taskSvc)
	// 注入 enrollment token 校验器（FR-080，见 ADR-020）：新节点首次注册必须凭有效一次性 token，
	// 老节点（name 命中）重注册不强制 token，避免在网节点重启掉线。
	grpcHandler.SetEnrollmentValidator(enrollTokenSvc)
	// 注入节点期望代理解析器（FR-185，见 ADR-043）：每次心跳响应携带该节点期望出站代理
	// （custom→节点值，inherit→全局默认）+ generation，Worker 据变化运行时重建出站 client。
	grpcHandler.SetNodeProxyResolver(nodeProxySvc)
	// 反向隧道注册表（FR-281，见 ADR-066）：Worker 主动在本 gRPC 端口开常驻反向隧道，
	// CP 指令经隧道下发（NAT/内网 worker 零入站）；pool 取连接隧道优先、直拨回退。
	// 鉴权拦截器仅拦 OpenReverseTunnel，其余流式方法（心跳等）原样放行。
	tunnelReg := cpgrpc.NewTunnelRegistry(db)
	pool.SetTunnelProvider(tunnelReg)
	// 节点与 Bot 容量观测面共用实时隧道状态，不读取 gorm:- 运行态字段。
	nodeSvc.SetTunnelStatus(tunnelReg)
	botLoadSvcs.capacity.SetTunnelStatus(tunnelReg)
	grpcServer := grpc.NewServer(grpc.ChainStreamInterceptor(tunnelReg.StreamAuthInterceptor()))
	workerpb.RegisterWorkerServiceServer(grpcServer, grpcHandler)
	tunnelpb.RegisterTunnelServiceServer(grpcServer, tunnelReg.Service())

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
	if err := recoverConnectedBotFleetSubscriptions(context.Background(), botLoadSvcs.execution, pool); err != nil {
		slog.Warn("恢复已连接 Worker 的 Bot Fleet 订阅失败", "error", err)
	}

	// 启动离线检测器
	cpgrpc.StartOfflineDetector(db)

	if err := runControlPlaneServer(func() error { return r.Run(addr) }, botLoadSvcs.subscriptions.Close); err != nil {
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

// runResetPassword 本机应急重置用户密码子命令（FR-333）。
// 用法: jianmanager-cp reset-password -u <用户名> [-c <config>] [-p <新密码>] [-list]
// 不给 -p 时生成随机密码并打印。密码重置同时解锁账号（Status 置回 Active）。
// 命令落在 CP 二进制自身而非 jmctl：数据库仅 Control Plane 可读写（架构不变量），
// jmctl 按 ADR-041 不得直连 DB。CP 服务运行中亦可执行（SQLite 短写由驱动 busy 兜底）。
func runResetPassword(args []string) {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	cfgPath := fs.String("c", "", "配置文件路径（默认与主程序相同解析规则）")
	username := fs.String("u", "", "要重置的用户名")
	password := fs.String("p", "", "新密码（留空自动生成 16 位随机密码）")
	list := fs.Bool("list", false, "仅列出全部用户名后退出")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	db, err := database.New(cfg.Database)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}

	if *list {
		var names []string
		if err := db.Model(&model.User{}).Order("id").Pluck("username", &names).Error; err != nil {
			log.Fatalf("查询用户失败: %v", err)
		}
		fmt.Printf("共 %d 个用户: %s\n", len(names), strings.Join(names, ", "))
		return
	}

	if *username == "" {
		log.Fatal("缺少 -u <用户名>（可先用 -list 查看现有用户）")
	}
	plain := *password
	if plain == "" {
		if plain, err = service.GenerateResetPassword(); err != nil {
			log.Fatalf("%v", err)
		}
	}
	user, err := service.ResetUserPassword(db, *username, plain)
	if err != nil {
		log.Fatalf("重置失败: %v", err)
	}
	fmt.Printf("密码已重置并解锁账号\n用户: %s\n新密码: %s\n", user.Username, plain)
}
