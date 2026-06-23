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
	Instance           *service.InstanceService
	InstanceBatch      *service.InstanceBatchService
	JDK                *service.JDKService
	DockerImage        *service.DockerImageService
	Terminal           *service.TerminalService
	File               *service.FileService
	FileVersion        *service.FileVersionService
	Plugin             *service.PluginService
	Player             *service.PlayerService
	PlayerEvent        *service.PlayerEventService
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
	JmPack             *service.JmPackService
	RuntimeAssets      *service.RuntimeAssetsService
	EnrollToken        *service.EnrollTokenService
	// EnrollInstall 拼装一键安装命令所需的对外地址（FR-080，见 ADR-020）。
	EnrollInstall EnrollInstallConfig
	Storage       *service.StorageService
	DBBrowse      *service.DBBrowseService
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
		nodeHandler := NewNodeHandler(svcs.Node)
		nodeHandler.RegisterRoutes(protected)

		jdkHandler := NewJDKHandler(svcs.JDK)
		jdkHandler.RegisterRoutes(protected)

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
	}

	// 前端静态文件（go:embed 嵌入）
	embed.RegisterStaticRoutes(r)

	return r
}
