package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
)

// ClientUpdaterJarsHandler 提供内嵌客户端 OTA 更新器 jar（wedge / updater-core）的版本查询与下载（FR-107）。
//
// 面向运营方「接入指引」：让运营方在后台一页拿到客户端更新器两件套。属管理/接入动作，
// 限平台管理员（JWT），与面向玩家的消费端点（拉取密钥鉴权）隔离——见 architecture-invariants。
type ClientUpdaterJarsHandler struct{}

// NewClientUpdaterJarsHandler 创建处理器（无依赖，jar 来自构建期内嵌）。
func NewClientUpdaterJarsHandler() *ClientUpdaterJarsHandler {
	return &ClientUpdaterJarsHandler{}
}

// Info GET /client-dist/updater-jars — 内嵌版本与各 jar 可用性（前端据此展示版本/禁用缺失下载）。
func (h *ClientUpdaterJarsHandler) Info(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	wedge := cpembed.WedgeJar()
	core := cpembed.UpdaterCoreJar()
	c.JSON(http.StatusOK, gin.H{
		"version": cpembed.ClientUpdaterEmbeddedVersion,
		"wedge":   gin.H{"available": len(wedge) > 0, "size": len(wedge)},
		"core":    gin.H{"available": len(core) > 0, "size": len(core)},
	})
}

// Download GET /client-dist/updater-jars/:component — 下载内嵌 jar（component ∈ wedge | core）。
func (h *ClientUpdaterJarsHandler) Download(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var data []byte
	var filename string
	switch c.Param("component") {
	case "wedge":
		data, filename = cpembed.WedgeJar(), "wedge.jar"
	case "core":
		data, filename = cpembed.UpdaterCoreJar(), "updater-core.jar"
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_COMPONENT",
			"message": "组件须为 wedge 或 core",
		})
		return
	}
	if len(data) == 0 {
		// 未经 `make embed-client-updater` 注入：友好提示而非裸 404。
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "JAR_NOT_EMBEDDED",
			"message": "更新器 jar 未内嵌（构建时需先 ./gradlew :wedge:jar :updater-core:jar 再 make embed-client-updater）",
		})
		return
	}
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Data(http.StatusOK, "application/java-archive", data)
}

// RegisterRoutes 注册更新器 jar 端点（须挂 JWT 平台管理员组）。
func (h *ClientUpdaterJarsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-dist/updater-jars", h.Info)
	rg.GET("/client-dist/updater-jars/:component", h.Download)
}
