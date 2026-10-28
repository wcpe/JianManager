package router

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ConfigFileHandler 配置分层覆盖路由处理器（FR-529）。
//
// 路径前缀刻意与既有 `/instances/:id/configs` 分开：那里是「某实例某文件」的实例级语义，
// 这里是「一条配置在多个作用域上的分层定义」，两者混在同一前缀下会让「这个 path 是相对谁」
// 变得无法回答。
type ConfigFileHandler struct {
	svc   *service.ConfigFileService
	authz *service.AuthzService
	audit *service.AuditService
}

// NewConfigFileHandler 创建配置分层覆盖路由处理器。
func NewConfigFileHandler(svc *service.ConfigFileService, authz *service.AuthzService, audit *service.AuditService) *ConfigFileHandler {
	return &ConfigFileHandler{svc: svc, authz: authz, audit: audit}
}

// RegisterRoutes 注册 /config-files 全部端点。
func (h *ConfigFileHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/config-files")
	g.GET("", h.List)
	g.POST("", h.Create)
	g.GET("/:id", h.Get)
	g.PUT("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	// 层版本：history 是固定尾段，必须在通配之前不影响——这里无通配，顺序仅为可读性。
	g.GET("/:id/layers", h.ListLayers)
	g.POST("/:id/layers", h.SaveLayer)
	g.GET("/:id/layers/:scopeType/:scopeId/history", h.LayerHistory)
	g.POST("/:id/layers/:scopeType/:scopeId/rollback", h.RollbackLayer)
	g.DELETE("/:id/layers/:scopeType/:scopeId", h.DeleteLayer)
	g.GET("/:id/effective", h.Effective)
}

// List 分页列出配置文件。门禁 file.read / instance.read。
func (h *ConfigFileHandler) List(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	rows, total, err := h.svc.List(c.Query("q"), page, pageSize)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": rows, "total": total, "page": page, "pageSize": pageSize})
}

// Create 创建配置文件。门禁 file.write / instance.write。
func (h *ConfigFileHandler) Create(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	var in service.ConfigFileInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	row, err := h.svc.Create(in, authorID)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	// 审计只记元数据，绝不记 content（§8）。
	h.recordAudit(c, authorID, "config_file_create", row.ID, row.Name+" format="+string(row.Format))
	c.JSON(http.StatusOK, row)
}

// Get 读取配置文件详情。
func (h *ConfigFileHandler) Get(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	view, err := h.svc.Get(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Update 修改元数据（name/format 不可改）。
func (h *ConfigFileHandler) Update(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	// sensitivePaths 用指针区分「不传」与「传空数组清空」，故先解到 map 再取。
	var raw map[string]any
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	// description / schemaJson 用指针表达三态：字段缺席=不改，显式空串=清空。
	// （旧实现解成 string 后空串被当作「没传」，导致这两个字段永远清不掉。）
	var in service.ConfigFileUpdateInput
	if v, exists := raw["name"]; exists {
		in.Name, _ = v.(string)
	}
	if v, exists := raw["format"]; exists {
		in.Format, _ = v.(string)
	}
	if v, exists := raw["description"]; exists {
		s, _ := v.(string)
		in.Description = &s
	}
	if v, exists := raw["schemaJson"]; exists {
		s, _ := v.(string)
		in.SchemaJSON = &s
	}
	var sensitive *[]string
	if v, exists := raw["sensitivePaths"]; exists {
		paths := make([]string, 0)
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				if s, isStr := item.(string); isStr {
					paths = append(paths, s)
				}
			}
		}
		sensitive = &paths
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	row, err := h.svc.Update(id, in, sensitive)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, authorID, "config_file_update", row.ID, row.Name)
	c.JSON(http.StatusOK, row)
}

// Delete 软删配置文件。
func (h *ConfigFileHandler) Delete(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		h.respondErr(c, err)
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	h.recordAudit(c, authorID, "config_file_delete", id, "")
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// ListLayers 列出各作用域当前 head。
func (h *ConfigFileHandler) ListLayers(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	heads, err := h.svc.ListLayerHeads(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"layers": heads})
}

// LayerHistory 返回某作用域的版本历史。
func (h *ConfigFileHandler) LayerHistory(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	level, scopeID, ok := h.parseScope(c)
	if !ok {
		return
	}
	versions, err := h.svc.LayerHistory(id, level, scopeID)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"versions": versions})
}

// SaveLayer 保存某作用域的新版本（追加）。
func (h *ConfigFileHandler) SaveLayer(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	var in service.SaveLayerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	ver, err := h.svc.SaveLayer(id, in, authorID)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	// 审计只记「作用域 + 版本号 + hash」，不记 content（§8）。
	h.recordAudit(c, authorID, "config_layer_save", id,
		"scope="+string(ver.ScopeLevel)+":"+strconv.FormatUint(uint64(ver.ScopeRefID), 10)+
			" v"+strconv.Itoa(ver.VersionNo)+" hash="+ver.ContentHash)
	c.JSON(http.StatusOK, ver)
}

type layerRollbackRequest struct {
	VersionID uint   `json:"versionId" binding:"required"`
	Remark    string `json:"remark"`
}

// RollbackLayer 回退到指定版本（追加新版本，不改历史）。
func (h *ConfigFileHandler) RollbackLayer(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	level, scopeID, ok := h.parseScope(c)
	if !ok {
		return
	}
	var req layerRollbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	ver, err := h.svc.RollbackLayer(id, level, scopeID, req.VersionID, authorID, req.Remark)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, authorID, "config_layer_rollback", id,
		"scope="+string(ver.ScopeLevel)+":"+strconv.FormatUint(uint64(ver.ScopeRefID), 10)+
			" to=v"+strconv.Itoa(ver.VersionNo)+" from=#"+
			strconv.FormatUint(uint64(derefUint(ver.BasedOnVersionID)), 10))
	c.JSON(http.StatusOK, ver)
}

// DeleteLayer 删除某作用域的全部版本（撤销该层覆盖）。
func (h *ConfigFileHandler) DeleteLayer(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	level, scopeID, ok := h.parseScope(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteLayer(id, level, scopeID); err != nil {
		h.respondErr(c, err)
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	h.recordAudit(c, authorID, "config_layer_delete", id,
		"scope="+string(level)+":"+strconv.FormatUint(uint64(scopeID), 10))
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// Effective 返回某实例在该配置文件上的有效值预览（含逐键溯源）。
func (h *ConfigFileHandler) Effective(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	rawInstance := c.Query("instanceId")
	if rawInstance == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 instanceId 参数"})
		return
	}
	instanceID, err := strconv.ParseUint(rawInstance, 10, 64)
	if err != nil || instanceID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 instanceId"})
		return
	}
	// 越权隔离：非管理员只能预览其可访问实例的有效值，否则 404 隐藏存在性。
	if h.authz != nil && !canAccessInstance(c, h.authz, uint(instanceID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	preview, err := h.svc.Effective(id, uint(instanceID))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

// parseID 解析路径参数 :id。
func (h *ConfigFileHandler) parseID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的配置文件 ID"})
		return 0, false
	}
	return uint(id), true
}

// parseScope 解析路径参数 :scopeType / :scopeId。
func (h *ConfigFileHandler) parseScope(c *gin.Context) (model.ConfigScopeLevel, uint, bool) {
	level := model.ConfigScopeLevel(strings.ToLower(c.Param("scopeType")))
	if !model.ValidConfigScopeLevel(level) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "scopeType 只支持 group / instance"})
		return "", 0, false
	}
	scopeID, err := strconv.ParseUint(c.Param("scopeId"), 10, 64)
	if err != nil || scopeID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 scopeId"})
		return "", 0, false
	}
	return level, uint(scopeID), true
}

// recordAudit 写审计（失败不阻断主流程）。detail 由调用方拼装且绝不含 content。
//
// 参数顺序与全部调用点一致：action 在 id 之前。这与仓库内其它 handler 的
// recordAudit（runtime_library / node_proxy 等）同为「action 优先」，
// 原先定义写成 (id, action) 与本文件 6 处调用全部相反，导致该包无法编译。
func (h *ConfigFileHandler) recordAudit(c *gin.Context, authorID uint, action string, id uint, detail string) {
	if h.audit == nil {
		return
	}
	h.audit.RecordSafe(authorID, action, "config_file", strconv.FormatUint(uint64(id), 10), detail, c.ClientIP())
}

// respondErr 把服务层错误映射到稳定的 HTTP 状态与错误码。
func (h *ConfigFileHandler) respondErr(c *gin.Context, err error) {
	var validationErr *service.ConfigLayerValidationError
	if errors.As(err, &validationErr) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "CONFIG_INVALID", "message": validationErr.Error(), "issues": validationErr.Issues,
		})
		return
	}
	switch {
	case errors.Is(err, service.ErrConfigFileNotFound),
		errors.Is(err, service.ErrConfigLayerNotFound),
		errors.Is(err, service.ErrConfigVersionNotFound),
		errors.Is(err, service.ErrConfigInstanceNotInScope),
		errors.Is(err, service.ErrConfigScopeRefNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrConfigFileExists):
		c.JSON(http.StatusConflict, gin.H{"error": "ALREADY_EXISTS", "message": err.Error()})
	case errors.Is(err, service.ErrConfigFormatImmutable),
		errors.Is(err, service.ErrConfigNameImmutable),
		errors.Is(err, service.ErrConfigScopeInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

// derefUint 解引用 uint 指针；nil 返回 0（审计展示用）。
func derefUint(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}
