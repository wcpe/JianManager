package router

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// LogRuntimeHandler exposes Worker-managed VictoriaLogs control through CP.
// The handler never connects to VL directly; all calls use the reverse tunnel.
type LogRuntimeHandler struct {
	nodes     *service.NodeService
	pool      *cpgrpc.ClientPool
	authz     *service.AuthzService
	instances *service.InstanceService
}

func NewLogRuntimeHandler(nodes *service.NodeService, pool *cpgrpc.ClientPool, authz *service.AuthzService, instances *service.InstanceService) *LogRuntimeHandler {
	return &LogRuntimeHandler{nodes: nodes, pool: pool, authz: authz, instances: instances}
}

func (h *LogRuntimeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	if h == nil || h.nodes == nil || h.pool == nil {
		return
	}
	rg.GET("/nodes/:id/log-runtime", h.Status)
	rg.POST("/nodes/:id/log-runtime/:namespace/:action", h.Control)
	rg.POST("/nodes/:id/log-runtime/migrate", h.MigratePartition)
	rg.POST("/nodes/:id/log-runtime/ingest/resolve-gaps", h.ResolveIngestGaps)
	rg.GET("/nodes/:id/log-archive/status", h.ArchiveStatus)
	rg.POST("/nodes/:id/log-archive/rehydrate", h.Rehydrate)
}

func (h *LogRuntimeHandler) ResolveIngestGaps(c *gin.Context) {
	client, ok := h.client(c)
	if !ok {
		return
	}
	// 可选 body：{"storageNamespace":"inst:153/stderr"}。
	// 传了：只解该源（显式人工确认，见 Worker 侧 ResolveCoveredGapsForSource——
	// 用于解开「唯一未就绪目标正是持有缺口者」的自我指涉死锁）；
	// 不传：与既有整节点行为逐字一致（向后兼容）。
	var body struct {
		StorageNamespace string `json:"storageNamespace"`
	}
	if c.Request.ContentLength > 0 {
		if bindErr := c.ShouldBindJSON(&body); bindErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
			return
		}
	}
	resp, err := client.Worker.LogResolveIngestGaps(c.Request.Context(), &workerpb.LogResolveIngestGapsRequest{
		RequestId: c.GetHeader("X-Request-ID"), ProtocolVersion: "log-query/1",
		StorageNamespace: strings.TrimSpace(body.StorageNamespace),
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED"})
		return
	}
	if resp == nil || resp.GetError() != nil || resp.GetState() != workerpb.LogTaskState_LOG_TASK_SUCCEEDED {
		code := "LOG_NOT_READY"
		// 转述 Worker 的具体原因：只回裸 code 会让运维无从判断是「源不存在」「账本不一致」
		// 还是「缺口未覆盖」，实测会把排查拖成盲猜。
		message := ""
		if resp != nil && resp.GetError() != nil {
			code = resp.GetError().GetCode().String()
			message = resp.GetError().GetMessage()
		}
		c.JSON(http.StatusConflict, gin.H{"error": "GAP_RESOLUTION_FAILED", "code": code, "message": message})
		return
	}
	c.JSON(http.StatusOK, gin.H{"state": "resolved"})
}

type logMigratePartitionRequest struct {
	StorageNamespace string `json:"storageNamespace"`
	UTCDay           string `json:"utcDay"`
}

func (h *LogRuntimeHandler) MigratePartition(c *gin.Context) {
	client, ok := h.client(c)
	if !ok {
		return
	}
	var body logMigratePartitionRequest
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.StorageNamespace) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	if _, err := time.Parse("2006-01-02", body.UTCDay); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_UTC_DAY"})
		return
	}
	resp, err := client.Worker.LogMigratePartition(c.Request.Context(), &workerpb.LogMigratePartitionRequest{
		RequestId: c.GetHeader("X-Request-ID"), ProtocolVersion: "log-query/1",
		StorageNamespace: body.StorageNamespace, UtcDay: body.UTCDay, TargetTier: "cold",
	})
	if err != nil {
		statusCode := http.StatusBadGateway
		if status.Code(err) == codes.Unimplemented {
			statusCode = http.StatusNotImplemented
		}
		c.JSON(statusCode, gin.H{"error": "WORKER_RPC_FAILED"})
		return
	}
	if resp == nil || resp.GetError() != nil || resp.GetState() != workerpb.LogTaskState_LOG_TASK_SUCCEEDED {
		code := "LOG_NOT_READY"
		if resp != nil && resp.GetError() != nil {
			code = resp.GetError().GetCode().String()
		}
		c.JSON(http.StatusConflict, gin.H{"error": "MIGRATION_FAILED", "code": code})
		return
	}
	c.JSON(http.StatusOK, gin.H{"state": "migrated", "targetTier": "cold", "storageNamespace": body.StorageNamespace, "utcDay": body.UTCDay})
}

func (h *LogRuntimeHandler) client(c *gin.Context) (*cpgrpc.Client, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_NODE_ID"})
		return nil, false
	}
	node, err := h.nodes.GetByID(uint(id))
	if err != nil || node.UUID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "NODE_NOT_FOUND"})
		return nil, false
	}
	client, ok := h.pool.Get(node.UUID)
	if !ok || client == nil || client.Worker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "WORKER_OFFLINE"})
		return nil, false
	}
	return client, true
}

func (h *LogRuntimeHandler) Status(c *gin.Context) {
	client, ok := h.client(c)
	if !ok {
		return
	}
	resp, err := client.Worker.LogRuntimeStatus(c.Request.Context(), &workerpb.LogRuntimeStatusRequest{RequestId: c.GetHeader("X-Request-ID")})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED", "message": err.Error()})
		return
	}
	if resp.GetError() != nil {
		status := http.StatusServiceUnavailable
		if resp.GetError().GetCode() == workerpb.LogErrorCode_LOG_UNSUPPORTED {
			status = http.StatusNotImplemented
		}
		c.JSON(status, gin.H{"error": resp.GetError().GetCode().String(), "message": resp.GetError().GetMessage()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *LogRuntimeHandler) Control(c *gin.Context) {
	client, ok := h.client(c)
	if !ok {
		return
	}
	ns := strings.ToLower(strings.TrimSpace(c.Param("namespace")))
	action := strings.ToLower(strings.TrimSpace(c.Param("action")))
	if ns == "" || action == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RUNTIME_ACTION"})
		return
	}
	resp, err := client.Worker.LogRuntimeControl(c.Request.Context(), &workerpb.LogRuntimeControlRequest{
		RequestId: c.GetHeader("X-Request-ID"), Namespace: ns, Action: action,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED", "message": err.Error()})
		return
	}
	if resp.GetError() != nil {
		status := http.StatusServiceUnavailable
		if resp.GetError().GetCode() == workerpb.LogErrorCode_LOG_UNSUPPORTED {
			status = http.StatusNotImplemented
		}
		c.JSON(status, gin.H{"error": resp.GetError().GetCode().String(), "message": resp.GetError().GetMessage()})
		return
	}
	c.JSON(http.StatusAccepted, resp)
}

func archiveQueryBase(c *gin.Context, target string) *workerpb.LogQueryRequestBase {
	if target == "" {
		target = "node:" + c.Param("id")
	}
	return &workerpb.LogQueryRequestBase{
		ProtocolVersion:   "log-query/1",
		RequestId:         c.GetHeader("X-Request-ID"),
		TimeRange:         &workerpb.LogTimeRange{FromUtc: c.Query("from"), ToUtc: c.Query("to")},
		AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{target}},
		Budget:            &workerpb.LogQueryBudget{Limit: 200},
	}
}

func archiveObjectIDs(c *gin.Context) []string {
	parts := strings.Split(c.Query("objectIds"), ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// authorizeArchiveTarget 校验归档查询/恢复的目标是否在调用方授权范围内（M-4B）。
//
// 缺陷形态：这两个端点只挂 `node.manage`，且把请求体/查询参数里的 `target` 原样下发给
// Worker，Worker 侧再由首个 target 生成归档分区键。默认角色下 `node.manage` 仅属平台
// 管理员（本就有全局权限，不新增越权），但权限树允许自定义角色——一旦非平台用户获得
// 该权限，即可提交任意 target，绕过实例组/目标校验探测归档对象存在性，甚至对不属于
// 该路由节点的 namespace 触发 rehydrate（读取、校验并发布归档事件，造成跨目标资源
// 消耗与数据面污染）。
//
// 规则：
//   - 空 target 表示「本节点自身」（既有语义，见 archiveQueryBase），放行；
//   - `inst:<id>` 须通过 CanAccessInstance，且该实例须归属路由指定的 Worker 节点；
//   - `node:<id>` 只允许指向路由节点自身，防止借本节点通道读取其它节点数据；
//   - 其它格式一律拒绝，避免把无法识别的目标交给 Worker 自行解释。
func (h *LogRuntimeHandler) authorizeArchiveTarget(c *gin.Context, target string) bool {
	routeNodeID := c.Param("id")
	if h.authz == nil {
		// 未接线授权服务时拒绝而非放行：归档涉及跨目标读取与恢复，不能默认信任。
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "日志归档授权未就绪"})
		return false
	}
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return true
	}
	switch {
	case strings.HasPrefix(trimmed, "inst:"):
		raw := strings.TrimPrefix(trimmed, "inst:")
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "target 无效"})
			return false
		}
		ok, err := h.authz.CanAccessInstance(access, uint(id))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "授权校验失败"})
			return false
		}
		if !ok {
			// 不区分「不存在」与「无权限」，避免实例存在性枚举。
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权访问该实例归档"})
			return false
		}
		node, err := h.nodes.GetByID(uint(parseUintOrZero(routeNodeID)))
		if err != nil || node == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return false
		}
		if h.instances == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "授权校验失败"})
			return false
		}
		inst, err := h.instances.GetByID(uint(id))
		if err != nil || inst == nil {
			// 实例不存在按无权限处理：避免用该端点枚举实例存在性。
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权访问该实例归档"})
			return false
		}
		if inst.NodeID != node.ID {
			// 目标实例不属于该路由节点：拒绝跨节点读取。
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "目标实例不属于该节点"})
			return false
		}
		return true
	case strings.HasPrefix(trimmed, "node:"):
		if strings.TrimPrefix(trimmed, "node:") != routeNodeID {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "仅允许访问本节点归档"})
			return false
		}
		return true
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "target 仅支持 inst:<id> 或 node:<id>"})
		return false
	}
}

func parseUintOrZero(v string) uint64 {
	id, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func (h *LogRuntimeHandler) ArchiveStatus(c *gin.Context) {
	if !h.authorizeArchiveTarget(c, c.Query("target")) {
		return
	}
	client, ok := h.client(c)
	if !ok {
		return
	}
	resp, err := client.Worker.LogArchiveStatus(c.Request.Context(), &workerpb.LogArchiveStatusRequest{
		Query: archiveQueryBase(c, c.Query("target")), ArchiveObjectIds: archiveObjectIDs(c),
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED", "message": err.Error()})
		return
	}
	if resp.GetError() != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": resp.GetError().GetCode().String(), "message": resp.GetError().GetMessage()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

type logRehydrateRequest struct {
	ArchiveObjectIDs []string `json:"archiveObjectIds"`
	Target           string   `json:"target"`
	FromUTC          string   `json:"fromUtc"`
	ToUTC            string   `json:"toUtc"`
}

func (h *LogRuntimeHandler) Rehydrate(c *gin.Context) {
	var body logRehydrateRequest
	if err := c.ShouldBindJSON(&body); err != nil || len(body.ArchiveObjectIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "archiveObjectIds required"})
		return
	}
	// 授权校验必须在取 client 之前：否则未授权请求也会触发与 Worker 的连接。
	if !h.authorizeArchiveTarget(c, body.Target) {
		return
	}
	client, ok := h.client(c)
	if !ok {
		return
	}
	base := archiveQueryBase(c, body.Target)
	base.TimeRange.FromUtc, base.TimeRange.ToUtc = body.FromUTC, body.ToUTC
	resp, err := client.Worker.LogRehydrate(c.Request.Context(), &workerpb.LogRehydrateRequest{
		Query: base, ArchiveObjectIds: body.ArchiveObjectIDs,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED", "message": err.Error()})
		return
	}
	if resp.GetError() != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": resp.GetError().GetCode().String(), "message": resp.GetError().GetMessage()})
		return
	}
	c.JSON(http.StatusAccepted, resp)
}
