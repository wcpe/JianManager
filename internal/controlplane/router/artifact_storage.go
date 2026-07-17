package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ArtifactStorageHandler 制品存储渠道路由处理器（FR-347，见 ADR-073）。
// 全组限平台管理员（挂 admin 组）；响应永不含凭证明文或密文（model json:"-" + Has* 标示）。
type ArtifactStorageHandler struct {
	svc *service.ArtifactStorageChannelService
}

// NewArtifactStorageHandler 创建制品存储渠道处理器。
func NewArtifactStorageHandler(svc *service.ArtifactStorageChannelService) *ArtifactStorageHandler {
	return &ArtifactStorageHandler{svc: svc}
}

// saveArtifactStorageRequest 创建/编辑渠道请求体（spec §3.6）。
// accessKey/secretKey 为明文直填（编辑留空=保留）；useSsl/presignTtlSeconds 指针区分未填与显式值。
type saveArtifactStorageRequest struct {
	Name              string `json:"name" binding:"required"`
	Type              string `json:"type" binding:"required"` // s3（local 由内置行独占）
	Endpoint          string `json:"endpoint"`
	Bucket            string `json:"bucket"`
	Region            string `json:"region"`
	Prefix            string `json:"prefix"`
	AccessKey         string `json:"accessKey"`
	SecretKey         string `json:"secretKey"`
	UseSSL            *bool  `json:"useSsl"`
	PresignTTLSeconds *int   `json:"presignTtlSeconds"`
}

// testArtifactStorageRequest 候选连通测试请求体：同保存体 + 可选 id（编辑态凭证留空时复用存库凭证）。
type testArtifactStorageRequest struct {
	Name              string `json:"name"`
	Type              string `json:"type" binding:"required"`
	Endpoint          string `json:"endpoint"`
	Bucket            string `json:"bucket"`
	Region            string `json:"region"`
	Prefix            string `json:"prefix"`
	AccessKey         string `json:"accessKey"`
	SecretKey         string `json:"secretKey"`
	UseSSL            *bool  `json:"useSsl"`
	PresignTTLSeconds *int   `json:"presignTtlSeconds"`
	ID                uint   `json:"id"`
}

func artifactStorageParams(req saveArtifactStorageRequest) service.SaveArtifactStorageParams {
	return service.SaveArtifactStorageParams{
		Name:              req.Name,
		Type:              req.Type,
		Endpoint:          req.Endpoint,
		Bucket:            req.Bucket,
		Region:            req.Region,
		Prefix:            req.Prefix,
		AccessKey:         req.AccessKey,
		SecretKey:         req.SecretKey,
		UseSSL:            req.UseSSL,
		PresignTTLSeconds: req.PresignTTLSeconds,
	}
}

// List GET /artifact-storages — 全量渠道（Builtin 最前，含 hasAccessKey/hasSecretKey/active/lastTest*）。
func (h *ArtifactStorageHandler) List(c *gin.Context) {
	items, err := h.svc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, items)
}

// Create POST /artifact-storages — 创建 s3 渠道。
func (h *ArtifactStorageHandler) Create(c *gin.Context) {
	var req saveArtifactStorageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	created, err := h.svc.Create(artifactStorageParams(req))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

// Update PUT /artifact-storages/:id — 编辑渠道（type 不可改；凭证留空=保留）。
func (h *ArtifactStorageHandler) Update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req saveArtifactStorageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	updated, err := h.svc.Update(id, artifactStorageParams(req))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// Delete DELETE /artifact-storages/:id — 删除渠道（内置/活跃/被制品引用均拒）。
func (h *ArtifactStorageHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Activate POST /artifact-storages/:id/activate — 设活跃渠道（唯一写路径路由开关）。
func (h *ArtifactStorageHandler) Activate(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	ch, err := h.svc.SetActive(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, ch)
}

// TestCandidate POST /artifact-storages/test — 真连探测候选配置（不写库；可带 id 复用存库凭证）。
func (h *ArtifactStorageHandler) TestCandidate(c *gin.Context) {
	var req testArtifactStorageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	result := h.svc.TestCandidate(service.SaveArtifactStorageParams{
		Name:              req.Name,
		Type:              req.Type,
		Endpoint:          req.Endpoint,
		Bucket:            req.Bucket,
		Region:            req.Region,
		Prefix:            req.Prefix,
		AccessKey:         req.AccessKey,
		SecretKey:         req.SecretKey,
		UseSSL:            req.UseSSL,
		PresignTTLSeconds: req.PresignTTLSeconds,
	}, req.ID)
	c.JSON(http.StatusOK, result)
}

// TestSaved POST /artifact-storages/:id/test — 真连探测已存渠道并持久化 LastTest*。
func (h *ArtifactStorageHandler) TestSaved(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	result, err := h.svc.TestSaved(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// respondErr 统一错误映射：404 NOT_FOUND / 422 BUSINESS_ERROR / 500 INTERNAL_ERROR（spec §3.6）。
func (h *ArtifactStorageHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrArtifactStorageNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrArtifactStorageNameConflict),
		errors.Is(err, service.ErrArtifactStorageInvalidType),
		errors.Is(err, service.ErrArtifactStorageBuiltinImmutable),
		errors.Is(err, service.ErrArtifactStorageTypeImmutable),
		errors.Is(err, service.ErrArtifactStorageActiveDelete),
		errors.Is(err, service.ErrArtifactStorageInUse),
		errors.Is(err, service.ErrArtifactStorageEncryptorMissing),
		errors.Is(err, service.ErrArtifactStorageInvalidConfig):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}

// RegisterRoutes 注册制品存储渠道路由（挂 admin 组，JWT + 平台管理员）。
func (h *ArtifactStorageHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/artifact-storages")
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.POST("/test", h.TestCandidate)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.POST("/:id/test", h.TestSaved)
		g.POST("/:id/activate", h.Activate)
	}
}
