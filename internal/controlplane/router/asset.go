package router

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AssetHandler 制品库路由处理器。资产是平台级共享资源，统一由平台管理员管理。
type AssetHandler struct {
	svc *service.AssetService
}

// NewAssetHandler 创建制品库路由处理器。
func NewAssetHandler(svc *service.AssetService) *AssetHandler {
	return &AssetHandler{svc: svc}
}

// List GET /assets — 按 type 筛选、分页列出资产。
func (h *AssetHandler) List(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	typeFilter := model.AssetType(c.Query("type"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))

	assets, total, err := h.svc.List(typeFilter, page, pageSize)
	if err != nil {
		if errors.Is(err, service.ErrInvalidAssetType) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_TYPE", "message": "非法的资产类型"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询资产失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items":    assets,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// Get GET /assets/:id — 资产详情。
func (h *AssetHandler) Get(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	asset, err := h.svc.GetByID(id)
	if err != nil {
		if errors.Is(err, service.ErrAssetNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "资产不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询资产失败"})
		return
	}
	c.JSON(http.StatusOK, asset)
}

// Create POST /assets — multipart 上传或从本地路径登记。
// multipart 表单字段 file 存在 → 文件上传；否则读取 JSON/表单 path → 从路径登记。
// 公共字段：type(必填)、name、version、filename、contentType、sourceUrl、metadata、
// expectedSha256、expectedMd5（提供则校验，不符拒收）。
func (h *AssetHandler) Create(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}

	assetType := model.AssetType(c.PostForm("type"))
	if assetType == "" {
		// 兼容 register-from-path 走 JSON 提交。
		var body registerFromPathRequest
		if err := c.ShouldBindJSON(&body); err == nil && body.Type != "" {
			h.registerFromPath(c, body)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 type"})
		return
	}
	if !model.ValidAssetType(assetType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_TYPE", "message": "非法的资产类型"})
		return
	}

	params := service.IngestParams{
		Type:           assetType,
		Name:           c.PostForm("name"),
		Version:        c.PostForm("version"),
		ContentType:    c.PostForm("contentType"),
		SourceURL:      c.PostForm("sourceUrl"),
		Metadata:       c.PostForm("metadata"),
		ExpectedSHA256: c.PostForm("expectedSha256"),
		ExpectedMD5:    c.PostForm("expectedMd5"),
	}

	// 优先 multipart 文件上传。
	file, header, ferr := c.Request.FormFile("file")
	if ferr == nil {
		defer file.Close()
		params.Filename = header.Filename
		asset, err := h.svc.Ingest(io.Reader(file), params)
		h.respondIngest(c, asset, err)
		return
	}

	// 无文件 → 从本地路径登记。
	if path := c.PostForm("path"); path != "" {
		params.Filename = c.PostForm("filename")
		asset, err := h.svc.IngestFromPath(path, params)
		h.respondIngest(c, asset, err)
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供上传文件或本地路径"})
}

// registerFromPathRequest 从本地路径登记的 JSON 请求体。
type registerFromPathRequest struct {
	Type           model.AssetType `json:"type" binding:"required"`
	Path           string          `json:"path" binding:"required"`
	Name           string          `json:"name"`
	Version        string          `json:"version"`
	Filename       string          `json:"filename"`
	ContentType    string          `json:"contentType"`
	SourceURL      string          `json:"sourceUrl"`
	Metadata       string          `json:"metadata"`
	ExpectedSHA256 string          `json:"expectedSha256"`
	ExpectedMD5    string          `json:"expectedMd5"`
}

func (h *AssetHandler) registerFromPath(c *gin.Context, body registerFromPathRequest) {
	if !model.ValidAssetType(body.Type) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_TYPE", "message": "非法的资产类型"})
		return
	}
	asset, err := h.svc.IngestFromPath(body.Path, service.IngestParams{
		Type:           body.Type,
		Name:           body.Name,
		Version:        body.Version,
		Filename:       body.Filename,
		ContentType:    body.ContentType,
		SourceURL:      body.SourceURL,
		Metadata:       body.Metadata,
		ExpectedSHA256: body.ExpectedSHA256,
		ExpectedMD5:    body.ExpectedMD5,
	})
	h.respondIngest(c, asset, err)
}

func (h *AssetHandler) respondIngest(c *gin.Context, asset *model.Asset, err error) {
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidAssetType):
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_TYPE", "message": "非法的资产类型"})
		case errors.Is(err, service.ErrChecksumMismatch):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "CHECKSUM_MISMATCH", "message": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INGEST_FAILED", "message": err.Error()})
		}
		return
	}
	c.JSON(http.StatusCreated, asset)
}

// Delete DELETE /assets/:id — 删除资产；被引用时拒绝。
func (h *AssetHandler) Delete(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		switch {
		case errors.Is(err, service.ErrAssetNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "资产不存在"})
		case errors.Is(err, service.ErrAssetInUse):
			c.JSON(http.StatusConflict, gin.H{"error": "ASSET_IN_USE", "message": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "删除资产失败"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// RegisterRoutes 注册制品库路由。
func (h *AssetHandler) RegisterRoutes(rg *gin.RouterGroup) {
	assets := rg.Group("/assets")
	{
		assets.GET("", h.List)
		assets.GET("/:id", h.Get)
		assets.POST("", h.Create)
		assets.DELETE("/:id", h.Delete)
	}
}
