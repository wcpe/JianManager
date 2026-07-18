package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BotStressSessionHandler Bot 压测会话路由处理器。
type BotStressSessionHandler struct {
	svc       *service.BotStressSessionService
	authz     *service.AuthzService
	preflight *service.BotLoadPreflightService
	execution *service.BotLoadExecutionService
	audit     *service.AuditService
}

// NewBotStressSessionHandler 创建 Bot 压测会话路由处理器。
func NewBotStressSessionHandler(svc *service.BotStressSessionService, authz *service.AuthzService, preflight *service.BotLoadPreflightService, execution *service.BotLoadExecutionService, audit *service.AuditService) *BotStressSessionHandler {
	return &BotStressSessionHandler{svc: svc, authz: authz, preflight: preflight, execution: execution, audit: audit}
}

// Create 创建压测会话。
func (h *BotStressSessionHandler) Create(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req service.CreateBotStressSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if !access.IsPlatformAdmin {
		ok, err := h.authz.CanManageInstance(access, req.InstanceID)
		if err != nil || !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权为该实例创建压测会话"})
			return
		}
	}

	view, err := h.svc.Create(req)
	if err != nil {
		writeBotStressSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, view)
}

// List 查询压测会话列表。
func (h *BotStressSessionHandler) List(c *gin.Context) {
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
	res, err := h.svc.List(service.BotStressSessionListQuery{Page: page, PageSize: pageSize}, scopeIDs, scope)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Get 查询压测会话详情。
func (h *BotStressSessionHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !h.canReadSession(c, id) {
		return
	}
	view, err := h.svc.Get(id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

type botLoadPreflightRequest struct {
	ExecutorNodeIDs             []uint `json:"executorNodeIds"`
	ConnectRatePerSecondPerNode int    `json:"connectRatePerSecondPerNode"`
}

// Preflight 校验请求并调用共享预检核心，容量不足仍返回 200 ready=false。
func (h *BotStressSessionHandler) Preflight(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	session, ok := h.loadManagedSession(c, id)
	if !ok {
		return
	}
	if session.Status != model.BotStressSessionPending && session.Status != model.BotStressSessionError {
		writeBotLoadError(c, fmt.Errorf("%w: 当前状态为 %s", service.ErrBotLoadInvalidState, session.Status))
		return
	}
	var req botLoadPreflightRequest
	if err := bindOptionalJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	nodeIDs, err := normalizeExecutorNodeIDs(req.ExecutorNodeIDs)
	if err != nil || (req.ConnectRatePerSecondPerNode != 0 && (req.ConnectRatePerSecondPerNode < 1 || req.ConnectRatePerSecondPerNode > 50)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "预检参数无效"})
		return
	}
	result, err := h.preflight.Preflight(c.Request.Context(), session, service.BotLoadPreflightInput{
		TargetBots: session.BotCount, ExecutorNodeIDs: nodeIDs,
		ConnectRatePerSecondPerNode: req.ConnectRatePerSecondPerNode,
		Probe:                       service.BotLoadProbeStatus{InstanceID: session.InstanceID, InstanceUUID: session.Instance.UUID},
	})
	h.recordRunAudit(c, "bot_load.run.preflight", id, preflightAuditDetail(result, nodeIDs, req.ConnectRatePerSecondPerNode), err)
	if err != nil {
		writeBotLoadError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type botLoadStartRequest struct {
	PlanToken string `json:"planToken"`
}

// Start 使用短期计划令牌启动；旧 V1 空 body 仅允许内部目标节点单节点预检。
func (h *BotStressSessionHandler) Start(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if h.preflight == nil || h.execution == nil {
		h.startLegacyService(c, id)
		return
	}
	session, ok := h.loadManagedSession(c, id)
	if !ok {
		return
	}
	var req botLoadStartRequest
	if err := bindOptionalJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	legacyCompat := false
	if strings.TrimSpace(req.PlanToken) == "" {
		if !service.IsLegacyBotStressSession(session) {
			err = service.ErrBotLoadCapacityChanged
		} else {
			legacyCompat = true
			req.PlanToken, err = h.preflightLegacySession(c, session)
		}
	}
	if err == nil {
		_, err = h.execution.Start(c.Request.Context(), id, req.PlanToken)
	}
	if err != nil {
		h.recordRunAudit(c, "bot_load.run.start", id, gin.H{"legacyCompat": legacyCompat}, err)
		writeBotLoadError(c, err)
		return
	}
	view, err := h.svc.Get(id)
	h.recordRunAudit(c, "bot_load.run.start", id, startAuditDetail(view, legacyCompat), err)
	if err != nil {
		writeBotLoadError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, view)
}

type botLoadStopRequest struct {
	Reason string `json:"reason"`
}

// Stop 接受可选原因并调用分布式执行服务后台按节点停止。
func (h *BotStressSessionHandler) Stop(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if h.execution == nil {
		h.stopLegacyService(c, id)
		return
	}
	if _, ok := h.loadManagedSession(c, id); !ok {
		return
	}
	var req botLoadStopRequest
	if err := bindOptionalJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if utf8.RuneCountInString(req.Reason) > 255 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "reason 最长 255 个字符"})
		return
	}
	_, err = h.execution.Stop(c.Request.Context(), id, req.Reason)
	if err != nil {
		h.recordRunAudit(c, "bot_load.run.stop", id, gin.H{"reasonLength": utf8.RuneCountInString(req.Reason)}, err)
		writeBotLoadError(c, err)
		return
	}
	view, err := h.svc.Get(id)
	detail := gin.H{"reasonLength": utf8.RuneCountInString(req.Reason)}
	if view != nil {
		detail["status"] = view.Status
		detail["batchCount"] = len(view.Batches)
	}
	h.recordRunAudit(c, "bot_load.run.stop", id, detail, err)
	if err != nil {
		writeBotLoadError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, view)
}

func (h *BotStressSessionHandler) preflightLegacySession(c *gin.Context, session *model.BotStressSession) (string, error) {
	result, err := h.preflight.Preflight(c.Request.Context(), session, service.BotLoadPreflightInput{
		TargetBots: session.BotCount, ExecutorNodeIDs: []uint{session.Instance.NodeID},
		Probe: service.BotLoadProbeStatus{InstanceID: session.InstanceID, InstanceUUID: session.Instance.UUID},
	})
	if err != nil {
		return "", err
	}
	if result.Ready {
		return result.PlanToken, nil
	}
	return "", botLoadBlockerError(result.Blockers)
}

func botLoadBlockerError(blockers []service.BotLoadIssue) error {
	if len(blockers) == 0 {
		return service.ErrBotLoadCapacityChanged
	}
	blocker := blockers[0]
	switch blocker.Code {
	case service.BotLoadCapacityInsufficientCode:
		return fmt.Errorf("%w: %s", service.ErrBotLoadCapacityInsufficient, blocker.Message)
	case service.BotLoadNodeUnavailableCode:
		return fmt.Errorf("%w: %s", service.ErrBotLoadNodeUnavailable, blocker.Message)
	case service.BotLoadProbeRequiredCode:
		return &botLoadHTTPError{status: http.StatusUnprocessableEntity, code: service.BotLoadProbeRequiredCode, message: blocker.Message}
	default:
		return service.ErrBotLoadCapacityChanged
	}
}

func normalizeExecutorNodeIDs(ids []uint) ([]uint, error) {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, service.ErrBotLoadPreflightInvalid
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) > 256 {
		return nil, service.ErrBotLoadPreflightInvalid
	}
	return out, nil
}

func bindOptionalJSON(c *gin.Context, target any) error {
	err := c.ShouldBindJSON(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (h *BotStressSessionHandler) loadManagedSession(c *gin.Context, id uint) (*model.BotStressSession, bool) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return nil, false
	}
	session, err := h.svc.LoadForBotLoad(c.Request.Context(), id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return nil, false
	}
	ok, err := h.authz.CanManageInstance(access, session.InstanceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return nil, false
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "压测会话不存在"})
		return nil, false
	}
	return session, true
}

func (h *BotStressSessionHandler) canReadSession(c *gin.Context, id uint) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	session, err := h.svc.LoadForBotLoad(c.Request.Context(), id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return false
	}
	ok, err := h.authz.CanAccessInstance(access, session.InstanceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return false
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "压测会话不存在"})
		return false
	}
	return true
}

func (h *BotStressSessionHandler) startLegacyService(c *gin.Context, id uint) {
	if !h.canManageSession(c, id) {
		return
	}
	view, err := h.svc.Start(id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *BotStressSessionHandler) stopLegacyService(c *gin.Context, id uint) {
	if !h.canManageSession(c, id) {
		return
	}
	view, err := h.svc.Stop(id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *BotStressSessionHandler) canManageSession(c *gin.Context, id uint) bool {
	_, ok := h.loadManagedSession(c, id)
	return ok
}

func preflightAuditDetail(result *service.BotLoadPreflightResult, nodeIDs []uint, rate int) gin.H {
	detail := gin.H{"executorNodeCount": len(nodeIDs), "connectRatePerSecondPerNode": rate}
	if result == nil {
		return detail
	}
	codes := make([]string, 0, len(result.Blockers))
	for _, blocker := range result.Blockers {
		codes = append(codes, blocker.Code)
	}
	detail["ready"] = result.Ready
	detail["targetBots"] = result.TargetBots
	detail["allocationCount"] = len(result.Allocations)
	detail["blockers"] = codes
	return detail
}

func startAuditDetail(view *service.BotStressSessionView, legacyCompat bool) gin.H {
	detail := gin.H{"legacyCompat": legacyCompat}
	if view != nil {
		detail["status"] = view.Status
		detail["allocationCount"] = len(view.Allocations)
		detail["batchCount"] = len(view.Batches)
	}
	return detail
}

func (h *BotStressSessionHandler) recordRunAudit(c *gin.Context, action string, id uint, detail any, operationErr error) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	errMessage := ""
	if operationErr != nil {
		errMessage = operationErr.Error()
	}
	_ = h.audit.RecordResult(getUserID(c), action, "bot_load_run", strconv.FormatUint(uint64(id), 10), string(raw), c.ClientIP(), operationErr == nil, errMessage)
}

type botLoadHTTPError struct {
	status  int
	code    string
	message string
}

func (e *botLoadHTTPError) Error() string { return e.message }

func writeBotLoadError(c *gin.Context, err error) {
	var responseErr *botLoadHTTPError
	switch {
	case errors.As(err, &responseErr):
		c.JSON(responseErr.status, gin.H{"error": responseErr.code, "message": responseErr.message})
	case errors.Is(err, service.ErrBotStressSessionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "压测会话不存在"})
	case errors.Is(err, service.ErrBotLoadInvalidState), errors.Is(err, service.ErrBotStressSessionInvalid):
		c.JSON(http.StatusConflict, gin.H{"error": "BOT_LOAD_INVALID_STATE", "message": err.Error()})
	case errors.Is(err, service.ErrBotLoadCapacityChanged):
		c.JSON(http.StatusConflict, gin.H{"error": service.BotLoadCapacityChangedCode, "message": "容量计划已变化，请重新预检"})
	case errors.Is(err, service.ErrBotLoadCapacityInsufficient):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": service.BotLoadCapacityInsufficientCode, "message": err.Error()})
	case errors.Is(err, service.ErrBotLoadNodeUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": service.BotLoadNodeUnavailableCode, "message": err.Error()})
	case errors.Is(err, service.ErrBotLoadPreflightInvalid), errors.Is(err, service.ErrBotLoadConfigInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "Bot 负载操作失败"})
	}
}

func writeBotStressSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrBotStressSessionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "压测会话不存在"})
	case errors.Is(err, service.ErrBotStressSessionInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "压测会话操作失败"})
	}
}

// RegisterRoutes 注册压测会话路由。
func (h *BotStressSessionHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/bots/stress-test", h.Create)
	sessions := rg.Group("/bots/stress-sessions")
	{
		sessions.POST("", h.Create)
		sessions.GET("", h.List)
		sessions.GET("/:id", h.Get)
		if h.preflight != nil {
			sessions.POST("/:id/preflight", h.Preflight)
		}
		sessions.POST("/:id/start", h.Start)
		sessions.POST("/:id/stop", h.Stop)
	}
}
