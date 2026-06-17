package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

type JDKHandler struct{ svc *service.JDKService }

func NewJDKHandler(svc *service.JDKService) *JDKHandler { return &JDKHandler{svc: svc} }

func (h *JDKHandler) List(c *gin.Context) {
	if !requirePlatformAdmin(c) { return }
	nodeID, err := parseUintParam(c, "id"); if err != nil { return }
	jdks, err := h.svc.List(nodeID)
	if err != nil { c.JSON(http.StatusInternalServerError, gin.H{"error":"INTERNAL_ERROR","message":"查询 JDK 列表失败"}); return }
	c.JSON(http.StatusOK, jdks)
}

func (h *JDKHandler) Create(c *gin.Context) {
	if !requirePlatformAdmin(c) { return }
	nodeID, err := parseUintParam(c, "id"); if err != nil { return }
	var req service.CreateJDKRequest
	if err := c.ShouldBindJSON(&req); err != nil { c.JSON(http.StatusBadRequest, gin.H{"error":"INVALID_REQUEST","message":"请求参数错误"}); return }
	jdk, err := h.svc.Create(nodeID, req)
	if err != nil { c.JSON(http.StatusUnprocessableEntity, gin.H{"error":"BUSINESS_ERROR","message":err.Error()}); return }
	c.JSON(http.StatusCreated, jdk)
}

func (h *JDKHandler) Install(c *gin.Context) {
	if !requirePlatformAdmin(c) { return }
	nodeID, err := parseUintParam(c, "id"); if err != nil { return }
	var req service.InstallJDKRequest
	if err := c.ShouldBindJSON(&req); err != nil { c.JSON(http.StatusBadRequest, gin.H{"error":"INVALID_REQUEST","message":"请求参数错误"}); return }
	if err := h.svc.Install(nodeID, req); err != nil { c.JSON(http.StatusNotImplemented, gin.H{"error":"NOT_IMPLEMENTED","message":err.Error()}); return }
	c.JSON(http.StatusAccepted, gin.H{"message":"安装任务已创建"})
}

func (h *JDKHandler) Delete(c *gin.Context) {
	if !requirePlatformAdmin(c) { return }
	nodeID, err := parseUintParam(c, "id"); if err != nil { return }
	jdkID, err := parseUintParam(c, "jid"); if err != nil { return }
	used, err := h.svc.Delete(nodeID, jdkID)
	if err != nil {
		if errors.Is(err, service.ErrJDKInUse) { c.JSON(http.StatusConflict, gin.H{"error":"JDK_IN_USE","message":"JDK 正被实例占用","instances":used}); return }
		if errors.Is(err, service.ErrJDKNotFound) { c.JSON(http.StatusNotFound, gin.H{"error":"NOT_FOUND","message":"JDK 不存在"}); return }
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error":"BUSINESS_ERROR","message":err.Error()}); return
	}
	c.JSON(http.StatusOK, gin.H{"message":"已删除"})
}

func (h *JDKHandler) RegisterRoutes(rg *gin.RouterGroup) {
	jdks := rg.Group("/nodes/:id/jdks")
	jdks.GET("", h.List)
	jdks.POST("", h.Create)
	jdks.POST("/install", h.Install)
	jdks.DELETE("/:jid", h.Delete)
}

func parseUintParam(c *gin.Context, name string) (uint, error) {
	v, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil { c.JSON(http.StatusBadRequest, gin.H{"error":"INVALID_ID","message":"ID 格式错误"}); return 0, err }
	return uint(v), nil
}
