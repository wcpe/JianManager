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

	// 预读 index.html。SPA 回退时直接返回其内容，避免 http.FileServer
	// 对以 /index.html 结尾的请求触发 301 → "./" 的规范化重定向，
	// 该重定向会和根路径形成死循环（ERR_TOO_MANY_REDIRECTS）。
	indexHTML, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("读取嵌入 index.html 失败: " + err.Error())
	}
	serveIndex := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	}

	// SPA 路由：所有非 API 路径返回静态文件或 index.html
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		// API 路径不处理
		if strings.HasPrefix(path, "/api/") {
			c.JSON(404, gin.H{"error": "NOT_FOUND", "message": "接口不存在"})
			return
		}

		// 根路径或首页直接返回 index.html 内容
		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" || cleanPath == "index.html" {
			serveIndex(c)
			return
		}

		// 尝试提供静态资源（assets/*、favicon 等真实文件）
		file, err := sub.Open(cleanPath)
		if err == nil {
			file.Close()
			c.FileFromFS(cleanPath, staticFS)
			return
		}

		// SPA fallback：返回 index.html 内容
		serveIndex(c)
	})
}
