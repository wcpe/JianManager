package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ArtifactReconcileHandler 制品索引 ↔ S3 一致性对账路由处理器（FR-349）。
// 全组挂 admin（JWT + 平台管理员）；处置/触发/设置变更写审计（audit 可 nil，静默跳过）。
type ArtifactReconcileHandler struct {
	svc   *service.ArtifactReconcileService
	audit *service.AuditService
}

// NewArtifactReconcileHandler 创建对账路由处理器。
func NewArtifactReconcileHandler(svc *service.ArtifactReconcileService, audit *service.AuditService) *ArtifactReconcileHandler {
	return &ArtifactReconcileHandler{svc: svc, audit: audit}
}

// reconcileSettingsResponse 定期设置公开契约；不暴露单行内部 ID/更新时间。
type reconcileSettingsResponse struct {
	Enabled       bool       `json:"enabled"`
	IntervalHours int        `json:"intervalHours"`
	NextRunAt     *time.Time `json:"nextRunAt,omitempty"`
}

func toReconcileSettingsResponse(setting *model.ArtifactReconcileSetting) reconcileSettingsResponse {
	return reconcileSettingsResponse{
		Enabled: setting.Enabled, IntervalHours: setting.IntervalHours, NextRunAt: setting.NextRunAt,
	}
}

// GetSettings GET /artifact-reconcile/settings — 定期对账设置（单行，缺失自动 seed 默认每日）。
func (h *ArtifactReconcileHandler) GetSettings(c *gin.Context) {
	setting, err := h.svc.Settings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, toReconcileSettingsResponse(setting))
}

// updateReconcileSettingsRequest 定期对账设置更新体（spec §3.7）。
type updateReconcileSettingsRequest struct {
	Enabled       *bool `json:"enabled" binding:"required"`
	IntervalHours int   `json:"intervalHours" binding:"required"`
}

// UpdateSettings PUT /artifact-reconcile/settings — 更新周期/开关（变更即重算 NextRunAt）。
func (h *ArtifactReconcileHandler) UpdateSettings(c *gin.Context) {
	var req updateReconcileSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	setting, err := h.svc.UpdateSettings(*req.Enabled, req.IntervalHours)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "artifact_reconcile.settings_update", "", map[string]any{
		"enabled": *req.Enabled, "intervalHours": req.IntervalHours,
	})
	c.JSON(http.StatusOK, toReconcileSettingsResponse(setting))
}

// triggerReconcileRequest 触发对账请求体：channelId>0 单渠道；缺省/0 全部 s3 渠道。
type triggerReconcileRequest struct {
	ChannelID uint `json:"channelId"`
}

// triggerReconcileResponse 触发结果：started 已起 run、skipped 被跳过渠道（在途）。
type triggerReconcileResponse struct {
	Started []model.ArtifactReconcileRun `json:"started"`
	Skipped []service.ReconcileSkipped   `json:"skipped"`
}

// Trigger POST /artifact-reconcile/runs — 手动触发对账（异步执行，202 即返，前端轮询 runs）。
// 单渠道在途 → 409 RECONCILE_IN_PROGRESS；全局触发跳过在途渠道并回报。
func (h *ArtifactReconcileHandler) Trigger(c *gin.Context) {
	var req triggerReconcileRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
			return
		}
	}
	resp := triggerReconcileResponse{Started: []model.ArtifactReconcileRun{}, Skipped: []service.ReconcileSkipped{}}
	if req.ChannelID > 0 {
		run, err := h.svc.Trigger(req.ChannelID, model.ArtifactReconcileTriggerManual)
		if err != nil {
			h.respondErr(c, err)
			return
		}
		resp.Started = append(resp.Started, *run)
	} else {
		started, skipped, err := h.svc.TriggerAll(model.ArtifactReconcileTriggerManual)
		if err != nil {
			h.respondErr(c, err)
			return
		}
		resp.Started, resp.Skipped = started, skipped
	}
	h.recordAudit(c, "artifact_reconcile.trigger", "", map[string]any{
		"channelId": req.ChannelID, "started": len(resp.Started), "skipped": len(resp.Skipped),
	})
	c.JSON(http.StatusAccepted, resp)
}

// ListRuns GET /artifact-reconcile/runs?channelId=&limit= — 最近运行记录（id desc）。
func (h *ArtifactReconcileHandler) ListRuns(c *gin.Context) {
	channelID, _ := strconv.ParseUint(c.Query("channelId"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	runs, err := h.svc.ListRuns(uint(channelID), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, runs)
}

// GetRun GET /artifact-reconcile/runs/:id — 单条运行记录。
func (h *ArtifactReconcileHandler) GetRun(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	run, err := h.svc.GetRun(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, run)
}

// ListDiffs GET /artifact-reconcile/runs/:id/diffs?kind=&page=&pageSize= — 差异明细分页。
func (h *ArtifactReconcileHandler) ListDiffs(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	kind := c.Query("kind")
	if kind != "" && kind != model.ArtifactDiffMissing && kind != model.ArtifactDiffOrphan {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "kind 仅支持 missing | orphan"})
		return
	}
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize"))
	diffs, total, err := h.svc.ListDiffs(id, kind, page, pageSize)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	c.JSON(http.StatusOK, gin.H{"items": diffs, "total": total, "page": page, "pageSize": pageSize})
}

// ResolveMissing POST /artifact-reconcile/runs/:id/resolve-missing — 缺失明细批量「标记失效」
// （显式处置按钮，不自动；守卫见 spec §3.5）。
func (h *ArtifactReconcileHandler) ResolveMissing(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	res, err := h.svc.ResolveMissing(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "artifact_reconcile.mark_lost", itoaID(id), map[string]any{
		"runId": id, "marked": res.Marked, "stale": res.Stale,
	})
	c.JSON(http.StatusOK, res)
}

// CleanupOrphans POST /artifact-reconcile/runs/:id/cleanup-orphans — 孤儿对象批量清理
// （前端 DangerConfirm 二次确认后的显式处置；过时守卫防误删 run 后新上传对象）。
func (h *ArtifactReconcileHandler) CleanupOrphans(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	res, err := h.svc.CleanupOrphans(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "artifact_reconcile.cleanup_orphans", itoaID(id), map[string]any{
		"runId": id, "cleaned": res.Cleaned, "stale": res.Stale, "failed": res.Failed,
	})
	c.JSON(http.StatusOK, res)
}

// recordAudit 处置/触发/设置变更审计留痕（audit 为 nil 时静默跳过，沿既有约定）。
func (h *ArtifactReconcileHandler) recordAudit(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	_ = h.audit.Record(userID, action, "artifact_reconcile", targetID, string(raw), c.ClientIP())
}

// respondErr 统一错误映射（spec §3.7）：404 / 409 RECONCILE_IN_PROGRESS / 422 BUSINESS_ERROR / 500。
func (h *ArtifactReconcileHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrArtifactStorageNotFound),
		errors.Is(err, service.ErrReconcileRunNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrReconcileInProgress),
		errors.Is(err, service.ErrReconcileRunRunning):
		c.JSON(http.StatusConflict, gin.H{"error": "RECONCILE_IN_PROGRESS", "message": err.Error()})
	case errors.Is(err, service.ErrReconcileChannelUnsupported),
		errors.Is(err, service.ErrReconcileNoChannel),
		errors.Is(err, service.ErrReconcileInvalidInterval),
		errors.Is(err, service.ErrReconcileRunNotSucceeded):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}

// itoaID uint → 字符串（审计 targetID 用）。
func itoaID(n uint) string { return strconv.FormatUint(uint64(n), 10) }

// RegisterRoutes 注册对账路由（挂 admin 组，JWT + 平台管理员）。
func (h *ArtifactReconcileHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/artifact-reconcile")
	{
		g.GET("/settings", h.GetSettings)
		g.PUT("/settings", h.UpdateSettings)
		g.POST("/runs", h.Trigger)
		g.GET("/runs", h.ListRuns)
		g.GET("/runs/:id", h.GetRun)
		g.GET("/runs/:id/diffs", h.ListDiffs)
		g.POST("/runs/:id/resolve-missing", h.ResolveMissing)
		g.POST("/runs/:id/cleanup-orphans", h.CleanupOrphans)
	}
}
