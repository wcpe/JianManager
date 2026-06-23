package router

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

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
	Path    string `json:"path" binding:"required"`
	Content string `json:"content" binding:"required"`
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

	if err := h.fileSvc.WriteFile(id, req.Path, []byte(req.Content)); err != nil {
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

// Upload 文件上传。
func (h *FileHandler) Upload(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	path := c.PostForm("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path"})
		return
	}

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少文件"})
		return
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取上传文件失败"})
		return
	}

	// FR-051：上传覆盖已存在文件前先做改前快照（新文件则自动跳过）。
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	if err := h.versionSvc.SnapshotBeforeWrite(id, path, authorID); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	if err := h.fileSvc.WriteFile(id, path, content); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已上传"})
}

// Download 文件下载。
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

	content, err := h.fileSvc.ReadFile(id, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.Header("Content-Disposition", "attachment; filename="+path)
	c.Data(http.StatusOK, "application/octet-stream", content)
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
	Query      string `json:"query" binding:"required"`
	Mode       string `json:"mode"`       // content（默认）| filename
	MaxResults int    `json:"maxResults"` // 命中上限；<=0 时由 Worker 取默认
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

	res, err := h.fileSvc.SearchFiles(id, req.Query, req.Mode, req.MaxResults)
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
	}
}
