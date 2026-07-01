package router

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientChunkUploadHandler 客户端分发大文件分块上传端点（FR-251，增强 FR-088 单次上传）。
//
// 鉴权与 ClientVersionHandler.PublishFile 同组：JWT 平台管理员（requirePlatformAdmin），
// 挂 admin 组。与玩家拉取密钥端点物理隔离（ADR-022/023）——分块上传是运营发布动作。
//
// 协议：init（声明文件大小得 uploadId/chunkSize/chunkCount）→ PUT 各分片（原始字节，幂等）
// → complete（校验齐全 + 拼装喂 CAS 得与单次上传一致的 {sha256,md5,size,codec}）→ DELETE 弃单。
// 本 handler 独立于 client_version.go（不改之），复用 ChunkedUploadService + ClientChannelService。
type ClientChunkUploadHandler struct {
	svc     *service.ChunkedUploadService
	channel *service.ClientChannelService
	audit   *service.AuditService
}

// NewClientChunkUploadHandler 创建分块上传端点处理器。
func NewClientChunkUploadHandler(svc *service.ChunkedUploadService, channel *service.ClientChannelService, audit *service.AuditService) *ClientChunkUploadHandler {
	return &ClientChunkUploadHandler{svc: svc, channel: channel, audit: audit}
}

// initUploadRequest init 请求体。
type initUploadRequest struct {
	// Filename 原始文件名（决定 CAS 扩展名/下载名），可空。
	Filename string `json:"filename"`
	// TotalSize 文件总字节数（必，>0）。
	TotalSize int64 `json:"totalSize"`
	// ChunkSize 期望分片大小（可空；<=0 用服务端默认，越界自动夹取）。
	ChunkSize int64 `json:"chunkSize"`
}

// InitUpload POST /client-channels/:id/uploads — 建立分块上传会话（运营，平台管理员）。
// 校验频道存在 → 建会话 → 返回 {uploadId,chunkSize,chunkCount}（前端据 chunkSize 切片）。
func (h *ClientChunkUploadHandler) InitUpload(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	// 频道须存在（制品虽跨频道共享，但上传动作绑定频道、便于审计与 404 语义，与 PublishFile 一致）。
	if _, err := h.channel.GetChannel(channelID); err != nil {
		h.respondErr(c, err)
		return
	}
	var body initUploadRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	res, err := h.svc.Init(channelID, service.InitParams{
		Filename:  body.Filename,
		TotalSize: body.TotalSize,
		ChunkSize: body.ChunkSize,
	})
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

// WriteChunk PUT /client-channels/:id/uploads/:uploadId/chunks/:index — 写入一个分片（运营，平台管理员）。
// body 为该分片原始字节（application/octet-stream）；index 0 基；幂等（重传同 index 覆盖）。
// 返回 {received,total}。分片大小/序号/频道由服务层校验。
func (h *ClientChunkUploadHandler) WriteChunk(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	uploadID := c.Param("uploadId")
	index, err := strconv.ParseInt(c.Param("index"), 10, 64)
	if err != nil || index < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "分片序号非法"})
		return
	}
	// 直接消费请求体作为分片字节流（不缓冲整片进内存，流式落盘）。
	res, err := h.svc.WriteChunk(channelID, uploadID, index, c.Request.Body)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// completeUploadRequest complete 请求体。
type completeUploadRequest struct {
	// Codec 制品压缩算法（"zstd"|"none"），透传落库；本期发布恒 none。
	Codec string `json:"codec"`
	// ExpectedSHA256 期望的制品自身 sha256；非空则比对，不符拒收。
	ExpectedSHA256 string `json:"expectedSha256"`
}

// CompleteUpload POST /client-channels/:id/uploads/:uploadId/complete — 完成上传（运营，平台管理员）。
// 校验分片齐全 + 总字节 → 拼装喂 CAS → 返回 ClientFileResult{sha256,md5,size,codec}（与单次上传一致）。
// complete 请求体可空（codec/expectedSha256 均可选）。
func (h *ClientChunkUploadHandler) CompleteUpload(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	uploadID := c.Param("uploadId")
	var body completeUploadRequest
	// 请求体可空——绑定失败（无 body / 非 JSON）不阻断，用零值默认（codec 由服务补 none）。
	_ = c.ShouldBindJSON(&body)

	res, err := h.svc.Complete(channelID, uploadID, service.CompleteParams{
		Codec:          body.Codec,
		ExpectedSHA256: body.ExpectedSHA256,
	})
	if err != nil {
		h.respondErr(c, err)
		return
	}
	h.recordAudit(c, "client_file.publish", map[string]any{
		"channelId": channelID, "sha256": res.SHA256, "size": res.Size, "codec": res.Codec, "via": "chunked",
	})
	c.JSON(http.StatusCreated, res)
}

// AbortUpload DELETE /client-channels/:id/uploads/:uploadId — 弃单（运营，平台管理员）。
// 移除会话 + 清临时分片。会话不存在按幂等处理返回 204（弃单本就是「确保不存在」语义）。
func (h *ClientChunkUploadHandler) AbortUpload(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	uploadID := c.Param("uploadId")
	err := h.svc.Abort(channelID, uploadID)
	// 会话不存在视为已弃单（幂等）；频道不匹配才是真错误（越权）。
	if err != nil && !errors.Is(err, service.ErrUploadNotFound) {
		h.respondErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// respondErr 分块上传端点错误映射。
func (h *ClientChunkUploadHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
	case errors.Is(err, service.ErrUploadNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "UPLOAD_NOT_FOUND", "message": "上传会话不存在或已过期"})
	case errors.Is(err, service.ErrUploadChannelMismatch):
		c.JSON(http.StatusForbidden, gin.H{"error": "UPLOAD_CHANNEL_MISMATCH", "message": "上传会话与频道不匹配"})
	case errors.Is(err, service.ErrInvalidUploadInit):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_UPLOAD_INIT", "message": err.Error()})
	case errors.Is(err, service.ErrInvalidChunkIndex):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_CHUNK_INDEX", "message": "分片序号越界"})
	case errors.Is(err, service.ErrInvalidChunkSize):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_CHUNK_SIZE", "message": err.Error()})
	case errors.Is(err, service.ErrUploadIncomplete):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "UPLOAD_INCOMPLETE", "message": err.Error()})
	case errors.Is(err, service.ErrChecksumMismatch):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "CHECKSUM_MISMATCH", "message": err.Error()})
	default:
		slog.Error("客户端分发分块上传内部错误", "path", c.Request.URL.Path, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
	}
}

// recordAudit 记录分块上传完成审计（detail 仅含可公开元数据）。
func (h *ClientChunkUploadHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	_ = h.audit.Record(id, action, "client_channel", "", string(raw), c.ClientIP())
}

// RegisterRoutes 注册分块上传端点（运营操作，须挂 JWT 平台管理员组）。
// 挂在与 ClientVersionHandler.RegisterPublishRoutes 同一 admin 组下，路径同 /client-channels 前缀。
func (h *ClientChunkUploadHandler) RegisterRoutes(rg *gin.RouterGroup) {
	ch := rg.Group("/client-channels")
	{
		ch.POST("/:id/uploads", h.InitUpload)
		ch.PUT("/:id/uploads/:uploadId/chunks/:index", h.WriteChunk)
		ch.POST("/:id/uploads/:uploadId/complete", h.CompleteUpload)
		ch.DELETE("/:id/uploads/:uploadId", h.AbortUpload)
	}
}
