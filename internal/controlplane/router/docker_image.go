package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// DockerImageHandler 暴露节点级 Docker 镜像管理（FR-078，见 ADR-019）。
// 所有操作经 service 委托目标节点 Worker；仅平台管理员可访问。
type DockerImageHandler struct{ svc *service.DockerImageService }

// NewDockerImageHandler 创建 Docker 镜像管理 handler。
func NewDockerImageHandler(svc *service.DockerImageService) *DockerImageHandler {
	return &DockerImageHandler{svc: svc}
}

// List GET /nodes/:id/docker/images —— 列出节点本机 Docker 镜像。
func (h *DockerImageHandler) List(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	images, err := h.svc.List(nodeID)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, images)
}

type pullImageRequest struct {
	Image string `json:"image" binding:"required"`
}

// Pull POST /nodes/:id/docker/images/pull —— 在节点拉取镜像。
func (h *DockerImageHandler) Pull(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var req pullImageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if err := h.svc.Pull(nodeID, req.Image); err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已拉取"})
}

type removeImageRequest struct {
	Image string `json:"image" binding:"required"`
	Force bool   `json:"force"`
}

// Remove POST /nodes/:id/docker/images/remove —— 在节点删除镜像。
// 用 POST 而非 DELETE：镜像引用含 / 和 :，作为路径参数会破坏路由匹配，故放请求体。
func (h *DockerImageHandler) Remove(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var req removeImageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if err := h.svc.Remove(nodeID, req.Image, req.Force); err != nil {
		h.writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// writeErr 把 service 错误映射为合适的 HTTP 状态码。
func (h *DockerImageHandler) writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNodeOffline):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": "节点未连接"})
	case errors.Is(err, service.ErrDockerUnavailable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "DOCKER_UNAVAILABLE", "message": "目标节点未安装或未运行 Docker"})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "DOCKER_OP_FAILED", "message": err.Error()})
	}
}

// RegisterRoutes 注册 Docker 镜像管理路由。
func (h *DockerImageHandler) RegisterRoutes(rg *gin.RouterGroup) {
	images := rg.Group("/nodes/:id/docker/images")
	images.GET("", h.List)
	images.POST("/pull", h.Pull)
	images.POST("/remove", h.Remove)
}
