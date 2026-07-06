package router

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AuditHandler 审计日志路由处理器。
type AuditHandler struct {
	auditSvc *service.AuditService
}

type auditExportRow struct {
	ID         uint      `json:"id"`
	UserID     uint      `json:"userId"`
	Username   string    `json:"username"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"createdAt"`
}

func NewAuditHandler(auditSvc *service.AuditService) *AuditHandler {
	return &AuditHandler{auditSvc: auditSvc}
}

// List 审计日志列表。
func (h *AuditHandler) List(c *gin.Context) {
	filter := parseAuditFilter(c)
	_, hasPage := c.GetQuery("page")
	_, hasPageSize := c.GetQuery("pageSize")
	if hasPage || hasPageSize {
		page, err := h.auditSvc.ListPage(filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
			return
		}
		c.JSON(http.StatusOK, page)
		return
	}

	logs, err := h.auditSvc.List(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}

	c.JSON(http.StatusOK, logs)
}

// Export 导出审计日志，按白名单字段输出 NDJSON。
func (h *AuditHandler) Export(c *gin.Context) {
	if format := c.Query("format"); format != "" && format != "ndjson" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "UNSUPPORTED_FORMAT", "message": "当前仅支持 ndjson 导出"})
		return
	}
	filter := parseAuditFilter(c)
	enc := json.NewEncoder(c.Writer)
	wrote := false
	err := h.auditSvc.StreamExport(filter, 0, func(log model.AuditLog) error {
		return writeAuditExportLine(c, enc, &wrote, log)
	})
	h.recordAuditExport(c, filter, auditExportStatus(err))
	if err != nil {
		if !wrote {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		}
		return
	}
	if !wrote {
		c.Header("Content-Type", "application/x-ndjson")
		c.Status(http.StatusOK)
	}
}

func writeAuditExportLine(c *gin.Context, enc *json.Encoder, wrote *bool, log model.AuditLog) error {
	if !*wrote {
		c.Header("Content-Type", "application/x-ndjson")
		c.Status(http.StatusOK)
		*wrote = true
	}
	if err := enc.Encode(auditExportLine(log)); err != nil {
		return err
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func auditExportStatus(err error) string {
	if err != nil {
		return "failure"
	}
	return "success"
}

func auditExportLine(log model.AuditLog) auditExportRow {
	return auditExportRow{
		ID:         log.ID,
		UserID:     log.UserID,
		Username:   log.User.Username,
		Action:     log.Action,
		TargetType: log.TargetType,
		TargetID:   log.TargetID,
		IP:         log.IP,
		CreatedAt:  log.CreatedAt,
	}
}

func (h *AuditHandler) recordAuditExport(c *gin.Context, filter service.AuditFilter, status string) {
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	_ = h.auditSvc.Record(userID, "audit.export", "audit", "", auditExportDetail(filter, status), c.ClientIP())
}

func auditExportDetail(filter service.AuditFilter, status string) string {
	detail := map[string]interface{}{
		"format": "ndjson",
		"status": status,
	}
	filters := map[string]interface{}{}
	if filter.UserID != nil {
		filters["userId"] = *filter.UserID
	}
	if filter.Action != nil {
		filters["action"] = *filter.Action
	}
	if filter.TargetType != nil {
		filters["targetType"] = *filter.TargetType
	}
	if filter.From != nil {
		filters["from"] = filter.From.Format(time.RFC3339)
	}
	if filter.To != nil {
		filters["to"] = filter.To.Format(time.RFC3339)
	}
	if len(filters) > 0 {
		detail["filters"] = filters
	}
	b, _ := json.Marshal(detail)
	return string(b)
}

func parseAuditFilter(c *gin.Context) service.AuditFilter {
	filter := service.AuditFilter{}

	if v := c.Query("userId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		filter.UserID = &u
	}
	if v := c.Query("action"); v != "" {
		filter.Action = &v
	}
	if v := c.Query("targetType"); v != "" {
		filter.TargetType = &v
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Page = n
		}
	}
	if v := c.Query("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.PageSize = n
		}
	}
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.From = &t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.To = &t
		}
	}
	return filter
}

// RegisterRoutes 注册审计日志路由。
func (h *AuditHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/audit", h.List)
	rg.GET("/audit/export", h.Export)
}
