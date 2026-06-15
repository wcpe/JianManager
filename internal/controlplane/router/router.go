package router

import (
	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/middleware"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// Setup 创建并配置 Gin 路由引擎。
func Setup(authSvc *service.AuthService, jwtSecret string) *gin.Engine {
	r := gin.Default()

	api := r.Group("/api/v1")

	// 公开路由（无需认证）
	authHandler := NewAuthHandler(authSvc)
	authHandler.RegisterRoutes(api)

	// 需要认证的路由
	protected := api.Group("")
	protected.Use(middleware.JWTAuth(jwtSecret))
	{
		// TODO: 注册其他业务路由
		_ = protected
	}

	return r
}
