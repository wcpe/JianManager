package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
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
	security *service.ClientDistSecurityService
}

// NewClientVersionHandler 创建客户端分发版本/消费端点处理器。machine/tracking 可为 nil（不登记机器码/不追踪）。
func NewClientVersionHandler(svc *service.ClientVersionService, channel *service.ClientChannelService, audit *service.AuditService, machine *service.ClientMachineService, tracking *service.ClientDistTrackingService, security ...*service.ClientDistSecurityService) *ClientVersionHandler {
	var sec *service.ClientDistSecurityService
	if len(security) > 0 {
		sec = security[0]
	}
	return &ClientVersionHandler{svc: svc, channel: channel, audit: audit, machine: machine, tracking: tracking, security: sec}
}

// clientKeyHeader 玩家拉取密钥请求头（contract §5）。
const clientKeyHeader = "X-Client-Key"

// machineIDHeader 玩家机器码请求头（contract §5，FR-092）。客户端生成、不可信，仅统计/辅助限流。
const machineIDHeader = "X-Machine-Id"

// installIDHeader 安装标识请求头（FR-264）。客户端生成、不可信，仅用于关联安全画像。
const installIDHeader = "X-Install-Id"

// playerNameHeader 玩家名请求头。客户端从 jm-updater.json 构造，不可信，仅用于观测与排障。
const playerNameHeader = "X-Player-Name"

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
	Files        []service.ManifestFile `json:"files" binding:"required"`
	ManagedDirs  []string               `json:"managedDirs"`
	CleanExclude []string               `json:"cleanExclude"`
	Agent        *service.ManifestAgent `json:"agent"`
	Note         string                 `json:"note"`
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
		Files:        body.Files,
		ManagedDirs:  body.ManagedDirs,
		CleanExclude: body.CleanExclude,
		Agent:        body.Agent,
		Note:         body.Note,
		CreatedBy:    createdBy,
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

// GetArtifactContent GET /client-channels/:id/files/content?sha256= — 管理面文本预览制品内容（运营，平台管理员；FR-214）。
//
// 玩家消费端点 GET /client-artifacts/:sha256 走拉取密钥鉴权、与浏览器 JWT 入口物理隔离（ADR-022/023），
// 管理台无拉取密钥不能复用之取预览。故补此 JWT **只读**端点，仅服务发布页/版本详情的内容预览（FR-214）。
// 二进制/压缩/超大由服务层判定并以 kind 显式返回，前端据此渲染或降级。
func (h *ClientVersionHandler) GetArtifactContent(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	sha := c.Query("sha256")
	if sha == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供 sha256"})
		return
	}
	preview, err := h.svc.ReadArtifactText(sha)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

// DownloadArtifact GET /client-channels/:id/files/download?sha256= — 管理面下载制品（运营，平台管理员；FR-214）。
//
// 与 GetArtifactContent 同理：玩家制品端点走拉取密钥，管理台浏览器需一个 JWT 下载入口（含降级态「不可预览必可下载」）。
// 复用 OpenArtifact 取物理文件，附下载文件名（取 sha 末段，前端 source 会以 manifest path 末段重命名亦可）。
func (h *ClientVersionHandler) DownloadArtifact(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	sha := c.Query("sha256")
	if sha == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供 sha256"})
		return
	}
	asset, absPath, err := h.svc.OpenArtifact(sha)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	// 失效制品（FR-349）：外置对象缺失，管理面下载同样给明确 410（提示重传自愈）。
	if asset.StorageState == model.AssetStorageLost {
		c.JSON(http.StatusGone, gin.H{"error": "ARTIFACT_LOST", "message": "制品外置对象已缺失，重传同内容文件即可恢复"})
		return
	}
	contentType := asset.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// s3 制品：CP 经 BlobStore 代理直流，不走 302（FR-347，见 ADR-073 决策 6）——浏览器
	// axios blob fetch 跨域跟随预签名 URL 会撞对象存储 CORS；管理面下载是低频运维动作，
	// CP 中继代价可接受、前端零改动、对象存储无需配 CORS。
	if asset.StorageBackend == model.AssetBackendS3 {
		rc, oerr := h.svc.OpenArtifactContent(asset, absPath)
		if oerr != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "ARTIFACT_NOT_FOUND", "message": "制品文件缺失"})
			return
		}
		defer rc.Close()
		c.Header("Content-Type", contentType)
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", asset.SHA256))
		c.Header("Content-Length", strconv.FormatInt(asset.Size, 10))
		c.Status(http.StatusOK)
		_, _ = io.Copy(c.Writer, rc)
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
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", asset.SHA256))
	http.ServeContent(c.Writer, c.Request, asset.SHA256, stat.ModTime(), f)
}

// ---- 消费端点（X-Client-Key 玩家鉴权）----

// GetManifest GET /client-channels/:id/manifest — 返回频道 latest 的签名 manifest（玩家，拉取密钥鉴权）。
// 鉴权：X-Client-Key 经 VerifyKey 校验绑定本频道（吊销/过期/不匹配→401）。
// 缓存：ETag=version:keyId、Cache-Control 短缓存（CDN 友好）；If-None-Match 命中返回 304（contract §4.1）。
func (h *ClientVersionHandler) GetManifest(c *gin.Context) {
	channelID := c.Param("id")
	start := time.Now()
	mid := c.GetHeader(machineIDHeader)

	// 拉取追踪（FR-093/249）：defer 前置到鉴权之前注册，闭包捕获最终 status/errCode——
	// 鉴权失败(401)也记事件（此前漏记）。best-effort、不阻断玩家。
	manifestVersion := 0
	errCode := ""
	responseBody := ""
	defer func() {
		if h.tracking != nil {
			_ = h.tracking.Record(service.ClientDistEventInput{
				ChannelID: channelID, MachineID: mid, IP: c.ClientIP(), Kind: "manifest",
				Version: manifestVersion, Bytes: int64(c.Writer.Size()), Status: c.Writer.Status(),
				ErrCode: errCode, DurationMs: time.Since(start).Milliseconds(), Method: c.Request.Method,
				Path: c.Request.URL.RequestURI(), ResponseBody: responseBodyForLog(responseBody, errCode),
				RequestHeaders: requestHeaderMap(c), ResponseHeaders: responseHeaderMap(c),
			})
		}
	}()

	key, ec, ok := h.authChannelKey(c, channelID)
	if !ok {
		errCode = ec
		return
	}
	if h.security != nil {
		if err := h.checkCommonSecurity(c, channelID, key); err != nil {
			errCode = h.respondSecurityErr(c, err)
			return
		}
	}

	// 机器码登记（FR-092）：鉴权通过后若携带 X-Machine-Id 则 best-effort upsert（弱一致、失败不阻断）。
	// 机器码不可信，仅统计/辅助限流（限流主键为 IP，FR-096）。
	if h.machine != nil && mid != "" {
		_ = h.machine.Record(channelID, mid)
	}

	manifest, err := h.svc.BuildManifest(channelID)
	if err != nil {
		errCode = h.respondConsumerErr(c, err)
		return
	}
	manifestVersion = manifest.Version

	// ETag = version（内容随版本变化；contract §4.1）。FR-256 起不再签名，ETag 只用 version。
	etag := fmt.Sprintf(`"%d"`, manifest.Version)
	c.Header("ETag", etag)
	c.Header("Cache-Control", "no-cache") // manifest 随版本变，须每次校验新鲜度（弱缓存，靠 ETag 命中省传输）。
	if match := c.GetHeader("If-None-Match"); match == etag {
		c.Status(http.StatusNotModified)
		return
	}
	if raw, merr := json.Marshal(manifest); merr == nil {
		responseBody = string(raw)
	}
	c.JSON(http.StatusOK, manifest)
}

// GetArtifact GET /client-artifacts/:sha256 — 按内容寻址下载客户端制品（玩家，拉取密钥鉴权）。
// 鉴权：X-Client-Key 经 VerifyAnyKey 校验（路径无频道段；制品跨频道共享，任一有效密钥授权路由）。
// 分发：http.ServeContent 自动处理 Range/If-Range（206 部分内容、416 越界）+ 强缓存（内容寻址不可变）。
func (h *ClientVersionHandler) GetArtifact(c *gin.Context) {
	sha := c.Param("sha256")
	start := time.Now()
	// 下载追踪（FR-093/249）：defer 前置到鉴权之前，闭包捕获最终 status/errCode——鉴权失败(401)也记事件。
	// 频道取自密钥归属（URL 内容寻址、不带频道）；鉴权失败时频道未知，ChannelID 记空可接受。best-effort、不阻断。
	channelID := ""
	errCode := ""
	responseBody := ""
	defer func() {
		if h.tracking != nil {
			_ = h.tracking.Record(service.ClientDistEventInput{
				ChannelID: channelID, MachineID: c.GetHeader(machineIDHeader), IP: c.ClientIP(),
				Kind: "artifact", ArtifactSHA: sha, Bytes: int64(c.Writer.Size()),
				Status: c.Writer.Status(), ErrCode: errCode, DurationMs: time.Since(start).Milliseconds(), Method: c.Request.Method,
				Path: c.Request.URL.RequestURI(), ResponseBody: responseBodyForLog(responseBody, errCode),
				RequestHeaders: requestHeaderMap(c), ResponseHeaders: responseHeaderMap(c),
			})
		}
	}()

	key, ch, ec, ok := h.authAnyKey(c)
	if !ok {
		errCode = ec
		return
	}
	channelID = ch
	asset, absPath, err := h.svc.OpenArtifact(sha)
	if err != nil {
		errCode = h.respondConsumerErr(c, err)
		return
	}
	if h.security != nil {
		if err := h.checkCommonSecurity(c, channelID, key); err != nil {
			errCode = h.respondSecurityErr(c, err)
			return
		}
		if mode, err := h.security.ChannelProtectionMode(channelID); err == nil && mode == service.ClientChannelModeProtected {
			errCode = h.respondSecurityErr(c, service.ErrChannelProtected)
			return
		}
		if ok, err := h.security.IsArtifactAllowedForChannel(channelID, sha); err != nil || !ok {
			if err != nil {
				errCode = h.respondSecurityErr(c, err)
			} else {
				errCode = h.respondSecurityErr(c, service.ErrArtifactNotAllowed)
			}
			return
		}
		if invalidMultiRange(c.GetHeader("Range")) {
			_ = h.security.RecordRiskEvent("INVALID_RANGE", channelID, c.GetHeader(machineIDHeader), "", "", c.ClientIP(), key, "medium", nil)
			errCode = "INVALID_RANGE"
			c.JSON(http.StatusRequestedRangeNotSatisfiable, gin.H{"error": "INVALID_RANGE", "message": "不支持多段 Range"})
			return
		}
		if smallRange(c.GetHeader("Range")) {
			_ = h.security.RecordRiskEvent("RANGE_SMALL", channelID, c.GetHeader(machineIDHeader), "", "", c.ClientIP(), key, "low", nil)
		}
		lease, err := h.security.AcquireArtifact(c.ClientIP(), key.ID, channelID)
		if err != nil {
			errCode = h.respondSecurityErr(c, err)
			return
		}
		defer lease.Release()
	}

	// 失效制品（FR-349）：完整鉴权/安全策略通过后、后端分流前返回 410，
	// 不泄露未授权制品状态，也不再 302 到必 404 的预签名 URL。重传同内容即自愈。
	if asset.StorageState == model.AssetStorageLost {
		errCode = "ARTIFACT_LOST"
		c.JSON(http.StatusGone, gin.H{"error": "ARTIFACT_LOST", "message": "制品外置对象已缺失，请联系运营重传"})
		return
	}

	// s3 制品：鉴权/防护/限流/带宽检查全部照旧先行后，302 到预签名短时效 URL
	//（FR-347，见 ADR-073 决策 1）。CP 只当授权与调度面，字节流量走对象存储出口；
	// updater 已 setInstanceFollowRedirects(true) 自动跟随（跨协议限制见 spec §3.8 部署约束）。
	if asset.StorageBackend == model.AssetBackendS3 {
		if h.security != nil {
			if err := h.security.CheckBandwidth(c.ClientIP(), key.ID, channelID, asset.Size); err != nil {
				errCode = h.respondSecurityErr(c, err)
				return
			}
		}
		loc, perr := h.svc.PresignArtifactURL(asset)
		if perr != nil {
			// 渠道缺失/凭证解密失败：对 updater 给可重试语义（快失败，运维恢复渠道即愈）。
			errCode = "ARTIFACT_STORAGE_UNAVAILABLE"
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ARTIFACT_STORAGE_UNAVAILABLE", "message": "制品外置存储暂不可用"})
			return
		}
		// 短时效预签名 URL 禁缓存（缓存会把带签名的 URL 固化成过期死链）。
		c.Header("Cache-Control", "no-store")
		c.Redirect(http.StatusFound, loc)
		return
	}

	f, oerr := os.Open(absPath)
	if oerr != nil {
		errCode = "ARTIFACT_NOT_FOUND"
		c.JSON(http.StatusNotFound, gin.H{"error": "ARTIFACT_NOT_FOUND", "message": "制品文件缺失"})
		return
	}
	defer f.Close()
	stat, serr := f.Stat()
	if serr != nil {
		errCode = "INTERNAL_ERROR"
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取制品失败"})
		return
	}
	if h.security != nil {
		if err := h.security.CheckBandwidth(c.ClientIP(), key.ID, channelID, asset.Size); err != nil {
			errCode = h.respondSecurityErr(c, err)
			return
		}
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

// authChannelKey 校验请求头 X-Client-Key 绑定指定频道；失败已写响应并返回 (错误码, false)。
// 成功返回 ("", true)。错误码供调用方记入追踪事件（FR-249）。
func (h *ClientVersionHandler) authChannelKey(c *gin.Context, channelID string) (*model.ClientPullKey, string, bool) {
	if h.security != nil {
		if _, blocked := h.security.ActiveIPBlock(c.ClientIP()); blocked {
			return nil, h.respondSecurityErr(c, service.ErrIPTempBlocked), false
		}
	}
	plaintext := c.GetHeader(clientKeyHeader)
	var key *model.ClientPullKey
	var err error
	if h.security != nil {
		var check *service.SecurityKeyCheck
		check, err = h.security.VerifyChannelKey(channelID, plaintext)
		if check != nil {
			key = check.Key
		}
	} else {
		key, err = h.channel.VerifyKey(channelID, plaintext)
	}
	if err != nil {
		return nil, h.respondKeyAuthErr(c, err), false
	}
	return key, "", true
}

// authAnyKey 校验请求头 X-Client-Key 为任一有效密钥（不绑定频道）；失败已写响应并返回 (频道空, 错误码, false)。
// 成功时返回密钥所属频道 ID——制品下载 URL 内容寻址、不带频道，靠密钥归属频道以供按频道统计（FR-093/095）。
// 错误码供调用方记入追踪事件（FR-249；鉴权失败时频道未知，事件 ChannelID 记空可接受）。
func (h *ClientVersionHandler) authAnyKey(c *gin.Context) (*model.ClientPullKey, string, string, bool) {
	if h.security != nil {
		if _, blocked := h.security.ActiveIPBlock(c.ClientIP()); blocked {
			return nil, "", h.respondSecurityErr(c, service.ErrIPTempBlocked), false
		}
	}
	plaintext := c.GetHeader(clientKeyHeader)
	var key *model.ClientPullKey
	var err error
	if h.security != nil {
		var check *service.SecurityKeyCheck
		check, err = h.security.VerifyAnyKey(plaintext)
		if check != nil {
			key = check.Key
		}
	} else {
		key, err = h.channel.VerifyAnyKey(plaintext)
	}
	if err != nil {
		return nil, "", h.respondKeyAuthErr(c, err), false
	}
	return key, key.ChannelID, "", true
}

// respondKeyAuthErr 统一拉取密钥鉴权失败响应（不区分缺失/吊销/过期，避免泄露密钥状态）；返回所写错误码（FR-249）。
func (h *ClientVersionHandler) respondKeyAuthErr(c *gin.Context, err error) string {
	if h.security != nil && (errors.Is(err, service.ErrClientKeySuspended) || errors.Is(err, service.ErrClientKeyThrottled)) {
		return respondSecurityAuthErr(c, h.security, err)
	}
	if errors.Is(err, service.ErrPullKeyInvalid) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "INVALID_CLIENT_KEY", "message": "拉取密钥无效"})
		return "INVALID_CLIENT_KEY"
	}
	slog.Error("客户端分发鉴权失败", "channel", c.Param("id"), "error", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "鉴权失败"})
	return "INTERNAL_ERROR"
}

func (h *ClientVersionHandler) checkCommonSecurity(c *gin.Context, channelID string, key *model.ClientPullKey) error {
	if h.security == nil {
		return nil
	}
	if key != nil {
		if err := h.security.CheckRate("key", strconv.FormatUint(uint64(key.ID), 10)); err != nil {
			return err
		}
	}
	return h.security.CheckRate("channel", channelID)
}

func (h *ClientVersionHandler) respondSecurityErr(c *gin.Context, err error) string {
	switch {
	case errors.Is(err, service.ErrIPTempBlocked):
		setRetryAfter(c, h.security)
		c.JSON(http.StatusForbidden, gin.H{"error": "IP_TEMP_BLOCKED", "message": "IP 已临时封禁"})
		return "IP_TEMP_BLOCKED"
	case errors.Is(err, service.ErrChannelProtected):
		setRetryAfter(c, h.security)
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "CHANNEL_PROTECTED", "message": "频道保护中，请稍后再试"})
		return "CHANNEL_PROTECTED"
	case errors.Is(err, service.ErrArtifactNotAllowed):
		c.JSON(http.StatusForbidden, gin.H{"error": "ARTIFACT_NOT_ALLOWED", "message": "制品不属于当前频道"})
		return "ARTIFACT_NOT_ALLOWED"
	case errors.Is(err, service.ErrDownloadConcurrencyLimited):
		setRetryAfter(c, h.security)
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "DOWNLOAD_CONCURRENCY_LIMITED", "message": "下载并发过高"})
		return "DOWNLOAD_CONCURRENCY_LIMITED"
	case errors.Is(err, service.ErrBandwidthLimited):
		setRetryAfter(c, h.security)
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "BANDWIDTH_LIMITED", "message": "带宽配额已用尽"})
		return "BANDWIDTH_LIMITED"
	case errors.Is(err, service.ErrRateLimited):
		setRetryAfter(c, h.security)
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "RATE_LIMITED", "message": "请求过于频繁，请稍后再试"})
		return "RATE_LIMITED"
	default:
		slog.Error("客户端分发安全检查失败", "path", c.Request.URL.Path, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "安全检查失败"})
		return "INTERNAL_ERROR"
	}
}

func invalidMultiRange(header string) bool { return strings.Contains(header, ",") }
func smallRange(header string) bool {
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "bytes="), "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	start, e1 := strconv.ParseInt(parts[0], 10, 64)
	end, e2 := strconv.ParseInt(parts[1], 10, 64)
	return e1 == nil && e2 == nil && end >= start && end-start+1 <= 4
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
	case errors.Is(err, service.ErrAssetNotFound):
		// 管理面制品内容/下载（FR-214）取不存在的制品 → 404。
		c.JSON(http.StatusNotFound, gin.H{"error": "ARTIFACT_NOT_FOUND", "message": "制品不存在"})
	case errors.Is(err, service.ErrArtifactLost):
		// 失效制品（FR-349）：索引在、外置对象缺失 → 410（重传同内容自愈）。
		c.JSON(http.StatusGone, gin.H{"error": "ARTIFACT_LOST", "message": "制品外置对象已缺失，重传同内容文件即可恢复"})
	case errors.Is(err, service.ErrChecksumMismatch):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "CHECKSUM_MISMATCH", "message": err.Error()})
	default:
		slog.Error("客户端分发发布端点内部错误", "path", c.Request.URL.Path, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
	}
}

// respondConsumerErr 消费端点错误映射（manifest/制品/core；鉴权已先行通过）；返回所写错误码供追踪（FR-249）。
func (h *ClientVersionHandler) respondConsumerErr(c *gin.Context, err error) string {
	switch {
	case errors.Is(err, service.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
		return "CHANNEL_NOT_FOUND"
	case errors.Is(err, service.ErrNoLatestVersion):
		c.JSON(http.StatusNotFound, gin.H{"error": "NO_LATEST_VERSION", "message": "频道尚未发布版本"})
		return "NO_LATEST_VERSION"
	case errors.Is(err, service.ErrNoCoreVersion):
		// FR-259：无 core 归档 → 404（coreEndpoint 端点）。
		c.JSON(http.StatusNotFound, gin.H{"error": "NO_CORE_VERSION", "message": "无已归档的 updater-core 版本"})
		return "NO_CORE_VERSION"
	case errors.Is(err, service.ErrAssetNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "ARTIFACT_NOT_FOUND", "message": "制品不存在"})
		return "ARTIFACT_NOT_FOUND"
	case errors.Is(err, service.ErrArtifactLost):
		// 失效制品（FR-349）：明确 410 终态（防御映射；GetArtifact 已在分流前拦截）。
		c.JSON(http.StatusGone, gin.H{"error": "ARTIFACT_LOST", "message": "制品外置对象已缺失，请联系运营重传"})
		return "ARTIFACT_LOST"
	default:
		slog.Error("客户端分发消费端点内部错误", "path", c.Request.URL.Path, "channel", c.Param("id"), "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
		return "INTERNAL_ERROR"
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

// ListEvents GET /client-dist/events — 拉取/下载明细检索（运营，平台管理员；FR-093/249）。
// 按 channelId/machineId/ip/kind/outcome/errCode/version/since/until/limit 过滤，created_at DESC。
func requestHeaderMap(c *gin.Context) map[string]string {
	out := map[string]string{}
	for k, vals := range c.Request.Header {
		out[k] = strings.Join(vals, ", ")
	}
	return out
}

func responseHeaderMap(c *gin.Context) map[string]string {
	out := map[string]string{}
	for k, vals := range c.Writer.Header() {
		out[k] = strings.Join(vals, ", ")
	}
	return out
}

func responseBodyForLog(body, errCode string) string {
	if body != "" {
		return body
	}
	if errCode == "" {
		return ""
	}
	raw, _ := json.Marshal(gin.H{"error": errCode})
	return string(raw)
}

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
		Outcome:   c.Query("outcome"),
		ErrCode:   c.Query("errCode"),
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
		// 管理面制品内容预览 / 下载（FR-214）：JWT 平台管理员，供发布页/版本详情复用共享文件浏览器。
		// 与玩家 GET /client-artifacts/:sha256（拉取密钥）物理隔离——浏览器无拉取密钥不能复用之。
		ch.GET("/:id/files/content", h.GetArtifactContent)
		ch.GET("/:id/files/download", h.DownloadArtifact)
		// updater-core 版本管理（FR-259）：列出归档版本 + 手动上传 hotfix + 切换频道选定版本（回滚）。
		ch.GET("/:id/updater-core/versions", h.ListUpdaterCoreVersions)
		ch.POST("/:id/updater-core/versions", h.UploadUpdaterCoreVersion)
		ch.PUT("/:id/updater-core/selected", h.SelectUpdaterCore)
	}
	// 拉取/下载明细检索（FR-093）：管理面。
	rg.GET("/client-dist/events", h.ListEvents)
}

// RegisterConsumerRoutes 注册面向玩家的消费端点（须挂公网组：拉取密钥鉴权，与 JWT 入口隔离）。
func (h *ClientVersionHandler) RegisterConsumerRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-channels/:id/manifest", h.GetManifest)
	rg.GET("/client-artifacts/:sha256", h.GetArtifact)
	// updater-core 版本查询（FR-259）：楔子经 coreEndpoint 拉取选定版本信息，拉取密钥鉴权。
	rg.GET("/client-channels/:id/updater-core", h.GetUpdaterCore)
}

// ---- updater-core 版本管理端点（FR-259）----

// GetUpdaterCore GET /client-channels/:id/updater-core — 返回频道选定 core 版本信息（玩家，拉取密钥鉴权）。
// 返回 {version, sha256, downloadUrl, size}（spec §2.5.3 冻结格式），楔子据此下载 core jar。
func (h *ClientVersionHandler) GetUpdaterCore(c *gin.Context) {
	channelID := c.Param("id")
	start := time.Now()
	errCode := ""
	responseBody := ""
	defer func() {
		if h.tracking != nil {
			_ = h.tracking.Record(service.ClientDistEventInput{
				ChannelID: channelID, MachineID: c.GetHeader(machineIDHeader), IP: c.ClientIP(), Kind: "core",
				Bytes: int64(c.Writer.Size()), Status: c.Writer.Status(), ErrCode: errCode,
				DurationMs: time.Since(start).Milliseconds(), Method: c.Request.Method,
				Path: c.Request.URL.RequestURI(), ResponseBody: responseBodyForLog(responseBody, errCode),
				RequestHeaders: requestHeaderMap(c), ResponseHeaders: responseHeaderMap(c),
			})
		}
	}()

	key, ec, ok := h.authChannelKey(c, channelID)
	if !ok {
		errCode = ec
		return
	}
	if h.security != nil {
		if err := h.checkCommonSecurity(c, channelID, key); err != nil {
			errCode = h.respondSecurityErr(c, err)
			return
		}
	}
	info, err := h.svc.GetCoreEndpointInfo(channelID)
	if err != nil {
		errCode = h.respondConsumerErr(c, err)
		return
	}
	body := gin.H{
		"version":     info.Version,
		"sha256":      info.SHA256,
		"downloadUrl": resolvePublicBaseURL(c) + "/client-artifacts/" + info.SHA256,
		"size":        info.Size,
	}
	if raw, merr := json.Marshal(body); merr == nil {
		responseBody = string(raw)
	}
	c.JSON(http.StatusOK, body)
}

// ListUpdaterCoreVersions GET /client-channels/:id/updater-core/versions — 列出所有归档 core 版本（运营，平台管理员）。
func (h *ClientVersionHandler) ListUpdaterCoreVersions(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	// 校验频道存在。
	if _, err := h.channel.GetChannel(c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
		return
	}
	versions, err := h.svc.ListCoreVersions(c.Param("id"))
	if err != nil {
		slog.Error("查询 core 归档列表失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, versions)
}

// UploadUpdaterCoreVersion POST /client-channels/:id/updater-core/versions — 手动上传 updater-core.jar（运营，平台管理员；hotfix）。
func (h *ClientVersionHandler) UploadUpdaterCoreVersion(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	if _, err := h.channel.GetChannel(channelID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需上传 updater-core.jar"})
		return
	}
	opened, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "读取上传文件失败"})
		return
	}
	defer opened.Close()
	asset, err := h.svc.ArchiveCoreJar(opened, c.PostForm("version"))
	if err != nil {
		slog.Error("上传 updater-core 归档失败", "channel", channelID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "上传失败"})
		return
	}
	selected := strings.EqualFold(c.PostForm("select"), "true") || c.PostForm("select") == "1"
	if selected {
		if err := h.svc.SelectCoreVersion(channelID, asset.SHA256); err != nil {
			slog.Error("上传后选定 updater-core 失败", "channel", channelID, "sha256", asset.SHA256, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "上传后选定失败"})
			return
		}
	}
	h.recordAudit(c, "client_core.upload", map[string]any{"channelId": channelID, "sha256": asset.SHA256, "selected": selected})
	c.JSON(http.StatusOK, service.CoreVersionSummaryFromAsset(asset, selected))
}

// selectUpdaterCoreRequest 切换选定 core 版本请求体。
type selectUpdaterCoreRequest struct {
	// SHA256 要选定为当前版本的 core jar 制品 sha256。
	SHA256 string `json:"sha256" binding:"required"`
}

// SelectUpdaterCore PUT /client-channels/:id/updater-core/selected — 切换频道选定 core 版本（运营，平台管理员；FR-259 回滚）。
func (h *ClientVersionHandler) SelectUpdaterCore(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	var body selectUpdaterCoreRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.SHA256 == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供 sha256"})
		return
	}
	if err := h.svc.SelectCoreVersion(channelID, body.SHA256); err != nil {
		switch {
		case errors.Is(err, service.ErrChannelNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
		case errors.Is(err, service.ErrCoreVersionNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "CORE_VERSION_NOT_FOUND", "message": "updater-core 版本不存在"})
		default:
			slog.Error("切换选定 core 版本失败", "channel", channelID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
		}
		return
	}
	h.recordAudit(c, "client_core.select", map[string]any{"channelId": channelID, "sha256": body.SHA256})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
