package main

import (
	"bytes"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/database"
	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/router"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
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

	authSvc := service.NewAuthService(db, cfg.JWT)
	userSvc := service.NewUserService(db)
	groupSvc := service.NewGroupService(db)
	nodeSvc := service.NewNodeService(db)
	// 坏节点检测/修复（见 ADR-039 §2）：检测重名/被串改节点、重新 enroll、清理孤立 JDK/实例。
	nodeRepairSvc := service.NewNodeRepairService(db)
	pool := cpgrpc.NewClientPool()
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	// 优雅关闭：停止接受新的后台 Worker 委托并等待在途异步状态回写收尾，避免泄漏 goroutine。
	defer instanceSvc.Shutdown()
	instanceBatchSvc := service.NewInstanceBatchService(db, pool)
	// 实例组织分组树（FR-165，见 ADR-033）：文件夹式多级嵌套归类 + 实例 M:N，仅 CP 读写。
	instanceGroupSvc := service.NewInstanceGroupService(db)
	// 回填实例服务，供节点排空（drain）复用实例停止逻辑（FR-048）。
	nodeSvc.SetInstanceService(instanceSvc)
	jdkSvc := service.NewJDKService(db, pool)
	// 节点运行时管理（FR-178）：制品缓存 + JDK 版本目录（foojay）+ 目录浏览，经 gRPC 委托 Worker。
	nodeRuntimeSvc := service.NewNodeRuntimeService(db, pool)
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
	// 制品入库下载（如服务端核心 IngestFromURL）经进程级出站代理（FR-174，见 ADR-037）。
	// 用持有者注入，使全局代理改动运行时即时生效（FR-185，见 ADR-043）。
	assetSvc.SetHTTPClientProvider(outboundProvider.Client)
	// 运行时与制品全局页只读聚合（FR-082）：跨节点 JDK 矩阵 + 引用实例 + 制品占用/去重/冷热。
	runtimeAssetsSvc := service.NewRuntimeAssetsService(db)
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
	// 客户端分发频道与拉取密钥（FR-086，见 ADR-022）：鉴权只用哈希比对。
	clientChannelSvc := service.NewClientChannelService(db)
	// 拉取密钥可逆加密 + 管理员可查看（FR-192，见 ADR-044）：另存 AES-256-GCM 加密副本供查看明文。
	// 密钥经 env JIANMANAGER_CLIENT_KEY_ENC_SECRET 注入；未配优雅降级——dev 回退内置密钥，
	// 生产未配则不写 KeyEnc、密钥不可查看（不阻断建密钥，与 ADR-038 降级哲学一致）。
	keyEncryptor, usedDevKeyEnc, err := service.ResolveKeyEncryptor(cfg.ClientDist.KeyEncSecret, cfg.Server.DevMode)
	if err != nil {
		// 仅「注入了非法密钥」会到此（配错快失败，让运维即时修正）；未配置走降级返 nil 不报错。
		log.Fatalf("初始化拉取密钥加密器失败: %v", err)
	}
	if keyEncryptor == nil {
		slog.Warn("未配置拉取密钥加密密钥，新建/轮换的拉取密钥将不可查看明文；如需可查看请经 JIANMANAGER_CLIENT_KEY_ENC_SECRET 注入 32 字节 base64 密钥（FR-192，见 ADR-044）")
	}
	if usedDevKeyEnc {
		slog.Warn("拉取密钥加密使用内置开发密钥（仅 dev_mode 生效），生产务必经 JIANMANAGER_CLIENT_KEY_ENC_SECRET 注入独立密钥")
	}
	clientChannelSvc.SetKeyEncryptor(keyEncryptor)
	// 客户端分发版本与签名 manifest（FR-087，见 ADR-022、contract §2/§3）。
	// 签名私钥经 env 注入的生产私钥（config.client_dist.sign_priv_key ← JIANMANAGER_CLIENT_SIGN_PRIVKEY）。
	// fail-closed：绝不回退源码公开的内置开发密钥对外签 OTA manifest（否则攻击者可用人人可得的开发私钥
	// 伪造玩家客户端信任的 OTA 包，供应链/RCE）；仅 dev_mode=true 维持零配置回退内置开发密钥（公钥已回填客户端）。
	// 启动策略：生产态「未注入私钥」=未启用 OTA → 降级（签名器不可用、CP 照常启动）；
	// 「注入了无效/开发密钥」=配错 → 快失败（见 service.StartableWithoutSigner）。
	clientSigner, usedDevSignKey, err := service.ResolveManifestSigner(cfg.ClientDist.SignPrivKey, cfg.ClientDist.SignKeyID, cfg.Server.DevMode)
	if err != nil {
		if service.StartableWithoutSigner(err) {
			// 未配置签名私钥：降级——客户端 OTA 分发/签名功能不可用（消费服务持 nil signer 返回
			// ErrSignKeyNotConfigured，绝不回退开发密钥对外签名），其余 CP 功能照常启动。
			slog.Warn("未配置客户端分发签名私钥，客户端 OTA 分发/签名功能降级不可用；如需启用请经 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入独立 Ed25519 私钥（ADR-022）", "error", err)
			clientSigner = nil
		} else {
			// 注入了无效私钥或误用源码公开的开发密钥：配置错误，fail-fast 让运维即时修正。
			log.Fatalf("初始化客户端分发签名器失败: %v", err)
		}
	}
	if usedDevSignKey {
		slog.Warn("客户端分发签名使用内置开发密钥（仅 dev_mode 生效），生产务必经 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入独立私钥")
	}
	clientVersionSvc := service.NewClientVersionService(db, assetSvc, clientChannelSvc, clientSigner)
	// updater-core 默认随 CP 内嵌、自动驱动 manifest agent.core，运营不管理（FR-193，见 ADR-045 改写）。
	// 从内嵌 updater-core jar 算 sha256/size + 整数版本，注入版本服务 → BuildManifest 自动产出 agent.core；
	// 并把内嵌 core 当作 client-file 制品入库（内容寻址去重），使其可经公网 client-artifacts 端点下发。
	// 无内嵌 jar（未经 make embed-client-updater）时优雅降级：省略 agent.core，不破 FR-087/088。
	if coreJar := cpembed.UpdaterCoreJar(); len(coreJar) > 0 {
		embeddedCore := service.NewEmbeddedCoreFromJar(coreJar, cpembed.ClientUpdaterEmbeddedCoreVersion)
		clientVersionSvc.SetEmbeddedCore(embeddedCore)
		// 内嵌 core 入 client-file 制品库（与 manifest files 制品同型，OpenArtifact 据此按 sha256 下发）。
		// 内容寻址去重：每次启动落同一 sha256，命中即复用，不产生重复制品。
		if _, ierr := assetSvc.Ingest(bytes.NewReader(coreJar), service.IngestParams{
			Type:     model.AssetTypeClientFile,
			Filename: "updater-core.jar",
			Metadata: `{"codec":"none","source":"embedded-updater-core"}`,
		}); ierr != nil {
			slog.Warn("内嵌 updater-core 制品入库失败，客户端可能无法下载默认 core（FR-193）", "error", ierr)
		}
	} else {
		slog.Warn("未内嵌 updater-core jar（make embed-client-updater 未注入），manifest 将省略 agent.core；客户端不自更新 core（FR-193）")
	}
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

	// 全局任务中心 + 站内信（FR-183，见 ADR-040）：长任务进度汇聚 + 终态发站内信。
	// TaskService 在心跳路径处理任务快照、终态落 NodeJDK 并经 NotificationService 发信；
	// JDKService 据此把 JDK 安装改为异步（建任务→Worker 启动即返回 taskId）。
	notificationSvc := service.NewNotificationService(db)
	taskSvc := service.NewTaskService(db)
	taskSvc.SetNotificationService(notificationSvc)
	jdkSvc.SetTaskService(taskSvc)

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
		DockerImage:             dockerImageSvc,
		Terminal:                terminalSvc,
		File:                    fileSvc,
		FileVersion:             fileVersionSvc,
		Plugin:                  pluginSvc,
		Player:                  playerSvc,
		PlayerEvent:             playerEventSvc,
		ServerState:             serverStateSvc,
		Business:                businessSvc,
		BusinessEvent:           businessEventSvc,
		Config:                  configSvc,
		Bot:                     botSvc,
		Alert:                   alertSvc,
		AlertChannel:            alertChannelSvc,
		Schedule:                scheduleSvc,
		Backup:                  backupSvc,
		BackupStorage:           backupStorageSvc,
		Template:                templateSvc,
		Audit:                   auditSvc,
		Authz:                   authzSvc,
		Event:                   eventSvc,
		Asset:                   assetSvc,
		Core:                    coreSvc,
		Provision:               provisionSvc,
		Proxy:                   proxySvc,
		Clone:                   cloneSvc,
		Registration:            registrationSvc,
		Network:                 networkSvc,
		Log:                     logSvc,
		Metric:                  metricSvc,
		Settings:                settingsSvc,
		ProbeUpdate:             probeUpdateSvc,
		ClientChannel:           clientChannelSvc,
		ClientVersion:           clientVersionSvc,
		ClientMachine:           clientMachineSvc,
		ClientDistTracking:      clientDistTrackingSvc,
		ClientIPGuard:           clientIPGuardSvc,
		ClientTelemetry:         clientTelemetrySvc,
		ClientDistStats:         clientDistStatsSvc,
		ClientDistObservability: clientDistObsSvc,
		JmPack:                  jmPackSvc,
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
