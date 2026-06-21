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
		files.POST("/rename", h.Rename)
		files.DELETE("", h.Delete)
		// FR-051 文件版本：加性追加，不重排既有路由。
		files.GET("/versions", h.Versions)
		files.GET("/diff", h.FileDiff)
		files.POST("/rollback", h.Rollback)
	}
}
