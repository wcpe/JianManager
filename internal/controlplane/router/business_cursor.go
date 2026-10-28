package router

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BusinessCursorHandler 业务事件位点缺口查询端点（ADR-107 §3）。
//
// 契约：
//
//	GET /api/v1/business/cursor?instanceId=&domain=&fromSeq=&limit=
//
// 返回该 (instance, domain) 区间内**存在过的位点列表**——只有位点，没有 payload。
//
// 【为什么不返回"缺失区间"】JM 不知道调用方收到了哪些事件，无法单方计算缺口；
// 由调用方拿自己的已收位点做差集是唯一正确的信息来源（ADR-107 §3）。
// 本处理器只做参数解析与响应写出，**永不做差集、不猜缺口**。
type BusinessCursorHandler struct {
	cursor *service.BusinessCursorService
}

// NewBusinessCursorHandler 创建缺口查询处理器。
func NewBusinessCursorHandler(cursor *service.BusinessCursorService) *BusinessCursorHandler {
	return &BusinessCursorHandler{cursor: cursor}
}

// RegisterRoutes 注册缺口查询路由。调用方须已挂 businessStreamAuth（与 SSE 同一套鉴权：
// 这是同一条业务通道的位点视角，不该出现"能订阅却不能查位点"的分裂）。
func (h *BusinessCursorHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/business/cursor", h.Cursor)
}

// businessCursorResponse 缺口查询响应体。
//
// cursors 用非 nil 切片语义由 service 保证（无数据返回 []，不是 null、不是 404）：
// 「这段时间没有位点」是有效答案，不是错误。
type businessCursorResponse struct {
	Cursors []service.BusinessCursorItem `json:"cursors"`
}

// Cursor GET /business/cursor — 查询某实例某域区间内存在过的位点。
func (h *BusinessCursorHandler) Cursor(c *gin.Context) {
	if h.cursor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "CURSOR_UNAVAILABLE", "message": "位点账本未启用"})
		return
	}
	instanceID, err := strconv.ParseUint(strings.TrimSpace(c.Query("instanceId")), 10, 64)
	if err != nil || instanceID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "INVALID_INSTANCE_ID", "message": "instanceId 必填且必须是正整数"})
		return
	}
	domain := strings.TrimSpace(c.Query("domain"))
	if domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": "INVALID_DOMAIN", "message": "domain 必填"})
		return
	}
	// fromSeq / toSeq 缺省为 0（不限下界 / 上界，由 limit 兜住结果规模）。
	// 非法数字直接 400 而不是静默当 0：静默会返回一个"看起来正常"的全量结果，掩盖调用方 bug。
	fromSeq, err := parseBusinessCursorSeq(c.Query("fromSeq"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "INVALID_FROM_SEQ", "message": "fromSeq 必须是非负整数"})
		return
	}
	toSeq, err := parseBusinessCursorSeq(c.Query("toSeq"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "INVALID_TO_SEQ", "message": "toSeq 必须是非负整数"})
		return
	}
	limit := 0
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": "INVALID_LIMIT", "message": "limit 必须是非负整数"})
			return
		}
		limit = n
	}

	items, err := h.cursor.Query(service.BusinessCursorQuery{
		InstanceID: uint(instanceID),
		Domain:     domain,
		FromSeq:    fromSeq,
		ToSeq:      toSeq,
		Limit:      limit,
	})
	if err != nil {
		if errors.Is(err, service.ErrBusinessCursorInvalidQuery) {
			c.JSON(http.StatusBadRequest, gin.H{"code": "INVALID_QUERY", "message": "查询参数非法"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": "CURSOR_QUERY_FAILED", "message": "位点查询失败"})
		return
	}
	c.JSON(http.StatusOK, businessCursorResponse{Cursors: items})
}

// parseBusinessCursorSeq 解析可选位点参数：空串或缺省视为 0（不限），非法负数/非数字报错。
func parseBusinessCursorSeq(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, errors.New("位点必须是非负整数")
	}
	return v, nil
}
