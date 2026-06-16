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
}

// NewInstanceHandler 创建实例路由处理器。
func NewInstanceHandler(instanceSvc *service.InstanceService) *InstanceHandler {
	return &InstanceHandler{instanceSvc: instanceSvc}
}

// List 实例列表。
func (h *InstanceHandler) List(c *gin.Context) {
	var nodeID *uint
	if v := c.Query("nodeId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		nodeID = &u
	}

	var status *model.InstanceStatus
	if v := c.Query("status"); v != "" {
		s := model.InstanceStatus(v)
		status = &s
	}

	var groupID *uint
	if v := c.Query("groupId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		groupID = &u
	}

	instances, err := h.instanceSvc.List(nodeID, status, groupID)
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
	NodeID       uint              `json:"nodeId" binding:"required"`
	Name         string            `json:"name" binding:"required"`
	Type         model.InstanceType `json:"type" binding:"required"`
	ProcessType  model.ProcessType  `json:"processType" binding:"required"`
	StartCommand string            `json:"startCommand" binding:"required"`
	WorkDir      string            `json:"workDir"`
	AutoStart    bool              `json:"autoStart"`
	AutoRestart  bool              `json:"autoRestart"`
	GroupID      uint              `json:"groupId"`
}

// Create 创建实例。
func (h *InstanceHandler) Create(c *gin.Context) {
	var req createInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	instance, err := h.instanceSvc.Create(service.CreateInstanceRequest{
		NodeID:       req.NodeID,
		Name:         req.Name,
		Type:         req.Type,
		ProcessType:  req.ProcessType,
		StartCommand: req.StartCommand,
		WorkDir:      req.WorkDir,
		AutoStart:    req.AutoStart,
		AutoRestart:  req.AutoRestart,
		GroupID:      req.GroupID,
	})
	if err != nil {
		if errors.Is(err, service.ErrQuotaExceeded) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "QUOTA_EXCEEDED", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建实例失败"})
		return
	}

	c.JSON(http.StatusCreated, instance)
}

type updateInstanceRequest struct {
	Name         *string `json:"name"`
	StartCommand *string `json:"startCommand"`
	AutoStart    *bool   `json:"autoStart"`
	AutoRestart  *bool   `json:"autoRestart"`
}

// Update 更新实例配置。
func (h *InstanceHandler) Update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	var req updateInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	instance, err := h.instanceSvc.Update(id, req.Name, req.StartCommand, req.AutoStart, req.AutoRestart)
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
