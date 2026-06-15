package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// ScheduleHandler 定时任务路由处理器。
type ScheduleHandler struct {
	scheduleSvc *service.ScheduleService
}

func NewScheduleHandler(scheduleSvc *service.ScheduleService) *ScheduleHandler {
	return &ScheduleHandler{scheduleSvc: scheduleSvc}
}

func (h *ScheduleHandler) List(c *gin.Context) {
	var instanceID *uint
	if v := c.Query("instanceId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		instanceID = &u
	}
	schedules, err := h.scheduleSvc.List(instanceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, schedules)
}

func (h *ScheduleHandler) Create(c *gin.Context) {
	var req service.CreateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	schedule, err := h.scheduleSvc.Create(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusCreated, schedule)
}

func (h *ScheduleHandler) Update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req struct {
		CronExpr *string `json:"cronExpr"`
		Enabled  *bool   `json:"enabled"`
		Action   *string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	schedule, err := h.scheduleSvc.Update(id, req.CronExpr, req.Enabled, req.Action)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
		return
	}
	c.JSON(http.StatusOK, schedule)
}

func (h *ScheduleHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.scheduleSvc.Delete(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

func (h *ScheduleHandler) RegisterRoutes(rg *gin.RouterGroup) {
	schedules := rg.Group("/schedules")
	{
		schedules.GET("", h.List)
		schedules.POST("", h.Create)
		schedules.PUT("/:id", h.Update)
		schedules.DELETE("/:id", h.Delete)
	}
}
