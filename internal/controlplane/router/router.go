package router

import (
	"github.com/gin-gonic/gin"

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
}

// Setup 创建并配置 Gin 路由引擎。
func Setup(svcs *Services, jwtSecret string) *gin.Engine {
	r := gin.Default()

	api := r.Group("/api/v1")

	// 公开路由（无需认证）
	authHandler := NewAuthHandler(svcs.Auth)
	authHandler.RegisterRoutes(api)

	// 需要认证的路由
	protected := api.Group("")
	protected.Use(middleware.JWTAuth(jwtSecret))

	// 所有认证用户可访问
	{
		nodeHandler := NewNodeHandler(svcs.Node)
		nodeHandler.RegisterRoutes(protected)

		instanceHandler := NewInstanceHandler(svcs.Instance)
		instanceHandler.RegisterRoutes(protected)

		terminalHandler := NewTerminalHandler(svcs.Terminal)
		terminalHandler.RegisterRoutes(protected)

		fileHandler := NewFileHandler(svcs.File)
		fileHandler.RegisterRoutes(protected)
	}

	// 仅平台管理员
	admin := protected.Group("")
	admin.Use(middleware.RequireRole(model.RolePlatformAdmin))
	{
		userHandler := NewUserHandler(svcs.User)
		userHandler.RegisterRoutes(admin)

		groupHandler := NewGroupHandler(svcs.Group)
		groupHandler.RegisterRoutes(admin)
	}

	return r
}
