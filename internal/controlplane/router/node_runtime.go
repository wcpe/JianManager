package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// NodeRuntimeHandler 节点运行时管理路由（FR-178）：制品缓存、JDK 版本目录（foojay）、目录浏览。
// 全部仅平台管理员可达；缓存的破坏性操作（清/逐项清/设上限）写审计。
type NodeRuntimeHandler struct {
	svc   *service.NodeRuntimeService
	audit *service.AuditService
}

// NewNodeRuntimeHandler 创建节点运行时路由处理器。audit 可为 nil（审计随之关闭）。
func NewNodeRuntimeHandler(svc *service.NodeRuntimeService, audit *service.AuditService) *NodeRuntimeHandler {
	return &NodeRuntimeHandler{svc: svc, audit: audit}
}

// writeRuntimeErr 把服务错误映射为 HTTP：节点不存在→404、节点离线→503、其它→502。
func (h *NodeRuntimeHandler) writeRuntimeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
	case errors.Is(err, service.ErrNodeOffline):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": "节点未连接"})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_ERROR", "message": err.Error()})
	}
}

// recordAudit 记录节点运行时危险操作审计；audit 未注入时静默跳过。
func (h *NodeRuntimeHandler) recordAudit(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(userID, action, "node", targetID, string(raw), c.ClientIP())
}

// ListArtifactCache GET /nodes/:id/artifact-cache — 列出节点制品缓存 + 总占用 + 上限（只读）。
func (h *NodeRuntimeHandler) ListArtifactCache(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	view, err := h.svc.ListArtifactCache(nodeID)
	if err != nil {
		h.writeRuntimeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// EvictArtifactCache DELETE /nodes/:id/artifact-cache/:sha256 — 逐项清除（破坏性，审计）。
func (h *NodeRuntimeHandler) EvictArtifactCache(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	sha := c.Param("sha256")
	if err := h.svc.EvictArtifactCache(nodeID, sha); err != nil {
		h.writeRuntimeErr(c, err)
		return
	}
	h.recordAudit(c, "node.artifact_cache.evict", c.Param("id"), map[string]any{"nodeId": nodeID, "sha256": sha})
	c.JSON(http.StatusOK, gin.H{"message": "已清除"})
}

// ClearArtifactCache POST /nodes/:id/artifact-cache/clear — 清空全部（破坏性，审计）。
func (h *NodeRuntimeHandler) ClearArtifactCache(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	removed, err := h.svc.ClearArtifactCache(nodeID)
	if err != nil {
		h.writeRuntimeErr(c, err)
		return
	}
	h.recordAudit(c, "node.artifact_cache.clear", c.Param("id"), map[string]any{"nodeId": nodeID, "removed": removed})
	c.JSON(http.StatusOK, gin.H{"removed": removed})
}

type setCapRequest struct {
	// CapBytes 容量上限（字节，0=不限）。
	CapBytes int64 `json:"capBytes"`
}

// SetArtifactCacheCap PUT /nodes/:id/artifact-cache/cap — 设容量上限（破坏性，可能触发 LRU 淘汰，审计）。
func (h *NodeRuntimeHandler) SetArtifactCacheCap(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var req setCapRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if req.CapBytes < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "上限不得为负"})
		return
	}
	view, err := h.svc.SetArtifactCacheCap(nodeID, req.CapBytes)
	if err != nil {
		h.writeRuntimeErr(c, err)
		return
	}
	h.recordAudit(c, "node.artifact_cache.set_cap", c.Param("id"), map[string]any{"nodeId": nodeID, "capBytes": req.CapBytes})
	c.JSON(http.StatusOK, view)
}

// JDKCatalog GET /nodes/:id/jdk/catalog?vendor=&major= — 经 foojay 查可选 JDK 版本（只读）。
func (h *NodeRuntimeHandler) JDKCatalog(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	vendor := c.Query("vendor")
	if vendor == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 vendor"})
		return
	}
	major := 0
	if v := c.Query("major"); v != "" {
		major, _ = strconv.Atoi(v)
	}
	pkgs, err := h.svc.JDKCatalog(nodeID, vendor, major, c.Query("arch"))
	if err != nil {
		h.writeRuntimeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, pkgs)
}

// BrowseDir GET /nodes/:id/browse?path= — 只读列出节点某绝对路径下的子目录（目录选择器）。
func (h *NodeRuntimeHandler) BrowseDir(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	view, err := h.svc.BrowseDir(nodeID, c.Query("path"))
	if err != nil {
		h.writeRuntimeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// RegisterRoutes 注册节点运行时路由（FR-178）。
func (h *NodeRuntimeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	ac := rg.Group("/nodes/:id/artifact-cache")
	ac.GET("", h.ListArtifactCache)
	ac.POST("/clear", h.ClearArtifactCache)
	ac.PUT("/cap", h.SetArtifactCacheCap)
	ac.DELETE("/:sha256", h.EvictArtifactCache)

	rg.GET("/nodes/:id/jdk/catalog", h.JDKCatalog)
	rg.GET("/nodes/:id/browse", h.BrowseDir)
}
