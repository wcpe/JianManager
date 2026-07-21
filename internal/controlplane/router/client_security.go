package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

type ClientSecurityHandler struct {
	svc   *service.ClientDistSecurityService
	audit *service.AuditService
}

func NewClientSecurityHandler(svc *service.ClientDistSecurityService, audit ...*service.AuditService) *ClientSecurityHandler {
	handler := &ClientSecurityHandler{svc: svc}
	if len(audit) > 0 {
		handler.audit = audit[0]
	}
	return handler
}

func (h *ClientSecurityHandler) Hello(c *gin.Context) {
	if _, blocked := h.svc.ActiveIPBlock(c.ClientIP()); blocked {
		respondSecurityAuthErr(c, h.svc, service.ErrIPTempBlocked)
		return
	}
	var body service.ClientSecurityHelloInput
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if body.PlayerName == "" {
		body.PlayerName = c.GetHeader(playerNameHeader)
	}
	if body.Channel == "" || body.MachineID == "" || body.InstallID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "channel/machineId/installId 必填"})
		return
	}
	check, err := h.svc.VerifyChannelKey(body.Channel, c.GetHeader(clientKeyHeader))
	if err != nil {
		respondSecurityAuthErr(c, h.svc, err)
		return
	}
	if err := h.svc.RecordHello(body, check.Key, c.ClientIP(), c.GetHeader("User-Agent")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "记录启动画像失败"})
		return
	}
	c.Status(http.StatusAccepted)
}

func (h *ClientSecurityHandler) Overview(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	out, err := h.svc.Overview()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *ClientSecurityHandler) Profiles(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	out, err := h.svc.ListProfiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *ClientSecurityHandler) Profile(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	out, err := h.svc.ProfileDetail(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "PROFILE_NOT_FOUND", "message": "画像不存在"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *ClientSecurityHandler) ChannelSummary(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	out, err := h.svc.ChannelSummary(c.Param("id"), time.Hour)
	if errors.Is(err, service.ErrChannelNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *ClientSecurityHandler) Events(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	out, err := h.svc.ListRiskEvents()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *ClientSecurityHandler) Actions(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	out, err := h.svc.ListActions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *ClientSecurityHandler) Logs(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	out, err := h.svc.SearchLogs(service.ClientDistSecurityLogFilter{Type: c.Query("type"), ChannelID: c.Query("channelId"), MachineID: c.Query("machineId"), PlayerName: c.Query("playerName"), IP: c.Query("ip"), Page: parseIntDefault(c.Query("page"), 1), PageSize: parseIntDefault(c.Query("pageSize"), 50)})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}

type ipBlockRequest struct {
	IP              string `json:"ip"`
	ChannelID       string `json:"channelId"`
	Reason          string `json:"reason"`
	TTLSeconds      int    `json:"ttlSeconds"`
	DurationMinutes int    `json:"durationMinutes"`
}

func (h *ClientSecurityHandler) BlockIP(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var body ipBlockRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.IP == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供 ip"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	createdBy, _ := uid.(uint)
	ttl := time.Duration(body.TTLSeconds) * time.Second
	if ttl <= 0 && body.DurationMinutes > 0 {
		ttl = time.Duration(body.DurationMinutes) * time.Minute
	}
	a, err := h.svc.BlockIP(body.IP, body.ChannelID, body.Reason, ttl, createdBy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	h.recordAction(c, "client_dist_security.ip_block", "ip", body.IP, body)
	c.JSON(http.StatusCreated, a)
}
func (h *ClientSecurityHandler) CancelAction(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	if err := h.svc.CancelAction(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "取消处置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type keyStateRequest struct {
	State  string `json:"state"`
	Note   string `json:"note"`
	Reason string `json:"reason"`
}

func (h *ClientSecurityHandler) SetKeyState(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var body keyStateRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.State == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供 state"})
		return
	}
	if body.Note == "" {
		body.Note = body.Reason
	}
	if err := h.svc.SetKeyState(id, body.State, body.Note); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	h.recordAction(c, "client_dist_security.key_state", "client_key", strconv.FormatUint(uint64(id), 10), body)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type channelProtectionRequest struct {
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

func (h *ClientSecurityHandler) SetChannelProtection(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var body channelProtectionRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.Mode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供 mode"})
		return
	}
	if err := h.svc.SetChannelProtection(c.Param("id"), body.Mode); err != nil {
		if errors.Is(err, service.ErrChannelNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	h.recordAction(c, "client_dist_security.channel_protection", "client_channel", c.Param("id"), body)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *ClientSecurityHandler) ClearChannelProtection(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	if err := h.svc.SetChannelProtection(c.Param("id"), service.ClientChannelModeNormal); err != nil {
		if errors.Is(err, service.ErrChannelNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *ClientSecurityHandler) StubOK(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	c.JSON(http.StatusOK, []any{})
}

func (h *ClientSecurityHandler) IPAnalysis(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	rows, err := h.svc.ListIPAnalysis(parseLimit(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (h *ClientSecurityHandler) PlayerAnalysis(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	rows, err := h.svc.ListPlayerAnalysis(parseLimit(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (h *ClientSecurityHandler) Groups(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	groups, err := h.svc.ListGroups()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, groups)
}

type securityGroupRequest struct {
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	TargetType   string         `json:"targetType"`
	Rule         map[string]any `json:"rule"`
	ActionPolicy map[string]any `json:"actionPolicy"`
	Enabled      bool           `json:"enabled"`
}

func (h *ClientSecurityHandler) CreateGroup(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var body securityGroupRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	createdBy, _ := uid.(uint)
	group := &model.ClientSecurityGroup{Name: body.Name, Kind: body.Kind, TargetType: body.TargetType, Enabled: body.Enabled, CreatedBy: createdBy}
	if body.Rule != nil {
		group.RuleJSON = mustSecurityJSON(body.Rule)
	}
	if body.ActionPolicy != nil {
		group.ActionPolicyJSON = mustSecurityJSON(body.ActionPolicy)
	}
	if err := h.svc.CreateGroup(group); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, group)
}

func (h *ClientSecurityHandler) UpdateGroup(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var body securityGroupRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	group := &model.ClientSecurityGroup{Name: body.Name, Kind: body.Kind, TargetType: body.TargetType, Enabled: body.Enabled}
	if body.Rule != nil {
		group.RuleJSON = mustSecurityJSON(body.Rule)
	}
	if body.ActionPolicy != nil {
		group.ActionPolicyJSON = mustSecurityJSON(body.ActionPolicy)
	}
	if err := h.svc.UpdateGroup(id, group); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, group)
}

func (h *ClientSecurityHandler) DeleteGroup(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	if err := h.svc.DeleteGroup(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *ClientSecurityHandler) PrivacyNotice(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"requiredFields":   []string{"IP", "请求时间", "endpoint", "status", "errCode", "keyId/keyPrefix", "channel", "User-Agent", "machineId", "installId", "playerName"},
		"diagnosticFields": []string{"更新结果", "耗时", "OS/Java/launcher", "bootSuccess"},
		"notice":           "启动器会在启动时上报玩家名、machineId、installId、版本与基础环境信息，用于安全审查、防刷、防滥用、封禁解封与审计。玩家名由客户端提供，可能被篡改；平台不会采集账号 token、原始硬件序列号、完整本地路径或文件内容。",
		"retentionDays":    30,
	})
}
func (h *ClientSecurityHandler) RegisterConsumerRoutes(rg *gin.RouterGroup) {
	rg.POST("/client-security/hello", h.Hello)
}
func (h *ClientSecurityHandler) RegisterAdminRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-channels/:id/security-summary", h.ChannelSummary)
	sec := rg.Group("/client-dist/security")
	sec.GET("/overview", h.Overview)
	sec.GET("/events", h.Events)
	sec.GET("/logs", h.Logs)
	sec.GET("/profiles", h.Profiles)
	sec.GET("/profiles/:id", h.Profile)
	sec.GET("/ip-analysis", h.IPAnalysis)
	sec.GET("/player-analysis", h.PlayerAnalysis)
	sec.GET("/actions", h.Actions)
	sec.POST("/ip-blocks", h.BlockIP)
	sec.POST("/ip-blocks/:id/cancel", h.CancelAction)

	sec.GET("/groups", h.Groups)
	sec.POST("/groups", h.CreateGroup)
	sec.PUT("/groups/:id", h.UpdateGroup)
	sec.DELETE("/groups/:id", h.DeleteGroup)
	sec.GET("/privacy-notice", h.PrivacyNotice)
	sec.POST("/keys/:id/state", h.SetKeyState)
	sec.PUT("/channels/:id/protection", h.SetChannelProtection)

	sec.DELETE("/channels/:id/protection", h.ClearChannelProtection)
}

func (h *ClientSecurityHandler) recordAction(c *gin.Context, action, targetType, targetID string, detail any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(getUserID(c), action, targetType, targetID, string(raw), c.ClientIP())
}

func respondSecurityAuthErr(c *gin.Context, svc *service.ClientDistSecurityService, err error) string {
	switch {
	case errors.Is(err, service.ErrIPTempBlocked):
		setRetryAfter(c, svc)
		c.JSON(http.StatusForbidden, gin.H{"error": "IP_TEMP_BLOCKED", "message": "IP 已临时封禁"})
		return "IP_TEMP_BLOCKED"
	case errors.Is(err, service.ErrPullKeyInvalid):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "INVALID_CLIENT_KEY", "message": "拉取密钥无效"})
		return "INVALID_CLIENT_KEY"
	case errors.Is(err, service.ErrClientKeySuspended):
		setRetryAfter(c, svc)
		c.JSON(http.StatusForbidden, gin.H{"error": "CLIENT_KEY_SUSPENDED", "message": "拉取密钥已暂停"})
		return "CLIENT_KEY_SUSPENDED"
	case errors.Is(err, service.ErrClientKeyThrottled):
		setRetryAfter(c, svc)
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "RATE_LIMITED", "message": "拉取密钥已限速"})
		return "RATE_LIMITED"
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "鉴权失败"})
		return "INTERNAL_ERROR"
	}
}
func setRetryAfter(c *gin.Context, svc *service.ClientDistSecurityService) {
	sec := 60
	if svc != nil {
		sec = svc.RetryAfter()
	}
	c.Header("Retry-After", strconv.Itoa(sec))
}

func parseLimit(c *gin.Context) int {
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 200
}

func mustSecurityJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
