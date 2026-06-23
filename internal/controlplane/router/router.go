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
	Auth          *service.AuthService
	User          *service.UserService
	Group         *service.GroupService
	Node          *service.NodeService
	Instance      *service.InstanceService
	InstanceBatch *service.InstanceBatchService
	JDK           *service.JDKService
	Terminal      *service.TerminalService
	File          *service.FileService
	FileVersion   *service.FileVersionService
	Plugin        *service.PluginService
	Player        *service.PlayerService
	PlayerEvent   *service.PlayerEventService
	Config        *service.ConfigService
	Bot           *service.BotService
	Alert         *service.AlertService
	Schedule      *service.ScheduleService
	Backup        *service.BackupService
	BackupStorage *service.BackupStorageService
	Template      *service.TemplateService
	Audit         *service.AuditService
	Authz         *service.AuthzService
	Event         *service.EventService
	Asset         *service.AssetService
	Core          *service.CoreService
	Provision     *service.ProvisionService
	Proxy         *service.ProxyService
	Clone         *service.CloneService
	Registration  *service.RegistrationService
	Network       *service.NetworkService
	Log           *service.LogService
	Metric        *service.MetricService
	Settings      *service.SettingsService
	ProbeUpdate   *service.ProbeUpdateService
	ClientChannel *service.ClientChannelService
	ClientVersion *service.ClientVersionService
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
		clientConsumerHandler := NewClientVersionHandler(svcs.ClientVersion, svcs.ClientChannel, svcs.Audit)
		clientConsumerHandler.RegisterConsumerRoutes(api)
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

		alertHandler := NewAlertHandler(svcs.Alert)
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

		// 客户端分发频道与拉取密钥：平台级，限平台管理员（FR-086 / ADR-022）。
		if svcs.ClientChannel != nil {
			clientChannelHandler := NewClientChannelHandler(svcs.ClientChannel, svcs.Audit)
			clientChannelHandler.RegisterRoutes(admin)
		}

		// 客户端分发发布端点（文件制品 + 版本发布、切 latest 指针）：运营操作，限平台管理员
		// （FR-087 / ADR-022）。消费端点（manifest/制品）走公网 key 鉴权，已在 api 组注册。
		if svcs.ClientVersion != nil && svcs.ClientChannel != nil {
			clientVersionHandler := NewClientVersionHandler(svcs.ClientVersion, svcs.ClientChannel, svcs.Audit)
			clientVersionHandler.RegisterPublishRoutes(admin)
		}
	}

	// 前端静态文件（go:embed 嵌入）
	embed.RegisterStaticRoutes(r)

	return r
}
