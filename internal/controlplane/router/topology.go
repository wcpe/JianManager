package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// TopologyHandler 群组拓扑聚合路由（FR-335）：一次返回全量 proxy 及其注册与 network 成员归属，
// 消除拓扑页 per-proxy N+1。注册在平台管理员组下（与注册/群组读取同权限面）。
type TopologyHandler struct {
	reg *service.RegistrationService
	net *service.NetworkService
}

// NewTopologyHandler 创建拓扑聚合路由处理器。
func NewTopologyHandler(reg *service.RegistrationService, net *service.NetworkService) *TopologyHandler {
	return &TopologyHandler{reg: reg, net: net}
}

// topologyResponse GET /topology 的聚合响应体（FR-335，契约见 specs/topology-scale/api.md）。
type topologyResponse struct {
	Proxies  []service.ProxyTopology    `json:"proxies"`
	Networks []service.NetworkTopoBrief `json:"networks"`
}

// Get GET /topology —— 全量群组拓扑聚合。
func (h *TopologyHandler) Get(c *gin.Context) {
	proxies, existing, err := h.reg.Topology()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}
	networks, err := h.net.TopoBriefs(existing)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, topologyResponse{Proxies: proxies, Networks: networks})
}

// RegisterRoutes 注册拓扑聚合路由。
func (h *TopologyHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/topology", h.Get)
}
