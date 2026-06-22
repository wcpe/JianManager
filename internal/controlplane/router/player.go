package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// PlayerHandler 玩家管理路由处理器（FR-054 + FR-066 实时玩家事件）。
type PlayerHandler struct {
	playerSvc *service.PlayerService
	eventSvc  *service.PlayerEventService
	authz     *service.AuthzService
	audit     *service.AuditService
}

// NewPlayerHandler 创建玩家管理路由处理器。
// eventSvc 提供探针经反向 WS 实时上报的玩家事件流与在线名册（FR-066）。
func NewPlayerHandler(playerSvc *service.PlayerService, eventSvc *service.PlayerEventService, authz *service.AuthzService, audit *service.AuditService) *PlayerHandler {
	return &PlayerHandler{playerSvc: playerSvc, eventSvc: eventSvc, authz: authz, audit: audit}
}

// playerActionRequest 踢/封/解封请求体（范围与原因均可选）。
type playerActionRequest struct {
	// InstanceID 仅作用于单后端子服（最高优先级）。
	InstanceID uint `json:"instanceId"`
	// NetworkID 作用于一个群组内的后端子服。
	NetworkID uint `json:"networkId"`
	// Reason 封禁/踢出原因。
	Reason string `json:"reason"`
}

// Online 在线玩家列表（聚合可达后端 RCON 的 list，标注所在子服）。
func (h *PlayerHandler) Online(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	scopeIDs, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	res, err := h.playerSvc.OnlinePlayers(scopeIDs, scoped)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询在线玩家失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Kick 踢出玩家。
func (h *PlayerHandler) Kick(c *gin.Context) {
	h.action(c, "kick")
}

// Ban 封禁玩家。
func (h *PlayerHandler) Ban(c *gin.Context) {
	h.action(c, "ban")
}

// Unban 解封玩家。
func (h *PlayerHandler) Unban(c *gin.Context) {
	h.action(c, "unban")
}

// action 处理踢/封/解封：权限校验 → 作用域校验 → 执行 → 显式审计。
// 破坏性操作（kick/ban）经显式审计记录玩家名/范围/原因（自动审计中间件不识别玩家路由）。
func (h *PlayerHandler) action(c *gin.Context, kind string) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	player := c.Param("name")

	var req playerActionRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
			return
		}
	}

	// 指定单实例/群组时校验访问权限（非平台管理员不得越权操作不可见实例）。
	if req.InstanceID > 0 {
		ok, err := h.authz.CanAccessInstance(access, req.InstanceID)
		if err != nil || !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
	}

	scopeIDs, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	scope := service.PlayerActionScope{InstanceID: req.InstanceID, NetworkID: req.NetworkID, Reason: req.Reason}

	var res *service.PlayerActionResult
	switch kind {
	case "kick":
		res, err = h.playerSvc.Kick(player, scope, scopeIDs, scoped)
	case "ban":
		res, err = h.playerSvc.Ban(player, scope, h.actorID(c), scopeIDs, scoped)
	case "unban":
		res, err = h.playerSvc.Unban(player, scope, scopeIDs, scoped)
	}
	if err != nil {
		if errors.Is(err, service.ErrNoReachableBackend) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "NO_REACHABLE_BACKEND", "message": "没有可达的后端子服"})
			return
		}
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	h.recordAudit(c, "player."+kind, player, req)
	c.JSON(http.StatusOK, res)
}

// Whitelist 查询单后端白名单。
func (h *PlayerHandler) Whitelist(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	res, err := h.playerSvc.Whitelist(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询白名单失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// whitelistActionRequest 白名单增删请求体。
type whitelistActionRequest struct {
	Action string `json:"action" binding:"required"` // add | remove
	Player string `json:"player" binding:"required"`
}

// WhitelistAction 单后端白名单增删。
func (h *PlayerHandler) WhitelistAction(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req whitelistActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	item, err := h.playerSvc.WhitelistAction(id, req.Action, req.Player)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	h.recordAudit(c, "player.whitelist."+req.Action, req.Player, map[string]any{"instanceId": id})
	c.JSON(http.StatusOK, item)
}

// Bans 封禁记录查询。
func (h *PlayerHandler) Bans(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	var filter service.BanFilter
	if v := c.Query("player"); v != "" {
		filter.PlayerName = &v
	}
	if c.Query("active") == "true" {
		filter.ActiveOnly = true
	}
	if v := c.Query("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			filter.Limit = n
		}
	}
	bans, err := h.playerSvc.ListBans(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询封禁记录失败"})
		return
	}
	c.JSON(http.StatusOK, bans)
}

// PlayersEvents SSE 推送某实例（及其后端子服）的实时玩家事件（join/quit/chat/cross_server，FR-066）。
// 仅订阅当前实例的事件（按实例 UUID 过滤），探针未连入时事件流为空，前端据此降级提示。
// 首帧附带当前在线名册快照，使前端打开即见在线列表（之后由事件流实时增量）。
func (h *PlayerHandler) PlayersEvents(c *gin.Context) {
	if h.eventSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "UNAVAILABLE", "message": "玩家事件服务未启用"})
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
	instanceUUID := h.eventSvc.InstanceUUIDByID(id)
	if instanceUUID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	ch, unsub := h.eventSvc.Subscribe(instanceUUID)
	defer unsub()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	// 初始帧：当前探针连接状态 + 在线名册快照，前端据此渲染初始在线列表与未连入提示。
	snapshot := h.eventSvc.OnlineSnapshot(instanceUUID)
	connected := h.eventSvc.IsProbeConnected(instanceUUID)
	initBytes, _ := json.Marshal(gin.H{"connected": connected, "players": snapshot})
	fmt.Fprintf(c.Writer, "event: init\ndata: %s\n\n", initBytes)
	c.Writer.Flush()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(c.Writer, "event: player\ndata: %s\n\n", data); err != nil {
				slog.Debug("玩家事件 SSE 写入失败", "err", err)
				return
			}
			c.Writer.Flush()
		}
	}
}

// actorID 取当前操作用户 ID。
func (h *PlayerHandler) actorID(c *gin.Context) uint {
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	return id
}

// recordAudit 记录玩家管理破坏性操作的审计日志（玩家名 + 范围/原因）。
func (h *PlayerHandler) recordAudit(c *gin.Context, action, player string, detail any) {
	if h.audit == nil {
		return
	}
	payload := map[string]any{"player": player, "detail": detail}
	raw, _ := json.Marshal(payload)
	_ = h.audit.Record(h.actorID(c), action, "player", player, string(raw), c.ClientIP())
}

// RegisterRoutes 注册玩家管理路由（FR-054）。
func (h *PlayerHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/players", h.Online)
	rg.GET("/instances/:id/players/events", h.PlayersEvents)
	rg.POST("/players/:name/kick", h.Kick)
	rg.POST("/players/:name/ban", h.Ban)
	rg.POST("/players/:name/unban", h.Unban)
	rg.GET("/instances/:id/whitelist", h.Whitelist)
	rg.POST("/instances/:id/whitelist", h.WhitelistAction)
	rg.GET("/bans", h.Bans)
}
