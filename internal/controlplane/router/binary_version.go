package router

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BinaryVersionHandler 实例二进制/Beacon 版本管理路由处理器（FR-468）。
// 读走 instance.read（且实例可访问），升级/回滚走实例写权限（canManageInstance）。
type BinaryVersionHandler struct {
	svc   *service.BinaryVersionService
	authz *service.AuthzService
}

// NewBinaryVersionHandler 创建二进制版本路由处理器。
func NewBinaryVersionHandler(svc *service.BinaryVersionService, authz *service.AuthzService) *BinaryVersionHandler {
	return &BinaryVersionHandler{svc: svc, authz: authz}
}

// Get GET /instances/:id/binary-version — 当前版本 + 可升级候选 + 可回滚性 + 漂移提示。
func (h *BinaryVersionHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	view, err := h.svc.View(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询二进制版本失败"})
		return
	}
	c.JSON(http.StatusOK, view)
}

// binaryUpgradeRequest 升级请求体：目标制品 ID。
type binaryUpgradeRequest struct {
	AssetID uint `json:"assetId" binding:"required"`
}

// Upgrade POST /instances/:id/binary-upgrade — 受控升级到指定制品版本（异步任务）。
func (h *BinaryVersionHandler) Upgrade(c *gin.Context) {
	// B-2：升级会替换实例工作目录里的可执行文件，必须校验权限树节点——
	// group_viewer 只读角色同样满足 CanAccessGroup，仅凭 canManageInstance 会放行越权替换。
	if !requireNodes(c, "instance.write") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req binaryUpgradeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.AssetID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 assetId"})
		return
	}
	res, err := h.svc.Upgrade(c.Request.Context(), id, req.AssetID, binaryVersionActorID(c))
	if err != nil {
		writeBinaryVersionError(c, err, "升级失败")
		return
	}
	c.JSON(http.StatusOK, res)
}

// Rollback POST /instances/:id/binary-rollback — 回滚到上一版本（异步任务）。
func (h *BinaryVersionHandler) Rollback(c *gin.Context) {
	// B-2：回滚同样替换可执行文件，与升级同权（instance.write 节点）。
	if !requireNodes(c, "instance.write") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	res, err := h.svc.Rollback(c.Request.Context(), id, binaryVersionActorID(c))
	if err != nil {
		writeBinaryVersionError(c, err, "回滚失败")
		return
	}
	c.JSON(http.StatusOK, res)
}

// writeBinaryVersionError 把版本变更错误映射为 HTTP 状态：状态冲突 409、无回滚点 400、
// 实例不存在 404、其余 500。
//
// M-1（edge 报告）：500 分支只回固定文案，不把 err.Error() 拼进响应体——
// service 层错误含归档路径、worker 侧文件错误（formatPermError 会带 os 错误与路径）
// 等实现细节，直接回传等于把服务端文件系统结构与内部实现送到浏览器。
// 详情落服务端日志，响应只留可操作提示。
func writeBinaryVersionError(c *gin.Context, err error, fallbackMsg string) {
	switch {
	case errors.Is(err, service.ErrInstanceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
	case errors.Is(err, service.ErrBinaryUpgradeInstanceRunning):
		c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
	case errors.Is(err, service.ErrBinaryVersionOperationInFlight):
		c.JSON(http.StatusConflict, gin.H{"error": "OPERATION_IN_FLIGHT", "message": err.Error()})
	case errors.Is(err, service.ErrNoPreviousBinaryVersion):
		c.JSON(http.StatusBadRequest, gin.H{"error": "NO_PREVIOUS_VERSION", "message": err.Error()})
	case errors.Is(err, service.ErrBinaryVersionTargetInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_TARGET", "message": err.Error()})
	default:
		slog.Warn("二进制版本操作失败（未分类错误）", "fallback", fallbackMsg, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "INTERNAL_ERROR", "message": fallbackMsg + "，请查看任务中心或服务端日志"})
	}
}

// binaryVersionActorID 取当前请求的用户 ID（复用 getAccess，缺失时回 0）。
func binaryVersionActorID(c *gin.Context) uint {
	if access := getAccess(c); access != nil {
		return access.UserID
	}
	return 0
}

// RegisterRoutes 注册二进制版本管理路由（加性追加）。
func (h *BinaryVersionHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/:id/binary-version", h.Get)
	rg.POST("/instances/:id/binary-upgrade", h.Upgrade)
	rg.POST("/instances/:id/binary-rollback", h.Rollback)
}
