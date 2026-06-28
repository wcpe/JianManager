package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/embed"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientCoreVersionHandler updater-core 集中版本管理端点（FR-193，见 ADR-045）。
//
// 全部为**运营操作**：JWT 平台管理员 + 审计，与玩家拉取密钥端点物理隔离（同 FR-086/087 鉴权分组）。
//   - core 制品上传 / 版本登记 / 版本列表：core jar 与频道无关（一份 jar 三平台通用，ADR-021），全局共享。
//   - 频道 core pin 查询 / 设置·更新 / 回退：pin 落在 client_channels.pinned_core_version。
//
// 「回退」按 ADR-045 决策 4「以更高版本号重发旧 core 字节」实现，绝不降 agent.core.version。
type ClientCoreVersionHandler struct {
	core    *service.ClientCoreVersionService
	channel *service.ClientChannelService
	audit   *service.AuditService
}

// NewClientCoreVersionHandler 创建 core 版本管理端点处理器。
func NewClientCoreVersionHandler(core *service.ClientCoreVersionService, channel *service.ClientChannelService, audit *service.AuditService) *ClientCoreVersionHandler {
	return &ClientCoreVersionHandler{core: core, channel: channel, audit: audit}
}

// UploadCore POST /client-core-versions/upload — 上传 updater-core jar 制品（运营，平台管理员）。
// multipart 表单：file（必）、codec（可，zstd|none）、expectedSha256（可）。
// 返回的 sha256 即 manifest agent.core.platforms[os].artifact.sha256；随后用 POST /client-core-versions 登记版本。
func (h *ClientCoreVersionHandler) UploadCore(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	file, header, ferr := c.Request.FormFile("file")
	if ferr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供上传文件 file"})
		return
	}
	defer file.Close()

	res, err := h.core.UploadCore(file, service.UploadCoreParams{
		Filename:       header.Filename,
		Codec:          c.PostForm("codec"),
		ExpectedSHA256: c.PostForm("expectedSha256"),
	})
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "client_core.upload", map[string]any{"sha256": res.SHA256, "size": res.Size, "codec": res.Codec})
	c.JSON(http.StatusCreated, res)
}

// registerCoreVersionRequest 登记 core 版本请求体（制品须已上传）。
type registerCoreVersionRequest struct {
	// ArtifactSHA256 core jar 制品自身 sha256（须由上传返回、已在 client-core 库）。
	ArtifactSHA256 string `json:"artifactSha256" binding:"required"`
	// ArtifactSize 制品字节数。
	ArtifactSize int64 `json:"artifactSize"`
	// Codec 制品压缩算法（zstd|none）。
	Codec string `json:"codec"`
	// Note 登记备注。
	Note string `json:"note"`
}

// RegisterVersion POST /client-core-versions — 登记 core 版本（version 服务端单调递增分配）。
func (h *ClientCoreVersionHandler) RegisterVersion(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var body registerCoreVersionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	rec, err := h.core.RegisterVersion(service.RegisterCoreVersionParams{
		ArtifactSHA256: body.ArtifactSHA256,
		ArtifactSize:   body.ArtifactSize,
		Codec:          body.Codec,
		Note:           body.Note,
		CreatedBy:      ctxUserID(c),
	})
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "client_core.register", map[string]any{"version": rec.Version, "sha256": rec.ArtifactSHA256})
	c.JSON(http.StatusCreated, rec)
}

// ListVersions GET /client-core-versions — 列出全部已登记 core 版本（版本号 DESC，运营）。
// 同时返回内嵌楔子版本（信息性，冻结、不纳管，供前端只读展示）。
func (h *ClientCoreVersionHandler) ListVersions(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	versions, err := h.core.ListVersions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询 core 版本失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"versions": versions,
		// 楔子冻结、单版本、不纳入版本管理（ADR-045 决策 2）：仅展示内嵌楔子版本号供运营知情。
		"wedge": gin.H{"version": embed.ClientUpdaterEmbeddedVersion, "frozen": true},
	})
}

// GetChannelPin GET /client-channels/:id/core-pin — 取频道 core pin 现状（运营）。
// 返回 pinnedCoreVersion（0=自动用最新）+ 解析后的有效版本（effectiveVersion，无注册为 0）。
func (h *ClientCoreVersionHandler) GetChannelPin(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	ch, err := h.channel.GetChannel(channelID)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	rec, err := h.core.ResolveForChannel(ch.PinnedCoreVersion)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	effective := 0
	if rec != nil {
		effective = rec.Version
	}
	c.JSON(http.StatusOK, gin.H{
		"channelId":         channelID,
		"pinnedCoreVersion": ch.PinnedCoreVersion,
		"effectiveVersion":  effective,
	})
}

// setChannelPinRequest 设置/更新频道 core pin 请求体。
type setChannelPinRequest struct {
	// Version 目标 core 版本号（0=恢复自动用最新；>0 须为已登记版本）。
	Version int `json:"version"`
}

// SetChannelPin PUT /client-channels/:id/core-pin — 设/更新频道 core pin（运营，平台管理员）。
// 「更新到更高版本」即客户端下次启动 promote 新 core（ADR-045）。
func (h *ClientCoreVersionHandler) SetChannelPin(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	var body setChannelPinRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.Version < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供有效的 version（0=自动用最新）"})
		return
	}
	if err := h.core.SetChannelPin(channelID, body.Version); err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "client_core.pin", map[string]any{"channelId": channelID, "version": body.Version})
	c.JSON(http.StatusOK, gin.H{"channelId": channelID, "pinnedCoreVersion": body.Version})
}

// rollbackChannelCoreRequest 频道 core 回退请求体（ADR-045 决策 4）。
type rollbackChannelCoreRequest struct {
	// SourceVersion 要回退到的历史 core 版本号（其字节将以更高版本号重发为新版并 pin）。
	SourceVersion int `json:"sourceVersion"`
	// Note 回退备注（可空；空则生成「回退至 core vN」）。
	Note string `json:"note"`
}

// RollbackChannelCore POST /client-channels/:id/core-rollback — 回退坏 core（运营，平台管理员）。
// 以更高版本号重发历史 core 字节为新版并 pin（不降 agent.core.version、不违反客户端防降级，ADR-045 决策 4）。
func (h *ClientCoreVersionHandler) RollbackChannelCore(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	var body rollbackChannelCoreRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.SourceVersion <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供有效的 sourceVersion"})
		return
	}
	rec, err := h.core.RollbackChannelCore(channelID, body.SourceVersion, ctxUserID(c), body.Note)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "client_core.rollback", map[string]any{
		"channelId": channelID, "sourceVersion": body.SourceVersion, "newVersion": rec.Version,
	})
	c.JSON(http.StatusCreated, gin.H{
		"channelId": channelID, "version": rec.Version, "sourceVersion": body.SourceVersion, "note": rec.Note,
	})
}

// respondErr core 版本端点错误映射。
func (h *ClientCoreVersionHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
	case errors.Is(err, service.ErrCoreArtifactNotFound):
		c.JSON(http.StatusBadRequest, gin.H{"error": "CORE_ARTIFACT_NOT_FOUND", "message": "updater-core 制品不存在，请先上传"})
	case errors.Is(err, service.ErrCoreVersionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CORE_VERSION_NOT_FOUND", "message": "updater-core 版本不存在"})
	case errors.Is(err, service.ErrChecksumMismatch):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "CHECKSUM_MISMATCH", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
	}
}

// recordAudit 记录 core 版本管理操作审计（detail 仅含可公开元数据）。
func (h *ClientCoreVersionHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(ctxUserID(c), action, "client_channel", "", string(raw), c.ClientIP())
}

// ctxUserID 取请求上下文里的用户 ID（0=未知）。
func ctxUserID(c *gin.Context) uint {
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	return id
}

// RegisterRoutes 注册 core 版本管理端点（运营操作，须挂 JWT 平台管理员组）。
func (h *ClientCoreVersionHandler) RegisterRoutes(rg *gin.RouterGroup) {
	cv := rg.Group("/client-core-versions")
	{
		cv.GET("", h.ListVersions)
		cv.POST("", h.RegisterVersion)
		cv.POST("/upload", h.UploadCore)
	}
	ch := rg.Group("/client-channels")
	{
		ch.GET("/:id/core-pin", h.GetChannelPin)
		ch.PUT("/:id/core-pin", h.SetChannelPin)
		ch.POST("/:id/core-rollback", h.RollbackChannelCore)
	}
}
