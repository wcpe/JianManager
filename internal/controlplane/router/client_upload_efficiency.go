package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// errBatchMultipartInvalid 聚合上传 multipart 结构非法（meta 非首 part / part 数不符 / 字段名不符）。
var errBatchMultipartInvalid = errors.New("聚合上传请求体非法")

// batchMetaMaxBytes meta part 的 JSON 读取上限（200 项声明远小于此；防恶意超大 meta 撑内存）。
const batchMetaMaxBytes = 1 << 20 // 1 MiB

// ClientUploadEfficiencyHandler 客户端分发上传增效端点（FR-346，增强 FR-250/251）。
//
// 鉴权与 FR-251 分块上传同组：JWT 平台管理员（requirePlatformAdmin），挂 admin 组，
// 与玩家拉取密钥端点物理隔离（ADR-022/023）。独立 handler 不碰 client_version.go / client_chunk_upload.go。
//
// 协议（spec docs/specs/client-upload-efficiency/spec.md §3）：
//   - POST /client-channels/:id/files/precheck：批量秒传预查（≤500 hash/次，只读）。
//   - POST /client-channels/:id/files/batch：小文件聚合上传（multipart：meta 首 part +
//     同序 files part；≤200 个、单文件 ≤8 MiB、总 ≤32 MiB；fail-fast）。
type ClientUploadEfficiencyHandler struct {
	svc     *service.ClientUploadEfficiencyService
	channel *service.ClientChannelService
	audit   *service.AuditService
}

// NewClientUploadEfficiencyHandler 创建上传增效端点处理器。
func NewClientUploadEfficiencyHandler(svc *service.ClientUploadEfficiencyService, channel *service.ClientChannelService, audit *service.AuditService) *ClientUploadEfficiencyHandler {
	return &ClientUploadEfficiencyHandler{svc: svc, channel: channel, audit: audit}
}

// precheckRequest 秒传预查请求体。
type precheckRequest struct {
	// Files 待查项（1..500）：原始内容 sha256 + 字节数。
	Files []service.PrecheckEntry `json:"files" binding:"required"`
}

// Precheck POST /client-channels/:id/files/precheck — 批量秒传预查（运营，平台管理员）。
// 命中制品库者返回与真上传同构的 {sha256,md5,size,codec}，前端据此跳过字节上传直接引用。
// 只读预查不产生审计记录。
func (h *ClientUploadEfficiencyHandler) Precheck(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	// 频道须存在（与 FR-251 一致：上传动作绑定频道、便于 404 语义）。
	if _, err := h.channel.GetChannel(channelID); err != nil {
		h.respondErr(c, err)
		return
	}
	var body precheckRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	results, err := h.svc.Precheck(body.Files)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// UploadBatch POST /client-channels/:id/files/batch — 小文件聚合上传（运营，平台管理员）。
// multipart 流式消费：meta 首 part（JSON 声明数组）→ 恰好 len(meta) 个同序 files part。
// 任一文件校验失败即中止（fail-fast）；已入库的前序文件保留（CAS 无引用制品无害，重试即秒传）。
func (h *ClientUploadEfficiencyHandler) UploadBatch(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	if _, err := h.channel.GetChannel(channelID); err != nil {
		h.respondErr(c, err)
		return
	}
	mr, err := c.Request.MultipartReader()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求须为 multipart/form-data"})
		return
	}
	metas, err := readBatchMetaPart(mr)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	// 护栏前置：读任何文件字节前校验数量/单文件/总字节上限。
	if err := h.svc.ValidateBatchMetas(metas); err != nil {
		h.respondErr(c, err)
		return
	}

	results := make([]*service.ClientFileResult, len(metas))
	var totalBytes int64
	err = consumeBatchFileParts(mr, len(metas), func(i int, r io.Reader) error {
		res, ierr := h.svc.IngestBatchFile(metas[i], r)
		if ierr != nil {
			return ierr
		}
		results[i] = res
		totalBytes += res.Size
		return nil
	})
	if err != nil {
		h.respondErr(c, err)
		return
	}

	// 每批 1 条审计（不逐文件刷记录）。
	h.recordAudit(c, "client_file.publish", map[string]any{
		"channelId": channelID, "via": "batch", "count": len(results), "totalBytes": totalBytes,
	})
	c.JSON(http.StatusCreated, gin.H{"results": results})
}

// readBatchMetaPart 读取聚合上传的首 part：字段名必须为 meta，JSON 数组解析为声明表。
// 结构不符返回 errBatchMultipartInvalid（包装具体原因）。
func readBatchMetaPart(mr *multipart.Reader) ([]service.BatchFileMeta, error) {
	part, err := mr.NextPart()
	if err != nil {
		return nil, fmt.Errorf("%w: 缺少 meta part", errBatchMultipartInvalid)
	}
	defer part.Close()
	if part.FormName() != "meta" {
		return nil, fmt.Errorf("%w: 首个 part 必须为 meta（实际 %q）", errBatchMultipartInvalid, part.FormName())
	}
	raw, err := io.ReadAll(io.LimitReader(part, batchMetaMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("读取 meta 失败: %w", err)
	}
	var metas []service.BatchFileMeta
	if err := json.Unmarshal(raw, &metas); err != nil {
		return nil, fmt.Errorf("%w: meta JSON 非法", errBatchMultipartInvalid)
	}
	return metas, nil
}

// consumeBatchFileParts 依序消费恰好 count 个字段名为 files 的 part，逐个交给 fn(index, reader)。
// part 数不足/过多/字段名不符返回 errBatchMultipartInvalid；fn 出错即中止透传（fail-fast）。
func consumeBatchFileParts(mr *multipart.Reader, count int, fn func(index int, r io.Reader) error) error {
	for i := 0; i < count; i++ {
		part, err := mr.NextPart()
		if err != nil {
			return fmt.Errorf("%w: files part 不足（期望 %d 个，第 %d 个缺失）", errBatchMultipartInvalid, count, i)
		}
		if part.FormName() != "files" {
			_ = part.Close()
			return fmt.Errorf("%w: 第 %d 个文件 part 字段名须为 files（实际 %q）", errBatchMultipartInvalid, i, part.FormName())
		}
		ferr := fn(i, part)
		_ = part.Close()
		if ferr != nil {
			return ferr
		}
	}
	// 消费完声明数量后不得再有多余 part（防 meta 与实体错位静默截断）。
	if _, err := mr.NextPart(); err != io.EOF {
		return fmt.Errorf("%w: files part 多于 meta 声明（%d 个）", errBatchMultipartInvalid, count)
	}
	return nil
}

// respondErr 上传增效端点错误映射（spec §3 错误码表）。
func (h *ClientUploadEfficiencyHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
	case errors.Is(err, service.ErrBatchLimitExceeded):
		c.JSON(http.StatusBadRequest, gin.H{"error": "BATCH_LIMIT_EXCEEDED", "message": err.Error()})
	case errors.Is(err, service.ErrUploadPrecheckInvalid),
		errors.Is(err, service.ErrBatchInvalid),
		errors.Is(err, errBatchMultipartInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	case errors.Is(err, service.ErrChecksumMismatch):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "CHECKSUM_MISMATCH", "message": err.Error()})
	default:
		slog.Error("客户端分发上传增效端点内部错误", "path", c.Request.URL.Path, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
	}
}

// recordAudit 记录聚合上传审计（detail 仅含可公开元数据）。
func (h *ClientUploadEfficiencyHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	h.audit.RecordSafe(id, action, "client_channel", "", marshalAuditDetail(detail), c.ClientIP())
}

// RegisterRoutes 注册上传增效端点（运营操作，须挂 JWT 平台管理员组）。
// 与 FR-251 分块上传同一 admin 组、同 /client-channels 前缀。
func (h *ClientUploadEfficiencyHandler) RegisterRoutes(rg *gin.RouterGroup) {
	ch := rg.Group("/client-channels")
	{
		ch.POST("/:id/files/precheck", h.Precheck)
		ch.POST("/:id/files/batch", h.UploadBatch)
	}
}
