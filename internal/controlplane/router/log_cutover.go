package router

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// logCutoverAuditAction FR-481 管理面变更审计动作。
const logCutoverAuditAction = "log.cutover.update"

// WorkerUUIDLister 列出 CP 注册的全部 Worker/节点身份（F-005 全集就绪校验）。
type WorkerUUIDLister interface {
	ListWorkerUUIDs() ([]string, error)
}

type WorkerCutoverReadinessProvider interface {
	CutoverReadinessAvailable() bool
	CutoverReadiness(ctx context.Context, workerUUID string) (service.LogCutoverWatermark, error)
}

// nodeWorkerUUIDLister 用 NodeService 列出注册节点 UUID。
type nodeWorkerUUIDLister struct {
	svcs *Services
}

func (n nodeWorkerUUIDLister) ListWorkerUUIDs() ([]string, error) {
	if n.svcs == nil || n.svcs.Node == nil {
		return nil, fmt.Errorf("node service unavailable")
	}
	nodes, err := n.svcs.Node.List()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(nodes))
	for _, nd := range nodes {
		if nd.UUID != "" {
			out = append(out, nd.UUID)
		}
	}
	return out, nil
}

func (n nodeWorkerUUIDLister) CutoverReadiness(ctx context.Context, workerUUID string) (service.LogCutoverWatermark, error) {
	if n.svcs == nil || n.svcs.LogRuntimePool == nil {
		return service.LogCutoverWatermark{}, fmt.Errorf("Worker pool unavailable")
	}
	client, ok := n.svcs.LogRuntimePool.Get(workerUUID)
	if !ok || client == nil || client.Worker == nil {
		return service.LogCutoverWatermark{}, fmt.Errorf("worker %s offline", workerUUID)
	}
	resp, err := client.Worker.LogCutoverReadiness(ctx, &workerpb.LogCutoverReadinessRequest{ProtocolVersion: "log-query/1"})
	if err != nil {
		return service.LogCutoverWatermark{}, err
	}
	if resp.GetError() != nil {
		return service.LogCutoverWatermark{}, fmt.Errorf("worker %s: %s", workerUUID, resp.GetError().GetMessage())
	}
	mark := service.LogCutoverWatermark{WorkerUUID: workerUUID, CapabilityConfirmed: resp.GetCapabilityConfirmed(), LedgerReady: resp.GetLedgerReady()}
	if resp.GetCutoffTimeUtc() != "" {
		mark.CutoffTime, _ = time.Parse(time.RFC3339Nano, resp.GetCutoffTimeUtc())
	}
	return mark, nil
}

func (n nodeWorkerUUIDLister) CutoverReadinessAvailable() bool {
	return n.svcs != nil && n.svcs.LogRuntimePool != nil
}

// LogCutoverHandler FR-481 日志入库切换与 Legacy 只读管理面。
//
// 全部路由平台管理员专用；PUT 写审计 action=log.cutover.update（audit 存在时）。
type LogCutoverHandler struct {
	logSvc *service.LogService
	audit  *service.AuditService
	// workers 受影响 Worker 全集来源；nil 时仅用请求∪库内水位（测试）。
	workers WorkerUUIDLister
}

// NewLogCutoverHandler 创建切换/Legacy 管理面处理器。
func NewLogCutoverHandler(logSvc *service.LogService, audit *service.AuditService, workers WorkerUUIDLister) *LogCutoverHandler {
	return &LogCutoverHandler{logSvc: logSvc, audit: audit, workers: workers}
}

// validateCutoverEnable 校验全局 enabled=true（F-002/F-005）。
//
// 必须证明 CP 注册的 **每一个** Worker 都已就绪；仅存在部分 ready 水位时拒绝，
// 避免全局门闩把未就绪节点的旧入库路径一起关掉造成日志缺口。
func (h *LogCutoverHandler) validateCutoverEnable(req putLogCutoverRequest) error {
	st := h.logSvc.CutoverStatus()
	ready := make(map[string]bool)
	for _, w := range req.WorkerWatermarks {
		if !w.Apply {
			continue
		}
		if !(w.CapabilityConfirmed && w.LedgerReady) {
			return fmt.Errorf("apply 水位要求 capabilityConfirmed 与 ledgerReady: %s", w.WorkerUUID)
		}
		ready[w.WorkerUUID] = true
	}
	for _, w := range st.Watermarks {
		if w.CutoverApplied || w.Ready() {
			ready[w.WorkerUUID] = true
		}
	}

	// 受影响全集 = CP 注册节点 ∪ 库内水位 ∪ 本请求水位；全集内每个都必须 ready。
	requiredSet := map[string]bool{}
	if h.workers != nil {
		ids, err := h.workers.ListWorkerUUIDs()
		if err != nil {
			return fmt.Errorf("无法获取 Worker 全集，禁止全局开启切换: %w", err)
		}
		for _, id := range ids {
			if id != "" {
				requiredSet[id] = true
			}
		}
	}
	for _, w := range st.Watermarks {
		if w.WorkerUUID != "" {
			requiredSet[w.WorkerUUID] = true
		}
	}
	for _, w := range req.WorkerWatermarks {
		if w.WorkerUUID != "" {
			requiredSet[w.WorkerUUID] = true
		}
	}
	if len(requiredSet) == 0 {
		return fmt.Errorf("无法确定受影响 Worker 集合，禁止全局开启切换")
	}
	var missing []string
	for id := range requiredSet {
		if !ready[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("以下 Worker 尚未全部就绪，禁止全局开启切换: %v（需 capabilityConfirmed+ledgerReady）", missing)
	}
	return nil
}

// RegisterRoutes 注册 FR-481 管理面路由。rg 应已挂平台级权限节点；handler 再判 IsPlatformAdmin。
func (h *LogCutoverHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/logs/cutover", h.GetCutover)
	rg.PUT("/logs/cutover", h.UpdateCutover)
	rg.GET("/logs/legacy", h.ListLegacy)
}

// requirePlatformAdmin 管理面门禁：仅平台管理员。
func (h *LogCutoverHandler) requirePlatformAdmin(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "仅平台管理员可管理日志切换与 Legacy"})
		return false
	}
	return true
}

// GetCutover GET /api/v1/logs/cutover — 切换状态 + 水位 + Legacy 保留预算。
func (h *LogCutoverHandler) GetCutover(c *gin.Context) {
	if !h.requirePlatformAdmin(c) {
		return
	}
	if h.logSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SERVICE_UNAVAILABLE", "message": "日志服务未初始化"})
		return
	}
	c.JSON(http.StatusOK, cutoverStatusJSON(h.logSvc.CutoverStatus()))
}

// cutoverStatusJSON 统一 GET/PUT 响应信封：legacy 预算嵌套展示。
func cutoverStatusJSON(st service.LogCutoverStatus) gin.H {
	return gin.H{
		"enabled":      st.Enabled,
		"blockedCount": st.BlockedCount,
		"watermarks":   st.Watermarks,
		"legacy": gin.H{
			"retentionDays": st.LegacyRetentionDays,
			"maxTotalMB":    st.LegacyMaxTotalMB,
		},
		"platformPurgeExcludesLegacy": st.PlatformPurgeExcludesLegacy,
	}
}

// logCutoverWatermarkRequest PUT 水位条目。apply=true 时要求 capability+ledger_ready。
type logCutoverWatermarkRequest struct {
	WorkerUUID          string     `json:"workerUuid"`
	CapabilityConfirmed bool       `json:"capabilityConfirmed"`
	LedgerReady         bool       `json:"ledgerReady"`
	CutoffTime          *time.Time `json:"cutoffTime"`
	// Apply 尝试原子登记切换水位。true 时 CapabilityConfirmed && LedgerReady 必填。
	Apply bool `json:"apply"`
}

// putLogCutoverRequest PUT /api/v1/logs/cutover 请求体。
type putLogCutoverRequest struct {
	// Enabled 全局切换开关；nil 表示本次不改。
	Enabled *bool `json:"enabled"`
	// WorkerWatermarks 逐 Worker 水位登记/应用。
	WorkerWatermarks []logCutoverWatermarkRequest `json:"workerWatermarks"`
}

// UpdateCutover PUT /api/v1/logs/cutover — {enabled, workerWatermarks[]}。
//
// 校验：apply=true 的 worker 必须 capabilityConfirmed+ledgerReady；不满足返回 400 且不写该 worker。
// 打开 enabled 不会停止 control_plane/platform 入库路径。
func (h *LogCutoverHandler) UpdateCutover(c *gin.Context) {
	if !h.requirePlatformAdmin(c) {
		return
	}
	if h.logSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SERVICE_UNAVAILABLE", "message": "日志服务未初始化"})
		return
	}

	var req putLogCutoverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式非法"})
		return
	}
	watermarks := append([]logCutoverWatermarkRequest(nil), req.WorkerWatermarks...)
	if readiness, ok := h.workers.(WorkerCutoverReadinessProvider); ok && readiness.CutoverReadinessAvailable() {
		requested := make(map[string]bool)
		for _, watermark := range watermarks {
			requested[watermark.WorkerUUID] = watermark.Apply
		}
		workerIDs := make([]string, 0, len(requested))
		if req.Enabled != nil && *req.Enabled {
			var err error
			workerIDs, err = h.workers.ListWorkerUUIDs()
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "WORKER_READINESS_FAILED", "message": err.Error()})
				return
			}
		} else {
			for workerUUID := range requested {
				workerIDs = append(workerIDs, workerUUID)
			}
			sort.Strings(workerIDs)
		}
		watermarks = watermarks[:0]
		for _, workerUUID := range workerIDs {
			mark, err := readiness.CutoverReadiness(c.Request.Context(), workerUUID)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "WORKER_READINESS_FAILED", "message": err.Error(), "workerUuid": workerUUID})
				return
			}
			watermarks = append(watermarks, logCutoverWatermarkRequest{WorkerUUID: workerUUID,
				CapabilityConfirmed: mark.CapabilityConfirmed, LedgerReady: mark.LedgerReady,
				CutoffTime: &mark.CutoffTime, Apply: req.Enabled != nil && *req.Enabled || requested[workerUUID]})
		}
		req.WorkerWatermarks = watermarks
	}

	// 先整单校验水位，避免部分应用后才发现非法条目。
	for _, w := range req.WorkerWatermarks {
		if w.WorkerUUID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "workerWatermarks[].workerUuid 不能为空"})
			return
		}
		if w.Apply && !(w.CapabilityConfirmed && w.LedgerReady) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "INVALID_REQUEST",
				"message":    "应用切换水位要求 capabilityConfirmed 与 ledgerReady",
				"workerUuid": w.WorkerUUID,
			})
			return
		}
	}

	if req.Enabled != nil && *req.Enabled {
		// F-002：全局开启切换前必须逐 Worker 就绪。
		// 请求中的 apply 水位 + 库内已有水位，必须覆盖待切换集合且全部 Ready/Applied；
		// 空水位直接拒绝，禁止 `{enabled:true}` 一刀切关掉 instance/worker 入库。
		if err := h.validateCutoverEnable(req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "INVALID_REQUEST",
				"message": err.Error(),
			})
			return
		}
	}

	applied := make([]service.LogCutoverWatermark, 0, len(req.WorkerWatermarks))
	for _, w := range req.WorkerWatermarks {
		mark := service.LogCutoverWatermark{
			WorkerUUID:          w.WorkerUUID,
			CapabilityConfirmed: w.CapabilityConfirmed,
			LedgerReady:         w.LedgerReady,
		}
		if w.CutoffTime != nil {
			mark.CutoffTime = *w.CutoffTime
		}
		got, err := h.logSvc.ApplyAdminWatermark(mark, w.Apply)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "INVALID_REQUEST",
				"message":    err.Error(),
				"workerUuid": w.WorkerUUID,
			})
			return
		}
		applied = append(applied, got)
	}
	if req.Enabled != nil {
		// Persist watermarks first; the global ingest gate is committed last.
		if err := h.logSvc.Cutover().SetEnabledChecked(*req.Enabled); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "CUTOVER_PERSIST_FAILED", "message": err.Error()})
			return
		}
	}

	// 审计：audit 存在时记 log.cutover.update。
	if h.audit != nil {
		h.audit.RecordSafe(getUserID(c), logCutoverAuditAction, "log_cutover", "",
			marshalAuditDetail(gin.H{
				"enabled":          h.logSvc.Cutover().Enabled(),
				"workerWatermarks": req.WorkerWatermarks,
			}), c.ClientIP())
	}

	st := h.logSvc.CutoverStatus()
	resp := cutoverStatusJSON(st)
	resp["applied"] = applied
	c.JSON(http.StatusOK, resp)
}

// buildLegacyFilter 复用日志查询过滤参数，但收敛到 Legacy 只读路径（FR-481）。
// source 仅允许空或 legacy；其他值拒绝，避免把 platform/federated 查询误接到 Legacy 集合。
func (h *LogCutoverHandler) buildLegacyFilter(c *gin.Context) (service.LogFilter, bool) {
	f := service.LogFilter{Keyword: c.Query("keyword")}

	if v := c.Query("source"); v != "" && v != string(service.LogQuerySourceLegacy) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "Legacy 查询 source 仅支持 legacy"})
		return service.LogFilter{}, false
	}
	if v := c.Query("level"); v != "" {
		l := model.LogLevel(v)
		f.Level = &l
	}
	if v := c.Query("instanceId"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			u := uint(id)
			f.InstanceID = &u
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "instanceId 非法"})
			return service.LogFilter{}, false
		}
	}
	if v := c.Query("nodeId"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			u := uint(id)
			f.NodeID = &u
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "nodeId 非法"})
			return service.LogFilter{}, false
		}
	}
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "from 需为 RFC3339"})
			return service.LogFilter{}, false
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "to 需为 RFC3339"})
			return service.LogFilter{}, false
		}
	}
	f.Page = parseIntDefault(c.Query("page"), 0)
	f.PageSize = parseIntDefault(c.Query("pageSize"), 0)
	return f, true
}

// ListLegacy GET /api/v1/logs/legacy — 双路径门面 Legacy 侧只读查询。
//
// 强制 sourceTag=legacy（instance/worker 存量行）；NDJSON 归档不纳入。
// 复用 LogFilter 维度，不拼接 federated 事件统计。
func (h *LogCutoverHandler) ListLegacy(c *gin.Context) {
	if !h.requirePlatformAdmin(c) {
		return
	}
	if h.logSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SERVICE_UNAVAILABLE", "message": "日志服务未初始化"})
		return
	}

	f, ok := h.buildLegacyFilter(c)
	if !ok {
		return
	}

	dual := h.logSvc.DualPath()
	if dual == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "SERVICE_UNAVAILABLE", "message": "Legacy 查询路径未初始化"})
		return
	}

	res, err := dual.QueryLegacy(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询 Legacy 日志失败"})
		return
	}
	if res == nil {
		res = &service.LogQueryResult{
			SourceTag: service.LogQuerySourceLegacy,
			Items:     []model.LogEntry{},
		}
	}
	c.JSON(http.StatusOK, res)
}
