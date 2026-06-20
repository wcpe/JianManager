package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// InstanceHandler 实例路由处理器。
type InstanceHandler struct {
	instanceSvc *service.InstanceService
	authz       *service.AuthzService
}

// NewInstanceHandler 创建实例路由处理器。
func NewInstanceHandler(instanceSvc *service.InstanceService, authz *service.AuthzService) *InstanceHandler {
	return &InstanceHandler{instanceSvc: instanceSvc, authz: authz}
}

// List 实例列表。
// 平台管理员返回全部；组管理员/成员仅返回其可访问组下的实例。
func (h *InstanceHandler) List(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	// 多维筛选（FR-047）：节点/状态/角色/群组/环境/标签任意组合。
	filter := service.InstanceFilter{
		Env: c.Query("env"),
		Tag: c.Query("tag"),
	}
	if v := c.Query("nodeId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		filter.NodeID = &u
	}
	if v := c.Query("status"); v != "" {
		s := model.InstanceStatus(v)
		filter.Status = &s
	}
	if v := c.Query("role"); v != "" {
		r := model.InstanceRole(v)
		filter.Role = &r
	}
	if v := c.Query("networkId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		filter.NetworkID = &u
	}

	// 非平台管理员强制按其可访问组过滤，忽略前端传入的 groupId
	if !access.IsPlatformAdmin {
		groupIDs := accessibleGroupIDs(access)
		if len(groupIDs) == 0 {
			c.JSON(http.StatusOK, []interface{}{})
			return
		}
		instances, err := h.instanceSvc.ListByGroups(groupIDs, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询实例列表失败"})
			return
		}
		c.JSON(http.StatusOK, instances)
		return
	}

	if v := c.Query("groupId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		filter.GroupID = &u
	}

	instances, err := h.instanceSvc.List(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询实例列表失败"})
		return
	}

	c.JSON(http.StatusOK, instances)
}

// Get 实例详情。
func (h *InstanceHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	instance, err := h.instanceSvc.GetByID(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	c.JSON(http.StatusOK, instance)
}

type createInstanceRequest struct {
	NodeID            uint               `json:"nodeId" binding:"required"`
	Name              string             `json:"name" binding:"required"`
	Type              model.InstanceType `json:"type" binding:"required"`
	Role              model.InstanceRole `json:"role"`
	ProcessType       model.ProcessType  `json:"processType" binding:"required"`
	StartCommand      string             `json:"startCommand" binding:"required"`
	JDKID             uint               `json:"jdkId"`
	JavaMajorVersion  int                `json:"javaMajorVersion"`
	LaunchSpec        string             `json:"launchSpec"`
	WorkDir           string             `json:"workDir"`
	EnvVars           map[string]string  `json:"envVars"`
	AutoStart         bool               `json:"autoStart"`
	AutoRestart       bool               `json:"autoRestart"`
	GroupID           uint               `json:"groupId"`
}

// Create 创建实例。
// 平台管理员可创建并指定任意组；组管理员仅可创建并分配到自己管理的组。
func (h *InstanceHandler) Create(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !access.HasPermission(service.PermInstanceCreate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req createInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	// 非平台管理员：必须分配到自己可管理的组
	if !access.IsPlatformAdmin {
		if req.GroupID == 0 || !access.CanManageGroup(req.GroupID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权向该用户组分配实例"})
			return
		}
	}

	instance, err := h.instanceSvc.Create(service.CreateInstanceRequest{
		NodeID:           req.NodeID,
		Name:             req.Name,
		Type:             req.Type,
		Role:             req.Role,
		ProcessType:      req.ProcessType,
		StartCommand:     req.StartCommand,
		JDKID:            req.JDKID,
		JavaMajorVersion: req.JavaMajorVersion,
		LaunchSpec:       req.LaunchSpec,
		WorkDir:          req.WorkDir,
		EnvVars:          req.EnvVars,
		AutoStart:        req.AutoStart,
		AutoRestart:      req.AutoRestart,
		GroupID:          req.GroupID,
	})
	if err != nil {
		if errors.Is(err, service.ErrQuotaExceeded) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "QUOTA_EXCEEDED", "message": err.Error()})
			return
		}
		// 调度拦截（FR-048）：目标节点维护中拒绝接纳新实例。
		if errors.Is(err, service.ErrNodeInMaintenance) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "NODE_MAINTENANCE", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建实例失败"})
		return
	}

	c.JSON(http.StatusCreated, instance)
}

type updateInstanceRequest struct {
	Name         *string            `json:"name"`
	StartCommand *string            `json:"startCommand"`
	AutoStart    *bool              `json:"autoStart"`
	AutoRestart  *bool              `json:"autoRestart"`
	JDKID        *uint              `json:"jdkId"`
	EnvVars      *map[string]string `json:"envVars"`
	// Tags 环境/标签维度（FR-047）：传 null/缺省不变，传数组（含空数组）覆盖。
	Tags *[]string `json:"tags"`
}

// Update 更新实例配置。
func (h *InstanceHandler) Update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req updateInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	instance, err := h.instanceSvc.Update(id, service.UpdateInstanceFields{
		Name:         req.Name,
		StartCommand: req.StartCommand,
		AutoStart:    req.AutoStart,
		AutoRestart:  req.AutoRestart,
		JDKID:        req.JDKID,
		EnvVars:      req.EnvVars,
		Tags:         req.Tags,
	})
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "更新失败"})
		return
	}

	c.JSON(http.StatusOK, instance)
}

// Delete 删除实例。
func (h *InstanceHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	if err := h.instanceSvc.Delete(id); err != nil {
		if errors.Is(err, service.ErrInstanceRunning) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INSTANCE_RUNNING", "message": "实例正在运行，需先停止"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "删除失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Start 启动实例。
func (h *InstanceHandler) Start(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	if err := h.instanceSvc.Start(id); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_TRANSITION", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "启动中"})
}

// Stop 停止实例。
func (h *InstanceHandler) Stop(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	if err := h.instanceSvc.Stop(id); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_TRANSITION", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "停止中"})
}

// Restart 重启实例。
func (h *InstanceHandler) Restart(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	if err := h.instanceSvc.Restart(id); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_TRANSITION", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "重启中"})
}

// Kill 强制终止实例。
func (h *InstanceHandler) Kill(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	if err := h.instanceSvc.Kill(id); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_TRANSITION", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已终止"})
}

// Metrics 获取实例指标。
func (h *InstanceHandler) Metrics(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	metrics, err := h.instanceSvc.GetMetrics(id)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "METRICS_UNAVAILABLE", "message": "无法获取指标"})
		return
	}

	c.JSON(http.StatusOK, metrics)
}

// RegisterRoutes 注册实例路由。
func (h *InstanceHandler) RegisterRoutes(rg *gin.RouterGroup) {
	instances := rg.Group("/instances")
	{
		instances.GET("", h.List)
		instances.POST("", h.Create)
		instances.GET("/:id", h.Get)
		instances.PUT("/:id", h.Update)
		instances.DELETE("/:id", h.Delete)
		instances.POST("/:id/start", h.Start)
		instances.POST("/:id/stop", h.Stop)
		instances.POST("/:id/restart", h.Restart)
		instances.POST("/:id/kill", h.Kill)
		instances.GET("/:id/metrics", h.Metrics)
	}
}

// canAccessInstance 校验当前用户能否访问指定实例，失败或出错均返回 false。
func canAccessInstance(c *gin.Context, authz *service.AuthzService, instanceID uint) bool {
	access := getAccess(c)
	if access == nil {
		return false
	}
	ok, err := authz.CanAccessInstance(access, instanceID)
	if err != nil {
		return false
	}
	return ok
}

// canManageInstance 校验当前用户能否管理（写/删除）指定实例。
func canManageInstance(c *gin.Context, authz *service.AuthzService, instanceID uint) bool {
	access := getAccess(c)
	if access == nil {
		return false
	}
	ok, err := authz.CanManageInstance(access, instanceID)
	if err != nil {
		return false
	}
	return ok
}

// accessibleGroupIDs 将授权上下文中的可访问组集合转为切片。
func accessibleGroupIDs(access *service.UserAccess) []uint {
	ids := make([]uint, 0, len(access.AccessibleGroups))
	for id := range access.AccessibleGroups {
		ids = append(ids, id)
	}
	return ids
}
