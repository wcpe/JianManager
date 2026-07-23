package router

import (
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FileHandler 文件路由处理器。
type FileHandler struct {
	fileSvc    *service.FileService
	versionSvc *service.FileVersionService
	authz      *service.AuthzService
}

// NewFileHandler 创建文件路由处理器。
func NewFileHandler(fileSvc *service.FileService, versionSvc *service.FileVersionService, authz *service.AuthzService) *FileHandler {
	return &FileHandler{fileSvc: fileSvc, versionSvc: versionSvc, authz: authz}
}

// List 文件列表。
func (h *FileHandler) List(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	path := c.DefaultQuery("path", "")

	files, err := h.fileSvc.ListFiles(id, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, files)
}

// Read 读取文件内容。
func (h *FileHandler) Read(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path 参数"})
		return
	}

	content, err := h.fileSvc.ReadFile(id, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.Data(http.StatusOK, "application/octet-stream", content)
}

type writeRequest struct {
	Path    string  `json:"path" binding:"required"`
	Content *string `json:"content" binding:"required"`
}

// Write 写入文件。
func (h *FileHandler) Write(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req writeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	// FR-051：覆盖已存在文件前先做改前快照（文件不存在则自动跳过）。
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	if err := h.versionSvc.SnapshotBeforeWrite(id, req.Path, authorID); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	if err := h.fileSvc.WriteFile(id, req.Path, []byte(*req.Content)); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

type deleteFileRequest struct {
	Path string `json:"path" binding:"required"`
}

// Delete 删除文件。
func (h *FileHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req deleteFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	if err := h.fileSvc.DeleteFile(id, req.Path); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Upload 文件流式上传（FR-304）。multipart 流式读、经 Worker UploadFile 流式转发，
// 全程不整块缓冲（修复：曾 io.ReadAll 全量进内存 + WriteFile unary 直传，直拨 >64MB 被
// gRPC 单消息上限拒收、反向隧道下双侧内存整块缓冲）。
// 目标路径优先经 query 参数 `path` 传递；兼容先于 file 部分的 multipart path 字段
// （流式顺序读所限：读到 file 时必须已知目标路径）。
func (h *FileHandler) Upload(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	path := c.Query("path")

	mr, err := c.Request.MultipartReader()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 multipart 请求"})
		return
	}

	var filePart *multipart.Part
	for filePart == nil {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "读取上传表单失败"})
			return
		}
		switch part.FormName() {
		case "file":
			filePart = part
		case "path":
			if path == "" {
				b, rerr := io.ReadAll(io.LimitReader(part, 4096))
				if rerr != nil {
					_ = part.Close()
					c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "读取 path 字段失败"})
					return
				}
				path = strings.TrimSpace(string(b))
			}
			_ = part.Close()
		default:
			_ = part.Close()
		}
	}
	if filePart == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少文件"})
		return
	}
	defer filePart.Close()
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path（经 query 参数或先于 file 的表单字段提供）"})
		return
	}

	// FR-051：上传覆盖已存在文件前先做改前快照（新文件则自动跳过）。
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	if err := h.versionSvc.SnapshotBeforeWrite(id, path, authorID); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	if err := h.fileSvc.UploadFile(c.Request.Context(), id, path, filePart); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已上传"})
}

// Download 单文件流式下载。经 Worker DownloadFile 流逐帧转写响应体，任意大小不截断
// （修复：曾复用编辑器 ReadFile 的 10MiB 上限，超限大文件被静默截断为损坏内容）。
func (h *FileHandler) Download(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path 参数"})
		return
	}

	stream, err := h.fileSvc.DownloadFile(c.Request.Context(), id, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	// 先收首帧再写头：打开失败/越界/目录（含老 Worker 不支持本 RPC）仍能返回 JSON 错误而非半截文件。
	// Worker 成功必发首帧（空文件亦然），故首帧即 EOF 视为异常。
	first, err := stream.Recv()
	if err != nil {
		msg := err.Error()
		switch {
		case status.Code(err) == codes.Unimplemented:
			// 老 Worker 无 DownloadFile：明确报错引导升级，绝不回退会截断的 ReadFile。
			msg = "节点 Worker 版本过旧，不支持大文件流式下载，请先升级节点"
		case err == io.EOF:
			msg = "下载流异常结束"
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": msg})
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename="+path)
	// 首帧携带文件总大小：显式 Content-Length 让客户端可校验完整性——流中途失败时
	// 收到的字节数与之不符，浏览器/HTTP 客户端会判下载失败，而非拿到静默截断的文件。
	c.Header("Content-Length", strconv.FormatInt(first.TotalSize, 10))
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)

	writeChunk := func(b []byte) bool {
		if len(b) == 0 {
			return true
		}
		if _, werr := c.Writer.Write(b); werr != nil {
			return false // 客户端断开
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	if !writeChunk(first.Content) {
		return
	}
	for {
		chunk, rerr := stream.Recv()
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			// 流中途失败：响应头已发出，只能截断连接（Content-Length 不符，客户端按下载失败处理）。
			return
		}
		if !writeChunk(chunk.Content) {
			return
		}
	}
}

// archiveRequest 批量打包下载请求（FR-070）。
type archiveRequest struct {
	Paths []string `json:"paths" binding:"required"`
}

// DownloadArchive 批量打包下载：把选中的文件/目录即时打包为 zip 流式返回（FR-070）。
// CP 逐帧 Recv Worker 流并写入响应体并 Flush，全程不缓冲整个归档。
func (h *FileHandler) DownloadArchive(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req archiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if len(req.Paths) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "未选择要下载的文件"})
		return
	}

	stream, err := h.fileSvc.DownloadArchive(c.Request.Context(), id, req.Paths)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	// 先收第一帧再写头：若打包在开始前就失败（如越界/缺文件），仍能返回 JSON 错误而非半截 zip。
	first, err := stream.Recv()
	if err != nil {
		if err == io.EOF {
			// 空归档（理论上不会，paths 非空且条目存在）：返回空 zip 头交给客户端。
			c.Header("Content-Type", "application/zip")
			c.Status(http.StatusOK)
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="files.zip"`)
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)

	writeChunk := func(b []byte) bool {
		if _, werr := c.Writer.Write(b); werr != nil {
			return false // 客户端断开
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	if !writeChunk(first.Content) {
		return
	}
	for {
		chunk, rerr := stream.Recv()
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			// 流中途失败：响应头已发出，只能截断连接（前端按下载失败处理）。
			return
		}
		if !writeChunk(chunk.Content) {
			return
		}
	}
}

// renameRequest 文件重命名请求。
type renameRequest struct {
	OldPath string `json:"oldPath" binding:"required"`
	NewPath string `json:"newPath" binding:"required"`
}

// Rename 重命名文件或目录。
func (h *FileHandler) Rename(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req renameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	if err := h.fileSvc.RenameFile(id, req.OldPath, req.NewPath); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已重命名"})
}

// searchRequest 全文搜索 / 文件名快速打开请求（FR-074）。
type searchRequest struct {
	Query      string   `json:"query" binding:"required"`
	Mode       string   `json:"mode"`       // content（默认）| filename
	MaxResults int      `json:"maxResults"` // 命中上限；<=0 时由 Worker 取默认
	RootPath   string   `json:"rootPath"`   // 可选：限定相对目录范围
	Extensions []string `json:"extensions"` // 可选：限定扩展名范围
}

// Search 全文搜索 / 文件名快速打开（FR-074，见 ADR-017）。
// 经 gRPC 转发到目标节点 Worker 的本地倒排索引查询；CP 仅转发不持有索引。
func (h *FileHandler) Search(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req searchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	res, err := h.fileSvc.SearchFiles(id, req.Query, req.Mode, req.MaxResults, service.SearchScope{
		RootPath:   req.RootPath,
		Extensions: req.Extensions,
	})
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, res)
}

// ArchiveEntries 列出归档（jar/zip）内条目（FR-075）。只读浏览，复用文件「查看」级权限。
func (h *FileHandler) ArchiveEntries(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path 参数"})
		return
	}
	res, err := h.fileSvc.ListArchiveEntries(id, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// ArchiveRead 读取归档内某条目内容（FR-075）。返回原始字节，截断/二进制经响应头标注。
func (h *FileHandler) ArchiveRead(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	path := c.Query("path")
	entry := c.Query("entry")
	if path == "" || entry == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path 或 entry 参数"})
		return
	}
	res, err := h.fileSvc.ReadArchiveEntry(id, path, entry)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	if res.Truncated {
		c.Header("X-Truncated", "true")
	}
	if res.Binary {
		c.Header("X-Binary", "true")
	}
	c.Data(http.StatusOK, "application/octet-stream", res.Content)
}

// decompileRequest 反编译请求（FR-075）。
type decompileRequest struct {
	Path  string `json:"path" binding:"required"`
	Entry string `json:"entry"`
}

// Decompile 反编译工作目录内 class/jar（或归档内某 class）为 Java 源码（FR-075）。
// 只读浏览，复用文件「查看」级权限；反编译失败/降级以 success=false 在 200 体内返回。
func (h *FileHandler) Decompile(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req decompileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	res, err := h.fileSvc.DecompileClass(id, req.Path, req.Entry)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Versions 列出某文件的历史版本（FR-051）。
func (h *FileHandler) Versions(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path 参数"})
		return
	}
	versions, err := h.versionSvc.Versions(id, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, versions)
}

// FileDiff 返回某文件 from→to 版本的差异（FR-051）。to=0/缺省表示与当前文件比较。
func (h *FileHandler) FileDiff(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path 参数"})
		return
	}
	fromRaw := c.Query("from")
	if fromRaw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 from 版本 ID"})
		return
	}
	fromID, err := strconv.ParseUint(fromRaw, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 from 版本 ID"})
		return
	}
	var toID uint64
	if raw := c.Query("to"); raw != "" {
		t, perr := strconv.ParseUint(raw, 10, 64)
		if perr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 to 版本 ID"})
			return
		}
		toID = t
	}
	res, err := h.versionSvc.Diff(id, path, uint(fromID), uint(toID))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

type fileRollbackRequest struct {
	Path      string `json:"path" binding:"required"`
	VersionID uint   `json:"versionId" binding:"required"`
}

// Rollback 把文件回滚到指定版本（FR-051），回滚前自动快照当前内容。
func (h *FileHandler) Rollback(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req fileRollbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	versionID, err := h.versionSvc.Rollback(id, strings.TrimPrefix(req.Path, "/"), req.VersionID, authorID)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versionId": versionID})
}

// CheckAccess POST /instances/:id/files/check-access — 写前权限探测（FR-373）。
func (h *FileHandler) CheckAccess(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path"})
		return
	}
	access, err := h.fileSvc.CheckPathAccess(id, req.Path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, access)
}

// Chmod POST /instances/:id/files/chmod — 单 path 非递归 chmod（FR-373）。
func (h *FileHandler) Chmod(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req struct {
		Path string `json:"path" binding:"required"`
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path"})
		return
	}
	modeOctal, err := h.fileSvc.ChmodPath(id, req.Path, req.Mode)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已修改权限", "modeOctal": modeOctal})
}

// RegisterRoutes 注册文件路由。
func (h *FileHandler) RegisterRoutes(rg *gin.RouterGroup) {
	files := rg.Group("/instances/:id/files")
	{
		files.GET("", h.List)
		files.GET("/read", h.Read)
		files.POST("/write", h.Write)
		files.POST("/upload", h.Upload)
		files.GET("/download", h.Download)
		// FR-070 批量下载：选中多文件/目录即时打包 zip 流式返回（加性追加）。
		files.POST("/archive", h.DownloadArchive)
		// FR-075 归档浏览与反编译：只读，加性追加。
		files.GET("/archive/entries", h.ArchiveEntries)
		files.GET("/archive/read", h.ArchiveRead)
		files.POST("/decompile", h.Decompile)
		// FR-074 全文搜索 / 文件名快速打开：转发到 Worker 本地倒排索引（加性追加）。
		files.POST("/search", h.Search)
		files.POST("/rename", h.Rename)
		files.DELETE("", h.Delete)
		// FR-051 文件版本：加性追加，不重排既有路由。
		files.GET("/versions", h.Versions)
		files.GET("/diff", h.FileDiff)
		files.POST("/rollback", h.Rollback)
		// FR-373 权限探测与单 path chmod（加性追加）。
		files.POST("/check-access", h.CheckAccess)
		files.POST("/chmod", h.Chmod)
	}
}
