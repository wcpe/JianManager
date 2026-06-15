package router

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// FileHandler 文件路由处理器。
type FileHandler struct {
	fileSvc *service.FileService
}

// NewFileHandler 创建文件路由处理器。
func NewFileHandler(fileSvc *service.FileService) *FileHandler {
	return &FileHandler{fileSvc: fileSvc}
}

// List 文件列表。
func (h *FileHandler) List(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
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

	var req writeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
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

// RegisterRoutes 注册文件路由。
func (h *FileHandler) RegisterRoutes(rg *gin.RouterGroup) {
	files := rg.Group("/instances/:id/files")
	{
		files.GET("", h.List)
		files.GET("/read", h.Read)
		files.POST("/write", h.Write)
		files.POST("/upload", h.Upload)
		files.GET("/download", h.Download)
		files.DELETE("", h.Delete)
	}
}
