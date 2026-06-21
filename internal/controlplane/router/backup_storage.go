package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BackupStorageHandler 备份远程存储后端路由处理器（FR-057）。
type BackupStorageHandler struct {
	svc *service.BackupStorageService
}

func NewBackupStorageHandler(svc *service.BackupStorageService) *BackupStorageHandler {
	return &BackupStorageHandler{svc: svc}
}

func (h *BackupStorageHandler) List(c *gin.Context) {
	items, err := h.svc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, items)
}

type createStorageRequest struct {
	Name     string `json:"name" binding:"required"`
	Type     string `json:"type" binding:"required"` // s3 | sftp | webdav
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	Prefix   string `json:"prefix"`
	// 凭证以 ${ENV_VAR} 形式引用，不接受明文（config-files.md）。
	AccessKeyEnv string `json:"accessKeyEnv"`
	SecretKeyEnv string `json:"secretKeyEnv"`
	UseSSL       *bool  `json:"useSsl"`
}

func (h *BackupStorageHandler) Create(c *gin.Context) {
	var req createStorageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	st := &model.BackupStorage{
		Name:         req.Name,
		Type:         model.BackupStorageType(req.Type),
		Endpoint:     req.Endpoint,
		Bucket:       req.Bucket,
		Region:       req.Region,
		Prefix:       req.Prefix,
		AccessKeyEnv: req.AccessKeyEnv,
		SecretKeyEnv: req.SecretKeyEnv,
		UseSSL:       true,
	}
	if req.UseSSL != nil {
		st.UseSSL = *req.UseSSL
	}
	created, err := h.svc.Create(st)
	if err != nil {
		// 类型非法 / 凭证非 ${ENV_VAR} 引用为可预期校验错误，回 422。
		if errors.Is(err, service.ErrInvalidStorageType) || errors.Is(err, service.ErrCredentialNotEnvRef) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *BackupStorageHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		if errors.Is(err, service.ErrStorageNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		if errors.Is(err, service.ErrStorageInUse) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

func (h *BackupStorageHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/backup-storages")
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.DELETE("/:id", h.Delete)
	}
}
