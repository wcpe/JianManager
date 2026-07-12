package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ImportServerHandler 导入现有服务器（FR-302，见 ADR-XXXX）：探测 + 导入两端点。
// 注册在平台管理员路由组下；两操作均写审计（instance.import.inspect / instance.import）。
type ImportServerHandler struct {
	svc   *service.ImportServerService
	audit *service.AuditService
}

// NewImportServerHandler 创建导入处理器。
func NewImportServerHandler(svc *service.ImportServerService, audit *service.AuditService) *ImportServerHandler {
	return &ImportServerHandler{svc: svc, audit: audit}
}

// Inspect POST /instances/import/inspect —— 探测节点上某现成服务器目录（只读）。
func (h *ImportServerHandler) Inspect(c *gin.Context) {
	var req service.ImportInspectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	res, err := h.svc.Inspect(c.Request.Context(), req.NodeID, req.Path)
	if err != nil {
		h.mapError(c, err)
		return
	}
	h.recordAudit(c, "instance.import.inspect", map[string]any{"nodeId": req.NodeID, "path": req.Path})
	c.JSON(http.StatusOK, res)
}

// Import POST /instances/import —— 导入现成目录为受管实例（就地接管 / 搬迁托管区）。
func (h *ImportServerHandler) Import(c *gin.Context) {
	var req service.ImportServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	inst, err := h.svc.Import(c.Request.Context(), req)
	if err != nil {
		h.mapError(c, err)
		return
	}
	h.recordAudit(c, "instance.import", map[string]any{
		"nodeId": req.NodeID, "path": req.Path, "mode": req.Mode,
		"name": req.Name, "jarPath": req.JarPath, "registerJdkPaths": len(req.RegisterJdkPaths),
	})
	c.JSON(http.StatusCreated, inst)
}

// mapError 统一错误映射：节点缺失 404、离线 503、守卫/业务拒绝 422、其余 502（Worker 链路）。
func (h *ImportServerHandler) mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NODE_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrNodeOffline):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": "节点未连接"})
	case errors.Is(err, service.ErrImportRejected):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "IMPORT_REJECTED", "message": err.Error()})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "IMPORT_FAILED", "message": err.Error()})
	}
}

func (h *ImportServerHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(getUserID(c), action, "instance", "", string(raw), c.ClientIP())
}

// RegisterRoutes 注册导入端点（挂平台管理员组）。
func (h *ImportServerHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/instances/import/inspect", h.Inspect)
	rg.POST("/instances/import", h.Import)
}
