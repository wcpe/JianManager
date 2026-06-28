package router

import (
	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/embed"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Services 聚合所有服务依赖。
type Services struct {
	Auth               *service.AuthService
	User               *service.UserService
	Group              *service.GroupService
	Node               *service.NodeService
	NodeRepair         *service.NodeRepairService
	// NodeProxy 节点级出站代理管控（FR-185，见 ADR-043）；nil 时节点代理端点关闭。
	NodeProxy          *service.NodeProxyService
	Instance           *service.InstanceService
	InstanceBatch      *service.InstanceBatchService
	InstanceGroup      *service.InstanceGroupService
	JDK                *service.JDKService
	NodeRuntime        *service.NodeRuntimeService
	DockerImage        *service.DockerImageService
	Terminal           *service.TerminalService
	File               *service.FileService
	FileVersion        *service.FileVersionService
	Plugin             *service.PluginService
	Player             *service.PlayerService
	PlayerEvent        *service.PlayerEventService
	ServerState        *service.ServerStateService
	Business           *service.BusinessService
	BusinessEvent      *service.BusinessEventService
	Config             *service.ConfigService
	Bot                *service.BotService
	Alert              *service.AlertService
	AlertChannel       *service.AlertChannelService
	Schedule           *service.ScheduleService
	Backup             *service.BackupService
	BackupStorage      *service.BackupStorageService
	Template           *service.TemplateService
	Audit              *service.AuditService
	Authz              *service.AuthzService
	Event              *service.EventService
	Asset              *service.AssetService
	Core               *service.CoreService
	Provision          *service.ProvisionService
	Proxy              *service.ProxyService
	Clone              *service.CloneService
	Registration       *service.RegistrationService
	Network            *service.NetworkService
	Log                *service.LogService
	Metric             *service.MetricService
	Settings           *service.SettingsService
	ProbeUpdate        *service.ProbeUpdateService
	ClientChannel      *service.ClientChannelService
	ClientVersion      *service.ClientVersionService
	ClientMachine      *service.ClientMachineService
	ClientDistTracking *service.ClientDistTrackingService
	ClientIPGuard      *service.ClientIPGuardService
	ClientTelemetry    *service.ClientTelemetryService
	ClientDistStats    *service.ClientDistStatsService
	JmPack             *service.JmPackService
	RuntimeAssets      *service.RuntimeAssetsService
	EnrollToken        *service.EnrollTokenService
	// EnrollInstall 拼装一键安装命令所需的对外地址（FR-080，见 ADR-020）。
	EnrollInstall EnrollInstallConfig
	Storage       *service.StorageService
	DBBrowse      *service.DBBrowseService
	SelfUpdate    *service.SelfUpdateService
	// 全局任务中心 + 站内信（FR-183，见 ADR-040）。
	Task         *service.TaskService
	Notification *service.NotificationService
}

// Setup 创建并配置 Gin 路由引擎。
func Setup(svcs *Services, jwtSecret string) *gin.Engine {
	r := gin.Default()

	api := r.Group("/api/v1")
	api.Use(middleware.RateLimit(10, 20)) // 10 请求/秒，桶容量 20

	// 公开路由（无需认证）
	authHandler := NewAuthHandler(svcs.Auth)
	authHandler.RegisterRoutes(api)

	setupHandler := NewSetupHandler(svcs.Auth)
	setupHandler.RegisterRoutes(api)

	// 面向玩家的客户端分发消费端点（FR-087，见 ADR-022/023、contract §4）：
	// manifest/制品端点用拉取密钥（X-Client-Key）鉴权，与运营浏览器 JWT 入口物理隔离，
	// 故挂在 api（公网、仅限流）而非 protected（JWT）。内容可信靠 manifest 签名而非密钥。
	if svcs.ClientVersion != nil && svcs.ClientChannel != nil {
		clientConsumerHandler := NewClientVersionHandler(svcs.ClientVersion, svcs.ClientChannel, svcs.Audit, svcs.ClientMachine, svcs.ClientDistTracking)
		// L7 防护（FR-096，见 ADR-023）：消费端点独立子组挂 IP 黑白名单 + per-IP 限流 + 并发信号量，
		// 不影响其它 api 路由。L3/L4 容量型 DDoS 靠 CDN/云清洗，不在此。
		consumerGroup := api.Group("")
		if svcs.ClientIPGuard != nil {
			consumerGroup.Use(middleware.ClientDistGuard(svcs.ClientIPGuard, 5, 20, 256))
		}
		clientConsumerHandler.RegisterConsumerRoutes(consumerGroup)
		// 客户端遥测上报（FR-094）：同为面向玩家公网端点，挂守卫子组（拉取密钥鉴权 + L7 防护）。
		if svcs.ClientTelemetry != nil {
			NewClientTelemetryHandler(svcs.ClientTelemetry, svcs.ClientChannel).RegisterRoutes(consumerGroup)
		}
	}

	// 需要认证的路由
	protected := api.Group("")
	protected.Use(middleware.JWTAuth(jwtSecret))
	protected.Use(middleware.Audit(middleware.AuditConfig{
		RecordFunc: func(userID uint, action, targetType, targetID, detail, ip string) {
			_ = svcs.Audit.Record(userID, action, targetType, targetID, detail, ip)
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
		pluginHandler := NewPluginHandler(svcs.Plugin, svcs.Authz)
		pluginHandler.RegisterRoutes(protected)

		configHandler := NewConfigHandler(svcs.Config, svcs.Authz)
		configHandler.RegisterRoutes(protected)

		botHandler := NewBotHandler(svcs.Bot, svcs.Authz)
		botHandler.RegisterRoutes(protected)

		playerHandler := NewPlayerHandler(svcs.Player, svcs.PlayerEvent, svcs.Authz, svcs.Audit)
		playerHandler.RegisterRoutes(protected)

		// 服务器状态：按需经探针桥取回某实例全量 Bukkit 状态（FR-076/077），instance:read 且实例可访问。
		if svcs.ServerState != nil {
			serverStateHandler := NewServerStateHandler(svcs.ServerState, svcs.Authz)
			serverStateHandler.RegisterRoutes(protected)
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

		// 运行时与制品全局页只读聚合（FR-082）：JDK 矩阵 + 引用实例 + 制品占用/去重/冷热。
		// 平台级共享资源，Handler 内部按平台管理员收敛。
		if svcs.RuntimeAssets != nil {
			runtimeAssetsHandler := NewRuntimeAssetsHandler(svcs.RuntimeAssets)
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

		// 群组服关系模型：角色注册、Network 软标签（FR-032）。平台管理员。
		registrationHandler := NewRegistrationHandler(svcs.Registration)
		registrationHandler.RegisterRoutes(admin)

		networkHandler := NewNetworkHandler(svcs.Network)
		networkHandler.RegisterRoutes(admin)

		// 备份远程存储后端：含凭证 env 引用，平台级配置，限平台管理员（FR-057）。
		if svcs.BackupStorage != nil {
			backupStorageHandler := NewBackupStorageHandler(svcs.BackupStorage)
			backupStorageHandler.RegisterRoutes(admin)
		}

		// 平台配置：全量配置可视化 + 白名单运行时覆盖，限平台管理员（FR-063 / ADR-015）。
		if svcs.Settings != nil {
			settingsHandler := NewSettingsHandler(svcs.Settings)
			settingsHandler.RegisterRoutes(admin)
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
			clientVersionHandler := NewClientVersionHandler(svcs.ClientVersion, svcs.ClientChannel, svcs.Audit, svcs.ClientMachine, svcs.ClientDistTracking)
			clientVersionHandler.RegisterPublishRoutes(admin)
		}

		// updater-core 默认随 CP 内嵌、自动驱动 manifest agent.core，运营不管理（FR-193，见 ADR-045 改写）。
		// 已删除原运营侧 core 版本管理端点（上传/登记/pin/更新/回退）；agent.core 由内嵌 updater-core 自动产出
		// （见 service.BuildManifest.applyEmbeddedCore），内嵌 core 当作 client-file 制品经公网 client-artifacts 下发。

		// 客户端分发端点 L7 防护：IP 黑白名单规则管理 + 防护拦截计数（FR-096 / ADR-023）。限平台管理员。
		if svcs.ClientIPGuard != nil {
			clientIPRuleHandler := NewClientIPRuleHandler(svcs.ClientIPGuard, svcs.Audit)
			clientIPRuleHandler.RegisterRoutes(admin)
		}

		// 客户端分发 .jmpack 打包（latest 版本压缩+签名入库）：运营操作，限平台管理员（FR-097 / ADR-021/022）。
		if svcs.JmPack != nil {
			jmPackHandler := NewJmPackHandler(svcs.JmPack, svcs.Audit)
			jmPackHandler.RegisterRoutes(admin)
		}

		// 分发统计后台：下载趋势/版本分布/成功率/活跃机器码/TopIP 只读聚合（FR-095 / ADR-023）。限平台管理员。
		if svcs.ClientDistStats != nil {
			clientStatsHandler := NewClientStatsHandler(svcs.ClientDistStats)
			clientStatsHandler.RegisterRoutes(admin)
		}

		// 客户端更新器接入引导：内嵌 wedge/updater-core jar 版本查询 + 下载（FR-107）。
		// 无 service 依赖（jar 构建期内嵌），无条件注册；限平台管理员。
		NewClientUpdaterJarsHandler().RegisterRoutes(admin)

		// 节点 enrollment token（一键安装 / 傻瓜部署）：签发一次性准入凭据 + 一键命令，
		// 限平台管理员（FR-080，见 ADR-020）。签发/吊销写审计，明文绝不入审计。
		if svcs.EnrollToken != nil {
			enrollTokenHandler := NewEnrollTokenHandler(svcs.EnrollToken, svcs.Audit, svcs.EnrollInstall)
			enrollTokenHandler.RegisterRoutes(admin)
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

	// Worker 一键安装脚本匿名静态端点（FR-080，见 ADR-020 §2）：一键命令 `curl <cp>/install-worker.sh | sh`
	// 依赖 CP 自托管这两个脚本。显式注册（根路径、非 /api/v1）以先于下方 SPA NoRoute 回退命中。
	registerInstallScriptRoutes(r)

	// 前端静态文件（go:embed 嵌入）
	embed.RegisterStaticRoutes(r)

	return r
}
