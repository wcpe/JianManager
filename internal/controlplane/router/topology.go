package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// TopologyHandler 群组拓扑聚合路由（FR-335）：一次返回全量 proxy 及其注册与 network 成员归属，
// 消除拓扑页 per-proxy N+1。注册在平台管理员组下（与注册/群组读取同权限面）。
type TopologyHandler struct {
	reg   *service.RegistrationService
	net   *service.NetworkService
	inst  *service.InstanceService
	authz *service.AuthzService
}

// NewTopologyHandler 创建拓扑聚合路由处理器。inst 为 nil 时 instances 投影退化为空数组
// （FR-453 全量实例上拓扑依赖它；测试装配缺省时不影响既有 proxy/network 契约）。
// authz 用于把 instances 投影按调用者可访问实例收敛（FR-453 自审修复）；nil 时不做收敛。
func NewTopologyHandler(
	reg *service.RegistrationService,
	net *service.NetworkService,
	inst *service.InstanceService,
	authz *service.AuthzService,
) *TopologyHandler {
	return &TopologyHandler{reg: reg, net: net, inst: inst, authz: authz}
}

// topologyResponse GET /topology 的聚合响应体（FR-335 / FR-452/453，契约见 specs/topology-scale/api.md）。
type topologyResponse struct {
	Proxies  []service.ProxyTopology    `json:"proxies"`
	Networks []service.NetworkTopoBrief `json:"networks"`
	// Instances 为全量实例最小投影（FR-452/453）：含未注册实例与配套服务，供完整网络视图。
	Instances []service.TopologyInstanceBrief `json:"instances"`
}

// Get GET /topology —— 全量群组拓扑聚合 + 实例投影（按调用者可访问实例收敛）。
// proxies/networks 维持既有全量契约（FR-335）；instances 投影（FR-452/453）按调用者可访问
// 实例收敛——路由守卫 network.read 被 group_admin/operator/viewer 持有，不收敛会向非管理员
// 泄露全量实例元数据。
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
	// 实例投影（FR-452/453）：未注册实例作为孤立节点上拓扑；一次 IN 查询，无 N+1。
	// 非管理员按可访问实例集合收敛（scope 非 nil）；平台管理员取全量（scope=nil）。
	instances := []service.TopologyInstanceBrief{}
	if h.inst != nil {
		var scope []uint
		// getAccess 在鉴权中间件未装配/顺序变化时可能返回 nil；此处显式判空（与 player.go:44 同风格），
		// 避免 nil 传入 AccessibleInstanceIDs 触发 panic。access==nil 时退化为不收敛（scope=nil 全量）。
		if access := getAccess(c); h.authz != nil && access != nil {
			ids, scoped, err := h.authz.AccessibleInstanceIDs(access)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询可访问实例失败"})
				return
			}
			if scoped {
				// 已收敛：ids 可能为空切片（无可访问实例）→ 空结果，不回落全量。
				scope = ids
				if scope == nil {
					scope = []uint{}
				}
			}
		}
		if instances, err = h.inst.AllBriefs(scope); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, topologyResponse{Proxies: proxies, Networks: networks, Instances: instances})
}

// RegisterRoutes 注册拓扑聚合路由。
func (h *TopologyHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/topology", h.Get)
}
