package router

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ConfigDeliveryHandler 配置分批灰度与交付编排路由处理器。
//
// 路径前缀 `/config-deliveries` 与 `/config-files`（分层定义）、`/config-baselines`
// （模板收敛）刻意分开：三者描述的是同一份配置在不同阶段的三件事——定义、模板、变更，
// 混在同一前缀下会让「这个 id 是文件还是交付单」变得无法回答。
type ConfigDeliveryHandler struct {
	svc   *service.ConfigDeliveryService
	authz *service.AuthzService
	audit *service.AuditService
	// groups 用于把配置层的 group 作用域展开成成员实例（判定可见性）。
	// nil 时 group 层一律不可见（fail-closed）。
	groups *service.InstanceGroupService
}

// NewConfigDeliveryHandler 创建路由处理器。
func NewConfigDeliveryHandler(svc *service.ConfigDeliveryService, authz *service.AuthzService, audit *service.AuditService) *ConfigDeliveryHandler {
	return &ConfigDeliveryHandler{svc: svc, authz: authz, audit: audit}
}

// WithInstanceGroups 注入组织分组服务，启用 group 层作用域的可见性判定。
//
// 单独一个 setter 而不改构造签名：本仓库大量测试直接构造 handler，
// 改签名会牵连一大片无关用例；未注入时 group 层 fail-closed（安全方向）。
func (h *ConfigDeliveryHandler) WithInstanceGroups(g *service.InstanceGroupService) *ConfigDeliveryHandler {
	h.groups = g
	return h
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
	// 交付会向目标实例批量写盘：selector 必须按调用者可见实例收敛
	//（越权目标不得进入交付单）。
	//
	// 【还要校 targetLayerScopeId】selector 收敛的是**目标实例**，而交付单还会
	// 把「目标层」写到 ConfigLayerVersion。两者是独立的授权面：selector 只选自己
	// 的实例（完全合法）也能把 layer scope 指向别人的组织组，之后整单回滚会调
	// RollbackLayer 向那个层**追加新版本**——实测评测：foreign 组层 2 版 → 3 版。
	// 层作用域的可见性判定必须走与分层接口同一口径（requireScopeAccessible）。
	if !newConfigScopeGuard(c, h.authz).withGroups(h.groups).requireScopeAccessible(
		in.TargetLayerLevel, in.TargetLayerScopeID) {
		return
	}
	order, err := h.svc.CreateScoped(in, currentUserID(c), newConfigScopeGuard(c, h.authz).targetScope())
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
		return
	}
	var in service.ConfigDeliveryInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	// 更新后的目标也必须按调用者可见范围收窄：仅校旧目标不够——
	// 一个已经收窄到 A 的草稿，再 PUT 一次就能把 selector 换成不可见的 B、C。
	//
	// 同理，**新的 targetLayerScopeId** 也必须校：改单同样能把目标层改到别人的组。
	if !newConfigScopeGuard(c, h.authz).withGroups(h.groups).requireScopeAccessible(
		in.TargetLayerLevel, in.TargetLayerScopeID) {
		return
	}
	order, err := h.svc.UpdateScoped(id, in, currentUserID(c),
		newConfigScopeGuard(c, h.authz).targetScope())
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
		return
	}
	// 【整单回滚会写配置层】requireDeliveryOwned 只校**目标实例**，而整单回滚还会调
	// RollbackLayer 向 order.TargetLayerLevel:TargetLayerScopeID **追加一个版本**。
	// 层作用域与目标实例是两个独立授权面：selector 只选自己的实例（完全合法）
	// 也能把层指向别人的组织组——实测：foreign 组层 2 版 → 3 版。
	//
	// 这里校的是**已持久化**的层引用（不是请求体），旧交付单同样覆盖。
	if !h.requireOrderLayerAccessible(c, id) {
		return
	}
	var body struct {
		InstanceIDs []uint `json:"instanceIds"`
		Reason      string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	// 越权隔离：子集回滚逐台校验可访问性，任一越权整体拒绝（不做部分执行）。
	//
	// **fail-closed**：authz 未装配时不能跳过这段——那等于让任何持配置写权限的
	// 调用者对任意实例做子集回滚。装配缺失按拒绝处理。
	if h.authz == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	for _, instID := range body.InstanceIDs {
		if !canAccessInstance(c, h.authz, instID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
		return
	}
	// 归属守卫只能保证**已持久的目标**在范围内；而 Impact 会基于 selector 重解
	// 候选，selector 可能是 nodeIds/tags，能带出同类其他未授权实例。
	// 所以这里必须把可见范围一并下推给服务层，不能只校旧目标。
	impact, err := h.svc.ImpactScoped(id, newConfigScopeGuard(c, h.authz).targetScope())
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
	// 交付单归属守卫：目标实例必须全部在调用者可见范围内。
	// 不按 CreatedBy 判定——同组多人可协作，但绝不能跨组。
	if !newConfigScopeGuard(c, h.authz).withDelivery(h.svc).requireDeliveryOwned(id) {
		return
	}
	observe, err := h.svc.Observe(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, observe)
}

// requireOrderLayerAccessible 校验交付单**已持久化**的目标层作用域在调用者可见范围内。
//
// 【为何要独立成函数】除 Create/Update 外，还有多处操作会间接碰配置层：
// 整单回滚会调 RollbackLayer 追加版本；旧交付单（或他人建的单）可能带着
// 建单时未校验的层引用。每次都在 handler 里重拔一遍既重复又容易漏，故集中在此。
//
// 读的是**库里的订单**，不是请求体——旧交付单也因此被覆盖。
// 订单不存在时返回 true（交给后续业务层报 404，不在这里混淆错误语义）。
func (h *ConfigDeliveryHandler) requireOrderLayerAccessible(c *gin.Context, orderID uint) bool {
	if h.svc == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "配置交付服务未就绪"})
		return false
	}
	order, err := h.svc.Get(orderID)
	if err != nil {
		// 不存在/查询失败：交给后续业务层报错，保持原有 404 语义。
		return true
	}
	if order == nil {
		return true
	}
	return newConfigScopeGuard(c, h.authz).withGroups(h.groups).requireScopeAccessible(
		order.TargetLayerLevel, order.TargetLayerScopeID)
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
