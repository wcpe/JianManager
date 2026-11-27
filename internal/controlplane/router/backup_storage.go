package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BackupStorageHandler 备份远程存储后端路由处理器。
type BackupStorageHandler struct {
	svc *service.BackupStorageService
}

func NewBackupStorageHandler(svc *service.BackupStorageService) *BackupStorageHandler {
	return &BackupStorageHandler{svc: svc}
}

func (h *BackupStorageHandler) List(c *gin.Context) {
	items, err := h.svc.ListWithStats()
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
	if !requireNodes(c, "node.manage") {
		return
	}
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
		// 类型非法 / 凭证非 ${ENV_VAR} 引用 / 名称冲突为可预期校验错误，回 422（收口）。
		if errors.Is(err, service.ErrInvalidStorageType) || errors.Is(err, service.ErrCredentialNotEnvRef) ||
			errors.Is(err, service.ErrStorageNameConflict) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusCreated, created)
}

// Update 全量替换式编辑存储后端。body 同 Create（复用 createStorageRequest）；
// type 不可改、名称冲突排除自身，404/422 语义与既有端点对齐。
func (h *BackupStorageHandler) Update(c *gin.Context) {
	if !requireNodes(c, "node.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req createStorageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	updated, err := h.svc.Update(id, storageFromRequest(req))
	if err != nil {
		if errors.Is(err, service.ErrStorageNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		if errors.Is(err, service.ErrInvalidStorageType) || errors.Is(err, service.ErrCredentialNotEnvRef) ||
			errors.Is(err, service.ErrStorageTypeImmutable) || errors.Is(err, service.ErrStorageNameConflict) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, updated)
}

func storageFromRequest(req createStorageRequest) model.BackupStorage {
	st := model.BackupStorage{
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
	return st
}

// TestCandidate 测试未保存的存储配置。
//
// 必须要求 node.manage（路由组的 backup.read 只够进门）：该端点是**写侧风险**而非
// 只读——它用控制面主机的身份向调用方给定的任意 endpoint 发起出站请求，并把
// ${ENV_VAR} 引用的明文凭据放进 Authorization 头。只持 backup.read 的人因此可以
// 把 CP 上任意已设置环境变量的值送到自己控制的地址，也能把 CP 当内网探测器。
// 与 Create / Update / Delete 同口径。
func (h *BackupStorageHandler) TestCandidate(c *gin.Context) {
	if !requireNodes(c, "node.manage") {
		return
	}
	var req createStorageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	result := h.svc.TestCandidate(storageFromRequest(req))
	c.JSON(http.StatusOK, result)
}

// TestSaved 测试已保存的存储后端。与 TestCandidate 同理：会以 CP 身份发起真实
// 出站探测（并把已存凭据发出去），因此同样要求 node.manage，不能只靠 backup.read。
func (h *BackupStorageHandler) TestSaved(c *gin.Context) {
	if !requireNodes(c, "node.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	result, err := h.svc.TestSaved(id)
	if err != nil {
		if errors.Is(err, service.ErrStorageNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *BackupStorageHandler) Delete(c *gin.Context) {
	if !requireNodes(c, "node.manage") {
		return
	}
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

func (h *BackupStorageHandler) Stats(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	stats, err := h.svc.Stats(id)
	if err != nil {
		if errors.Is(err, service.ErrStorageNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, stats)
}

func (h *BackupStorageHandler) LocalStats(c *gin.Context) {
	stats, err := h.svc.LocalStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// TestConnection 让 Worker 用**已保存的凭据**做一次真实连接测试（凭证会下发给节点）。
//
// 与 TestCandidate 同族、更危险的一个：调用方不提供 endpoint，用的是库里已有的那套，
// 因此拿到它的唯一门槛就是路由组的 any-of（backup.read 即可）。必须补 node.manage：
// 它既把凭据下发到远端节点，也代表修改了该存储的 lastTest* 记录。
func (h *BackupStorageHandler) TestConnection(c *gin.Context) {
	if !requireNodes(c, "node.manage") {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	result, err := h.svc.TestConnection(id)
	if err != nil {
		if errors.Is(err, service.ErrStorageNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *BackupStorageHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/backup-storages")
	{
		g.GET("", h.List)
		g.POST("/test", h.TestCandidate)
		g.POST("", h.Create)
		g.GET("/local/stats", h.LocalStats)
		g.GET("/:id/stats", h.Stats)
		g.POST("/:id/test", h.TestSaved)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
	}
}
