package router

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// SnapshotHandler 实例整机快照路由处理器（FR-466）。
// 读走 instance:read（且实例可访问）；创建/回滚走实例写权限（canManageInstance）。
type SnapshotHandler struct {
	svc   *service.SnapshotService
	authz *service.AuthzService
	audit *service.AuditService
}

// NewSnapshotHandler 创建快照路由处理器。
func NewSnapshotHandler(svc *service.SnapshotService, authz *service.AuthzService, audit *service.AuditService) *SnapshotHandler {
	return &SnapshotHandler{svc: svc, authz: authz, audit: audit}
}

// List GET /instances/:id/snapshots — 快照列表（创建时间倒序）。
func (h *SnapshotHandler) List(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询快照失败"})
		return
	}
	if snaps == nil {
		snaps = []model.InstanceSnapshot{}
	}
	c.JSON(http.StatusOK, snaps)
}

// snapshotCreateRequest 创建快照请求体（name 可选）。
type snapshotCreateRequest struct {
	Name string `json:"name"`
}

// Create POST /instances/:id/snapshots — 创建整机快照（异步任务 + 阶段进度）。
func (h *SnapshotHandler) Create(c *gin.Context) {
	// B-2：写端点必须校验**权限树节点**，不能只看组归属——group_viewer 只读角色
	// 也满足 CanAccessGroup，仅凭 canManageInstance 会放行其覆盖实例工作目录的写操作。
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
	var req snapshotCreateRequest
	if c.Request.ContentLength > 0 {
		// 请求体可选：空体（无 name）也接受。
		_ = c.ShouldBindJSON(&req)
	}
	actor := binaryVersionActorID(c)
	snap, err := h.svc.Create(id, strings.TrimSpace(req.Name), service.SnapshotCreateOptions{
		Kind:        model.SnapshotKindManual,
		TriggeredBy: actor,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInstanceNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		case errors.Is(err, service.ErrSnapshotOperationInFlight):
			c.JSON(http.StatusConflict, gin.H{"error": "OPERATION_IN_FLIGHT", "message": err.Error()})
		case errors.Is(err, service.ErrSnapshotQuotaExceeded):
			c.JSON(http.StatusConflict, gin.H{"error": "SNAPSHOT_QUOTA_EXCEEDED", "message": err.Error()})
		default:
			// M-1（edge 报告）：不把 err.Error() 拼进响应体——service 层错误含归档路径、
			// Backup 行内容、worker 侧文件错误（formatPermError 会带上 os 错误与路径），
			// 直接回传等于把服务端文件系统绝对路径与内部结构送到浏览器。
			// 详情落服务端日志（按 snapshotId 可检索），响应只给固定文案。
			slog.Warn("创建快照失败（未分类错误）", "instanceId", id, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "INTERNAL_ERROR", "message": "创建快照失败，请查看任务中心或服务端日志"})
		}
		return
	}
	// 审计已在 service 层写入（edge m-3：HTTP 与 MCP 两条入口共用同一落点）。
	c.JSON(http.StatusAccepted, snap)
}

// Rollback POST /snapshots/:sid/rollback — 一键回滚到该快照（强制先建 pre_rollback 快照）。
func (h *SnapshotHandler) Rollback(c *gin.Context) {
	// B-2：回滚是覆盖实例工作目录的破坏性写操作，必须校验权限树节点。
	if !requireNodes(c, "instance.write") {
		return
	}
	sid, err := parseIDParam(c, "sid")
	if err != nil {
		return
	}
	target, err := h.svc.GetByID(sid)
	if err != nil {
		if errors.Is(err, service.ErrSnapshotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "快照不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询快照失败"})
		return
	}
	if !canManageInstance(c, h.authz, target.InstanceID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "快照不存在"})
		return
	}
	// m-1：破坏性回滚要求服务端确认要素——请求体必须带回快照名或 ID 一致确认，
	// 避免「点错按钮 / 脚本循环变量写错」直接覆盖生产数据。
	if err := requireSnapshotRollbackConfirm(c, target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CONFIRM_REQUIRED", "message": err.Error()})
		return
	}
	actor := binaryVersionActorID(c)
	res, err := h.svc.Rollback(c.Request.Context(), sid, actor)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSnapshotNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "快照不存在"})
		case errors.Is(err, service.ErrSnapshotNotRollable):
			c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
		case errors.Is(err, service.ErrSnapshotOperationInFlight):
			c.JSON(http.StatusConflict, gin.H{"error": "OPERATION_IN_FLIGHT", "message": err.Error()})
		case errors.Is(err, service.ErrInstanceRunning), errors.Is(err, service.ErrSnapshotInstanceRunning):
			c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
		default:
			// M-1（edge 报告）：同 Create——固定文案 + 详情入日志，杜绝内部错误外泄。
			slog.Warn("快照回滚失败（未分类错误）", "snapshotId", sid, "instanceId", target.InstanceID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "INTERNAL_ERROR", "message": "回滚失败，请查看任务中心或服务端日志"})
		}
		return
	}
	c.JSON(http.StatusAccepted, res)
}

// snapshotRollbackConfirm 破坏性回滚的服务端确认要素（m-1）。
//
// 接受两种任一即视为已确认（前端二次确认框回填其中一种即可，脚本化调用也需显式带上）：
//   - confirmSnapshotId 必须等于目标快照 ID；
//   - confirmName 必须等于目标快照名（非空时）。
type snapshotRollbackConfirm struct {
	ConfirmSnapshotID uint   `json:"confirmSnapshotId"`
	ConfirmName       string `json:"confirmName"`
}

// requireSnapshotRollbackConfirm 校验回滚确认要素；缺失或不匹配返回错误。
func requireSnapshotRollbackConfirm(c *gin.Context, target *model.InstanceSnapshot) error {
	var req snapshotRollbackConfirm
	if c.Request.ContentLength > 0 {
		_ = c.ShouldBindJSON(&req)
	}
	if req.ConfirmSnapshotID == 0 && strings.TrimSpace(req.ConfirmName) == "" {
		return fmt.Errorf("回滚为破坏性操作：请在请求体携带 confirmSnapshotId=%d 或 confirmName=%q 以确认目标快照",
			target.ID, target.Name)
	}
	if req.ConfirmSnapshotID != 0 && req.ConfirmSnapshotID != target.ID {
		return fmt.Errorf("确认要素不匹配：confirmSnapshotId=%d 与目标快照 #%d 不一致", req.ConfirmSnapshotID, target.ID)
	}
	if name := strings.TrimSpace(req.ConfirmName); name != "" && name != target.Name {
		return fmt.Errorf("确认要素不匹配：confirmName 与目标快照名不一致（目标为 %q）", target.Name)
	}
	return nil
}

// Delete DELETE /snapshots/:sid — 删除快照（含底层备份）。
func (h *SnapshotHandler) Delete(c *gin.Context) {
	// B-2：删除快照会一并删除底链归档，用 instance.delete 节点（与实例删除同口径）。
	if !requireNodes(c, "instance.delete") {
		return
	}
	sid, err := parseIDParam(c, "sid")
	if err != nil {
		return
	}
	target, gerr := h.svc.GetByID(sid)
	if gerr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "快照不存在"})
		return
	}
	if !canManageInstance(c, h.authz, target.InstanceID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "快照不存在"})
		return
	}
	actor := binaryVersionActorID(c)
	if err := h.svc.Delete(sid); err != nil {
		h.recordAudit(actor, "instance.snapshot_delete", target.InstanceID, "snapshot#"+itoaUint(sid), c.ClientIP(), false, err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "删除快照失败"})
		return
	}
	h.recordAudit(actor, "instance.snapshot_delete", target.InstanceID, "snapshot#"+itoaUint(sid), c.ClientIP(), true, "")
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// parseIDParam 解析任意具名路径参数为 uint（失败写 400，与 parseID 同口径）。
func parseIDParam(c *gin.Context, name string) (uint, error) {
	v, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 ID"})
		return 0, err
	}
	return uint(v), nil
}

// itoaUint uint → 十进制字符串（审计 target/detail 用）。
func itoaUint(v uint) string { return strconv.FormatUint(uint64(v), 10) }

// recordAudit 写快照审计（动作命名 instance.snapshot_*）。
func (h *SnapshotHandler) recordAudit(actor uint, action string, instanceID uint, detail, ip string, success bool, errMsg string) {
	if h.audit == nil {
		return
	}
	h.audit.RecordResultSafe(actor, action, "instance", itoaUint(instanceID), detail, ip, success, errMsg)
}

// RegisterRoutes 注册快照路由（加性追加；/snapshots/:sid 与 /instances/:id 同级共存）。
func (h *SnapshotHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/:id/snapshots", h.List)
	rg.POST("/instances/:id/snapshots", h.Create)
	rg.POST("/snapshots/:sid/rollback", h.Rollback)
	rg.DELETE("/snapshots/:sid", h.Delete)
}
