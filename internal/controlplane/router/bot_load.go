package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BotLoadHandler 提供目标实例作用域的发压节点容量摘要。
type BotLoadHandler struct {
	capacities *service.BotLoadCapacityDirectory
	instances  *service.InstanceService
	authz      *service.AuthzService
}

// NewBotLoadHandler 创建 Bot 分布式负载容量处理器。
func NewBotLoadHandler(capacities *service.BotLoadCapacityDirectory, instances *service.InstanceService, authz *service.AuthzService) *BotLoadHandler {
	return &BotLoadHandler{capacities: capacities, instances: instances, authz: authz}
}

// LoadNodes 返回共享 CapacityDirectory 的缓存快照，不在 HTTP 层重复缓存。
func (h *BotLoadHandler) LoadNodes(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	instanceID, err := parseRequiredQueryID(c, "instanceId")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "instanceId 必须为有效正整数"})
		return
	}
	if !h.canReadInstance(c, access, instanceID) {
		return
	}
	snapshot, err := h.capacities.Snapshot(c.Request.Context(), 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询发压节点容量失败"})
		return
	}
	total, available := 0, 0
	for _, item := range snapshot.NodeCapacities {
		total += item.MaxBots
		available += item.AvailableBots
	}
	c.JSON(http.StatusOK, gin.H{
		"items": snapshot.NodeCapacities, "totalCapacity": total,
		"availableCapacity": available, "updatedAt": snapshot.UpdatedAt,
	})
}

func (h *BotLoadHandler) canReadInstance(c *gin.Context, access *service.UserAccess, instanceID uint) bool {
	if !access.IsPlatformAdmin {
		ok, err := h.authz.CanAccessInstance(access, instanceID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询实例权限失败"})
			return false
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return false
		}
	}
	if _, err := h.instances.GetByID(instanceID); err != nil {
		if err == service.ErrInstanceNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询实例失败"})
		return false
	}
	return true
}

func parseRequiredQueryID(c *gin.Context, key string) (uint, error) {
	value, err := strconv.ParseUint(c.Query(key), 10, 64)
	if err != nil || value == 0 {
		return 0, strconv.ErrSyntax
	}
	return uint(value), nil
}

// RegisterRoutes 在 /bots/:id 前注册静态路径，避免被参数路由吞掉。
func (h *BotLoadHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/bots/load-nodes", h.LoadNodes)
}
