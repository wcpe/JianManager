package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BusinessEventHandler JBIS 业务事件汇聚只读路由（FR-122，见 ADR-027/028）。
//
// 暴露汇聚后的业务事件流 / 经济镜像 / 跨区聚合的只读视图（平台侧汇聚镜像，非业务真源）。
// 写入由探针事件流自动汇聚（PlayerEventService → BusinessEventService），本处不提供写端点。
// 经济定制页（FR-123）将基于本读契约扩展丰富视图。
type BusinessEventHandler struct {
	svc   *service.BusinessEventService
	authz *service.AuthzService
}

// NewBusinessEventHandler 创建业务事件汇聚只读路由处理器。
func NewBusinessEventHandler(svc *service.BusinessEventService, authz *service.AuthzService) *BusinessEventHandler {
	return &BusinessEventHandler{svc: svc, authz: authz}
}

// requireRead 校验只读权限（instance:read：任意属组用户即可，与 manifest 端点同口径）。
// 返回 false 时已写好响应，调用方直接 return。
func (h *BusinessEventHandler) requireRead(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	return true
}

// ListEvents GET /business/events?domain=&node=&limit= — 取最近业务事件（通用 envelope 视图）。
func (h *BusinessEventHandler) ListEvents(c *gin.Context) {
	if !h.requireRead(c) {
		return
	}
	events, err := h.svc.ListBusinessEvents(service.BusinessEventQuery{
		Domain:   c.Query("domain"),
		NodeUUID: c.Query("node"),
		Limit:    atoiDefault(c.Query("limit"), 0),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询业务事件失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

// ListEconomyMirror GET /business/economy/mirror?player=&currency=&node=&zone=&limit= —
// 查经济镜像最新余额（按 node→zone 维度逐行，跨区同名玩家分行不混）。
func (h *BusinessEventHandler) ListEconomyMirror(c *gin.Context) {
	if !h.requireRead(c) {
		return
	}
	rows, err := h.svc.ListEconomyMirror(service.EconomyMirrorQuery{
		PlayerName: c.Query("player"),
		Currency:   c.Query("currency"),
		NodeUUID:   c.Query("node"),
		ZoneID:     c.Query("zone"),
		Limit:      atoiDefault(c.Query("limit"), 0),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询经济镜像失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"balances": rows})
}

// AggregateEconomy GET /business/economy/aggregate?player=&currency= —
// 取某玩家在各 node→zone 的余额明细（跨区聚合视图基元，刻意逐区返回不盲目求和）。
func (h *BusinessEventHandler) AggregateEconomy(c *gin.Context) {
	if !h.requireRead(c) {
		return
	}
	player := c.Query("player")
	if player == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 player 参数"})
		return
	}
	rows, err := h.svc.AggregateEconomyByZone(player, c.Query("currency"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "经济跨区聚合失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"player": player, "rows": rows})
}

// LeaderboardEconomy GET /business/economy/leaderboard?currency=&zone=&node=&limit= —
// 取某货币余额倒序的 Top-N（FR-123 旁路排行：mce 无 leaderboard API，从 JM 自有镜像表派生，不穿透探针）。
// currency 必填（跨货币余额不可比）；逐 node→zone 行（同名玩家跨区各占一行，不合并）。
func (h *BusinessEventHandler) LeaderboardEconomy(c *gin.Context) {
	if !h.requireRead(c) {
		return
	}
	currency := c.Query("currency")
	if currency == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 currency 参数"})
		return
	}
	rows, err := h.svc.LeaderboardEconomy(service.EconomyLeaderboardQuery{
		Currency: currency,
		ZoneID:   c.Query("zone"),
		NodeUUID: c.Query("node"),
		Limit:    atoiDefault(c.Query("limit"), 0),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "经济排行查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"currency": currency, "rows": rows})
}

// RegisterRoutes 注册业务事件汇聚只读路由（加性追加，平台级只读，不绑定单实例）。
func (h *BusinessEventHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/business/events", h.ListEvents)
	rg.GET("/business/economy/mirror", h.ListEconomyMirror)
	rg.GET("/business/economy/aggregate", h.AggregateEconomy)
	rg.GET("/business/economy/leaderboard", h.LeaderboardEconomy)
}

// atoiDefault 解析查询整型参数，失败/空时回退默认值。
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}
