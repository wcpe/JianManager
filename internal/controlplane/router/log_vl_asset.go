package router

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/logasset"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type LogVLAssetHandler struct {
	store   *service.ApprovedVLAssetStore
	signer  *service.SelfUpdateService
	nodes   *service.NodeService
	pool    *cpgrpc.ClientPool
	baseURL string
}

func (h *LogVLAssetHandler) SetDelivery(pool *cpgrpc.ClientPool, baseURL string) {
	h.pool, h.baseURL = pool, baseURL
}

func NewLogVLAssetHandler(store *service.ApprovedVLAssetStore, signer *service.SelfUpdateService, nodes *service.NodeService) *LogVLAssetHandler {
	return &LogVLAssetHandler{store: store, signer: signer, nodes: nodes}
}

func (h *LogVLAssetHandler) RegisterAdminRoutes(rg *gin.RouterGroup) {
	if h == nil || h.store == nil || h.signer == nil || h.nodes == nil {
		return
	}
	rg.POST("/log-runtime/assets/:os/:arch", h.Upload)
	rg.GET("/log-runtime/assets", h.Status)
}

func (h *LogVLAssetHandler) RegisterDownloadRoutes(r gin.IRouter) {
	if h == nil || h.store == nil || h.signer == nil || h.nodes == nil {
		return
	}
	r.GET("/log-vl-assets/:tag/:os/:arch/package", h.Download)
}

func (h *LogVLAssetHandler) RegisterNodeRoutes(rg *gin.RouterGroup) {
	if h == nil || h.pool == nil || h.store == nil || h.signer == nil || h.nodes == nil {
		return
	}
	rg.POST("/nodes/:id/log-runtime/install", h.Install)
}

func (h *LogVLAssetHandler) Install(c *gin.Context) {
	if !h.platformAdmin(c) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_NODE_ID"})
		return
	}
	node, err := h.nodes.GetByID(uint(id))
	if err != nil || node.UUID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "NODE_NOT_FOUND"})
		return
	}
	goos, arch := strings.ToLower(node.OS), strings.ToLower(node.Arch)
	if arch == "x64" {
		arch = "amd64"
	}
	if _, ok := logasset.Approved(goos, arch); !ok {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ASSET_NOT_APPROVED"})
		return
	}
	f, _, err := h.store.Open(goos, arch)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "ASSET_NOT_AVAILABLE"})
		return
	}
	f.Close()
	client, ok := h.pool.Get(node.UUID)
	if !ok || client == nil || client.Worker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "WORKER_OFFLINE"})
		return
	}
	baseURL := h.baseURL
	if baseURL == "" {
		baseURL = selfUpdateRequestBaseURL(c)
	}
	packageURL, sha, err := h.PackageURL(baseURL, node.UUID, goos, arch)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ASSET_TOKEN_FAILED"})
		return
	}
	result, err := client.Worker.LogRuntimeControl(c.Request.Context(), &workerpb.LogRuntimeControlRequest{
		RequestId: c.GetHeader("X-Request-ID"), Action: "install", PackageUrl: packageURL, PackageSha256: sha,
	})
	if err != nil {
		statusCode := http.StatusBadGateway
		if status.Code(err) == codes.Unimplemented {
			statusCode = http.StatusNotImplemented
		}
		c.JSON(statusCode, gin.H{"error": "WORKER_RPC_FAILED"})
		return
	}
	if result == nil || result.GetError() != nil || result.GetState() != workerpb.LogTaskState_LOG_TASK_SUCCEEDED {
		status := http.StatusBadGateway
		code := "LOG_NOT_READY"
		if result != nil && result.GetError() != nil {
			code = result.GetError().GetCode().String()
			if result.GetError().GetCode() == workerpb.LogErrorCode_LOG_UNSUPPORTED {
				status = http.StatusNotImplemented
			}
		}
		c.JSON(status, gin.H{"error": "ASSET_INSTALL_FAILED", "code": code})
		return
	}
	c.JSON(http.StatusOK, gin.H{"state": "installed", "tag": logasset.Tag, "os": goos, "arch": arch, "packageSha256": sha})
}

func (h *LogVLAssetHandler) platformAdmin(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN"})
		return false
	}
	return true
}

func (h *LogVLAssetHandler) Upload(c *gin.Context) {
	if !h.platformAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<20+1)
	header, err := c.FormFile("package")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_PACKAGE"})
		return
	}
	f, err := header.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_PACKAGE"})
		return
	}
	defer f.Close()
	_, err = h.store.Cache(c.Request.Context(), c.Param("os"), c.Param("arch"), f)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ASSET_NOT_APPROVED", "message": err.Error()})
		return
	}
	pkg, _ := logasset.Approved(c.Param("os"), c.Param("arch"))
	c.JSON(http.StatusCreated, gin.H{"tag": logasset.Tag, "os": pkg.OS, "arch": pkg.Arch,
		"packageSha256": pkg.PackageSHA256, "executableSha256": pkg.ExecutableSHA256})
}

func (h *LogVLAssetHandler) Status(c *gin.Context) {
	if !h.platformAdmin(c) {
		return
	}
	items := make([]gin.H, 0, 2)
	for _, platform := range [][2]string{{"linux", "amd64"}, {"windows", "amd64"}} {
		pkg, _ := logasset.Approved(platform[0], platform[1])
		f, _, err := h.store.Open(platform[0], platform[1])
		if err == nil {
			f.Close()
		}
		items = append(items, gin.H{"tag": logasset.Tag, "os": pkg.OS, "arch": pkg.Arch,
			"cached": err == nil, "packageSha256": pkg.PackageSHA256,
			"executableSha256": pkg.ExecutableSHA256})
	}
	c.JSON(http.StatusOK, gin.H{"assets": items, "license": logasset.License})
}

// PackageURL signs a node-scoped ten-minute URL; callers never log the token.
func (h *LogVLAssetHandler) PackageURL(baseURL, nodeUUID, goos, arch string) (string, string, error) {
	if h == nil || h.signer == nil {
		return "", "", fmt.Errorf("log-vl asset signer unavailable")
	}
	pkg, ok := logasset.Approved(goos, arch)
	if !ok {
		return "", "", fmt.Errorf("unapproved log-vl platform")
	}
	if strings.TrimSpace(nodeUUID) == "" {
		return "", "", fmt.Errorf("node UUID required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("invalid CP asset base URL")
	}
	token, err := h.signer.IssueWorkerAssetToken(service.WorkerAssetTokenScope{
		Version: logasset.Tag, OS: goos, Arch: arch, Purpose: service.WorkerAssetPurposeLogVL, NodeUUID: nodeUUID,
	})
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%s://%s/log-vl-assets/%s/%s/%s/package?token=%s",
		parsed.Scheme, parsed.Host, logasset.Tag, goos, arch, url.QueryEscape(token)), pkg.PackageSHA256, nil
}

func (h *LogVLAssetHandler) Download(c *gin.Context) {
	if c.Param("tag") != logasset.Tag {
		c.JSON(http.StatusNotFound, gin.H{"error": "ASSET_NOT_APPROVED"})
		return
	}
	scope, err := h.signer.ValidateWorkerAssetToken(c.Query("token"), service.WorkerAssetTokenScope{
		Version: logasset.Tag, OS: c.Param("os"), Arch: c.Param("arch"), Purpose: service.WorkerAssetPurposeLogVL,
	})
	if err != nil || scope.NodeUUID == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "INVALID_ASSET_TOKEN"})
		return
	}
	if _, err := h.nodes.GetByUUID(scope.NodeUUID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "NODE_NOT_REGISTERED"})
		return
	}
	f, pkg, err := h.store.Open(c.Param("os"), c.Param("arch"))
	if err != nil {
		status := http.StatusNotFound
		if !os.IsNotExist(err) {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"error": "ASSET_NOT_AVAILABLE"})
		return
	}
	defer f.Close()
	c.Header("Content-Type", "application/octet-stream")
	c.Header("X-Content-SHA256", pkg.PackageSHA256)
	http.ServeContent(c.Writer, c.Request, pkg.FileName, time.Time{}, f)
}
