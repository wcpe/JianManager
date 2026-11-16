package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

type JDKHandler struct{ svc *service.JDKService }

func NewJDKHandler(svc *service.JDKService) *JDKHandler { return &JDKHandler{svc: svc} }


// List 返回节点 JDK 清单。
//
// 默认形状不变（裸 model.NodeJDK 数组）；`?withRefs=1` 时改为携带「引用实例 + 删除可行性」
// 的清单（JDKWithRefs），供节点页运行时清单表直接把占用和删除守卫前置到 UI。
// 用查询参数而不是新端点：同一资源的两种视图，权限、路径、缓存键都该一致。
func (h *JDKHandler) List(c *gin.Context) {
	if !requireNodes(c, "node.read", "node.manage") {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	if c.Query("withRefs") == "1" {
		jdks, err := h.svc.ListWithRefs(nodeID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询 JDK 引用关系失败"})
			return
		}
		c.JSON(http.StatusOK, jdks)
		return
	}
	jdks, err := h.svc.List(nodeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询 JDK 列表失败"})
		return
	}
	c.JSON(http.StatusOK, jdks)
}

func (h *JDKHandler) Create(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var req service.CreateJDKRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	jdk, err := h.svc.Create(nodeID, req)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, jdk)
}

// Install 异步发起 JDK 安装：建任务、令 Worker 启动即返回，
// 立即回 202 + {taskId}（不再阻塞最长 20min）。进度/完成经任务中心与站内信查看。
func (h *JDKHandler) Install(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var req service.InstallJDKRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	access := getAccess(c)
	var createdBy uint
	if access != nil {
		createdBy = access.UserID
	}
	task, reused, err := h.svc.InstallAsync(nodeID, req, createdBy)
	if err != nil {
		if errors.Is(err, service.ErrInvalidJDKArch) {
			// 未知 arch 明确 422 拒绝，不再直透下载源产出误导性 404。
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_ARCH", "message": err.Error()})
			return
		}
		if errors.Is(err, service.ErrNodeOffline) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": "节点未连接，无法下发安装任务"})
			return
		}
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		/*
		  同类重型任务已在跑 → 409，并把占用者一并回给前端。
		  【为什么不是 429/423】语义上是「资源被占用、状态冲突」，409 最贴切；
		  前端据此能区分「稍后重试的临时错误」与「需要先去任务中心处理」。
		  `existingTask` 让界面能直接显示「是谁在占着 + 跳过去停掉」，
		  比只给一句"任务冲突"有用得多。
		*/
		if errors.Is(err, service.ErrTaskExclusiveBusy) {
			var busy *service.TaskExclusiveBusyError
			if errors.As(err, &busy) && busy.Existing != nil {
				c.JSON(http.StatusConflict, gin.H{
					"error":        "TASK_EXCLUSIVE_BUSY",
					"message":      service.DescribeExclusiveBusy(err),
					"existingTask": busy.Existing,
				})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": "TASK_EXCLUSIVE_BUSY", "message": service.DescribeExclusiveBusy(err)})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "INSTALL_FAILED", "message": err.Error()})
		return
	}
	// reused=true：同参数的安装已在跑，返回的是**已存在**的任务。
	// 用 200 而不是 202 区分「这次真的受理了新任务」与「复用了一个在跑的任务」——
	// 前端据此显示不同文案（"已提交" vs "已有相同任务，正在跳转"），而不是再起一个下载。
	if reused {
		c.JSON(http.StatusOK, gin.H{"taskId": task.TaskID, "task": task, "reused": true})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"taskId": task.TaskID, "task": task})
}

// Probe 探测节点上某路径（JDK home 或 java 可执行文件）的 JDK 信息，供登记自动填。
func (h *JDKHandler) Probe(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path"})
		return
	}
	res, err := h.svc.Probe(nodeID, req.Path)
	if err != nil {
		if errors.Is(err, service.ErrNodeOffline) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": "节点未连接"})
			return
		}
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "PROBE_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *JDKHandler) Update(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	jdkID, err := parseUintParam(c, "jid")
	if err != nil {
		return
	}
	var req service.CreateJDKRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "bad request"})
		return
	}
	jdk, err := h.svc.Update(nodeID, jdkID, req)
	if err != nil {
		if errors.Is(err, service.ErrJDKInUse) {
			c.JSON(http.StatusConflict, gin.H{"error": "JDK_IN_USE", "message": "jdk in use"})
			return
		}
		if errors.Is(err, service.ErrJDKNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "jdk not found"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, jdk)
}

func (h *JDKHandler) Delete(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	jdkID, err := parseUintParam(c, "jid")
	if err != nil {
		return
	}
	used, err := h.svc.Delete(nodeID, jdkID)
	if err != nil {
		if errors.Is(err, service.ErrJDKInUse) {
			c.JSON(http.StatusConflict, gin.H{"error": "JDK_IN_USE", "message": "JDK 正被实例占用", "instances": used})
			return
		}
		if errors.Is(err, service.ErrJDKNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "JDK 不存在"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

func (h *JDKHandler) RegisterRoutes(rg *gin.RouterGroup) {
	jdks := rg.Group("/nodes/:id/jdks")
	jdks.GET("", h.List)
	jdks.POST("", h.Create)
	jdks.PUT("/:jid", h.Update)
	jdks.POST("/install", h.Install)
	jdks.POST("/probe", h.Probe)
	jdks.DELETE("/:jid", h.Delete)
}

func parseUintParam(c *gin.Context, name string) (uint, error) {
	v, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_ID", "message": "ID 格式错误"})
		return 0, err
	}
	return uint(v), nil
}
