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
	nodes *service.NodeService
	pool  *cpgrpc.ClientPool
}

func NewLogRuntimeHandler(nodes *service.NodeService, pool *cpgrpc.ClientPool) *LogRuntimeHandler {
	return &LogRuntimeHandler{nodes: nodes, pool: pool}
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
	resp, err := client.Worker.LogResolveIngestGaps(c.Request.Context(), &workerpb.LogResolveIngestGapsRequest{
		RequestId: c.GetHeader("X-Request-ID"), ProtocolVersion: "log-query/1",
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED"})
		return
	}
	if resp == nil || resp.GetError() != nil || resp.GetState() != workerpb.LogTaskState_LOG_TASK_SUCCEEDED {
		code := "LOG_NOT_READY"
		if resp != nil && resp.GetError() != nil {
			code = resp.GetError().GetCode().String()
		}
		c.JSON(http.StatusConflict, gin.H{"error": "GAP_RESOLUTION_FAILED", "code": code})
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

func (h *LogRuntimeHandler) ArchiveStatus(c *gin.Context) {
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
	client, ok := h.client(c)
	if !ok {
		return
	}
	var body logRehydrateRequest
	if err := c.ShouldBindJSON(&body); err != nil || len(body.ArchiveObjectIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "archiveObjectIds required"})
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
