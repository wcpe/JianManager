package router

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// RuntimeLibraryHandler 节点运行时库路由（FR-298）：统一 Runtime 视图（node_jdks +
// node_runtimes 读侧拼装）+ 扫描发现 + 泛化登记 + 删除。全部仅平台管理员可达；
// 扫描/登记/删除写审计（action 见 node.runtime.*，i18n 翻译随本 FR 进 audit.actions.*）。
type RuntimeLibraryHandler struct {
	svc   *service.RuntimeLibraryService
	audit *service.AuditService
}

// NewRuntimeLibraryHandler 创建节点运行时库路由处理器。audit 可为 nil（审计随之关闭）。
func NewRuntimeLibraryHandler(svc *service.RuntimeLibraryService, audit *service.AuditService) *RuntimeLibraryHandler {
	return &RuntimeLibraryHandler{svc: svc, audit: audit}
}

// writeRuntimeLibErr 把服务错误映射为 HTTP：节点不存在/记录不存在→404、节点离线→503、
// 未知类型/重复登记→422、JDK 占用→409、其它→502。
func (h *RuntimeLibraryHandler) writeRuntimeLibErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
	case errors.Is(err, service.ErrRuntimeNotFound), errors.Is(err, service.ErrJDKNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "运行时不存在"})
	case errors.Is(err, service.ErrNodeOffline):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": "节点未连接"})
	case errors.Is(err, service.ErrInvalidRuntimeType), errors.Is(err, service.ErrRuntimeDuplicated):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_ERROR", "message": err.Error()})
	}
}

// recordAudit 记录运行时库操作审计；audit 未注入时静默跳过。
func (h *RuntimeLibraryHandler) recordAudit(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(userID, action, "node", targetID, string(raw), c.ClientIP())
}

// List GET /nodes/:id/runtimes — 统一 Runtime 视图（含 syncFromWorker 容忍语义）。
func (h *RuntimeLibraryHandler) List(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	rows, err := h.svc.List(nodeID)
	if err != nil {
		h.writeRuntimeLibErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

type scanRuntimesRequest struct {
	// Types 过滤要扫描的类型（jdk/nodejs）；空 = 全部可扫描类型。
	Types []string `json:"types"`
}

// Scan POST /nodes/:id/runtimes/scan — 代理 Worker ScanRuntimes 回候选列表（节点离线 503）。
func (h *RuntimeLibraryHandler) Scan(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	// body 可选：空 body 视为扫描全部类型。
	var req scanRuntimesRequest
	if bindErr := c.ShouldBindJSON(&req); bindErr != nil && !errors.Is(bindErr, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	candidates, err := h.svc.Scan(nodeID, req.Types)
	if err != nil {
		h.writeRuntimeLibErr(c, err)
		return
	}
	h.recordAudit(c, "node.runtime.scan", c.Param("id"), map[string]any{
		"nodeId": nodeID, "types": req.Types, "found": len(candidates),
	})
	c.JSON(http.StatusOK, gin.H{"candidates": candidates})
}

// Register POST /nodes/:id/runtimes — 泛化登记（type=jdk 转发现有 JDK 链路，其它落 node_runtimes）。
func (h *RuntimeLibraryHandler) Register(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var req service.RegisterRuntimeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	view, err := h.svc.Register(nodeID, req)
	if err != nil {
		h.writeRuntimeLibErr(c, err)
		return
	}
	h.recordAudit(c, "node.runtime.register", c.Param("id"), map[string]any{
		"nodeId": nodeID, "type": view.Type, "path": view.Path, "version": view.Version,
	})
	c.JSON(http.StatusCreated, view)
}

// Delete DELETE /nodes/:id/runtimes/:rid?type= — 删除（type=jdk 走现链路含占用守卫与
// 托管连文件语义；其它类型只删记录）。type 必带以定位承载表。
func (h *RuntimeLibraryHandler) Delete(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	runtimeID, err := parseUintParam(c, "rid")
	if err != nil {
		return
	}
	typ := c.Query("type")
	if typ == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 type 查询参数"})
		return
	}
	used, err := h.svc.Delete(nodeID, runtimeID, typ)
	if err != nil {
		if errors.Is(err, service.ErrJDKInUse) {
			c.JSON(http.StatusConflict, gin.H{"error": "JDK_IN_USE", "message": "JDK 正被实例占用", "instances": used})
			return
		}
		h.writeRuntimeLibErr(c, err)
		return
	}
	h.recordAudit(c, "node.runtime.delete", c.Param("id"), map[string]any{
		"nodeId": nodeID, "runtimeId": runtimeID, "type": typ,
	})
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// RegisterRoutes 注册节点运行时库路由（FR-298）。
func (h *RuntimeLibraryHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rt := rg.Group("/nodes/:id/runtimes")
	rt.GET("", h.List)
	rt.POST("", h.Register)
	rt.POST("/scan", h.Scan)
	rt.DELETE("/:rid", h.Delete)
}
