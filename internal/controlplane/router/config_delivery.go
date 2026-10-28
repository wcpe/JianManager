package router

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ConfigDeliveryHandler 配置分批灰度与交付编排路由处理器（FR-531 / FR-532）。
//
// 路径前缀 `/config-deliveries` 与 `/config-files`（分层定义）、`/config-baselines`
// （模板收敛）刻意分开：三者描述的是同一份配置在不同阶段的三件事——定义、模板、变更，
// 混在同一前缀下会让「这个 id 是文件还是交付单」变得无法回答。
type ConfigDeliveryHandler struct {
	svc   *service.ConfigDeliveryService
	authz *service.AuthzService
	audit *service.AuditService
}

// NewConfigDeliveryHandler 创建路由处理器。
func NewConfigDeliveryHandler(svc *service.ConfigDeliveryService, authz *service.AuthzService, audit *service.AuditService) *ConfigDeliveryHandler {
	return &ConfigDeliveryHandler{svc: svc, authz: authz, audit: audit}
}

// RegisterRoutes 注册 /config-deliveries 全部端点（spec §11）。
func (h *ConfigDeliveryHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/config-deliveries")
	g.GET("", h.List)
	g.POST("", h.Create)
	g.GET("/:id", h.Get)
	g.PUT("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	g.POST("/:id/submit", h.Submit)
	// approve / start 是 spec §11 之外的两个端点：§11 只给了 submit（提交审批），
	// 但状态机（§7）要求先到 approved 才能 rolling，二者之间必须有触发点。
	g.POST("/:id/approve", h.Approve)
	g.POST("/:id/start", h.Start)
	g.POST("/:id/pause", h.Pause)
	g.POST("/:id/resume", h.Resume)
	g.POST("/:id/cancel", h.Cancel)
	g.POST("/:id/rollback", h.Rollback)
	g.GET("/:id/targets", h.Targets)
	g.GET("/:id/impact", h.Impact)
	g.GET("/:id/observe", h.Observe)
}

// List 分页列出交付单。门禁 instance.read。
func (h *ConfigDeliveryHandler) List(c *gin.Context) {
	if !requireNodes(c, "instance.read") {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	rows, total, err := h.svc.List(c.Query("status"), c.Query("q"), page, pageSize)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"orders": rows, "total": total, "page": page, "pageSize": pageSize})
}

// Create 创建交付单（draft）。门禁 instance.write。
func (h *ConfigDeliveryHandler) Create(c *gin.Context) {
	if !requireNodes(c, "instance.write", "file.write") {
		return
	}
	var in service.ConfigDeliveryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	order, err := h.svc.Create(in, currentUserID(c))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "config_delivery.create", order.ID)
	c.JSON(http.StatusCreated, order)
}

// Get 读取交付单详情。
func (h *ConfigDeliveryHandler) Get(c *gin.Context) {
	if !requireNodes(c, "instance.read") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	order, err := h.svc.Get(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, order)
}

// Update 编辑交付单（仅 draft）。
func (h *ConfigDeliveryHandler) Update(c *gin.Context) {
	if !requireNodes(c, "instance.write", "file.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	var in service.ConfigDeliveryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	order, err := h.svc.Update(id, in, currentUserID(c))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "config_delivery.update", order.ID)
	c.JSON(http.StatusOK, order)
}

// Delete 删除交付单（仅 draft）。
func (h *ConfigDeliveryHandler) Delete(c *gin.Context) {
	if !requireNodes(c, "instance.write", "file.write") {
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
	h.recordAudit(c, "config_delivery.delete", id)
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// Submit 提交审批。
func (h *ConfigDeliveryHandler) Submit(c *gin.Context) {
	if !requireNodes(c, "instance.write", "file.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	order, err := h.svc.Submit(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "config_delivery.submit", id)
	c.JSON(http.StatusOK, order)
}

// Approve 批准（pending_approval → approved）。
func (h *ConfigDeliveryHandler) Approve(c *gin.Context) {
	if !requireNodes(c, "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	order, err := h.svc.Approve(id, currentUserID(c))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, order)
}

// Start 开始放量（异步）。
func (h *ConfigDeliveryHandler) Start(c *gin.Context) {
	if !requireNodes(c, "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	if err := h.svc.Start(id); err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "config_delivery.start", id)
	order, err := h.svc.Get(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, order)
}

// Pause 暂停。
func (h *ConfigDeliveryHandler) Pause(c *gin.Context) {
	if !requireNodes(c, "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	order, err := h.svc.Pause(id, strings.TrimSpace(c.Query("reason")))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "config_delivery.pause", id)
	c.JSON(http.StatusOK, order)
}

// Resume 继续（retry_failed / skip_failed）。
func (h *ConfigDeliveryHandler) Resume(c *gin.Context) {
	if !requireNodes(c, "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	mode := service.DeliveryResumeMode(strings.TrimSpace(c.DefaultQuery("mode", string(service.ResumeRetryFailed))))
	var body struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err == nil && strings.TrimSpace(body.Mode) != "" {
		mode = service.DeliveryResumeMode(strings.TrimSpace(body.Mode))
	}
	order, err := h.svc.Resume(id, mode)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "config_delivery.resume", id)
	c.JSON(http.StatusOK, order)
}

// Cancel 终止。
func (h *ConfigDeliveryHandler) Cancel(c *gin.Context) {
	if !requireNodes(c, "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = strings.TrimSpace(c.Query("reason"))
	}
	order, err := h.svc.Cancel(id, reason)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "config_delivery.cancel", id)
	c.JSON(http.StatusOK, order)
}

// Rollback 回滚（可带 instanceIds 子集）。
func (h *ConfigDeliveryHandler) Rollback(c *gin.Context) {
	if !requireNodes(c, "instance.write") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	var body struct {
		InstanceIDs []uint `json:"instanceIds"`
		Reason      string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	// 越权隔离：子集回滚逐台校验可访问性，任一越权整体拒绝（不做部分执行）。
	if h.authz != nil {
		for _, instID := range body.InstanceIDs {
			if !canAccessInstance(c, h.authz, instID) {
				c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
				return
			}
		}
	}
	result, err := h.svc.Rollback(id, service.RollbackOptions{
		InstanceIDs: body.InstanceIDs,
		Reason:      body.Reason,
	}, currentUserID(c))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// Targets 目标分页。
func (h *ConfigDeliveryHandler) Targets(c *gin.Context) {
	if !requireNodes(c, "instance.read") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	batch, _ := strconv.Atoi(c.DefaultQuery("batch", "0"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "50"))
	rows, total, err := h.svc.Targets(id, c.Query("status"), batch, page, pageSize)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"targets": rows, "total": total, "page": page, "pageSize": pageSize})
}

// Impact 影响预览。
func (h *ConfigDeliveryHandler) Impact(c *gin.Context) {
	if !requireNodes(c, "instance.read") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	impact, err := h.svc.Impact(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, impact)
}

// Observe 观察窗数据。
func (h *ConfigDeliveryHandler) Observe(c *gin.Context) {
	if !requireNodes(c, "instance.read") {
		return
	}
	id, ok := h.parseID(c)
	if !ok {
		return
	}
	observe, err := h.svc.Observe(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, observe)
}

// parseID 解析路径参数 :id。
func (h *ConfigDeliveryHandler) parseID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的交付单 ID"})
		return 0, false
	}
	return uint(id), true
}

// recordAudit 写审计（失败不阻断主流程）。
func (h *ConfigDeliveryHandler) recordAudit(c *gin.Context, action string, orderID uint) {
	if h.audit == nil {
		return
	}
	uid := currentUserID(c)
	h.audit.RecordSafe(uid, action, "config_delivery",
		strconv.FormatUint(uint64(orderID), 10), "", c.ClientIP())
}

// respondErr 把服务层错误映射到稳定的 HTTP 状态与错误码。
func (h *ConfigDeliveryHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrDeliveryNotFound),
		errors.Is(err, service.ErrDeliveryTargetNotFound),
		errors.Is(err, service.ErrConfigFileNotFound),
		errors.Is(err, service.ErrConfigLayerNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrDeliveryStateInvalid):
		c.JSON(http.StatusConflict, gin.H{"error": "STATE_CONFLICT", "message": err.Error()})
	case errors.Is(err, service.ErrDeliveryInvalid),
		errors.Is(err, service.ErrDeliveryEmptyTarget),
		errors.Is(err, service.ErrConfigScopeInvalid),
		errors.Is(err, service.ErrConfigScopeRefNotFound):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}
