package router

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// LogAcqSettingsHandler 日志采集配置路由（FR-503）。
//
//	GET/PUT  /settings/log-acquisition            平台默认（5 个键）
//	GET      /nodes/:id/settings/log-acquisition  该节点对照视图（默认/平台/生效/覆盖）
//	PUT      /nodes/:id/settings/log-acquisition  写节点级覆盖
//	DELETE   /nodes/:id/settings/log-acquisition  清除节点级覆盖（恢复继承平台默认）
//
// 权限：平台端点沿用平台配置口径；节点端点读沿用节点读、写仅平台管理员（同节点代理 FR-185）。
// 写入一律落审计；生效仍由心跳下发（≤1 周期），本层不直接触达 Worker。
type LogAcqSettingsHandler struct {
	settings *service.SettingsService
	node     *service.NodeService
	audit    *service.AuditService
}

// NewLogAcqSettingsHandler 创建日志采集配置路由处理器。audit 可为 nil（审计随之关闭）。
func NewLogAcqSettingsHandler(settings *service.SettingsService, node *service.NodeService, audit *service.AuditService) *LogAcqSettingsHandler {
	return &LogAcqSettingsHandler{settings: settings, node: node, audit: audit}
}

// updateLogAcqRequest 提交一批「平台键 → 值」（节点端点由服务端加 `@node:` 后缀）。
type updateLogAcqRequest struct {
	Values map[string]string `json:"values" binding:"required"`
}

// GetPlatform GET /settings/log-acquisition — 平台默认视图。
func (h *LogAcqSettingsHandler) GetPlatform(c *gin.Context) {
	view, err := h.settings.LogAcqSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询日志采集配置失败"})
		return
	}
	c.JSON(http.StatusOK, view)
}

// UpdatePlatform PUT /settings/log-acquisition — 写平台默认。
func (h *LogAcqSettingsHandler) UpdatePlatform(c *gin.Context) {
	if !requireNodes(c, "settings.write") {
		return
	}
	var req updateLogAcqRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	for key := range req.Values {
		if !isLogAcqPlatformKey(key) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR",
				"message": fmt.Sprintf("不支持的日志采集配置项: %s", key)})
			return
		}
	}
	if err := h.settings.Update(req.Values); err != nil {
		writeLogAcqSettingError(c, err, "保存日志采集配置失败")
		return
	}
	h.recordAudit(c, "settings.log_acq.update", "platform", logAcqAuditDetail(req.Values))

	view, err := h.settings.LogAcqSettings()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "已保存"})
		return
	}
	c.JSON(http.StatusOK, view)
}

// GetNode GET /nodes/:id/settings/log-acquisition — 该节点的对照视图。
func (h *LogAcqSettingsHandler) GetNode(c *gin.Context) {
	if !requireNodeRead(c) {
		return
	}
	_, nodeUUID, nodeName, ok := h.resolveNode(c)
	if !ok {
		return
	}
	view, err := h.settings.LogAcqSettingsForNode(nodeUUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询节点日志采集配置失败"})
		return
	}
	view.NodeName = nodeName
	c.JSON(http.StatusOK, view)
}

// UpdateNode PUT /nodes/:id/settings/log-acquisition — 写该节点的采集覆盖（仅平台管理员）。
func (h *LogAcqSettingsHandler) UpdateNode(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	_, nodeUUID, nodeName, ok := h.resolveNode(c)
	if !ok {
		return
	}
	var req updateLogAcqRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	// 只接受日志采集的 5 个平台键：本端点不承担「任意键的节点级覆盖」，
	// 范围外的键整体拒绝（避免被当作通用节点设置入口误用）。
	//
	// 后缀拼接与写入统一由 service.UpdateNodeScoped 负责（2026-10-20 审查修正）：
	// 通用端点 PUT /api/v1/settings 只要求 settings.write 且不写审计，若节点级键也走
	// 共享白名单，它就能绕过本端点的 requirePlatformAdmin 静默停掉任意节点的采集。
	// 现在 @node: 键只在此路径可写。
	if err := h.settings.UpdateNodeScoped(nodeUUID, req.Values); err != nil {
		writeLogAcqSettingError(c, err, "保存节点日志采集覆盖失败")
		return
	}
	h.recordAudit(c, "node.log_acq.override.set", c.Param("id"),
		mergeAuditDetail(logAcqAuditDetail(req.Values), map[string]any{"nodeId": nodeUUID}))

	view, err := h.settings.LogAcqSettingsForNode(nodeUUID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "已保存"})
		return
	}
	view.NodeName = nodeName
	c.JSON(http.StatusOK, view)
}

// DeleteNode DELETE /nodes/:id/settings/log-acquisition — 清除该节点覆盖（恢复继承平台默认）。
//
// 可选 query `key=<平台键>` 只清一项；缺省清全部 5 项。删除是幂等的。
func (h *LogAcqSettingsHandler) DeleteNode(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	_, nodeUUID, nodeName, ok := h.resolveNode(c)
	if !ok {
		return
	}
	keys := service.NodeLogAcqKeys(nodeUUID)
	if only := strings.TrimSpace(c.Query("key")); only != "" {
		if !isLogAcqPlatformKey(only) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR",
				"message": fmt.Sprintf("不支持的日志采集配置项: %s", only)})
			return
		}
		keys = []string{service.NodeScopedSettingKey(only, nodeUUID)}
	}
	if err := h.settings.DeleteOverrides(keys); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "清除节点日志采集覆盖失败"})
		return
	}
	h.recordAudit(c, "node.log_acq.override.clear", c.Param("id"),
		map[string]any{"nodeId": nodeUUID, "keys": keys})

	view, err := h.settings.LogAcqSettingsForNode(nodeUUID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "已恢复默认"})
		return
	}
	view.NodeName = nodeName
	c.JSON(http.StatusOK, view)
}

// resolveNode 解析 :id 为节点并返回 (id, UUID, name)；失败时已写响应，调用方直接 return。
func (h *LogAcqSettingsHandler) resolveNode(c *gin.Context) (uint, string, string, bool) {
	id, err := parseID(c)
	if err != nil {
		return 0, "", "", false
	}
	node, err := h.node.GetByID(id)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return 0, "", "", false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询节点失败"})
		return 0, "", "", false
	}
	return id, node.UUID, node.Name, true
}

// recordAudit 记录设置审计；audit 未注入时静默跳过。
//
// targetType 按**动作作用域**区分（2026-10-20 审查修正）：节点级动作（`node.log_acq.*`）
// 记 `node`，平台级记 `settings`。原先一律写 `settings`，于是按 `targetType=node` 查
// 「某节点做过哪些操作」时看不到节点级采集配置的变更/清除——而同一批的
// router/log_runtime.go 对节点级动作用的是 `node`，两处口径不一致。
func (h *LogAcqSettingsHandler) recordAudit(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	targetType := "settings"
	if strings.HasPrefix(action, "node.") {
		targetType = "node"
	}
	h.audit.RecordSafe(userID, action, targetType, targetID, marshalAuditDetail(detail), c.ClientIP())
}

// RegisterPlatformRoutes 注册平台默认端点（挂在平台配置权限组下）。
func (h *LogAcqSettingsHandler) RegisterPlatformRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/settings/log-acquisition")
	{
		g.GET("", h.GetPlatform)
		g.PUT("", h.UpdatePlatform)
	}
}

// RegisterNodeRoutes 注册节点级端点（挂在节点权限组下）。
func (h *LogAcqSettingsHandler) RegisterNodeRoutes(rg *gin.RouterGroup) {
	rg.GET("/nodes/:id/settings/log-acquisition", h.GetNode)
	rg.PUT("/nodes/:id/settings/log-acquisition", h.UpdateNode)
	rg.DELETE("/nodes/:id/settings/log-acquisition", h.DeleteNode)
}

// isLogAcqPlatformKey 报告键是否属于日志采集的 5 个平台键。
func isLogAcqPlatformKey(key string) bool {
	for _, k := range service.LogAcqPlatformKeys() {
		if k == key {
			return true
		}
	}
	return false
}

// logAcqAuditDetail 组装审计明细（值原样记录：这些项不含敏感信息，
// 与代理 URL 不同，记录值本身即是运维复盘所需）。
func logAcqAuditDetail(values map[string]string) map[string]any {
	detail := make(map[string]any, len(values))
	for k, v := range values {
		detail[k] = v
	}
	return detail
}

// mergeAuditDetail 合并两份审计明细（后者覆盖同键）。
func mergeAuditDetail(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// writeLogAcqSettingError 把设置写入错误映射为响应：可预期校验错误回 422，其余 500。
func writeLogAcqSettingError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, service.ErrSettingKeyNotWritable) || errors.Is(err, service.ErrSettingValueInvalid) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": fallback})
}
