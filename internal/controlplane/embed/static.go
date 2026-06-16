package embed

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var distFS embed.FS

// RegisterStaticRoutes 注册前端静态文件路由。
func RegisterStaticRoutes(r *gin.Engine) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("嵌入前端文件失败: " + err.Error())
	}

	staticFS := http.FS(sub)

	// SPA 路由：所有非 API 路径返回 index.html
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		// API 路径不处理
		if strings.HasPrefix(path, "/api/") {
			c.JSON(404, gin.H{"error": "NOT_FOUND", "message": "接口不存在"})
			return
		}

		// 尝试提供静态文件
		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		file, err := sub.Open(cleanPath)
		if err == nil {
			file.Close()
			c.FileFromFS(cleanPath, staticFS)
			return
		}

		// SPA fallback：返回 index.html
		c.FileFromFS("index.html", staticFS)
	})
}
