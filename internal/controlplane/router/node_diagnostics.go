package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// NodeDiagnosticsHandler 拉取节点（Worker）进程自身日志的尾部。
//
// # 与日志中心的区别（不要合并这两件事）
//
// 日志中心（/logs、/logs/federation）回答「实例/平台日志里有什么」，可检索、可分页、
// 有统计；本接口只回答「**这个 Worker 进程**最近发生了什么」，拉完即走、不存储。
//
// 为什么需要单独一条：Worker 自身日志不在 CP 能读到的任何地方——
// Worker 不监听业务端口，它的 journald/日志文件都在节点本机。
// 节点出问题时运维只能 SSH；这条路径把它变成面板里的一次点击。
type NodeDiagnosticsHandler struct {
	nodes *service.NodeService
	pool  *cpgrpc.ClientPool
}

// NewNodeDiagnosticsHandler 创建处理器。nodes/Pool 为 nil 时不注册（见 RegisterRoutes）。
func NewNodeDiagnosticsHandler(nodes *service.NodeService, pool *cpgrpc.ClientPool) *NodeDiagnosticsHandler {
	return &NodeDiagnosticsHandler{nodes: nodes, pool: pool}
}

// RegisterRoutes 注册节点诊断路由。
func (h *NodeDiagnosticsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	if h == nil || h.nodes == nil || h.pool == nil {
		return
	}
	rg.GET("/nodes/:id/diagnostics/log", h.PullSelfLog)
}

// PullSelfLog GET /api/v1/nodes/:id/diagnostics/log?lines=200
//
// # 权限
//
// 仅平台管理员：Worker 日志含节点内部路径、配置键值、上游错误细节。
// 组管理员即使拥有该节点的实例权限，也不应看到节点进程自身的诊断输出。
func (h *NodeDiagnosticsHandler) PullSelfLog(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "节点日志诊断仅限平台管理员"})
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_NODE_ID"})
		return
	}
	node, err := h.nodes.GetByID(uint(id))
	if err != nil || node.UUID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "NODE_NOT_FOUND"})
		return
	}

	client, ok := h.pool.Get(node.UUID)
	if !ok || client == nil || client.Worker == nil {
		// 离线是明确状态，不是服务故障：前端据此提示「节点离线」而不是转圈。
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "WORKER_OFFLINE", "message": "节点离线，无法拉取日志"})
		return
	}

	lines, _ := strconv.Atoi(c.Query("lines"))
	resp, rpcErr := client.Worker.PullSelfLog(c.Request.Context(), &workerpb.PullSelfLogRequest{
		MaxLines: int32(lines),
	})
	if rpcErr != nil {
		if status.Code(rpcErr) == codes.Unimplemented {
			// 旧 Worker 没这个 RPC：必须显式告知升级，不能回空成功让人以为「没日志」。
			c.JSON(http.StatusNotImplemented, gin.H{
				"error":   "RPC_UNIMPLEMENTED",
				"message": "该节点版本不支持日志诊断，请升级节点",
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED", "message": "拉取节点日志失败"})
		return
	}
	if resp == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "EMPTY_RESPONSE", "message": "节点返回空响应"})
		return
	}

	body := gin.H{
		"content":   resp.GetContent(),
		"truncated": resp.GetTruncated(),
		"fileBytes": resp.GetFileBytes(),
		"path":      resp.GetPath(),
	}
	// 节点侧读不到（未启用文件 / 文件尚未创建）时带上说明，而不是让前端显示空白。
	if msg := resp.GetErrorMsg(); msg != "" {
		body["note"] = msg
	}
	c.JSON(http.StatusOK, body)
}
