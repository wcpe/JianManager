package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BotLoadTemplateHandler 命令压测模板 HTTP（FR-370）。
type BotLoadTemplateHandler struct {
	svc   *service.BotLoadTemplateService
	authz *service.AuthzService
}

// NewBotLoadTemplateHandler 创建模板路由处理器。
func NewBotLoadTemplateHandler(svc *service.BotLoadTemplateService, authz *service.AuthzService) *BotLoadTemplateHandler {
	return &BotLoadTemplateHandler{svc: svc, authz: authz}
}

// RegisterRoutes 注册 /bots/load-templates*（须先于 /bots/:id）。
func (h *BotLoadTemplateHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/bots/load-templates")
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.GET("/:id", h.Get)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.POST("/:id/runs", h.CreateRun)
	}
}

func (h *BotLoadTemplateHandler) List(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize"))
	q := service.BotLoadTemplateListQuery{
		Page: page, PageSize: pageSize, Q: c.Query("q"), Tag: c.Query("tag"),
	}
	if owner := c.Query("ownerId"); owner != "" && access.IsPlatformAdmin {
		if id, err := strconv.ParseUint(owner, 10, 64); err == nil {
			u := uint(id)
			q.OwnerID = &u
		}
	}
	res, err := h.svc.List(access.UserID, access.IsPlatformAdmin, q)
	if err != nil {
		writeBotLoadTemplateError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *BotLoadTemplateHandler) Create(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	var input service.BotLoadTemplateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	view, err := h.svc.Create(access.UserID, input)
	if err != nil {
		writeBotLoadTemplateError(c, err)
		return
	}
	c.JSON(http.StatusCreated, view)
}

func (h *BotLoadTemplateHandler) Get(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	view, err := h.svc.Get(id, access.UserID, access.IsPlatformAdmin)
	if err != nil {
		writeBotLoadTemplateError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *BotLoadTemplateHandler) Update(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var input service.BotLoadTemplateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	view, err := h.svc.Update(id, access.UserID, access.IsPlatformAdmin, input)
	if err != nil {
		writeBotLoadTemplateError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *BotLoadTemplateHandler) Delete(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(id, access.UserID, access.IsPlatformAdmin); err != nil {
		writeBotLoadTemplateError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type createRunFromTemplateBody struct {
	InstanceID          uint            `json:"instanceId"`
	Name                string          `json:"name"`
	NamePrefix          string          `json:"namePrefix"`
	Config              json.RawMessage `json:"config"`
	CommandSchedule     json.RawMessage `json:"commandScheduleOverride"`
	LoadProfile         json.RawMessage `json:"loadProfileOverride"`
	Thresholds          json.RawMessage `json:"thresholdsOverride"`
}

func (h *BotLoadTemplateHandler) CreateRun(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var body createRunFromTemplateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if !access.IsPlatformAdmin {
		ok, err := h.authz.CanManageInstance(access, body.InstanceID)
		if err != nil || !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权为该实例创建压测运行"})
			return
		}
	}
	sess, err := h.svc.CreateRunFromTemplate(
		id, access.UserID, access.IsPlatformAdmin,
		body.InstanceID, body.Name, body.NamePrefix, body.Config,
		body.CommandSchedule, body.LoadProfile, body.Thresholds,
	)
	if err != nil {
		writeBotLoadTemplateError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id": sess.ID, "uuid": sess.UUID, "schemaVersion": sess.SchemaVersion,
		"runState": sess.RunState, "status": sess.Status, "instanceId": sess.InstanceID,
		"name": sess.Name, "templateId": sess.TemplateID, "botCount": sess.BotCount,
	})
}

func writeBotLoadTemplateError(c *gin.Context, err error) {
	var scenarioErr *service.ScenarioValidationError
	switch {
	case errors.As(err, &scenarioErr):
		writeBotScenarioValidationError(c, scenarioErr)
	case errors.Is(err, service.ErrBotLoadTemplateNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "模板不存在"})
	case errors.Is(err, service.ErrBotLoadTemplateNameConflict):
		c.JSON(http.StatusConflict, gin.H{"error": service.BotLoadTemplateNameConflict, "message": "活跃模板名称冲突"})
	case errors.Is(err, service.ErrBotLoadProfileInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": service.BotLoadProfileInvalidCode, "message": err.Error()})
	case errors.Is(err, service.ErrBotLoadThresholdsInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": service.BotLoadThresholdsInvalidCode, "message": err.Error()})
	case errors.Is(err, service.ErrBotStressSessionInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		// command schedule 等校验可能包装 ScenarioValidationError 以外的 422
		if errors.Is(err, service.ErrBotLoadInvalidState) {
			c.JSON(http.StatusConflict, gin.H{"error": "BOT_LOAD_INVALID_STATE", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "模板操作失败"})
	}
}

