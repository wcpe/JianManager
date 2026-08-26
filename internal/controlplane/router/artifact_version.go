package router

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ArtifactVersionHandler 管理版本化制品包；首期提供 ServerProbe 来源同步、本地上传、缓存和默认版本操作。
type ArtifactVersionHandler struct {
	svc *service.ArtifactVersionService
}

// NewArtifactVersionHandler 创建制品版本库路由处理器。
func NewArtifactVersionHandler(svc *service.ArtifactVersionService) *ArtifactVersionHandler {
	return &ArtifactVersionHandler{svc: svc}
}

// Catalog GET /artifact-packages/serverprobe — 查看来源、已发现版本和缓存状态。
func (h *ArtifactVersionHandler) Catalog(c *gin.Context) {
	pkg, sources, versions, err := h.svc.ServerProbeCatalog()
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"package": pkg, "sources": sources, "versions": versions})
}

// SelectableVersions GET /probe-versions — 返回实例操作者可选择的已缓存版本。
// 来源配置和同步状态仅在管理员目录端点展示，避免把管理面细节暴露给实例成员。
func (h *ArtifactVersionHandler) SelectableVersions(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	pkg, _, versions, err := h.svc.ServerProbeCatalog()
	if err != nil {
		h.respondError(c, err)
		return
	}
	cached := make([]model.ArtifactVersion, 0, len(versions))
	for _, version := range versions {
		if version.AssetID != 0 && version.Asset != nil {
			cached = append(cached, version)
		}
	}
	c.JSON(http.StatusOK, gin.H{"package": pkg, "versions": cached})
}

// Sync POST /artifact-packages/serverprobe/sources/:id/sync — 从来源手动登记新版本，不下载也不改默认。
func (h *ArtifactVersionHandler) Sync(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	created, err := h.svc.SyncSource(c.Request.Context(), id)
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"created": created})
}

// Cache POST /artifact-packages/serverprobe/versions/:id/cache — 由 CP 下载并校验 jar 后写入 CAS。
func (h *ArtifactVersionHandler) Cache(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	version, err := h.svc.CacheVersion(c.Request.Context(), id)
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, version)
}

// UploadLocal POST /artifact-packages/serverprobe/versions/upload — 上传本地 jar 并立即登记为可选版本。
func (h *ArtifactVersionHandler) UploadLocal(c *gin.Context) {
	if c.Request.ContentLength > service.ServerProbeUploadMaxSize+(1<<20) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "UPLOAD_TOO_LARGE", "message": service.ErrArtifactLocalUploadTooLarge.Error()})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.ServerProbeUploadMaxSize+(1<<20))
	reader, err := c.Request.MultipartReader()
	if err != nil {
		h.respondMultipartError(c, err)
		return
	}

	var version string
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需提供 jar 文件"})
			return
		}
		if nextErr != nil {
			h.respondMultipartError(c, nextErr)
			return
		}
		switch part.FormName() {
		case "version":
			if version != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "version 只能提供一次"})
				return
			}
			value, readErr := io.ReadAll(io.LimitReader(part, 129))
			if readErr != nil {
				h.respondMultipartError(c, readErr)
				return
			}
			version = string(value)
		case "file":
			if version == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "version 必须位于 file 之前"})
				return
			}
			entry, uploadErr := h.svc.UploadLocalServerProbe(version, part.FileName(), part)
			if uploadErr != nil {
				h.respondUploadError(c, uploadErr)
				return
			}
			if err := h.rejectTrailingUploadPart(reader, entry.ID); err != nil {
				h.respondUploadError(c, err)
				return
			}
			c.JSON(http.StatusCreated, entry)
			return
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "不支持的上传字段"})
			return
		}
	}
}

func (h *ArtifactVersionHandler) rejectTrailingUploadPart(reader *multipart.Reader, versionID uint) error {
	_, err := reader.NextPart()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := h.svc.DeleteVersion(versionID); err != nil {
		return err
	}
	return service.ErrArtifactLocalUploadInvalid
}

type setArtifactVersionRequest struct {
	VersionID uint `json:"versionId"`
}

// SetGlobalDefault PUT /artifact-packages/serverprobe/default-version — 管理员显式设置全局默认。
func (h *ArtifactVersionHandler) SetGlobalDefault(c *gin.Context) {
	var req setArtifactVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.VersionID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "versionId 必须为正整数"})
		return
	}
	pkg, _, _, err := h.svc.ServerProbeCatalog()
	if err == nil {
		err = h.svc.SetPackageDefaultVersion(pkg.ID, req.VersionID)
	}
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"defaultVersionId": req.VersionID})
}

// DeleteVersion DELETE /artifact-packages/serverprobe/versions/:id — 仅删除未被引用的版本元数据。
func (h *ArtifactVersionHandler) DeleteVersion(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.DeleteVersion(id); err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// NodeDefault GET /nodes/:id/probe-version — 查询 Worker 后续新实例的默认覆盖。
func (h *ArtifactVersionHandler) NodeDefault(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	versionID, err := h.svc.NodeProbeVersion(id)
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"nodeId": id, "versionId": versionID})
}

// SetNodeDefault PUT /nodes/:id/probe-version — 只影响之后创建的新实例，绝不主动升级存量实例。
func (h *ArtifactVersionHandler) SetNodeDefault(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req setArtifactVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if err := h.svc.SetNodeProbeVersion(id, req.VersionID); err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"nodeId": id, "versionId": req.VersionID})
}

// Download GET /probe-artifacts/:id/download — 短 token 保护的 CP 本地 jar 分发端点。
func (h *ArtifactVersionHandler) Download(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if _, err := h.svc.ValidateProbeDownloadToken(c.Query("token"), service.ProbeDownloadTokenScope{VersionID: id}); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "INVALID_PROBE_DOWNLOAD_TOKEN"})
		return
	}
	file, version, err := h.svc.OpenCachedProbeVersion(id)
	if err != nil {
		h.respondDownloadError(c, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	filename := filepath.Base(version.AssetName)
	c.Header("Content-Type", "application/java-archive")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeContent(c.Writer, c.Request, filename, info.ModTime(), file)
}

func (h *ArtifactVersionHandler) respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrArtifactSourceNotFound), errors.Is(err, service.ErrArtifactVersionNotFound), errors.Is(err, service.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrArtifactVersionNotCached), errors.Is(err, service.ErrArtifactVersionInUse), errors.Is(err, service.ErrArtifactReleaseInvalid), errors.Is(err, service.ErrArtifactSourceNotSyncable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "ARTIFACT_OPERATION_FAILED", "message": err.Error()})
	}
}

func (h *ArtifactVersionHandler) respondUploadError(c *gin.Context, err error) {
	switch {
	case isMultipartBodyTooLarge(err):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "UPLOAD_TOO_LARGE", "message": service.ErrArtifactLocalUploadTooLarge.Error()})
	case errors.Is(err, service.ErrArtifactLocalUploadInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	case errors.Is(err, service.ErrArtifactLocalUploadTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "UPLOAD_TOO_LARGE", "message": err.Error()})
	case errors.Is(err, service.ErrArtifactVersionAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{"error": "VERSION_EXISTS", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ARTIFACT_OPERATION_FAILED", "message": err.Error()})
	}
}

func (h *ArtifactVersionHandler) respondMultipartError(c *gin.Context, err error) {
	if isMultipartBodyTooLarge(err) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "UPLOAD_TOO_LARGE", "message": service.ErrArtifactLocalUploadTooLarge.Error()})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "上传表单格式错误"})
}

func isMultipartBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

func (h *ArtifactVersionHandler) respondDownloadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrProbeDownloadTokenInvalid):
		c.JSON(http.StatusForbidden, gin.H{"error": "INVALID_PROBE_DOWNLOAD_TOKEN"})
	case errors.Is(err, service.ErrArtifactVersionNotFound), errors.Is(err, service.ErrArtifactVersionNotCached):
		c.JSON(http.StatusNotFound, gin.H{"error": "PROBE_ARTIFACT_NOT_FOUND"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}

// RegisterRoutes 注册平台管理员的版本库管理 API。
func (h *ArtifactVersionHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/artifact-packages/serverprobe", h.Catalog)
	rg.POST("/artifact-packages/serverprobe/sources/:id/sync", h.Sync)
	rg.POST("/artifact-packages/serverprobe/versions/:id/cache", h.Cache)
	rg.POST("/artifact-packages/serverprobe/versions/upload", h.UploadLocal)
	rg.PUT("/artifact-packages/serverprobe/default-version", h.SetGlobalDefault)
	rg.DELETE("/artifact-packages/serverprobe/versions/:id", h.DeleteVersion)
	rg.GET("/nodes/:id/probe-version", h.NodeDefault)
	rg.PUT("/nodes/:id/probe-version", h.SetNodeDefault)
}

// RegisterSelectionRoutes 注册实例操作者所需的只读版本选择列表。
func (h *ArtifactVersionHandler) RegisterSelectionRoutes(rg *gin.RouterGroup) {
	rg.GET("/probe-versions", h.SelectableVersions)
}

// RegisterDownloadRoutes 注册 CP 本地 jar 下载端点。
func (h *ArtifactVersionHandler) RegisterDownloadRoutes(r gin.IRouter) {
	r.GET("/probe-artifacts/:id/download", h.Download)
}
