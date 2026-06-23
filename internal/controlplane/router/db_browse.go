package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// DBBrowseHandler 数据库资源管理器（FR-084）只读浏览路由。
// 平台级独有数据源（架构不变量：数据库仅 Control Plane 读写），仅平台管理员可访问；
// 只读——仅元数据列举与分页行查询，无任何写端点。敏感列脱敏在服务层完成。
type DBBrowseHandler struct {
	svc *service.DBBrowseService
}

// NewDBBrowseHandler 创建数据库浏览路由处理器。
func NewDBBrowseHandler(svc *service.DBBrowseService) *DBBrowseHandler {
	return &DBBrowseHandler{svc: svc}
}

// Tables GET /db/tables — 列出 CP 数据库全部表及行数（仅平台管理员）。
func (h *DBBrowseHandler) Tables(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	tables, err := h.svc.Tables()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取数据库表清单失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tables": tables})
}

// Rows GET /db/tables/:name/rows — 分页查询某表的行（仅平台管理员，敏感列脱敏）。
func (h *DBBrowseHandler) Rows(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	name := c.Param("name")

	params := service.DBRowsParams{
		Sort:         c.Query("sort"),
		Order:        c.Query("order"),
		FilterColumn: c.Query("filterColumn"),
		FilterValue:  c.Query("filterValue"),
	}
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			params.Page = n
		}
	}
	if v := c.Query("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			params.PageSize = n
		}
	}

	res, err := h.svc.TableRows(name, params)
	if err != nil {
		if errors.Is(err, service.ErrTableNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "TABLE_NOT_FOUND", "message": "表不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询表数据失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// RegisterRoutes 注册数据库浏览路由（应挂在平台管理员路由组）。
func (h *DBBrowseHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/db/tables", h.Tables)
	rg.GET("/db/tables/:name/rows", h.Rows)
}
