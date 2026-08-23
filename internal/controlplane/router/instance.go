package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
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
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			u := uint(id)
			filter.NodeID = &u
		}
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
		u := uint(parseUintDefault(v, 0))
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
		u := uint(parseUintDefault(v, 0))
		filter.GroupID = &u
	}

	instances, err := h.instanceSvc.List(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询实例列表失败"})
		return
	}

	c.JSON(http.StatusOK, instances)
}

// Search 分页搜索实例（FR-247）：q 名称子串 + 多维筛选 + 排序 + 分页，返回 {items,total,page,pageSize}。
func (h *InstanceHandler) Search(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	p := parseInstanceSearchParams(c, access.IsPlatformAdmin)
	p.Normalize()
	items, total, err := h.instanceSvc.SearchInstances(instanceQueryScope(access), p)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询实例列表失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": p.Page, "pageSize": p.PageSize})
}

// Aggregate 实例维度计数（FR-247）：同筛选下按状态/节点/角色分组计数，供前端筛选 chip / 分组头。
func (h *InstanceHandler) Aggregate(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	p := parseInstanceSearchParams(c, access.IsPlatformAdmin)
	agg, err := h.instanceSvc.AggregateInstances(instanceQueryScope(access), p)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "聚合实例计数失败"})
		return
	}
	c.JSON(http.StatusOK, agg)
}

// parseInstanceSearchParams 从 query 解析搜索/筛选/分页参数。
// 非平台管理员忽略 groupId（其作用域由 instanceQueryScope 强制，与 List 一致）。
func parseInstanceSearchParams(c *gin.Context, isAdmin bool) service.InstanceSearchParams {
	p := service.InstanceSearchParams{
		Query: c.Query("q"),
		Sort:  c.Query("sort"),
		Order: c.Query("order"),
	}
	p.Env = c.Query("env")
	p.Tag = c.Query("tag")
	if v := c.Query("nodeId"); v != "" {
		u := uint(parseUintDefault(v, 0))
		p.NodeID = &u
	}
	if v := c.Query("status"); v != "" {
		st := model.InstanceStatus(v)
		p.Status = &st
	}
	if v := c.Query("role"); v != "" {
		r := model.InstanceRole(v)
		p.Role = &r
	}
	if v := c.Query("networkId"); v != "" {
		u := uint(parseUintDefault(v, 0))
		p.NetworkID = &u
	}
	if isAdmin {
		if v := c.Query("groupId"); v != "" {
			u := uint(parseUintDefault(v, 0))
			p.GroupID = &u
		}
	}
	if v := c.Query("page"); v != "" {
		p.Page = parseIntDefault(v, 0)
	}
	if v := c.Query("pageSize"); v != "" {
		p.PageSize = parseIntDefault(v, 0)
	}
	return p
}

// instanceQueryScope 返回查询作用域：平台管理员 nil（不限组）；否则其可访问组（空集=无可见实例）。
func instanceQueryScope(access *service.UserAccess) []uint {
	if access.IsPlatformAdmin {
		return nil
	}
	return accessibleGroupIDs(access)
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
	NodeID           uint               `json:"nodeId" binding:"required"`
	Name             string             `json:"name" binding:"required"`
	Type             model.InstanceType `json:"type" binding:"required"`
	Role             model.InstanceRole `json:"role"`
	ProcessType      model.ProcessType  `json:"processType" binding:"required"`
	StartCommand     string             `json:"startCommand"`
	JDKID            uint               `json:"jdkId"`
	JavaMajorVersion int                `json:"javaMajorVersion"`
	LaunchSpec       string             `json:"launchSpec"`
	WorkDir          string             `json:"workDir"`
	EnvVars          map[string]string  `json:"envVars"`
	// Image 是 docker 模式的容器镜像引用；仅 processType=docker 时使用（FR-078，ADR-019）。
	Image string `json:"image"`
	// CPULimit/MemLimitMB/DiskLimitMB 是 docker 模式资源限额（FR-079，ADR-019）；仅 docker 模式使用，0=不限制。
	CPULimit    float64 `json:"cpuLimit"`
	MemLimitMB  int64   `json:"memLimitMb"`
	DiskLimitMB int64   `json:"diskLimitMb"`
	AutoStart   bool    `json:"autoStart"`
	AutoRestart bool    `json:"autoRestart"`
	GroupID     uint    `json:"groupId"`
	// ServerPort/QueryPort/ProbePort 是 MC 实例的宿主监听端口（真机验收 FR-404 抓到：
	// 此前缺这三个字段导致 gin 绑定静默丢弃、创建后恒为 0，ServerProbe 插件桥上联 Worker 失败）。
	ServerPort int `json:"serverPort"`
	QueryPort  int `json:"queryPort"`
	ProbePort  int `json:"probePort"`
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
		Image:            req.Image,
		CPULimit:         req.CPULimit,
		MemLimitMB:       req.MemLimitMB,
		DiskLimitMB:      req.DiskLimitMB,
		AutoStart:        req.AutoStart,
		AutoRestart:      req.AutoRestart,
		GroupID:          req.GroupID,
		ServerPort:       req.ServerPort,
		QueryPort:        req.QueryPort,
		ProbePort:        req.ProbePort,
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
		// 非 docker 实例缺启动命令（FR-078）：docker 可空、其它必填。
		if errors.Is(err, service.ErrStartCommandRequired) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "START_COMMAND_REQUIRED", "message": err.Error()})
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
	// CPULimit/MemLimitMB/DiskLimitMB docker 资源限额（FR-079）：传 null/缺省不变，传值（含 0）覆盖。
	CPULimit    *float64 `json:"cpuLimit"`
	MemLimitMB  *int64   `json:"memLimitMb"`
	DiskLimitMB *int64   `json:"diskLimitMb"`
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
	if req.StartCommand != nil || req.EnvVars != nil {
		if !canManageInstanceLaunchSpec(c, h.authz, id) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
	}

	instance, err := h.instanceSvc.Update(id, service.UpdateInstanceFields{
		Name:         req.Name,
		StartCommand: req.StartCommand,
		AutoStart:    req.AutoStart,
		AutoRestart:  req.AutoRestart,
		JDKID:        req.JDKID,
		EnvVars:      req.EnvVars,
		Tags:         req.Tags,
		CPULimit:     req.CPULimit,
		MemLimitMB:   req.MemLimitMB,
		DiskLimitMB:  req.DiskLimitMB,
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
		// FR-310 后运行中实例删除由 CP 编排「先停再删」，仅在无法停止（节点未连接等）时
		// 返回 INSTANCE_RUNNING——透传具体原因与指引，而非笼统「需先停止」。
		if errors.Is(err, service.ErrInstanceRunning) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INSTANCE_RUNNING", "message": err.Error()})
			return
		}
		// 透传失败原因（如 Worker 侧「实例进程仍在运行」「删除工作目录失败」「删除前停止失败」），
		// 让用户知道删除为何中止、可否重试，而非笼统「删除失败」。
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
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
		var pfErr *service.PreflightError
		switch {
		case errors.Is(err, service.ErrNodeOffline):
			// 节点未连接：预检无法执行，返回 409（FR-314）。
			c.JSON(http.StatusConflict, gin.H{"error": "NODE_OFFLINE", "message": err.Error()})
		case errors.As(err, &pfErr):
			// 启动预检未通过：422 带具体原因，前端弹错并可在控制台横幅回显（FR-314）。
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "PREFLIGHT_FAILED", "message": pfErr.Reason})
		default:
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_TRANSITION", "message": err.Error()})
		}
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

// ProcessDetail 探查受管实例进程树内的单个 PID。
func (h *InstanceHandler) ProcessDetail(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	pid, ok := parseProcessPID(c)
	if !ok {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	detail, err := h.instanceSvc.InspectManagedProcess(id, pid)
	if err != nil {
		h.writeManagedProcessError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

type managedProcessActionRequest struct {
	Action  string `json:"action" binding:"required"`
	Confirm bool   `json:"confirm"`
}

// ProcessAction 处置受管实例进程树内的非根 PID。
func (h *InstanceHandler) ProcessAction(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	pid, ok := parseProcessPID(c)
	if !ok {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req managedProcessActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if c.Query("confirm") != "true" || !req.Confirm {
		c.JSON(http.StatusConflict, gin.H{"error": "CONFIRM_REQUIRED", "message": "破坏性操作需二次确认（confirm=true）"})
		return
	}
	result, err := h.instanceSvc.TerminateManagedProcess(id, pid, req.Action)
	if err != nil {
		h.writeManagedProcessError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func parseProcessPID(c *gin.Context) (int32, bool) {
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 32)
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_PID", "message": "pid 必须为正整数"})
		return 0, false
	}
	return int32(pid), true
}

func (h *InstanceHandler) writeManagedProcessError(c *gin.Context, err error) {
	var mpErr *service.ManagedProcessError
	if errors.As(err, &mpErr) {
		switch mpErr.Code {
		case "INVALID_PID", "INVALID_REQUEST":
			c.JSON(http.StatusBadRequest, gin.H{"error": mpErr.Code, "message": mpErr.Error()})
		case "INSTANCE_NOT_FOUND":
			c.JSON(http.StatusNotFound, gin.H{"error": mpErr.Code, "message": mpErr.Error()})
		case "INSTANCE_NOT_RUNNING", "PID_NOT_MANAGED", "ROOT_PROCESS_ACTION_DENIED", "PROCESS_ACTION_FAILED":
			c.JSON(http.StatusConflict, gin.H{"error": mpErr.Code, "message": mpErr.Error()})
		default:
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": mpErr.Code, "message": mpErr.Error()})
		}
		return
	}
	if errors.Is(err, service.ErrNodeOffline) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": err.Error()})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PROCESS_UNAVAILABLE", "message": err.Error()})
}

type instanceCommandRequest struct {
	Command string `json:"command" binding:"required"`
}

// Command 向运行中的实例下发控制台命令（FR-005）。
// 仅对 RUNNING 实例生效，复用既有 SendCommand 委托；命令不改变实例状态。
func (h *InstanceHandler) Command(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req instanceCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	if err := h.instanceSvc.SendCommand(id, req.Command); err != nil {
		switch {
		case errors.Is(err, service.ErrInstanceNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		case errors.Is(err, service.ErrInstanceNotRunning):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INSTANCE_NOT_RUNNING", "message": err.Error()})
		default:
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "COMMAND_FAILED", "message": err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已发送"})
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

// Env 获取实例运行时进程实际环境（FR-344 环境变量下区）。
func (h *InstanceHandler) Env(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	data, err := h.instanceSvc.GetInstanceEnv(id)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ENV_UNAVAILABLE", "message": "无法获取运行时环境"})
		return
	}
	c.JSON(http.StatusOK, data)
}

// RegisterRoutes 注册实例路由。
func (h *InstanceHandler) RegisterRoutes(rg *gin.RouterGroup) {
	instances := rg.Group("/instances")
	{
		instances.GET("", h.List)
		// 静态段与 /:id 同级共存（gin v1.12 支持）：分页搜索 + 维度聚合（FR-247）。
		instances.GET("/search", h.Search)
		instances.GET("/aggregate", h.Aggregate)
		instances.POST("", h.Create)
		instances.GET("/:id", h.Get)
		instances.PUT("/:id", h.Update)
		instances.DELETE("/:id", h.Delete)
		instances.POST("/:id/start", h.Start)
		instances.POST("/:id/stop", h.Stop)
		instances.POST("/:id/restart", h.Restart)
		instances.POST("/:id/kill", h.Kill)
		instances.GET("/:id/processes/:pid", h.ProcessDetail)
		instances.POST("/:id/processes/:pid/actions", h.ProcessAction)
		instances.POST("/:id/command", h.Command)
		instances.GET("/:id/metrics", h.Metrics)
		instances.GET("/:id/env", h.Env)
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

// canManageInstanceLaunchSpec 校验当前用户可修改实例的启动命令和环境变量等高风险启动规格。
func canManageInstanceLaunchSpec(c *gin.Context, authz *service.AuthzService, instanceID uint) bool {
	access := getAccess(c)
	if access == nil {
		return false
	}
	ok, err := authz.CanManageInstanceLaunchSpec(access, instanceID)
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
