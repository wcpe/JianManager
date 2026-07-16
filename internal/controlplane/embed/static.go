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
		// index.html 必须禁缓存（FIX，真机：部署新版后浏览器启发式缓存旧 index，
		// 引用旧哈希 chunk——已修复的前端缺陷在用户端看起来「还在」）。
		// chunk 文件名带内容哈希，index 每次重取即可保证版本一致。
		c.Header("Cache-Control", "no-cache")
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
			// 构建产物文件名带内容哈希，可安全长缓存；嵌入 FS 无修改时间，
			// 不显式设置时浏览器按启发式缓存，行为不可控。
			if strings.HasPrefix(cleanPath, "assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			}
			c.FileFromFS(cleanPath, staticFS)
			return
		}

		// SPA fallback：返回 index.html 内容
		serveIndex(c)
	})
}
