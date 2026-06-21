package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// LogHandler 日志中心路由处理器（FR-049 查询 + 导出）。
type LogHandler struct {
	logSvc *service.LogService
	authz  *service.AuthzService
}

// NewLogHandler 创建日志处理器。
func NewLogHandler(logSvc *service.LogService, authz *service.AuthzService) *LogHandler {
	return &LogHandler{logSvc: logSvc, authz: authz}
}

// RegisterRoutes 注册日志路由。
func (h *LogHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/logs", h.List)
	rg.GET("/logs/export", h.Export)
}

// buildFilter 从查询参数解析过滤条件，并按授权上下文收敛资源可见范围。
// 返回 (filter, ok)；ok=false 表示已写出错误响应。
//
// 可见性规则（FR-049/FR-050 权限）：
//   - 平台管理员：不收敛，可见全部（实例日志 + 平台日志）；
//   - 组成员/组管理员：仅见有权实例的日志，平台日志对其隐藏（强制 source=instance + 实例集合收敛）。
func (h *LogHandler) buildFilter(c *gin.Context) (service.LogFilter, bool) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return service.LogFilter{}, false
	}

	f := service.LogFilter{Keyword: c.Query("keyword")}

	if v := c.Query("source"); v != "" {
		s := model.LogSource(v)
		f.Source = &s
	}
	if v := c.Query("level"); v != "" {
		l := model.LogLevel(v)
		f.Level = &l
	}
	if v := c.Query("instanceId"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			u := uint(id)
			f.InstanceID = &u
		}
	}
	if v := c.Query("nodeId"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			u := uint(id)
			f.NodeID = &u
		}
	}
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		}
	}

	// 资源级隔离：非平台管理员强制收敛到可访问实例，且只看实例日志（隐藏平台日志）。
	if !access.IsPlatformAdmin {
		ids, _, err := h.authz.AccessibleInstanceIDs(access)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return service.LogFilter{}, false
		}
		f.InstanceIDs = ids
		src := model.LogSourceInstance
		f.Source = &src
		// 若调用方显式传了 instanceId，但不在可访问集合内，AccessibleInstanceIDs 收敛会自然过滤掉。
	}

	return f, true
}

// List 日志分页查询（FR-049/FR-050）。过滤与分页在 DB 完成，不全量序列化。
func (h *LogHandler) List(c *gin.Context) {
	f, ok := h.buildFilter(c)
	if !ok {
		return
	}
	f.Page, _ = strconv.Atoi(c.Query("page"))
	f.PageSize, _ = strconv.Atoi(c.Query("pageSize"))

	res, err := h.logSvc.Query(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询日志失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Export 按当前筛选导出日志为 NDJSON 附件（FR-049/FR-050）。
func (h *LogHandler) Export(c *gin.Context) {
	f, ok := h.buildFilter(c)
	if !ok {
		return
	}
	maxRows, _ := strconv.Atoi(c.Query("limit"))

	items, err := h.logSvc.Export(f, maxRows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "导出日志失败"})
		return
	}

	filename := fmt.Sprintf("logs-%s.ndjson", time.Now().Format("20060102-150405"))
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/x-ndjson")
	c.Status(http.StatusOK)

	enc := json.NewEncoder(c.Writer)
	for i := range items {
		// 单行 JSON；写错直接中断（客户端会感知截断）。
		if err := enc.Encode(&items[i]); err != nil {
			return
		}
	}
}
