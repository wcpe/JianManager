package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientVersionHandler 客户端分发 manifest 发布与面向玩家的消费端点（FR-087，见 ADR-022、contract §2/§4）。
//
// 鉴权分两组、物理隔离（关键安全设计）：
//   - 发布端点（运营操作）：JWT 平台管理员，挂 admin 组（同 FR-086 频道管理）。
//     POST /client-channels/:id/files、POST /client-channels/:id/versions。
//   - 消费端点（玩家）：X-Client-Key 拉取密钥鉴权（service.VerifyKey/VerifyAnyKey），挂公网组。
//     GET /client-channels/:id/manifest、GET /client-artifacts/:sha256。
//
// 理由：拉取密钥半公开（随整包分发必然泄露，ADR-022 §1），用它鉴权「发布」=严重漏洞；
// contract §4 只把 manifest/制品/遥测列为玩家 key 端点。发布走运营浏览器 JWT 入口。
type ClientVersionHandler struct {
	svc      *service.ClientVersionService
	channel  *service.ClientChannelService
	audit    *service.AuditService
	machine  *service.ClientMachineService
	tracking *service.ClientDistTrackingService
}

// NewClientVersionHandler 创建客户端分发版本/消费端点处理器。machine/tracking 可为 nil（不登记机器码/不追踪）。
func NewClientVersionHandler(svc *service.ClientVersionService, channel *service.ClientChannelService, audit *service.AuditService, machine *service.ClientMachineService, tracking *service.ClientDistTrackingService) *ClientVersionHandler {
	return &ClientVersionHandler{svc: svc, channel: channel, audit: audit, machine: machine, tracking: tracking}
}

// clientKeyHeader 玩家拉取密钥请求头（contract §5）。
const clientKeyHeader = "X-Client-Key"

// machineIDHeader 玩家机器码请求头（contract §5，FR-092）。客户端生成、不可信，仅统计/辅助限流。
const machineIDHeader = "X-Machine-Id"

// ---- 发布端点（JWT 平台管理员）----

// PublishFile POST /client-channels/:id/files — 上传客户端文件制品（运营，平台管理员）。
// multipart 表单：file（必）、codec（可，zstd|none）、expectedSha256（可，制品自身 sha256 校验）。
// 返回的 sha256 即 manifest files[].artifact.sha256；玩家按此值 GET /client-artifacts/{sha256}。
func (h *ClientVersionHandler) PublishFile(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	// 频道须存在（制品虽内容寻址跨频道共享，但发布动作绑定频道、便于审计与 404 语义）。
	if _, err := h.channel.GetChannel(channelID); err != nil {
		h.respondErr(c, err)
		return
	}

	file, header, ferr := c.Request.FormFile("file")
	if ferr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供上传文件 file"})
		return
	}
	defer file.Close()

	res, err := h.svc.PublishFile(file, service.PublishFileParams{
		Filename:       header.Filename,
		Codec:          c.PostForm("codec"),
		ExpectedSHA256: c.PostForm("expectedSha256"),
	})
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "client_file.publish", map[string]any{
		"channelId": channelID, "sha256": res.SHA256, "size": res.Size, "codec": res.Codec,
	})
	c.JSON(http.StatusCreated, res)
}

// publishVersionRequest 发布版本请求体（contract §2）。
type publishVersionRequest struct {
	Files       []service.ManifestFile `json:"files" binding:"required"`
	ManagedDirs []string               `json:"managedDirs"`
	Agent       *service.ManifestAgent `json:"agent"`
	Note        string                 `json:"note"`
}

// PublishVersion POST /client-channels/:id/versions — 发布版本并切 latest 指针（运营，平台管理员）。
// version 由服务端单调递增分配（防降级基准，contract §3）；不接受客户端指定版本号。
func (h *ClientVersionHandler) PublishVersion(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	var body publishVersionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	uid, _ := c.Get(middleware.CtxUserID)
	createdBy, _ := uid.(uint)
	ver, err := h.svc.PublishVersion(channelID, service.PublishVersionParams{
		Files:       body.Files,
		ManagedDirs: body.ManagedDirs,
		Agent:       body.Agent,
		Note:        body.Note,
		CreatedBy:   createdBy,
	})
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "client_version.publish", map[string]any{
		"channelId": channelID, "version": ver.Version, "fileCount": len(body.Files),
	})
	c.JSON(http.StatusCreated, gin.H{
		"id": ver.ID, "channelId": ver.ChannelID, "version": ver.Version,
		"note": ver.Note, "createdAt": ver.CreatedAt,
	})
}

// ListVersions GET /client-channels/:id/versions — 版本历史列表（运营，平台管理员；FR-088）。
// 历史**仅供管理面**（运营回滚/审计）；玩家侧只认 latest（contract §2），不经此端点拉取任意版本。
func (h *ClientVersionHandler) ListVersions(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	versions, err := h.svc.ListVersions(c.Param("id"))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, versions)
}

// GetVersion GET /client-channels/:id/versions/:version — 版本详情（含文件清单，运营，平台管理员；FR-088）。
func (h *ClientVersionHandler) GetVersion(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "版本号非法"})
		return
	}
	detail, err := h.svc.GetVersionDetail(c.Param("id"), version)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// clientRollbackRequest 运营回滚请求体（FR-088）。
type clientRollbackRequest struct {
	// SourceVersion 要回滚到的历史版本号（其内容将以更高版本号重发为新 latest）。
	SourceVersion int `json:"sourceVersion"`
	// Note 回滚备注（信息性，可空；空则服务端生成「回滚至 vN」）。
	Note string `json:"note"`
}

// RollbackVersion POST /client-channels/:id/rollback — 运营回滚（运营，平台管理员；FR-088）。
// 以更高版本号重发历史内容为新 latest（不下发更低号、保持单调、不触发客户端防降级，ADR-022 §3）。
func (h *ClientVersionHandler) RollbackVersion(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	var body clientRollbackRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.SourceVersion <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供有效的 sourceVersion"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	createdBy, _ := uid.(uint)
	ver, err := h.svc.Rollback(channelID, body.SourceVersion, createdBy, body.Note)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "client_version.rollback", map[string]any{
		"channelId": channelID, "sourceVersion": body.SourceVersion, "newVersion": ver.Version,
	})
	c.JSON(http.StatusCreated, gin.H{
		"id": ver.ID, "channelId": ver.ChannelID, "version": ver.Version,
		"sourceVersion": body.SourceVersion, "note": ver.Note, "createdAt": ver.CreatedAt,
	})
}

// ---- 消费端点（X-Client-Key 玩家鉴权）----

// GetManifest GET /client-channels/:id/manifest — 返回频道 latest 的签名 manifest（玩家，拉取密钥鉴权）。
// 鉴权：X-Client-Key 经 VerifyKey 校验绑定本频道（吊销/过期/不匹配→401）。
// 缓存：ETag=version:keyId、Cache-Control 短缓存（CDN 友好）；If-None-Match 命中返回 304（contract §4.1）。
func (h *ClientVersionHandler) GetManifest(c *gin.Context) {
	channelID := c.Param("id")
	if !h.authChannelKey(c, channelID) {
		return
	}
	start := time.Now()
	mid := c.GetHeader(machineIDHeader)

	// 机器码登记（FR-092）：鉴权通过后若携带 X-Machine-Id 则 best-effort upsert（弱一致、失败不阻断）。
	// 机器码不可信，仅统计/辅助限流（限流主键为 IP，FR-096）。
	if h.machine != nil && mid != "" {
		_ = h.machine.Record(channelID, mid)
	}

	// 拉取追踪（FR-093）：响应写出后记录 version/bytes/status/耗时（best-effort、不阻断）。
	manifestVersion := 0
	defer func() {
		if h.tracking != nil {
			_ = h.tracking.Record(service.ClientDistEventInput{
				ChannelID: channelID, MachineID: mid, IP: c.ClientIP(), Kind: "manifest",
				Version: manifestVersion, Bytes: int64(c.Writer.Size()), Status: c.Writer.Status(),
				DurationMs: time.Since(start).Milliseconds(),
			})
		}
	}()

	manifest, err := h.svc.BuildManifest(channelID)
	if err != nil {
		h.respondConsumerErr(c, err)
		return
	}
	manifestVersion = manifest.Version

	// ETag = version:keyId（内容随版本/签名密钥变化；contract §4.1）。
	keyID := ""
	if manifest.Sig != nil {
		keyID = manifest.Sig.KeyID
	}
	etag := fmt.Sprintf(`"%d:%s"`, manifest.Version, keyID)
	c.Header("ETag", etag)
	c.Header("Cache-Control", "no-cache") // manifest 随版本变，须每次校验新鲜度（弱缓存，靠 ETag 命中省传输）。
	if match := c.GetHeader("If-None-Match"); match == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.JSON(http.StatusOK, manifest)
}

// GetArtifact GET /client-artifacts/:sha256 — 按内容寻址下载客户端制品（玩家，拉取密钥鉴权）。
// 鉴权：X-Client-Key 经 VerifyAnyKey 校验（路径无频道段；制品跨频道共享，任一有效密钥授权路由）。
// 分发：http.ServeContent 自动处理 Range/If-Range（206 部分内容、416 越界）+ 强缓存（内容寻址不可变）。
func (h *ClientVersionHandler) GetArtifact(c *gin.Context) {
	channelID, ok := h.authAnyKey(c)
	if !ok {
		return
	}
	sha := c.Param("sha256")
	start := time.Now()
	// 下载追踪（FR-093）：响应写出后记录字节/状态/耗时（best-effort、不阻断）。
	// 频道取自密钥归属（URL 内容寻址、不带频道），使下载量/字节可按频道统计。
	defer func() {
		if h.tracking != nil {
			_ = h.tracking.Record(service.ClientDistEventInput{
				ChannelID: channelID, MachineID: c.GetHeader(machineIDHeader), IP: c.ClientIP(),
				Kind: "artifact", ArtifactSHA: sha, Bytes: int64(c.Writer.Size()),
				Status: c.Writer.Status(), DurationMs: time.Since(start).Milliseconds(),
			})
		}
	}()
	asset, absPath, err := h.svc.OpenArtifact(sha)
	if err != nil {
		h.respondConsumerErr(c, err)
		return
	}

	f, oerr := os.Open(absPath)
	if oerr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ARTIFACT_NOT_FOUND", "message": "制品文件缺失"})
		return
	}
	defer f.Close()
	stat, serr := f.Stat()
	if serr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取制品失败"})
		return
	}

	contentType := asset.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	// 内容寻址不可变 → 强缓存 + 内容 ETag（sha256）。
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("ETag", `"`+asset.SHA256+`"`)
	c.Header("Accept-Ranges", "bytes")
	// ServeContent 负责 Range/If-Range/If-None-Match/206/416 与 Content-Length。
	http.ServeContent(c.Writer, c.Request, asset.SHA256, stat.ModTime(), f)
}

// ---- 鉴权辅助 ----

// authChannelKey 校验请求头 X-Client-Key 绑定指定频道；失败已写响应并返回 false。
func (h *ClientVersionHandler) authChannelKey(c *gin.Context, channelID string) bool {
	plaintext := c.GetHeader(clientKeyHeader)
	if _, err := h.channel.VerifyKey(channelID, plaintext); err != nil {
		h.respondKeyAuthErr(c, err)
		return false
	}
	return true
}

// authAnyKey 校验请求头 X-Client-Key 为任一有效密钥（不绑定频道）；失败已写响应并返回 false。
// 成功时返回密钥所属频道 ID——制品下载 URL 内容寻址、不带频道，靠密钥归属频道以供按频道统计（FR-093/095）。
func (h *ClientVersionHandler) authAnyKey(c *gin.Context) (string, bool) {
	plaintext := c.GetHeader(clientKeyHeader)
	key, err := h.channel.VerifyAnyKey(plaintext)
	if err != nil {
		h.respondKeyAuthErr(c, err)
		return "", false
	}
	return key.ChannelID, true
}

// respondKeyAuthErr 统一拉取密钥鉴权失败响应（不区分缺失/吊销/过期，避免泄露密钥状态）。
func (h *ClientVersionHandler) respondKeyAuthErr(c *gin.Context, err error) {
	if errors.Is(err, service.ErrPullKeyInvalid) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "INVALID_CLIENT_KEY", "message": "拉取密钥无效"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "鉴权失败"})
}

// respondErr 发布端点错误映射（频道/清单/制品）。
func (h *ClientVersionHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
	case errors.Is(err, service.ErrInvalidVersionFiles):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_VERSION_FILES", "message": err.Error()})
	case errors.Is(err, service.ErrVersionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "VERSION_NOT_FOUND", "message": "版本不存在"})
	case errors.Is(err, service.ErrChecksumMismatch):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "CHECKSUM_MISMATCH", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
	}
}

// respondConsumerErr 消费端点错误映射（manifest/制品；鉴权已先行通过）。
func (h *ClientVersionHandler) respondConsumerErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
	case errors.Is(err, service.ErrNoLatestVersion):
		c.JSON(http.StatusNotFound, gin.H{"error": "NO_LATEST_VERSION", "message": "频道尚未发布版本"})
	case errors.Is(err, service.ErrAssetNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "ARTIFACT_NOT_FOUND", "message": "制品不存在"})
	case errors.Is(err, service.ErrSignKeyNotConfigured):
		c.JSON(http.StatusInternalServerError, gin.H{"error": "SIGN_KEY_NOT_CONFIGURED", "message": "签名私钥未配置"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
	}
}

// recordAudit 记录发布操作审计（detail 仅含可公开元数据）。
func (h *ClientVersionHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	_ = h.audit.Record(id, action, "client_channel", "", string(raw), c.ClientIP())
}

// ListEvents GET /client-dist/events — 拉取/下载明细检索（运营，平台管理员；FR-093）。
// 按 channelId/machineId/ip/kind/version/since/until/limit 过滤，created_at DESC。
func (h *ClientVersionHandler) ListEvents(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	if h.tracking == nil {
		c.JSON(http.StatusOK, []any{})
		return
	}
	f := service.ClientDistEventFilter{
		ChannelID: c.Query("channelId"),
		MachineID: c.Query("machineId"),
		IP:        c.Query("ip"),
		Kind:      c.Query("kind"),
	}
	if v := c.Query("version"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Version = &n
		}
	}
	if v := c.Query("since"); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = &ts
		}
	}
	if v := c.Query("until"); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = &ts
		}
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	events, err := h.tracking.QueryEvents(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "检索失败"})
		return
	}
	c.JSON(http.StatusOK, events)
}

// RegisterPublishRoutes 注册发布端点（运营操作，须挂 JWT 平台管理员组）。
func (h *ClientVersionHandler) RegisterPublishRoutes(rg *gin.RouterGroup) {
	ch := rg.Group("/client-channels")
	{
		ch.POST("/:id/files", h.PublishFile)
		ch.POST("/:id/versions", h.PublishVersion)
		// 版本历史 / 详情 / 回滚（FR-088）：仅管理面，与玩家拉取密钥端点物理隔离。
		ch.GET("/:id/versions", h.ListVersions)
		ch.GET("/:id/versions/:version", h.GetVersion)
		ch.POST("/:id/rollback", h.RollbackVersion)
	}
	// 拉取/下载明细检索（FR-093）：管理面。
	rg.GET("/client-dist/events", h.ListEvents)
}

// RegisterConsumerRoutes 注册面向玩家的消费端点（须挂公网组：拉取密钥鉴权，与 JWT 入口隔离）。
func (h *ClientVersionHandler) RegisterConsumerRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-channels/:id/manifest", h.GetManifest)
	rg.GET("/client-artifacts/:sha256", h.GetArtifact)
}
