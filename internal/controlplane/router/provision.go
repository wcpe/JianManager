package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// ProvisionHandler 处理核心查询、一键搭建子服（FR-034）、搭建代理（FR-035）与复制子服（FR-036）。
// 注册在平台管理员路由组下。
type ProvisionHandler struct {
	core  *service.CoreService
	prov  *service.ProvisionService
	proxy *service.ProxyService
	clone *service.CloneService
}

func NewProvisionHandler(core *service.CoreService, prov *service.ProvisionService, proxy *service.ProxyService, clone *service.CloneService) *ProvisionHandler {
	return &ProvisionHandler{core: core, prov: prov, proxy: proxy, clone: clone}
}

// Cores GET /cores?type=paper —— 无 mcVersion 时返回可用版本；带 mcVersion 时返回该版本的
// 下载信息（build<=0 取最新）。
func (h *ProvisionHandler) Cores(c *gin.Context) {
	coreType := c.DefaultQuery("type", "paper")
	mcVersion := c.Query("mcVersion")
	if mcVersion == "" {
		versions, err := h.core.ListVersions(c.Request.Context(), coreType)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "CORE_REPO_ERROR", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"type": coreType, "versions": versions})
		return
	}
	build, _ := strconv.Atoi(c.Query("build"))
	info, err := h.core.ResolveBuild(c.Request.Context(), coreType, mcVersion, build)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "CORE_REPO_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, info)
}

// ProvisionBukkit POST /instances/provision/bukkit —— 一键搭建 Paper 后端子服。
func (h *ProvisionHandler) ProvisionBukkit(c *gin.Context) {
	var req service.ProvisionBukkitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	inst, err := h.prov.ProvisionBukkit(c.Request.Context(), req)
	if err != nil {
		// inst 非空表示实例已创建但搭建步骤（下载/写配置）失败，回报实例供重试/删除。
		if inst != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "PROVISION_FAILED", "message": err.Error(), "instance": inst})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "PROVISION_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, inst)
}

// ProvisionProxy POST /instances/provision/proxy —— 向导式搭建代理实例（FR-035）。
func (h *ProvisionHandler) ProvisionProxy(c *gin.Context) {
	var req service.ProvisionProxyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	result, err := h.proxy.ProvisionProxy(c.Request.Context(), req)
	if err != nil {
		// result 非空（含已创建代理实例）表示部分失败，回报供重试/删除。
		if result != nil && result.Instance != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "PROVISION_FAILED", "message": err.Error(), "result": result})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "PROVISION_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, result)
}

// ResyncProxy POST /proxies/:id/resync —— 重新把注册关系与 secret 推到代理配置与各后端（FR-035）。
func (h *ProvisionHandler) ResyncProxy(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的代理 ID"})
		return
	}
	consistent, warnings, err := h.proxy.Resync(uint(id))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "RESYNC_FAILED", "message": err.Error(), "warnings": warnings})
		return
	}
	c.JSON(http.StatusOK, gin.H{"synced": true, "secretConsistent": consistent, "warnings": warnings})
}

// Ports GET /nodes/:id/ports —— 查看某节点端口占用与分配范围（FR-032）。
func (h *ProvisionHandler) Ports(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的节点 ID"})
		return
	}
	result, err := h.prov.NodePorts(uint(id))
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NODE_NOT_FOUND", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// CloneInstance POST /instances/:id/clone —— 一键复制 backend 子服（FR-036）。
func (h *ProvisionHandler) CloneInstance(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的实例 ID"})
		return
	}
	var req service.CloneInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	result, err := h.clone.Clone(c.Request.Context(), uint(id), req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInstanceNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "INSTANCE_NOT_FOUND", "message": err.Error()})
		case errors.Is(err, service.ErrSourceNotBackend):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "NOT_A_BACKEND", "message": err.Error()})
		case errors.Is(err, service.ErrSourceRunning):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "SOURCE_RUNNING", "message": err.Error()})
		default:
			// result 非空且含实例：已创建但复制/修正失败，回报供重试/删除。
			if result != nil && result.Instance != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "CLONE_FAILED", "message": err.Error(), "result": result})
				return
			}
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "CLONE_FAILED", "message": err.Error()})
		}
		return
	}
	status := http.StatusCreated
	if result.DryRun {
		status = http.StatusOK
	}
	c.JSON(status, result)
}

func (h *ProvisionHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/cores", h.Cores)
	rg.POST("/instances/provision/bukkit", h.ProvisionBukkit)
	rg.POST("/instances/provision/proxy", h.ProvisionProxy)
	rg.POST("/instances/:id/clone", h.CloneInstance)
	rg.POST("/proxies/:id/resync", h.ResyncProxy)
	rg.GET("/nodes/:id/ports", h.Ports)
}
