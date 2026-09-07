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

const (
	logViewPlatform     = "platform"
	logViewNodeInstance = "node_instance"
	logViewAll          = "all"
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
// 可见性规则（FR-049/FR-050/FR-403 权限）：
//   - 平台管理员：可选平台、节点/实例或全部日志视图；
//   - 组成员/组管理员：仅见有权实例的节点/实例日志，平台和全部视图均拒绝。
func (h *LogHandler) buildFilter(c *gin.Context) (service.LogFilter, bool) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return service.LogFilter{}, false
	}

	f := service.LogFilter{Keyword: c.Query("keyword")}
	view := c.DefaultQuery("view", logViewNodeInstance)
	if view != logViewPlatform && view != logViewNodeInstance && view != logViewAll {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "view 仅支持 platform、node_instance 或 all"})
		return service.LogFilter{}, false
	}

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

	if !access.IsPlatformAdmin && view != logViewNodeInstance {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "仅平台管理员可查看平台或全部日志"})
		return service.LogFilter{}, false
	}

	// 资源级隔离：非平台管理员强制收敛到可访问实例，且只看实例日志（隐藏平台日志和节点 Worker 日志）。
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
	if access.IsPlatformAdmin {
		switch view {
		case logViewPlatform:
			src := model.LogSourceControlPlane
			f.Source = &src
			f.Sources = nil
		case logViewNodeInstance:
			f.Source = nil
			f.Sources = []model.LogSource{model.LogSourceWorker, model.LogSourceInstance}
		}
	}

	return f, true
}

// List 日志分页查询（FR-049/FR-050），支持页码与游标两种分页（FR-419）。
//
// 分页模式由参数选择，二者并存互不影响（spec §4.2）：
//   - 页码（默认）：`page` / `pageSize`，返回 `{items,total,page,pageSize}`——日志中心翻页与
//     跳页需要 total，行为一字不改；
//   - 游标：出现 `limit` 或 `cursor` 即切换，返回 `{items,nextCursor,limit}`——控制台向上回溯
//     期间新日志持续涌入表头，OFFSET 会漂移出重复行/丢行，故用 (time,id) 游标。
//
// 过滤条件（含 view 权限收敛、level、instanceId、keyword、from/to）两种模式共用 buildFilter，
// 不存在「游标模式下权限视图变松」的分叉。
func (h *LogHandler) List(c *gin.Context) {
	f, ok := h.buildFilter(c)
	if !ok {
		return
	}

	rawCursor := c.Query("cursor")
	rawLimit := c.Query("limit")
	if rawCursor != "" || rawLimit != "" {
		if rawCursor != "" {
			cursor, err := service.ParseLogCursor(rawCursor)
			if err != nil {
				// 不静默回退到「从最新开始」：那会让翻页中途悄悄跳回表头，表现为反复加载同一页。
				c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "cursor 格式非法，应为 <RFC3339 时间>_<id>"})
				return
			}
			f.Cursor = &cursor
		}
		f.Limit = parseIntDefault(rawLimit, 0)

		res, err := h.logSvc.QueryCursor(f)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询日志失败"})
			return
		}
		c.JSON(http.StatusOK, res)
		return
	}

	f.Page = parseIntDefault(c.Query("page"), 0)
	f.PageSize = parseIntDefault(c.Query("pageSize"), 0)

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
	maxRows := parseIntDefault(c.Query("limit"), 0)

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
