package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BotHandler Bot 路由处理器。
type BotHandler struct {
	botSvc *service.BotService
	authz  *service.AuthzService
}

// NewBotHandler 创建 Bot 路由处理器。
func NewBotHandler(botSvc *service.BotService, authz *service.AuthzService) *BotHandler {
	return &BotHandler{botSvc: botSvc, authz: authz}
}

// parseBotFilter 从查询参数构造 Bot 筛选条件（列表/摘要/批量共用）。
func parseBotFilter(c *gin.Context) service.BotFilter {
	var f service.BotFilter
	if v := c.Query("instanceId"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			u := uint(id)
			f.InstanceID = &u
		}
	}
	if v := c.Query("nodeId"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			u := uint(id)
			f.NodeID = &u
		}
	}
	if v := c.Query("status"); v != "" {
		s := model.BotStatus(v)
		f.Status = &s
	}
	if v := c.Query("behavior"); v != "" {
		b := v
		f.Behavior = &b
	}
	f.Keyword = c.Query("q")
	return f
}

// List Bot 列表，分页 + 多维筛选（FR-038）。
// 平台管理员返回全部；其余按其可访问实例集合在 SQL 层收敛。
func (h *BotHandler) List(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize"))
	query := service.BotListQuery{Filter: parseBotFilter(c), Page: page, PageSize: pageSize}

	res, err := h.botSvc.ListPaged(query, scopeIDs, scope)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Summary Bot 计数聚合（全局或按 groupBy 分组），不返回逐条 Bot（FR-038）。
func (h *BotHandler) Summary(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	groupBy := c.Query("groupBy")
	switch groupBy {
	case "", "instance", "node", "status", "behavior":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "不支持的分组维度"})
		return
	}

	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	summary, err := h.botSvc.Summary(parseBotFilter(c), groupBy, scopeIDs, scope)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, summary)
}

type batchRequest struct {
	Action   string               `json:"action"`
	IDs      []uint               `json:"ids"`
	Filter   *service.BotFilterIn `json:"filter"`
	Behavior string               `json:"behavior"`
	Target   string               `json:"target"`
}

// Batch 批量执行 set-behavior/start/stop/delete，经 gRPC 委托对应 Worker（FR-038）。
func (h *BotHandler) Batch(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req batchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	action := service.BotBatchAction(req.Action)
	switch action {
	case service.BotBatchSetBehavior, service.BotBatchStart, service.BotBatchStop, service.BotBatchDelete:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "不支持的批量动作"})
		return
	}
	if len(req.IDs) == 0 && req.Filter == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需指定 ids 或 filter"})
		return
	}
	if action == service.BotBatchSetBehavior && req.Behavior == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "set-behavior 需指定 behavior"})
		return
	}

	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	svcReq := service.BotBatchRequest{
		Action:   action,
		IDs:      req.IDs,
		Behavior: req.Behavior,
		Target:   req.Target,
	}
	if req.Filter != nil {
		f := req.Filter.ToFilter()
		svcReq.Filter = &f
	}

	res, err := h.botSvc.Batch(svcReq, scopeIDs, scope)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Get Bot 详情。
func (h *BotHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN"})
		return
	}
	ok, err := h.authz.CanAccessBot(access, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
		return
	}

	bot, err := h.botSvc.GetByID(id)
	if err != nil {
		if errors.Is(err, service.ErrBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, bot)
}

// Create 创建 Bot。非平台管理员只能为自己可访问实例创建。
func (h *BotHandler) Create(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req service.CreateBotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	if !access.IsPlatformAdmin {
		ok, err := h.authz.CanAccessInstance(access, req.InstanceID)
		if err != nil || !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权为该实例创建 Bot"})
			return
		}
	}

	bot, err := h.botSvc.Create(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建 Bot 失败"})
		return
	}
	c.JSON(http.StatusCreated, bot)
}

// Delete 删除 Bot。
func (h *BotHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN"})
		return
	}
	ok, err := h.authz.CanManageBot(access, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
		return
	}

	if err := h.botSvc.Delete(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

type updateBehaviorRequest struct {
	Behavior string `json:"behavior" binding:"required"`
}

// UpdateBehavior 切换 Bot 行为模式。
func (h *BotHandler) UpdateBehavior(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN"})
		return
	}
	ok, err := h.authz.CanManageBot(access, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
		return
	}

	var req updateBehaviorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	if err := h.botSvc.UpdateBehavior(id, req.Behavior); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

type sendBotCommandRequest struct {
	Command string `json:"command" binding:"required"`
}

// SendCommand 向 Bot 下发聊天/控制命令（FR-009）。
func (h *BotHandler) SendCommand(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN"})
		return
	}
	ok, err := h.authz.CanManageBot(access, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
		return
	}

	var req sendBotCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}

	if err := h.botSvc.SendCommand(id, req.Command); err != nil {
		if errors.Is(err, service.ErrBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "COMMAND_FAILED", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已发送"})
}

func (h *BotHandler) RegisterRoutes(rg *gin.RouterGroup) {
	bots := rg.Group("/bots")
	{
		bots.GET("", h.List)
		bots.GET("/summary", h.Summary)
		bots.POST("", h.Create)
		bots.POST("/batch", h.Batch)
		bots.GET("/:id", h.Get)
		bots.DELETE("/:id", h.Delete)
		bots.POST("/:id/behavior", h.UpdateBehavior)
		bots.POST("/:id/command", h.SendCommand)
	}
}
