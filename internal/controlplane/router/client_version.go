package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

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
	svc     *service.ClientVersionService
	channel *service.ClientChannelService
	audit   *service.AuditService
}

// NewClientVersionHandler 创建客户端分发版本/消费端点处理器。
func NewClientVersionHandler(svc *service.ClientVersionService, channel *service.ClientChannelService, audit *service.AuditService) *ClientVersionHandler {
	return &ClientVersionHandler{svc: svc, channel: channel, audit: audit}
}

// clientKeyHeader 玩家拉取密钥请求头（contract §5）。
const clientKeyHeader = "X-Client-Key"

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

// ---- 消费端点（X-Client-Key 玩家鉴权）----

// GetManifest GET /client-channels/:id/manifest — 返回频道 latest 的签名 manifest（玩家，拉取密钥鉴权）。
// 鉴权：X-Client-Key 经 VerifyKey 校验绑定本频道（吊销/过期/不匹配→401）。
// 缓存：ETag=version:keyId、Cache-Control 短缓存（CDN 友好）；If-None-Match 命中返回 304（contract §4.1）。
func (h *ClientVersionHandler) GetManifest(c *gin.Context) {
	channelID := c.Param("id")
	if !h.authChannelKey(c, channelID) {
		return
	}

	manifest, err := h.svc.BuildManifest(channelID)
	if err != nil {
		h.respondConsumerErr(c, err)
		return
	}

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
	if !h.authAnyKey(c) {
		return
	}
	sha := c.Param("sha256")
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
func (h *ClientVersionHandler) authAnyKey(c *gin.Context) bool {
	plaintext := c.GetHeader(clientKeyHeader)
	if _, err := h.channel.VerifyAnyKey(plaintext); err != nil {
		h.respondKeyAuthErr(c, err)
		return false
	}
	return true
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

// RegisterPublishRoutes 注册发布端点（运营操作，须挂 JWT 平台管理员组）。
func (h *ClientVersionHandler) RegisterPublishRoutes(rg *gin.RouterGroup) {
	ch := rg.Group("/client-channels")
	{
		ch.POST("/:id/files", h.PublishFile)
		ch.POST("/:id/versions", h.PublishVersion)
	}
}

// RegisterConsumerRoutes 注册面向玩家的消费端点（须挂公网组：拉取密钥鉴权，与 JWT 入口隔离）。
func (h *ClientVersionHandler) RegisterConsumerRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-channels/:id/manifest", h.GetManifest)
	rg.GET("/client-artifacts/:sha256", h.GetArtifact)
}
