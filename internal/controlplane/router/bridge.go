package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BridgeHandler 桥能力接入路由处理器（FR-526）。
//
// 三条端点，全部是**只读诊断或通道治理**，不碰业务语义（ADR-106）：
//   - GET  /instances/:id/bridge/status        桥线路健康度（重连/队列/丢弃/在途重载）
//   - PUT  /instances/:id/bridge/subscription  事件主题订阅（整批覆盖）
//   - GET  /instances/:id/business/status      已注册业务域与健康状态（30s 缓存）
//
// 权限口径：三条都要求 instance:read 且实例可访问。订阅虽然会改探针行为，
// 但它改的是「上报多少」（流量/可观测性），不是实例配置也不是业务数据——
// 把它提到 instance:operate 只会逼运维账号去借写权限，反而鼓励共享高权账号。
type BridgeHandler struct {
	bridgeSvc *service.BridgeService
	authz     *service.AuthzService
}

// NewBridgeHandler 创建桥能力路由处理器。
func NewBridgeHandler(bridgeSvc *service.BridgeService, authz *service.AuthzService) *BridgeHandler {
	return &BridgeHandler{bridgeSvc: bridgeSvc, authz: authz}
}

// bridgeSubscriptionRequest 订阅设置请求体（对应 OpenAPI `SubscriptionRequest`）。
//
// 两个字段都用指针切片接收，以便区分"未提供"与"显式空数组"：
// OpenAPI 规定**空数组表示订阅全部**，而缺省字段在语义上也是"这一维度不管"，
// 故两者归一到同一条路径（都当作空集），不需要额外区分。
type bridgeSubscriptionRequest struct {
	Topics  []string `json:"topics"`
	Domains []string `json:"domains"`
}

// bridgeSubscriptionResponse 订阅设置响应（对应 OpenAPI 的 200 schema）。
type bridgeSubscriptionResponse struct {
	Applied bool     `json:"applied"`
	Topics  []string `json:"topics"`
	Domains []string `json:"domains"`
}

// Status GET /instances/:id/bridge/status — 查询探针桥状态。
//
// 探针未连/节点未连一律 200 + connected=false + error：排障接口不该在
// "想查的东西恰好不在"时返回错误，否则前端只能把它渲染成一个红框。
func (h *BridgeHandler) Status(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	res, err := h.bridgeSvc.QueryBridgeStatus(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询桥状态失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// SetSubscription PUT /instances/:id/bridge/subscription — 设置事件主题订阅。
//
// 语义（OpenAPI 明确规定，此处逐条落地）：
//   - 未设置过订阅 → 探针全量上报（兼容现状）；
//   - 重复 PUT 覆盖之前的订阅，**不是追加**；
//   - topics/domains 均为空数组表示"订阅全部"（等价于从未设置）；
//   - 探针未连接 → 409 PROBE_UNAVAILABLE（订阅没有探针就无从生效，不能假装成功）。
func (h *BridgeHandler) SetSubscription(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req bridgeSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	applied, err := h.bridgeSvc.SetSubscription(id, req.Topics, req.Domains)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInstanceNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		case errors.Is(err, service.ErrBridgeProbeUnavailable):
			c.JSON(http.StatusConflict, gin.H{"error": "PROBE_UNAVAILABLE", "message": "探针未连接，订阅无法生效"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "订阅下发失败"})
		}
		return
	}
	c.JSON(http.StatusOK, bridgeSubscriptionResponse{
		Applied: true,
		Topics:  applied,
		// domains 原样回显请求值：探针侧没有独立的 domain 维度（已展开进 topics），
		// 回显是让调用方确认"我提交的域被接受了"，而不是让它们去 topics 里反推。
		Domains: nonNilStrings(req.Domains),
	})
}

// BusinessStatus GET /instances/:id/business/status — 查询实例已注册的业务域。
//
// 只渲染不解释：返回探针上报的原始状态，`manifestDigest` 只是指纹。
// 探针不可达时返回最后已知状态并置 stale=true（200，不 5xx——面板还有东西可渲染）。
func (h *BridgeHandler) BusinessStatus(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	res, err := h.bridgeSvc.QueryBusinessStatus(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询域状态失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// nonNilStrings 保证 JSON 里是 `[]` 而不是 `null`（前端对两者处理不一致，契约写的是 array）。
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// RegisterRoutes 注册桥能力路由（加性追加）。
func (h *BridgeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/:id/bridge/status", h.Status)
	rg.PUT("/instances/:id/bridge/subscription", h.SetSubscription)
	rg.GET("/instances/:id/business/status", h.BusinessStatus)
	// 批量域状态：**必须注册在 `/instances/:id/...` 之前**。
	//
	// Gin 的 radix 树对同前缀下「静态段 vs 通配段」有优先级规则，但这条路径的
	// 通配段出现在中间（`/instances/:id/business/status`），与批量的静态段
	// `/instances/business/status/batch` 会在第 2 段分叉。显式前置是零成本的做法，
	// 不依赖框架内部排序，也避免将来新增同类路由时再踩一次。
	rg.POST("/instances/business/status/batch", h.BusinessStatusBatch)
}

// BusinessStatusBatch POST /instances/business/status/batch — 批量查询域状态。
//
// 【路由顺序】必须注册在 `/instances/:id/...` 之前，否则 Gin 会把 "business"
// 当成 :id 匹配进去，请求落到单实例接口并报 "无效的 ID"。
//
// 逐实例独立成败：某实例不存在/无权时在结果里标 notFound，
// 其余实例正常返回。整批失败没有意义——面板要的就是"哪些有数据"。
func (h *BridgeHandler) BusinessStatusBatch(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	var req struct {
		InstanceIDs []uint `json:"instanceIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if len(req.InstanceIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"results": []any{}})
		return
	}
	// 先去重保序：前端可能把同一实例从多个来源拼进来，
	// 而重复 ID 会产生重复探针查询（缓存未命中时是实打实的两次往返）。
	seen := make(map[uint]struct{}, len(req.InstanceIDs))
	ids := make([]uint, 0, len(req.InstanceIDs))
	for _, id := range req.InstanceIDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	// 权限前置过滤：无权访问的实例按 notFound 处理（不区分"不存在"与"无权"，
	// 与单实例接口同一套存在性隐藏口径）。
	allowed := make([]uint, 0, len(ids))
	denied := make(map[uint]struct{})
	for _, id := range ids {
		if canAccessInstance(c, h.authz, id) {
			allowed = append(allowed, id)
		} else {
			denied[id] = struct{}{}
		}
	}

	items, err := h.bridgeSvc.QueryBusinessStatusBatch(allowed)
	if err != nil {
		if errors.Is(err, service.ErrBusinessStatusBatchTooLarge) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":    "BATCH_TOO_LARGE",
				"message":  "单次最多查询 " + strconv.Itoa(service.BusinessStatusBatchMaxIDs) + " 个实例",
				"maxCount": service.BusinessStatusBatchMaxIDs,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "批量查询域状态失败"})
		return
	}

	// 把被拒的实例按原顺序插回结果，保证「等长同序」。
	result := make([]service.BusinessStatusBatchItem, 0, len(ids))
	byID := make(map[uint]service.BusinessStatusBatchItem, len(items))
	for _, it := range items {
		byID[it.InstanceID] = it
	}
	for _, id := range ids {
		if _, isDenied := denied[id]; isDenied {
			result = append(result, service.BusinessStatusBatchItem{InstanceID: id, NotFound: true})
			continue
		}
		if it, ok := byID[id]; ok {
			result = append(result, it)
		}
	}
	c.JSON(http.StatusOK, gin.H{"results": result})
}
