package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ConfigAdoptHandler 配置纳管路由处理器（FR-533）。
//
// 前缀刻意与 /config-files 的既有 CRUD 同组（同一资源域），
// 但 adopt / adopt/preview / health 是**静态段**，必须注册在 /:id 之前，
// 否则 gin 会把它们当成 id=adopt 的通配请求，行为变成「配置文件不存在」。
type ConfigAdoptHandler struct {
	svc   *service.ConfigAdoptService
	authz *service.AuthzService
	audit *service.AuditService
}

// NewConfigAdoptHandler 创建配置纳管路由处理器。
func NewConfigAdoptHandler(svc *service.ConfigAdoptService, authz *service.AuthzService, audit *service.AuditService) *ConfigAdoptHandler {
	return &ConfigAdoptHandler{svc: svc, authz: authz, audit: audit}
}

// RegisterRoutes 注册纳管端点。
//
// 调用方须保证本方法在 NewConfigFileHandler(...).RegisterRoutes(...) **之前**执行，
// 让静态段优先于 /config-files/:id 通配（gin 内部按注册顺序竞争，见 gin.Engine.addRoute）。
func (h *ConfigAdoptHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/config-files")
	g.POST("/adopt", h.Adopt)
	g.POST("/adopt/preview", h.Preview)
	g.GET("/health", h.Health)
}

// Adopt 纳管磁盘上的现有配置文件。门禁 file.write / instance.write。
func (h *ConfigAdoptHandler) Adopt(c *gin.Context) {
	if !requireNodes(c, "file.write", "instance.write") {
		return
	}
	var req service.ConfigAdoptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	h.run(c, req, false)
}

// Preview 预演纳管：等价 dryRun=true，独立端点便于前端不必自己翻转布尔值。
//
// 无论请求体里 dryRun 写什么，本端点都强制按 dryRun 执行——「预演端点真的没写库」
// 是调用方唯一合理的期待，若允许被请求体覆盖就无法作为安全预检使用。
func (h *ConfigAdoptHandler) Preview(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	var req service.ConfigAdoptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	h.run(c, req, true)
}

// run 执行纳管并按契约映射错误。
func (h *ConfigAdoptHandler) run(c *gin.Context, req service.ConfigAdoptRequest, forceDryRun bool) {
	if forceDryRun {
		req.DryRun = true
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	resp, err := h.svc.Adopt(req, authorID)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	// 审计只记动作与计数，绝不记内容或文件清单里的敏感路径细节（§8 同口径）。
	if !req.DryRun {
		h.recordAudit(c, authorID, "config_file_adopt",
			adoptAuditDetail(resp))
	}
	c.JSON(http.StatusOK, resp)
}

// Health 健康自检：只报告 JM 侧事实，不做基于 Beacon 状态的自动判定（spec §11）。
func (h *ConfigAdoptHandler) Health(c *gin.Context) {
	if !requireNodes(c, "file.read", "instance.read") {
		return
	}
	view, err := h.svc.Health()
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, view)
}

// respondErr 把纳管错误映射到 HTTP 状态与稳定错误码。
func (h *ConfigAdoptHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAdoptFilesRequired),
		errors.Is(err, service.ErrAdoptPatternRequired),
		errors.Is(err, service.ErrBusinessTargetUnspecified),
		errors.Is(err, service.ErrAdoptPathEscape),
		errors.Is(err, service.ErrAdoptPathEmpty):
		c.JSON(http.StatusBadRequest, gin.H{"error": adoptErrorCodeOf(err), "message": err.Error()})
	case errors.Is(err, service.ErrBusinessTargetEmpty):
		c.JSON(http.StatusBadRequest, gin.H{"error": "TARGET_EMPTY", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

// adoptErrorCodeOf 把请求级校验错误映射为契约错误码。
func adoptErrorCodeOf(err error) string {
	switch {
	case errors.Is(err, service.ErrAdoptPathEscape), errors.Is(err, service.ErrAdoptPathEmpty):
		return service.AdoptCodePathEscape
	case errors.Is(err, service.ErrAdoptPatternRequired):
		return "PATTERN_REQUIRED"
	case errors.Is(err, service.ErrAdoptFilesRequired):
		return "FILES_REQUIRED"
	default:
		return "INVALID_REQUEST"
	}
}

// adoptAuditDetail 拼装审计明细：只有计数，不含路径与内容。
func adoptAuditDetail(resp *service.ConfigAdoptResponse) string {
	if resp == nil {
		return ""
	}
	return "total=" + strconv.Itoa(resp.Summary.Total) +
		" created=" + strconv.Itoa(resp.Summary.Created) +
		" appended=" + strconv.Itoa(resp.Summary.Appended) +
		" alreadyAdopted=" + strconv.Itoa(resp.Summary.AlreadyAdopted) +
		" failed=" + strconv.Itoa(resp.Summary.Failed) +
		" instances=" + strconv.Itoa(resp.Summary.Instances)
}

// recordAudit 写审计（失败不阻断主流程）。
func (h *ConfigAdoptHandler) recordAudit(c *gin.Context, authorID uint, action, detail string) {
	if h.audit == nil {
		return
	}
	h.audit.RecordSafe(authorID, action, "config_file", "", detail, c.ClientIP())
}
