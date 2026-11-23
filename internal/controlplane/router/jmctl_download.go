package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
)

// registerJmctlDownloadRoutes 注册 jmctl 二进制的匿名下载端点（需求 1.3）。
//
// install-worker.sh / .ps1 装 Worker 时从这两个路由取 jmctl 注入 <安装目录>/bin/，
// Worker 自升级成功后也回这里拉同版本 jmctl。二进制本身不含机密，故匿名可读；
// 显式注册为真实路由（在 SPA NoRoute 之前），避免被 index.html 回退吞掉。
func registerJmctlDownloadRoutes(r *gin.Engine) {
	r.GET("/downloads/jmctl-linux-amd64", func(c *gin.Context) {
		serveJmctlAsset(c, "linux", "amd64")
	})
	r.GET("/downloads/jmctl-windows-amd64.exe", func(c *gin.Context) {
		serveJmctlAsset(c, "windows", "amd64")
	})
	// 清单端点：安装脚本/升级链路用它比对版本与 sha256，避免无谓重下。
	r.GET("/downloads/jmctl-manifest.json", func(c *gin.Context) {
		m := cpembed.EmbeddedJmctlManifest()
		if m == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":   "JMCTL_NOT_EMBEDDED",
				"message": "jmctl 未内嵌（构建缺 make embed-jmctl）",
			})
			return
		}
		c.JSON(http.StatusOK, m)
	})
}

// serveJmctlAsset 按平台返回内嵌 jmctl 字节；未内嵌时 503 明确报错，不回退 SPA。
func serveJmctlAsset(c *gin.Context, os, arch string) {
	m := cpembed.EmbeddedJmctlManifest()
	if m == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "JMCTL_NOT_EMBEDDED",
			"message": "jmctl 未内嵌（构建缺 make embed-jmctl）",
		})
		return
	}
	for _, a := range m.Assets {
		if a.OS == os && a.Arch == arch {
			body := cpembed.EmbeddedJmctlBinary(a)
			if len(body) == 0 {
				break
			}
			c.Header("X-Jmctl-Version", m.Version)
			c.Header("X-Jmctl-Sha256", a.SHA256)
			c.Data(http.StatusOK, "application/octet-stream", body)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error":   "JMCTL_PLATFORM_MISSING",
		"message": "内嵌清单中无该平台 jmctl: " + os + "/" + arch,
	})
}
