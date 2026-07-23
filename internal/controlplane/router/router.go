package router

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/embed"
	"github.com/wcpe/JianManager/internal/controlplane/mcp"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Services 聚合所有服务依赖。
type Services struct {
	Auth       *service.AuthService
	User       *service.UserService
	Group      *service.GroupService
	Node       *service.NodeService
	NodeRepair *service.NodeRepairService
	// NodeProxy 节点级出站代理管控（FR-185，见 ADR-043）；nil 时节点代理端点关闭。
	NodeProxy     *service.NodeProxyService
	Instance      *service.InstanceService
	InstanceBatch *service.InstanceBatchService
	InstanceGroup *service.InstanceGroupService
	JDK           *service.JDKService
	NodeRuntime   *service.NodeRuntimeService
	// RuntimeLibrary 节点运行时库（FR-298）：统一 Runtime 视图 + 扫描发现 + 泛化登记；
	// nil 时 /nodes/:id/runtimes 端点关闭。
	RuntimeLibrary *service.RuntimeLibraryService
	// PMConfig 节点包管理器与 registry 配置（FR-306）；nil 时 /nodes/:id/pm-config 端点关闭。
	PMConfig    *service.PMConfigService
	Diagnostics *service.DiagnosticsService
	DockerImage *service.DockerImageService
	Terminal    *service.TerminalService
	File        *service.FileService
	FileVersion *service.FileVersionService
	Plugin      *service.PluginService
	Player      *service.PlayerService
	PlayerEvent *service.PlayerEventService
	ServerState *service.ServerStateService
	// CrashSnapshot 实例崩溃快照只读查询（FR-313）；nil 时端点关闭。
	CrashSnapshot    *service.CrashSnapshotService
	Business         *service.BusinessService
	BusinessEvent    *service.BusinessEventService
	Config           *service.ConfigService
	Bot              *service.BotService
	BotStressSession *service.BotStressSessionService
	// BotLoadCapacity/Preflight/Execution 是 FR-351 进程级共享单例；nil 时仅保留旧会话入口。
	BotLoadCapacity  *service.BotLoadCapacityDirectory
	BotLoadPreflight *service.BotLoadPreflightService
	BotLoadExecution *service.BotLoadExecutionService
	// BotLoadTemplate 命令压测模板（FR-370）；nil 时 /bots/load-templates 关闭。
	BotLoadTemplate *service.BotLoadTemplateService
	Alert            *service.AlertService
	AlertChannel     *service.AlertChannelService
	Schedule         *service.ScheduleService
	Backup           *service.BackupService
	BackupStorage    *service.BackupStorageService
	// ArtifactStorage 制品存储渠道（FR-347，见 ADR-073）：client-file 外置对象存储配置；
	// nil 时渠道端点关闭（上传恒本地）。
	ArtifactStorage *service.ArtifactStorageChannelService
	// ArtifactMigration 制品存量迁移（FR-348）：渠道间搬运后台任务；nil 时迁移端点关闭。
	ArtifactMigration *service.ArtifactMigrationService
	// ArtifactReconcile 制品索引 ↔ S3 一致性对账（FR-349）：对账运行/差异报告/显式处置；
	// nil 时对账端点关闭。
	ArtifactReconcile *service.ArtifactReconcileService
	Template          *service.TemplateService
	Audit             *service.AuditService
	Authz             *service.AuthzService
	Event             *service.EventService
	Asset             *service.AssetService
	Core              *service.CoreService
	Provision         *service.ProvisionService
	Proxy             *service.ProxyService
	Clone             *service.CloneService
	// ImportServer 导入现有服务器（FR-302，见 ADR-069）；nil 时导入端点关闭。
	ImportServer  *service.ImportServerService
	Registration  *service.RegistrationService
	Network       *service.NetworkService
	Log           *service.LogService
	Metric        *service.MetricService
	Settings      *service.SettingsService
	// OrphanRuntime 实例反向对账无主运行时列表/确认处置（FR-326）；nil 时端点关闭。
	OrphanRuntime *service.OrphanRuntimeTracker
	ProbeUpdate   *service.ProbeUpdateService
	ClientChannel *service.ClientChannelService
	ClientVersion *service.ClientVersionService
	// ClientChunkUpload 大文件分块上传（FR-251，增强 FR-088）；nil 时分块端点关闭、前端回退单次上传。
	ClientChunkUpload *service.ChunkedUploadService
	// ClientUploadEfficiency 上传增效：秒传预查 + 小文件聚合（FR-346，增强 FR-250/251）；
	// nil 时增效端点关闭、前端预查失败降级为全量上传（不阻断发布）。
	ClientUploadEfficiency *service.ClientUploadEfficiencyService
	ClientMachine          *service.ClientMachineService
	ClientDistTracking     *service.ClientDistTrackingService
	ClientIPGuard          *service.ClientIPGuardService
	ClientTelemetry        *service.ClientTelemetryService
	ClientDistStats        *service.ClientDistStatsService
	// ClientRuntimeState 客户端运行态心跳与聚合（FR-265）。
	ClientRuntimeState *service.ClientRuntimeStateService
	ClientDistSecurity *service.ClientDistSecurityService
	// ClientDistObservability 分发观测时序底座（FR-217，见 ADR-049）。
	ClientDistObservability *service.ClientDistObservabilityService
	// ClientDistExport 分发统计与安全日志 CSV 导出（FR-361）。
	ClientDistExport *service.ClientDistExportService
	RuntimeAssets *service.RuntimeAssetsService
	EnrollToken   *service.EnrollTokenService
	// AgentToken Agent 专用令牌 + 策略引擎（FR-384，见 ADR-076）；nil 时 agent 端点关闭。
	AgentToken *service.AgentTokenService
	// AgentCallLog Agent 调用流水（FR-390，见 ADR-076）；nil 时不记流水、无 call-logs/count。
	AgentCallLog *service.AgentCallLogService
	// MCP 内嵌 MCP 网关（FR-389，见 ADR-077）；nil 时 /api/v1/mcp 与会话管理关闭。
	MCP *mcp.Handler
	// EnrollInstall 拼装一键安装命令所需的对外地址（FR-080，见 ADR-020）。
	EnrollInstall EnrollInstallConfig
	Storage       *service.StorageService
	DBBrowse      *service.DBBrowseService
	SelfUpdate    *service.SelfUpdateService
	// 全局任务中心 + 站内信（FR-183，见 ADR-040）。
	Task         *service.TaskService
	Notification *service.NotificationService
	// 统一通知中心（FR-216，见 ADR-048）：聚合站内信 + 告警为一条只读通知流。
	NotificationFeed *service.NotificationFeedService
}

// Setup 创建并配置 Gin 路由引擎。
func Setup(svcs *Services, jwtSecret string) *gin.Engine {
	r := gin.New()
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		Skip: func(c *gin.Context) bool {
			return strings.HasPrefix(c.Request.URL.Path, "/worker-assets/")
		},
	}))
	r.Use(gin.Recovery())

	api := r.Group("/api/v1")
	api.Use(middleware.RateLimit(10, 20)) // 10 请求/秒，桶容量 20
	// API 错误统一落平台日志（FR-320）：4xx 业务拒绝 warn / 5xx error，经 log_slog 桥进日志中心，
	// /logs 页可追查「某操作为什么报错」（挂全局含未认证路径；401/404/429 噪音跳过）。
	api.Use(middleware.ErrorLog())

	// 公开路由（无需认证）
	authHandler := NewAuthHandler(svcs.Auth)
	authHandler.RegisterRoutes(api)

	setupHandler := NewSetupHandler(svcs.Auth)
	setupHandler.RegisterRoutes(api)

	// 面向玩家的客户端分发消费端点（FR-087，见 ADR-022/023、contract §4）：
	// manifest/制品端点用拉取密钥（X-Client-Key）鉴权，与运营浏览器 JWT 入口物理隔离，
	// 故挂在 api（公网、仅限流）而非 protected（JWT）。内容可信靠 manifest 签名而非密钥。
	if svcs.ClientChannel != nil && (svcs.ClientVersion != nil || svcs.ClientTelemetry != nil || svcs.ClientRuntimeState != nil || svcs.ClientDistSecurity != nil) {
		// L7 防护（FR-096，见 ADR-023）：消费端点独立子组挂 IP 黑白名单 + per-IP 限流 + 并发信号量，
		// 不影响其它 api 路由。L3/L4 容量型 DDoS 靠 CDN/云清洗，不在此。
		consumerGroup := api.Group("")
		if svcs.ClientIPGuard != nil {
			consumerGroup.Use(middleware.ClientDistGuard(svcs.ClientIPGuard, 5, 20, 256))
		}
		if svcs.ClientVersion != nil {
			clientConsumerHandler := NewClientVersionHandler(svcs.ClientVersion, svcs.ClientChannel, svcs.Audit, svcs.ClientMachine, svcs.ClientDistTracking, svcs.ClientDistSecurity)
			clientConsumerHandler.RegisterConsumerRoutes(consumerGroup)
		}
		// 客户端遥测上报（FR-094）：同为面向玩家公网端点，挂守卫子组（拉取密钥鉴权 + L7 防护）。
		if svcs.ClientTelemetry != nil {
			NewClientTelemetryHandler(svcs.ClientTelemetry, svcs.ClientChannel, svcs.ClientDistSecurity).RegisterRoutes(consumerGroup)
		}
		if svcs.ClientRuntimeState != nil {
			NewClientDistRuntimeHandler(svcs.ClientRuntimeState, svcs.ClientDistTracking, svcs.ClientChannel, svcs.Audit, svcs.ClientDistSecurity).RegisterConsumerRoutes(consumerGroup)
		}
		if svcs.ClientDistSecurity != nil {
			NewClientSecurityHandler(svcs.ClientDistSecurity, svcs.Audit).RegisterConsumerRoutes(consumerGroup)
		}
	}

	// 需要认证的路由
	protected := api.Group("")
	protected.Use(middleware.JWTAuth(jwtSecret))
	protected.Use(middleware.Audit(middleware.AuditConfig{
		RecordFunc: func(userID uint, action, targetType, targetID, detail, ip string, success bool, errMsg string) {
			_ = svcs.Audit.RecordResult(userID, action, targetType, targetID, detail, ip, success, errMsg)
		},
	}))
	// 加载授权上下文（用户角色 + 组成员关系），供后续权限判断使用
	protected.Use(middleware.LoadAccess(svcs.Authz))

	// 所有认证用户可访问的资源（按权限节点 + 资源隔离收敛）
	{
		nodeHandler := NewNodeHandler(svcs.Node, svcs.NodeRepair, svcs.Audit)
		nodeHandler.RegisterRoutes(protected)

		jdkHandler := NewJDKHandler(svcs.JDK)
		jdkHandler.RegisterRoutes(protected)

		// 节点运行时管理（FR-178）：制品缓存（列/清/逐项清/设上限）+ JDK 版本目录（foojay）+ 目录浏览。
		// Handler 内部按平台管理员收敛 + 破坏性操作写审计。
		if svcs.NodeRuntime != nil {
			nodeRuntimeHandler := NewNodeRuntimeHandler(svcs.NodeRuntime, svcs.Audit)
			nodeRuntimeHandler.RegisterRoutes(protected)
		}

		// 节点运行时库（FR-298）：统一 Runtime 视图（node_jdks + node_runtimes 读侧拼装）+
		// 扫描发现 + 泛化登记/删除。仅平台管理员；扫描/登记/删除写审计。
		if svcs.RuntimeLibrary != nil {
			runtimeLibraryHandler := NewRuntimeLibraryHandler(svcs.RuntimeLibrary, svcs.Audit)
			runtimeLibraryHandler.RegisterRoutes(protected)
		}

		// 节点包管理器与 registry 配置（FR-306）：PM 偏好（corepack 激活）+ 多 registry。仅平台管理员 + 审计。
		if svcs.PMConfig != nil {
			NewPMConfigHandler(svcs.PMConfig, svcs.Audit).RegisterRoutes(protected)
		}

		// 节点级出站代理（FR-185，见 ADR-043）：查看/设置某节点继承全局或自定义代理。
		// Handler 内按平台管理员收敛 + 设置写审计；经心跳下发 Worker 运行时生效。
		if svcs.NodeProxy != nil {
			nodeProxyHandler := NewNodeProxyHandler(svcs.NodeProxy, svcs.Audit)
			nodeProxyHandler.RegisterRoutes(protected)
		}

		// Docker 镜像管理（FR-078，见 ADR-019）：节点级列出/拉取/删除本机镜像。仅平台管理员。
		if svcs.DockerImage != nil {
			dockerImageHandler := NewDockerImageHandler(svcs.DockerImage)
			dockerImageHandler.RegisterRoutes(protected)
		}

		instanceHandler := NewInstanceHandler(svcs.Instance, svcs.Authz)
		instanceHandler.RegisterRoutes(protected)

		// 实例批量操作（FR-058）：独立 handler，挂 /instances/batch（与单实例路由共存）。
		instanceBatchHandler := NewInstanceBatchHandler(svcs.InstanceBatch, svcs.Authz)
		instanceBatchHandler.RegisterRoutes(protected)

		// 实例组织分组树（FR-165，见 ADR-033）：多级嵌套文件夹式归类 + 实例 M:N，
		// 正交于用户组 / 网络群组；读 instance:read、写 instance:write，挂 /instance-groups。
		if svcs.InstanceGroup != nil {
			instanceGroupHandler := NewInstanceGroupHandler(svcs.InstanceGroup, svcs.Authz)
			instanceGroupHandler.RegisterRoutes(protected)
		}

		// 探针在线更新（FR-068）：单实例 + 批量推送内嵌探针 jar，下次重启生效。instance:operate。
		if svcs.ProbeUpdate != nil {
			probeUpdateHandler := NewProbeUpdateHandler(svcs.ProbeUpdate, svcs.Instance, svcs.Authz)
			probeUpdateHandler.RegisterRoutes(protected)
		}

		terminalHandler := NewTerminalHandler(svcs.Terminal, svcs.Authz)
		terminalHandler.RegisterRoutes(protected)

		fileHandler := NewFileHandler(svcs.File, svcs.FileVersion, svcs.Authz)
		fileHandler.RegisterRoutes(protected)

		// 插件/模组单服管理（FR-052）：实例级隔离，复用 file gRPC 完成文件操作。
		pluginHandler := NewPluginHandler(svcs.Plugin, svcs.Authz, svcs.Audit)
		pluginHandler.RegisterRoutes(protected)

		configHandler := NewConfigHandler(svcs.Config, svcs.Authz)
		configHandler.RegisterRoutes(protected)

		// Bot 分布式容量静态路由必须先于 /bots/:id 注册，且复用进程级 CapacityDirectory。
		if svcs.BotLoadCapacity != nil {
			NewBotLoadHandler(svcs.BotLoadCapacity, svcs.Instance, svcs.Authz).RegisterRoutes(protected)
		}
		// FR-370 模板静态路由也须先于 /bots/:id。
		if svcs.BotLoadTemplate != nil {
			NewBotLoadTemplateHandler(svcs.BotLoadTemplate, svcs.Authz).RegisterRoutes(protected)
		}
		if svcs.BotStressSession != nil {
			botStressSessionHandler := NewBotStressSessionHandler(
				svcs.BotStressSession, svcs.Authz, svcs.BotLoadPreflight, svcs.BotLoadExecution, svcs.Audit,
			)
			botStressSessionHandler.RegisterRoutes(protected)
		}

		botHandler := NewBotHandler(svcs.Bot, svcs.Authz)
		botHandler.RegisterRoutes(protected)

		playerHandler := NewPlayerHandler(svcs.Player, svcs.PlayerEvent, svcs.Authz, svcs.Audit)
		playerHandler.RegisterRoutes(protected)

		// 服务器状态：按需经探针桥取回某实例全量 Bukkit 状态（FR-076/077），instance:read 且实例可访问。
		if svcs.ServerState != nil {
			serverStateHandler := NewServerStateHandler(svcs.ServerState, svcs.Authz)
			serverStateHandler.RegisterRoutes(protected)
		}

		// 崩溃诊断：实例崩溃快照只读列表（FR-313），instance:read 且实例可访问。
		if svcs.CrashSnapshot != nil {
			NewCrashSnapshotHandler(svcs.CrashSnapshot, svcs.Authz).RegisterRoutes(protected)
		}

		// JBIS 业务对接：经探针桥下发业务命令（domain.action+payload）并透传结果（FR-116），instance:operate 且实例可访问。
		if svcs.Business != nil {
			businessHandler := NewBusinessHandler(svcs.Business, svcs.Authz, svcs.Audit)
			businessHandler.RegisterRoutes(protected)
		}

		// JBIS 业务事件汇聚只读视图（FR-122，见 ADR-027/028）：业务事件流 / 经济镜像 / 跨区聚合。
		// 平台级只读（instance:read），汇聚镜像非业务真源；写入由探针事件流自动汇聚。
		if svcs.BusinessEvent != nil {
			businessEventHandler := NewBusinessEventHandler(svcs.BusinessEvent, svcs.Authz)
			businessEventHandler.RegisterRoutes(protected)
		}

		eventHandler := NewEventHandler(svcs.Event)
		eventHandler.RegisterRoutes(protected)

		// 组相关：列表/创建由 group:read/group:manage 节点控制，
		// 组级资源（:id）由 GroupHandler 内部按授权上下文收敛
		groupHandler := NewGroupHandler(svcs.Group, svcs.Authz)
		groupHandler.RegisterRoutes(protected)

		alertHandler := NewAlertHandler(svcs.Alert, svcs.AlertChannel)
		alertHandler.RegisterRoutes(protected)

		scheduleHandler := NewScheduleHandler(svcs.Schedule)
		scheduleHandler.RegisterRoutes(protected)

		backupHandler := NewBackupHandler(svcs.Backup)
		backupHandler.RegisterRoutes(protected)

		templateHandler := NewTemplateHandler(svcs.Template)
		templateHandler.RegisterRoutes(protected)

		// 制品库：平台级共享资源，Handler 内部按平台管理员收敛（FR-045）。
		assetHandler := NewAssetHandler(svcs.Asset)
		assetHandler.RegisterRoutes(protected)

		// 运行时与制品全局页聚合（FR-082）：JDK 矩阵 + 引用实例 + 制品占用/去重/冷热；
		// FR-301 另含多运行时矩阵与强制刷新（写审计）。平台级共享资源，Handler 内部按平台管理员收敛。
		if svcs.RuntimeAssets != nil {
			runtimeAssetsHandler := NewRuntimeAssetsHandler(svcs.RuntimeAssets, svcs.Audit)
			runtimeAssetsHandler.RegisterRoutes(protected)
		}

		// 日志中心：所有认证用户可查询，Handler 内部按可访问实例收敛、平台日志仅管理员可见（FR-049）。
		logHandler := NewLogHandler(svcs.Log, svcs.Authz)
		logHandler.RegisterRoutes(protected)

		// 时序监控历史曲线（FR-060）：node 维度对认证用户开放，instance 维度按 CanAccessInstance 收敛。
		metricHandler := NewMetricHandler(svcs.Metric, svcs.Authz)
		metricHandler.RegisterRoutes(protected)

		// 全局任务中心（FR-183，见 ADR-040）：认证用户可见，非管理员只见自己发起的任务（service 层收敛）。
		if svcs.Task != nil {
			taskHandler := NewTaskHandler(svcs.Task)
			taskHandler.RegisterRoutes(protected)
		}

		// 站内信（FR-183，见 ADR-040）：认证用户只读/操作自己的站内信。
		if svcs.Notification != nil {
			notificationHandler := NewNotificationHandler(svcs.Notification)
			notificationHandler.RegisterRoutes(protected)
		}

		// 统一通知中心（FR-216，见 ADR-048）：聚合站内信 + 告警为一条只读通知流，
		// 页眉单铃铛 + 通知中心页消费。认证用户（消息按本人、告警全局）。
		if svcs.NotificationFeed != nil {
			notificationFeedHandler := NewNotificationFeedHandler(svcs.NotificationFeed)
			notificationFeedHandler.RegisterRoutes(protected)
		}
	}

	// 仅平台管理员
	admin := protected.Group("")
	admin.Use(middleware.RequireRole(model.RolePlatformAdmin))
	{
		userHandler := NewUserHandler(svcs.User)
		userHandler.RegisterRoutes(admin)

		auditHandler := NewAuditHandler(svcs.Audit)
		auditHandler.RegisterRoutes(admin)

		// 一键搭建子服与核心查询（FR-034）、搭建代理（FR-035）：平台管理员
		provisionHandler := NewProvisionHandler(svcs.Core, svcs.Provision, svcs.Proxy, svcs.Clone)
		provisionHandler.RegisterRoutes(admin)

		// 导入现有服务器：就地接管 / 搬迁托管区（FR-302，见 ADR-069）。平台管理员 + 审计。
		if svcs.ImportServer != nil {
			importServerHandler := NewImportServerHandler(svcs.ImportServer, svcs.Audit)
			importServerHandler.RegisterRoutes(admin)
		}

		// 群组服关系模型：角色注册、Network 软标签（FR-032）。平台管理员。
		registrationHandler := NewRegistrationHandler(svcs.Registration)
		registrationHandler.RegisterRoutes(admin)

		networkHandler := NewNetworkHandler(svcs.Network)
		networkHandler.RegisterRoutes(admin)

		// 群组拓扑聚合（FR-335）：一次返全量 proxy 注册 + network 成员归属，消 per-proxy N+1。平台管理员。
		topologyHandler := NewTopologyHandler(svcs.Registration, svcs.Network)
		topologyHandler.RegisterRoutes(admin)

		// 备份远程存储后端：含凭证 env 引用，平台级配置，限平台管理员（FR-057）。
		if svcs.BackupStorage != nil {
			backupStorageHandler := NewBackupStorageHandler(svcs.BackupStorage)
			backupStorageHandler.RegisterRoutes(admin)
		}

		// 制品存储渠道：client-file 制品外置对象存储配置（凭证可逆加密落库），
		// 限平台管理员（FR-347，见 ADR-073）。
		if svcs.ArtifactStorage != nil {
			NewArtifactStorageHandler(svcs.ArtifactStorage).RegisterRoutes(admin)
		}

		// 制品存量迁移：渠道间搬运后台任务（发起/状态/失败明细），限平台管理员（FR-348）。
		if svcs.ArtifactMigration != nil {
			NewArtifactMigrationHandler(svcs.ArtifactMigration).RegisterRoutes(admin)
		}

		// 制品对账：索引 ↔ S3 对象一致性运行/报告/显式处置，限平台管理员（FR-349）。
		if svcs.ArtifactReconcile != nil {
			NewArtifactReconcileHandler(svcs.ArtifactReconcile, svcs.Audit).RegisterRoutes(admin)
		}

		// 平台配置：全量配置可视化 + 白名单运行时覆盖，限平台管理员（FR-063 / ADR-015）。
		if svcs.Settings != nil {
			settingsHandler := NewSettingsHandler(svcs.Settings)
			settingsHandler.RegisterRoutes(admin)
		}

		// 无主运行时反向对账：列表 + 手动确认处置，限平台管理员（FR-326）。
		if svcs.OrphanRuntime != nil {
			NewOrphanRuntimeHandler(svcs.OrphanRuntime).RegisterRoutes(admin)
		}

		// 连通性测试：出站 HTTP 可达性（代理 / 下载源）+ 节点存活探测，限平台管理员（FR-229）。
		if svcs.Diagnostics != nil {
			NewDiagnosticsHandler(svcs.Diagnostics).RegisterRoutes(admin)
		}

		// 平台存储资源管理器：CP 侧数据根 FHS 只读浏览 + 占用统计 + cache 受控清理，
		// 数据根仅 CP 读写，限平台管理员（FR-083 / ADR-010）。
		if svcs.Storage != nil {
			storageHandler := NewStorageHandler(svcs.Storage)
			storageHandler.RegisterRoutes(admin)
		}

		// 客户端分发频道与拉取密钥：平台级，限平台管理员（FR-086 / ADR-022）。
		if svcs.ClientChannel != nil {
			clientChannelHandler := NewClientChannelHandler(svcs.ClientChannel, svcs.Audit)
			clientChannelHandler.RegisterRoutes(admin)
		}

		// 客户端分发发布端点（文件制品 + 版本发布、切 latest 指针）：运营操作，限平台管理员
		// （FR-087 / ADR-022）。消费端点（manifest/制品）走公网 key 鉴权，已在 api 组注册。
		if svcs.ClientVersion != nil && svcs.ClientChannel != nil {
			clientVersionHandler := NewClientVersionHandler(svcs.ClientVersion, svcs.ClientChannel, svcs.Audit, svcs.ClientMachine, svcs.ClientDistTracking, svcs.ClientDistSecurity)
			clientVersionHandler.RegisterPublishRoutes(admin)
		}

		// jm-updater.json 一键生成端点（FR-253，见 ADR-053）：按频道生成 jm-updater.json。
		// FR-256 起不再含签名公钥（验签已去，信任靠 HTTPS + 拉取密钥鉴权，推翻 ADR-022/053）。
		// 限平台管理员；依赖频道服务（校验存在）。
		if svcs.ClientChannel != nil {
			NewClientUpdaterConfigHandler(svcs.ClientChannel).RegisterRoutes(admin)
		}

		// 客户端分发大文件分块上传（init→chunk→complete，支持 4G+ 文件）：运营操作，限平台管理员
		// （FR-251，增强 FR-088）。与单次上传 POST /files 同鉴权组、落同一 CAS；独立 handler 不改 client_version.go。
		if svcs.ClientChunkUpload != nil && svcs.ClientChannel != nil {
			clientChunkUploadHandler := NewClientChunkUploadHandler(svcs.ClientChunkUpload, svcs.ClientChannel, svcs.Audit)
			clientChunkUploadHandler.RegisterRoutes(admin)
		}

		// 客户端分发上传增效：秒传预查 + 小文件聚合（FR-346，增强 FR-250/251）。与分块上传
		// 同鉴权组、落同一 CAS；独立 handler 不改既有上传端点。
		if svcs.ClientUploadEfficiency != nil && svcs.ClientChannel != nil {
			clientUploadEffHandler := NewClientUploadEfficiencyHandler(svcs.ClientUploadEfficiency, svcs.ClientChannel, svcs.Audit)
			clientUploadEffHandler.RegisterRoutes(admin)
		}

		// updater-core 版本归档 + 频道选定（FR-259，见 updater-arch-simplification spec §D）：
		// 内嵌 core jar 入库为 client-updater-core 类型（多版本归档不覆盖），运营经版本选择器切换频道选定版本。
		// coreEndpoint（拉取密钥鉴权）+ versions/selected（JWT 平台管理员）端点已注册于 ClientVersionHandler。

		// 客户端分发安全防护（FR-264）：画像、风险事件、处置与保护模式。限平台管理员。
		if svcs.ClientDistSecurity != nil {
			NewClientSecurityHandler(svcs.ClientDistSecurity, svcs.Audit).RegisterAdminRoutes(admin)
		}

		// 客户端分发端点 L7 防护：IP 黑白名单规则管理 + 防护拦截计数（FR-096 / ADR-023）。限平台管理员。
		if svcs.ClientIPGuard != nil {
			clientIPRuleHandler := NewClientIPRuleHandler(svcs.ClientIPGuard, svcs.Audit)
			clientIPRuleHandler.RegisterRoutes(admin)
		}

		// 分发统计后台：下载趋势/版本分布/成功率/活跃机器码/TopIP 只读聚合（FR-095 / ADR-023）。限平台管理员。
		if svcs.ClientDistStats != nil {
			clientStatsHandler := NewClientStatsHandler(svcs.ClientDistStats)
			clientStatsHandler.RegisterRoutes(admin)
		}

		// 客户端分发观测数据底座：跨频道/平台时序 + 区间分布聚合（FR-217 / ADR-049）。限平台管理员 + 审计。
		if svcs.ClientDistObservability != nil {
			clientDistObsHandler := NewClientDistObservabilityHandler(svcs.ClientDistObservability, svcs.Audit)
			clientDistObsHandler.RegisterRoutes(admin)
		}

		// 客户端分发运行态与请求近实时观测（FR-265）：客户端 Tab 独立使用运行态表，日志/实时只读分发事件。
		if svcs.ClientRuntimeState != nil && svcs.ClientDistTracking != nil {
			NewClientDistRuntimeHandler(svcs.ClientRuntimeState, svcs.ClientDistTracking, svcs.ClientChannel, svcs.Audit, svcs.ClientDistSecurity).RegisterAdminRoutes(admin)
		}

		// 分发统计、请求事件与安全日志 CSV 导出（FR-361）：平台管理员、每用户一分钟冷却、最多一万行。
		if svcs.ClientDistExport != nil {
			NewClientDistExportHandler(svcs.ClientDistExport, svcs.Audit).RegisterRoutes(admin)
		}

		// 客户端更新器接入引导：内嵌 wedge/updater-core jar 版本查询 + 下载（FR-107）。
		// 无 service 依赖（jar 构建期内嵌），无条件注册；限平台管理员。
		NewClientUpdaterJarsHandler().RegisterRoutes(admin)

		// 节点 enrollment token（一键安装 / 傻瓜部署）：签发一次性准入凭据 + 一键命令，
		// 限平台管理员（FR-080，见 ADR-020）。签发/吊销写审计，明文绝不入审计。
		if svcs.EnrollToken != nil {
			enrollTokenHandler := NewEnrollTokenHandler(svcs.EnrollToken, svcs.Audit, svcs.EnrollInstall, svcs.SelfUpdate)
			enrollTokenHandler.RegisterRoutes(admin)
		}
		// Agent Token 管理（FR-384，见 ADR-076）：颁发/列表/吊销，限平台管理员；明文仅创建响应返回。
		// FR-390：列表附 callCount24h；GET /agent/call-logs 查询调用流水。
		if svcs.AgentToken != nil {
			NewAgentTokenHandler(svcs.AgentToken, svcs.Audit, svcs.AgentCallLog).RegisterAdminRoutes(admin)
		}
		// MCP 会话运维（FR-389）：列表/踢线，限平台管理员 JWT。
		if svcs.MCP != nil {
			svcs.MCP.RegisterAdminRoutes(admin)
		}
		// 数据库资源管理器只读浏览（FR-084）：CP 独有数据源，仅平台管理员；只读 + 敏感列脱敏。
		if svcs.DBBrowse != nil {
			dbBrowseHandler := NewDBBrowseHandler(svcs.DBBrowse)
			dbBrowseHandler.RegisterRoutes(admin)
		}

		// 面板自更新（FR-081，见 ADR-020 §4）：检查更新 + CP 自升级 + 经 gRPC 编排全网 Worker 升级，
		// 仅平台管理员 + 升级审计。
		if svcs.SelfUpdate != nil {
			selfUpdateHandler := NewSelfUpdateHandler(svcs.SelfUpdate, svcs.Audit)
			selfUpdateHandler.RegisterRoutes(admin)
		}
	}

	// Agent 运维面（FR-384）：Bearer Agent Token（jmat_*），不走人类 JWT；策略在 CP 唯一真源。
	if svcs.AgentToken != nil && svcs.Instance != nil && svcs.Node != nil {
		agentGroup := api.Group("")
		agentGroup.Use(middleware.AgentAuth(svcs.AgentToken))
		// 要求已注入 agent principal，否则 401（拒绝纯 JWT 误入此组的写路径混用）
		agentGroup.Use(func(c *gin.Context) {
			if middleware.GetAgentPrincipal(c) == nil {
				c.AbortWithStatusJSON(401, gin.H{"error": "UNAUTHORIZED", "message": "需要有效的 Agent Token（jmat_ 前缀）"})
				return
			}
			c.Next()
		})
		NewAgentOpsHandler(svcs.AgentToken, svcs.Instance, svcs.Node, svcs.Audit, svcs.AgentCallLog).RegisterOpsRoutes(agentGroup)
	}

	// CP 内嵌 MCP 网关（FR-389 / ADR-077）：Streamable HTTP + SSE；仅 Agent Token。
	if svcs.MCP != nil && svcs.AgentToken != nil {
		mcpGroup := api.Group("/mcp")
		mcpGroup.Use(middleware.AgentAuth(svcs.AgentToken))
		mcpGroup.Use(func(c *gin.Context) {
			if middleware.GetAgentPrincipal(c) == nil {
				c.AbortWithStatusJSON(401, gin.H{"error": "UNAUTHORIZED", "message": "需要有效的 Agent Token（jmat_ 前缀）"})
				return
			}
			c.Next()
		})
		svcs.MCP.RegisterMCPRoutes(mcpGroup)
	}

	// Worker 一键安装脚本匿名静态端点（FR-080，见 ADR-020 §2）：一键命令 `curl <cp>/install-worker.sh | sh`
	// 依赖 CP 自托管这两个脚本。显式注册（根路径、非 /api/v1）以先于下方 SPA NoRoute 回退命中。
	registerInstallScriptRoutes(r)

	// Worker 二进制 CP-local 下载端点（FR-190）：匿名路径由短 token 保护，先于 SPA NoRoute 注册。
	if svcs.SelfUpdate != nil {
		NewSelfUpdateHandler(svcs.SelfUpdate, svcs.Audit).RegisterDownloadRoutes(r)
	}

	// 前端静态文件（go:embed 嵌入）
	embed.RegisterStaticRoutes(r)

	return r
}
