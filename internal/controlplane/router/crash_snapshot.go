package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// CrashSnapshotHandler 实例崩溃快照路由处理器（FR-313）。
// 崩溃现场由 Worker 经 gRPC 上报入库（滚动保留最近 5 条），此处只读供前端「崩溃诊断」卡。
type CrashSnapshotHandler struct {
	svc   *service.CrashSnapshotService
	authz *service.AuthzService
}

// NewCrashSnapshotHandler 创建崩溃快照路由处理器。
func NewCrashSnapshotHandler(svc *service.CrashSnapshotService, authz *service.AuthzService) *CrashSnapshotHandler {
	return &CrashSnapshotHandler{svc: svc, authz: authz}
}

// List GET /instances/:id/crash-snapshots — 实例崩溃快照列表（按发生时间倒序）。
// 权限 = 实例读权限（instance:read 且实例可访问）；不可访问按存在性隐藏返回 404。
func (h *CrashSnapshotHandler) List(c *gin.Context) {
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
	snaps, err := h.svc.ListByInstance(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询崩溃快照失败"})
		return
	}
	// 空结果回 []（非 null），前端空态直接消费。
	if snaps == nil {
		snaps = []model.InstanceCrashSnapshot{}
	}
	c.JSON(http.StatusOK, snaps)
}

// RegisterRoutes 注册崩溃快照路由（加性追加，与 /instances/:id/... 其余参数段共存）。
func (h *CrashSnapshotHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/:id/crash-snapshots", h.List)
}
