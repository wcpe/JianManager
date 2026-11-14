package router

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AlertHandler 告警路由处理器。
type AlertHandler struct {
	alertSvc   *service.AlertService
	channelSvc *service.AlertChannelService
	// qqDiscovery QQ 群发现服务；nil 时三端点返回 503（等价「未部署该能力」）。
	qqDiscovery *service.QQDiscoveryService
	// qqBind QQ 扫码绑定服务（增补）；nil 时两绑定端点返回 503。
	qqBind *service.QQBindService
}

func NewAlertHandler(alertSvc *service.AlertService, channelSvc *service.AlertChannelService) *AlertHandler {
	return &AlertHandler{alertSvc: alertSvc, channelSvc: channelSvc}
}

// SetQQDiscovery 注入 QQ 群发现服务。测试路由直接构造 handler 时可跳过注入。
func (h *AlertHandler) SetQQDiscovery(svc *service.QQDiscoveryService) {
	h.qqDiscovery = svc
}

// SetQQBind 注入 QQ 扫码绑定服务（增补）。未注入时两绑定端点返回 503。
func (h *AlertHandler) SetQQBind(svc *service.QQBindService) {
	h.qqBind = svc
}

// ── 告警规则 ──

func (h *AlertHandler) ListRules(c *gin.Context) {
	rules, err := h.alertSvc.ListRules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, rules)
}

func (h *AlertHandler) CreateRule(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	var req service.CreateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	rule, err := h.alertSvc.CreateRule(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, rule)
}

func (h *AlertHandler) UpdateRule(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req service.UpdateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	rule, err := h.alertSvc.UpdateRule(id, req)
	if err != nil {
		if errors.Is(err, service.ErrAlertRuleNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rule)
}

func (h *AlertHandler) DeleteRule(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.alertSvc.DeleteRule(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ── 告警事件 ──

func (h *AlertHandler) ListEvents(c *gin.Context) {
	f := service.EventFilter{}
	// 严格解析：非法值必须 400。此前 `resolved=oops` 会被静默当成 false，
	// 调用方得到的是一份「合法但含义相反」的列表（例如把非法值当成未解决）。
	if v := c.Query("ruleId"); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil || id == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 ruleId"})
			return
		}
		u := uint(id)
		f.RuleID = &u
	}
	for name, dst := range map[string]**bool{"resolved": &f.Resolved, "acknowledged": &f.Acknowledged} {
		v := c.Query(name)
		if v == "" {
			continue
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 " + name})
			return
		}
		*dst = &b
	}
	f.Level = c.Query("level")
	f.TriggerType = c.Query("triggerType")
	f.Keyword = c.Query("keyword")
	if v := c.Query("from"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 from（需 RFC3339）"})
			return
		}
		f.From = &ts
	}
	if v := c.Query("to"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 to（需 RFC3339）"})
			return
		}
		f.To = &ts
	}
	if v := c.Query("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 page"})
			return
		}
		f.Page = n
	}
	if v := c.Query("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 pageSize"})
			return
		}
		f.PageSize = n
	}
	events, total, err := h.alertSvc.ListEvents(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": events, "total": total})
}

// AcknowledgeEvent 确认/认领一条告警事件。
//
// 必须额外要求 alert.manage：路由组允许 alert.read|alert.manage，但确认会写入
// 全局可变的 acknowledged/acknowledged_by/read 字段——只读角色不应改变其他
// 运维看到的责任人与未读状态。标记已读（MarkEventsRead）仍允许 alert.read。
func (h *AlertHandler) AcknowledgeEvent(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	event, err := h.alertSvc.Acknowledge(id, userID)
	if err != nil {
		if errors.Is(err, service.ErrAlertEventNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, event)
}

// MarkEventsRead 标记一条（:id）或全部（无 :id）告警事件为已读。
func (h *AlertHandler) MarkEventsRead(c *gin.Context) {
	var eventID uint
	if raw := c.Param("id"); raw != "" {
		// 非数字 ID 必须拒绝：此前解析失败会保留 eventID=0，而 0 的语义是
		// 「标记全部未读为已读」——一个拼错的 URL 就能清空整个平台的未读状态。
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的告警事件 ID"})
			return
		}
		eventID = uint(parsed)
	}
	if err := h.alertSvc.MarkRead(eventID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已标记已读"})
}

// UnreadCount 返回未读告警数（站内角标）。
func (h *AlertHandler) UnreadCount(c *gin.Context) {
	n, err := h.alertSvc.UnreadCount()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread": n})
}

// ── 通知通道──

func (h *AlertHandler) ListChannels(c *gin.Context) {
	channels, err := h.channelSvc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, channels)
}

func (h *AlertHandler) CreateChannel(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	var req service.ChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	ch, err := h.channelSvc.Create(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, ch)
}

func (h *AlertHandler) UpdateChannel(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req service.ChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	ch, err := h.channelSvc.Update(id, req)
	if err != nil {
		if errors.Is(err, service.ErrAlertChannelNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ch)
}

func (h *AlertHandler) DeleteChannel(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.channelSvc.Delete(id); err != nil {
		if errors.Is(err, service.ErrAlertChannelNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		if errors.Is(err, service.ErrAlertChannelInUse) {
			c.JSON(http.StatusConflict, gin.H{"error": "CHANNEL_IN_USE", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// TestChannel 向通道发送测试通知。
func (h *AlertHandler) TestChannel(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.channelSvc.TestSend(id); err != nil {
		if errors.Is(err, service.ErrAlertChannelNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "TEST_SEND_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "测试通知已发送"})
}

func (h *AlertHandler) RegisterRoutes(rg *gin.RouterGroup) {
	alerts := rg.Group("/alerts")
	{
		alerts.GET("/rules", h.ListRules)
		alerts.POST("/rules", h.CreateRule)
		alerts.PUT("/rules/:id", h.UpdateRule)
		alerts.DELETE("/rules/:id", h.DeleteRule)

		alerts.GET("/events", h.ListEvents)
		alerts.GET("/events/unread-count", h.UnreadCount)
		alerts.POST("/events/:id/ack", h.AcknowledgeEvent)
		alerts.POST("/events/:id/read", h.MarkEventsRead)
		alerts.POST("/events/read-all", h.markAllRead)

		alerts.GET("/channels", h.ListChannels)
		alerts.POST("/channels", h.CreateChannel)
		alerts.PUT("/channels/:id", h.UpdateChannel)
		alerts.DELETE("/channels/:id", h.DeleteChannel)
		alerts.POST("/channels/:id/test", h.TestChannel)

		// QQ 扫码接入与已发现群：与通道 CRUD 同权限（alert.manage）。
		alerts.POST("/qq/share-link", h.QQShareLink)
		alerts.GET("/qq/groups", h.QQGroups)
		alerts.GET("/qq/gateway/status", h.QQGatewayStatus)

		// QQ 机器人扫码绑定（增补）：生成绑定二维码 / 轮询绑定结果。
		alerts.POST("/qq/bind-task", h.QQCreateBindTask)
		alerts.GET("/qq/bind-task/:taskId", h.QQBindTaskStatus)
	}
}

// ── QQ 扫码接入──

// QQShareLink 生成机器人分享链接（前端渲染为二维码）。响应只含 url，绝不含 appSecret。
func (h *AlertHandler) QQShareLink(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	if h.qqDiscovery == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NOT_CONFIGURED", "message": "QQ 群发现服务未启用"})
		return
	}
	var req service.ShareLinkRequest
	// body 可空（空 = 默认机器人）；显式传 appId 则指定机器人。
	_ = c.ShouldBindJSON(&req)
	url, err := h.qqDiscovery.GenerateShareLink(req.AppID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SHARE_LINK_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}

// QQGroups 已发现群列表（分页）。响应条目只含公开 openid 与时间，绝不含 appSecret。
func (h *AlertHandler) QQGroups(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	if h.qqDiscovery == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NOT_CONFIGURED", "message": "QQ 群发现服务未启用"})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	items, total, err := h.qqDiscovery.ListGroups(page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// QQGatewayStatus 网关连接状态。响应只含 appId/status/时间/错误，绝不含 appSecret。
func (h *AlertHandler) QQGatewayStatus(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	if h.qqDiscovery == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NOT_CONFIGURED", "message": "QQ 群发现服务未启用"})
		return
	}
	c.JSON(http.StatusOK, h.qqDiscovery.GatewayStatus())
}

// ── QQ 机器人扫码绑定（增补）──

// QQCreateBindTask 创建扫码绑定任务：返回 taskId 与二维码 URL（二维码由前端渲染）。
func (h *AlertHandler) QQCreateBindTask(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	if h.qqBind == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NOT_CONFIGURED", "message": "QQ 扫码绑定服务未启用"})
		return
	}
	taskID, qrURL, err := h.qqBind.CreateBindTask()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "BIND_TASK_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"taskId": taskID, "qrUrl": qrURL})
}

// QQBindTaskStatus 查询绑定任务状态（前端据此轮询）。
//
// 响应**绝不含 appSecret**：完成态只回 appId、userOpenid 与前端该填进表单的 ${ENV} 引用名。
// 密钥本身由服务端落盘到 <dataRoot>/etc，配置解析时经 resolveEnvRef 回落读取。
func (h *AlertHandler) QQBindTaskStatus(c *gin.Context) {
	if !requireNodes(c, "alert.manage") {
		return
	}
	if h.qqBind == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NOT_CONFIGURED", "message": "QQ 扫码绑定服务未启用"})
		return
	}
	taskID := strings.TrimSpace(c.Param("taskId"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "绑定任务 ID 不能为空"})
		return
	}
	// appSecret 故意丢弃：它只允许留在服务端（落盘 + 内存）。
	status, appID, _, userOpenID, err := h.qqBind.PollBindResult(taskID)
	if err != nil {
		// 本地密钥已被过期清理（上游已完成但解不开）：只能重扫，按 404 让前端提示刷新二维码。
		if errors.Is(err, service.ErrQQBindKeyExpired) {
			c.JSON(http.StatusNotFound, gin.H{"error": "BIND_TASK_EXPIRED", "message": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "BIND_TASK_FAILED", "message": err.Error()})
		return
	}
	resp := gin.H{"status": status}
	if status == service.QQBindStatusCompleted {
		resp["appId"] = appID
		resp["userOpenid"] = userOpenID
		if ref, refErr := service.QQSecretEnvRef(appID); refErr == nil {
			resp["secretEnv"] = ref
		}
	}
	c.JSON(http.StatusOK, resp)
}

// markAllRead 标记全部未读为已读（read-all 路由，避免与 :id/read 冲突单独成端点）。
func (h *AlertHandler) markAllRead(c *gin.Context) {
	if err := h.alertSvc.MarkRead(0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "全部已读"})
}
