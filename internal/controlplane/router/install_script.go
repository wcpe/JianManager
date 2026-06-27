package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/embed"
)

// registerInstallScriptRoutes 注册 Worker 一键安装脚本的匿名静态端点（FR-080，见 ADR-020 §2）。
//
// 一键命令拼 `curl <cp>/install-worker.sh | sh` / `iwr <cp>/install-worker.ps1 | iex`，
// 这两个路径必须由 CP 在根（非 /api/v1）以 200 返回脚本内容，否则 curl/iwr 404、安装失败（BUG-B 根因）。
//
// 匿名可拉：脚本本身不含任何机密，准入凭据（enrollment token）在一键命令的参数里、不在脚本里；
// 故与平台管理员 JWT 入口（签发 token 的 /api/v1/nodes/enroll-token）鉴权与暴露面隔离。
// 显式注册为真实路由（而非靠 SPA NoRoute 回退），避免被前端 index.html 回退吞掉。
func registerInstallScriptRoutes(r *gin.Engine) {
	r.GET("/install-worker.sh", func(c *gin.Context) {
		serveInstallScript(c, embed.InstallWorkerScriptSh(), "text/x-shellscript; charset=utf-8")
	})
	r.GET("/install-worker.ps1", func(c *gin.Context) {
		serveInstallScript(c, embed.InstallWorkerScriptPs1(), "text/plain; charset=utf-8")
	})
}

// serveInstallScript 以指定 Content-Type 返回内嵌脚本字节；内嵌缺失（构建未注入）时 503 明确报错，
// 不静默回退到 SPA index.html（那样 curl|sh 会把 HTML 当脚本执行、错得更隐蔽）。
func serveInstallScript(c *gin.Context, body []byte, contentType string) {
	if len(body) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "INSTALL_SCRIPT_UNAVAILABLE",
			"message": "安装脚本未内嵌（构建缺 make embed-install-scripts）",
		})
		return
	}
	c.Data(http.StatusOK, contentType, body)
}
