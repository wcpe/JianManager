package router

import (
	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/embed"
	"github.com/wxys233/JianManager/internal/controlplane/middleware"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// Services 聚合所有服务依赖。
type Services struct {
	Auth     *service.AuthService
	User     *service.UserService
	Group    *service.GroupService
	Node     *service.NodeService
	Instance *service.InstanceService
	Terminal *service.TerminalService
	File     *service.FileService
	Config   *service.ConfigService
	Bot      *service.BotService
	Alert    *service.AlertService
	Schedule *service.ScheduleService
	Backup   *service.BackupService
	Template *service.TemplateService
	Audit    *service.AuditService
	Authz    *service.AuthzService
	Event    *service.EventService
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

		instanceHandler := NewInstanceHandler(svcs.Instance, svcs.Authz)
		instanceHandler.RegisterRoutes(protected)

		terminalHandler := NewTerminalHandler(svcs.Terminal, svcs.Authz)
		terminalHandler.RegisterRoutes(protected)

		fileHandler := NewFileHandler(svcs.File, svcs.Authz)
		fileHandler.RegisterRoutes(protected)

		configHandler := NewConfigHandler(svcs.Config, svcs.Authz)
		configHandler.RegisterRoutes(protected)

		botHandler := NewBotHandler(svcs.Bot, svcs.Authz)
		botHandler.RegisterRoutes(protected)

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
	}

	// 仅平台管理员
	admin := protected.Group("")
	admin.Use(middleware.RequireRole(model.RolePlatformAdmin))
	{
		userHandler := NewUserHandler(svcs.User)
		userHandler.RegisterRoutes(admin)

		auditHandler := NewAuditHandler(svcs.Audit)
		auditHandler.RegisterRoutes(admin)
	}

	// 前端静态文件（go:embed 嵌入）
	embed.RegisterStaticRoutes(r)

	return r
}
