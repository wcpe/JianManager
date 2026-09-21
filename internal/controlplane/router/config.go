package router

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

type ConfigHandler struct {
	svc   *service.ConfigService
	authz *service.AuthzService
	// source 配置源明面化服务（FR-451）；nil 时 surface 端点不注册、写入不做冲突提示。
	source *service.ConfigSourceService
	// audit 审计服务；nil 时不写配置源变更审计。
	audit *service.AuditService
}

func NewConfigHandler(svc *service.ConfigService, authz *service.AuthzService) *ConfigHandler {
	return &ConfigHandler{svc: svc, authz: authz}
}

// SetConfigSource 注入配置源服务（FR-451），启用 surface 端点与写入冲突提示。
func (h *ConfigHandler) SetConfigSource(src *service.ConfigSourceService) {
	h.source = src
}

// SetAudit 注入审计服务，启用配置源变更审计。
func (h *ConfigHandler) SetAudit(a *service.AuditService) {
	h.audit = a
}

func (h *ConfigHandler) List(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	files, err := h.svc.List(id, c.DefaultQuery("path", ""))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, files)
}

// Discover 递归发现实例工作目录下全部配置文件（FR-071）。
func (h *ConfigHandler) Discover(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	files, truncated, err := h.svc.Discover(id)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": files, "truncated": truncated})
}

func (h *ConfigHandler) Read(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path 参数"})
		return
	}
	res, err := h.svc.Read(id, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

type configWriteRequest struct {
	Path    string `json:"path" binding:"required"`
	Content string `json:"content" binding:"required"`
	Message string `json:"message"`
}

func (h *ConfigHandler) Write(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
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
	var req configWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	versionID, validation, err := h.svc.Write(id, req.Path, req.Content, req.Message, authorID, nil)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error(), "validation": validation})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versionId": versionID, "validation": validation, "warnings": h.conflictWarnings(id, req.Path, service.SurfacePropKeys())})
}

type configWriteFieldsRequest struct {
	Path    string            `json:"path" binding:"required"`
	Fields  map[string]string `json:"fields" binding:"required"`
	Message string            `json:"message"`
}

// WriteFields 表单模式保存：字段级补丁回原文（保留注释），生成新版本。
func (h *ConfigHandler) WriteFields(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
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
	var req configWriteFieldsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	versionID, validation, err := h.svc.WriteFields(id, req.Path, req.Fields, req.Message, authorID)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error(), "validation": validation})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versionId": versionID, "validation": validation, "warnings": h.conflictWarnings(id, req.Path, mapKeys(req.Fields))})
}

// conflictWarnings 返回命中受管 file 项的写入提示（不静默覆盖，FR-451 §2.2）；未注入源服务时为空。
func (h *ConfigHandler) conflictWarnings(instanceID uint, filePath string, keys []string) []map[string]any {
	if h.source == nil {
		return []map[string]any{}
	}
	return h.source.ConflictWarnings(instanceID, filePath, keys)
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (h *ConfigHandler) Versions(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	versions, err := h.svc.Versions(id, strings.TrimPrefix(c.Param("file"), "/"))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, versions)
}

type rollbackRequest struct {
	VersionID uint   `json:"versionId" binding:"required"`
	Message   string `json:"message"`
}

func (h *ConfigHandler) Rollback(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
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
	var req rollbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	versionID, err := h.svc.Rollback(id, strings.TrimPrefix(c.Param("file"), "/"), req.VersionID, req.Message, authorID)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versionId": versionID})
}

func (h *ConfigHandler) RegisterRoutes(rg *gin.RouterGroup) {
	cfg := rg.Group("/instances/:id/configs")
	cfg.GET("", h.List)
	// discover 为固定段，须在通配 *file 路由之前注册（gin 同前缀下静态优先，明确置前更稳）。
	cfg.GET("/discover", h.Discover)
	cfg.GET("/read", h.Read)
	cfg.POST("/write", h.Write)
	cfg.POST("/write-fields", h.WriteFields)
	cfg.POST("/cross-check", h.CrossCheck)
	// 配置源明面化（FR-451）：受管项清单读 + 按项来源更新。
	if h.source != nil {
		cfg.GET("/surface", h.Surface)
		cfg.PUT("/surface", h.UpdateSurface)
	}
	// *file 通配符必须在路径末尾；文件路径可能含子目录（如 plugins/Foo/config.yml）。
	cfg.GET("/versions/*file", h.Versions)
	cfg.POST("/rollback/*file", h.Rollback)
	cfg.GET("/diff/*file", h.Diff)
}

// Surface 返回受管配置项清单（FR-451）。门禁 file.read/instance.read。
func (h *ConfigHandler) Surface(c *gin.Context) {
	if h.source == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "配置源端点未启用"})
		return
	}
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	items, err := h.source.Surface(id)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type surfaceUpdateRequest struct {
	Items []service.SurfaceUpdateItem `json:"items" binding:"required"`
}

// UpdateSurface 按项更新配置源（FR-451）：二态互斥、内联写回真源、文件引用仅登记 + 生效值预览。
// 门禁 file.write/instance.write。切换来源与内联写入均写审计。
func (h *ConfigHandler) UpdateSurface(c *gin.Context) {
	if h.source == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "配置源端点未启用"})
		return
	}
	if !requireNodes(c, "file.write", "instance.write") {
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
	var req surfaceUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	items, err := h.source.UpdateSource(id, req.Items, authorID)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	h.writeAudit(c, authorID, id, req.Items)
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// writeAudit 记录配置源变更审计（切换来源/内联写入），失败不阻断主流程。
func (h *ConfigHandler) writeAudit(c *gin.Context, authorID, instanceID uint, items []service.SurfaceUpdateItem) {
	if h.audit == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, it := range items {
		keys = append(keys, string(it.Source)+":"+it.ItemKey)
	}
	detail := strings.Join(keys, ",")
	h.audit.RecordSafe(authorID, "config_surface_update", "instance", strconv.FormatUint(uint64(instanceID), 10), detail, c.ClientIP())
}

// Diff 返回 fromID -> toID 的差异。
// toID=0 表示与当前文件内容对比。
func (h *ConfigHandler) Diff(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	fromRaw := c.Query("from")
	if fromRaw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 from 版本 ID"})
		return
	}
	fromID, err := strconv.ParseUint(fromRaw, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 from 版本 ID"})
		return
	}
	var toID uint64
	if raw := c.Query("to"); raw != "" {
		t, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 to 版本 ID"})
			return
		}
		toID = t
	}
	res, err := h.svc.Diff(id, strings.TrimPrefix(c.Param("file"), "/"), uint(fromID), uint(toID))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// CrossCheck 对提交内容做跨实例一致性校验，返回 warning 列表（不影响写入结果）。
type crossCheckRequest struct {
	Path    string `json:"path" binding:"required"`
	Content string `json:"content" binding:"required"`
}

func (h *ConfigHandler) CrossCheck(c *gin.Context) {
	// 只读校验：与配置读路径同门禁（file.read / instance.read）
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req crossCheckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	issues, err := h.svc.CheckCrossFile(id, req.Path, req.Content)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"issues": issues})
}
