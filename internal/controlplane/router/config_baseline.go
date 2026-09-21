package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ConfigBaselineHandler 配置基线（模板）下发/漂移/收敛路由处理器（FR-458）。
type ConfigBaselineHandler struct {
	svc   *service.ConfigBaselineService
	authz *service.AuthzService
	audit *service.AuditService
}

// NewConfigBaselineHandler 创建配置基线路由处理器。
func NewConfigBaselineHandler(svc *service.ConfigBaselineService, authz *service.AuthzService, audit *service.AuditService) *ConfigBaselineHandler {
	return &ConfigBaselineHandler{svc: svc, authz: authz, audit: audit}
}

// List 列出全部基线。
func (h *ConfigBaselineHandler) List(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	scopeIDs, scoped, ok := h.scope(c)
	if !ok {
		return
	}
	rows, err := h.svc.ListBaselinesScoped(scopeIDs, scoped)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"baselines": rows})
}

// scope 解析调用者可访问实例集合（FR-458 越权修复）。平台管理员返回 (nil,false)（不收敛）。
// 出错时已写响应并返回 ok=false。
func (h *ConfigBaselineHandler) scope(c *gin.Context) ([]uint, bool, bool) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权限"})
		return nil, false, false
	}
	if h.authz == nil {
		// fail-closed（FR-458 越权修复）：授权服务不可用时拒绝，绝不退化为「不收敛」的平台管理员视图。
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限校验不可用"})
		return nil, false, false
	}
	scopeIDs, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return nil, false, false
	}
	return scopeIDs, scoped, true
}

// Create 创建/更新基线（FR-458）。非管理员仅能创建/覆盖 scope 落在其可访问实例集合内的基线，
// 防止以 scopeKey:"all" 覆盖平台基线（越权修复）。
func (h *ConfigBaselineHandler) Create(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	var in service.BaselineInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	scopeIDs, scoped, ok := h.scope(c)
	if !ok {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	row, err := h.svc.UpsertBaselineScoped(in, authorID, scopeIDs, scoped)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordSafe(authorID, "config_baseline_upsert", "config_baseline",
			strconv.FormatUint(uint64(row.ID), 10), row.ScopeKey+"@"+row.FilePath, c.ClientIP())
	}
	c.JSON(http.StatusOK, row)
}

// Get 读取单条基线。
func (h *ConfigBaselineHandler) Get(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	scopeIDs, scoped, ok := h.scope(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的基线 ID"})
		return
	}
	row, err := h.svc.GetBaselineScoped(uint(id), scopeIDs, scoped)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

// Delete 删除基线（FR-458）。先按 scope 可见性校验归属（同 GetBaselineScoped 的 404 隐藏语义），
// 越权删除以 NOT_FOUND 拒绝，防止非管理员删除任意基线。
func (h *ConfigBaselineHandler) Delete(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	scopeIDs, scoped, ok := h.scope(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的基线 ID"})
		return
	}
	// 归属校验：非管理员仅能删除 scope 全部落在其可访问集合内的基线；越权/不可见以 404 隐藏存在性。
	if err := h.svc.DeleteBaselineScoped(uint(id), scopeIDs, scoped); err != nil {
		h.respondErr(c, err)
		return
	}
	if h.audit != nil {
		uid, _ := c.Get(middleware.CtxUserID)
		authorID, _ := uid.(uint)
		h.audit.RecordSafe(authorID, "config_baseline_delete", "config_baseline", c.Param("id"), "", c.ClientIP())
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// Drift 检测 scope 内实例相对基线的漂移（只读）。
func (h *ConfigBaselineHandler) Drift(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	scopeIDs, scoped, ok := h.scope(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的基线 ID"})
		return
	}
	items, err := h.svc.DetectDriftScoped(uint(id), scopeIDs, scoped)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	drifted := 0
	for _, it := range items {
		if it.Drift {
			drifted++
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "drifted": drifted})
}

type convergeRequest struct {
	BatchSize int  `json:"batchSize"`
	FailFast  bool `json:"failFast"`
}

// Converge 一键收敛：把基线推送到所有漂移实例，并复核残余漂移。
func (h *ConfigBaselineHandler) Converge(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	scopeIDs, scoped, ok := h.scope(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的基线 ID"})
		return
	}
	var req convergeRequest
	// 允许空 body。
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
			return
		}
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	res, err := h.svc.ConvergeScoped(uint(id), service.ConvergeOptions{BatchSize: req.BatchSize, FailFast: req.FailFast}, authorID, scopeIDs, scoped)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordSafe(authorID, "config_baseline_converge", "config_baseline", c.Param("id"),
			"targeted="+strconv.Itoa(res.Targeted)+" succeeded="+strconv.Itoa(res.Succeeded)+" failed="+strconv.Itoa(res.Failed), c.ClientIP())
	}
	c.JSON(http.StatusOK, res)
}

func (h *ConfigBaselineHandler) respondErr(c *gin.Context, err error) {
	if errors.Is(err, service.ErrRollingOpNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "基线不存在"})
		return
	}
	if errors.Is(err, service.ErrBaselineScopeGroupNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "SCOPE_GROUP_NOT_FOUND", "message": err.Error()})
		return
	}
	if errors.Is(err, service.ErrBaselineScopeGroupEmpty) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "SCOPE_GROUP_EMPTY", "message": err.Error()})
		return
	}
	if errors.Is(err, service.ErrBaselineScopeForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": err.Error()})
		return
	}
	c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
}

// RegisterRoutes 注册配置基线路由。
func (h *ConfigBaselineHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/config-baselines")
	g.GET("", h.List)
	g.POST("", h.Create)
	g.GET("/:id", h.Get)
	g.DELETE("/:id", h.Delete)
	g.GET("/:id/drift", h.Drift)
	g.POST("/:id/converge", h.Converge)
}
