package router

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// CrashSnapshotHandler 实例崩溃快照路由处理器（FR-313，增强 FR-470）。
// 崩溃现场由 Worker 经 gRPC 上报入库（滚动保留最近 5 条 + 同步分类），此处提供读侧
// （列表 / 趋势 / 总览）与管理员重分类入口。
type CrashSnapshotHandler struct {
	svc   *service.CrashSnapshotService
	authz *service.AuthzService
	audit *service.AuditService
}

// NewCrashSnapshotHandler 创建崩溃快照路由处理器。
func NewCrashSnapshotHandler(svc *service.CrashSnapshotService, authz *service.AuthzService, audit *service.AuditService) *CrashSnapshotHandler {
	return &CrashSnapshotHandler{svc: svc, authz: authz, audit: audit}
}

// crashSnapshotView 列表项视图（FR-470）：在快照之上附带解析后的证据行与资源关联。
type crashSnapshotView struct {
	model.InstanceCrashSnapshot
	// EvidenceLines 证据行（解析自 Evidence JSON 文本），前端「证据」区直接消费。
	EvidenceLines []string `json:"evidenceLines,omitempty"`
	// Correlation 崩溃与资源关联证据（FR-470 §2.3）；无关联服务时省略。
	Correlation *service.CrashCorrelation `json:"correlation,omitempty"`
}

// List GET /instances/:id/crash-snapshots — 实例崩溃快照列表（按发生时间倒序）。
// 权限 = 实例读权限（instance:read 且实例可访问）；不可访问按存在性隐藏返回 404。
func (h *CrashSnapshotHandler) List(c *gin.Context) {
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
	snaps, err := h.svc.ListByInstance(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询崩溃快照失败"})
		return
	}
	// 空结果回 []（非 null），前端空态直接消费。
	views := make([]crashSnapshotView, 0, len(snaps))
	// R14：关联证据一次批量算出（实例/节点各 1 次查询 + 1 次进程样本查询），
	// 而不是在循环里逐条 Correlate（那会变成 K×(实例+RSS+节点+指标) 次查询）。
	correlations := map[int64]service.CrashCorrelation{}
	if h.svc.CorrelationEnabled() && len(snaps) > 0 {
		occurred := make([]time.Time, 0, len(snaps))
		for _, snap := range snaps {
			occurred = append(occurred, snap.OccurredAt)
		}
		if corr, cerr := h.svc.CorrelateBatch(id, occurred); cerr == nil {
			correlations = corr
		}
	}
	for _, snap := range snaps {
		v := crashSnapshotView{InstanceCrashSnapshot: snap, EvidenceLines: service.DecodeCrashEvidence(snap.Evidence)}
		if corr, ok := correlations[snap.OccurredAt.UnixNano()]; ok {
			c := corr
			v.Correlation = &c
		}
		views = append(views, v)
	}
	c.JSON(http.StatusOK, views)
}

// Trend GET /instances/:id/crash-trend?days=30 — 实例崩溃趋势（FR-470）。
// 数据来自独立汇总表 InstanceCrashStat，不受 K=5 快照裁剪影响。
func (h *CrashSnapshotHandler) Trend(c *gin.Context) {
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
	days := parseDaysQuery(c, service.CrashTrendDefaultDays)
	trend, err := h.svc.TrendByInstance(id, days)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询崩溃趋势失败"})
		return
	}
	c.JSON(http.StatusOK, trend)
}

// Overview GET /crash-overview?days=30 — 平台崩溃总览（FR-470）：Top 根因/实例/同类 + 趋势。
//
// m-3：总览会回填实例名，属跨实例聚合读，不能只判 instance.read——否则组只读角色
// 能经此看到其它组的实例清单。按 AccessibleInstanceIDs 收敛到调用方可见范围
// （平台管理员 scoped=false，即不限）。
func (h *CrashSnapshotHandler) Overview(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	scopeIDs, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	var scope []uint
	if scoped {
		scope = scopeIDs
		if scope == nil {
			scope = []uint{}
		}
	}
	days := parseDaysQuery(c, service.CrashTrendDefaultDays)
	ov, err := h.svc.Overview(days, scope)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询崩溃总览失败"})
		return
	}
	c.JSON(http.StatusOK, ov)
}

// Reclassify POST /crash-snapshots/reclassify — 对历史快照按当前规则重跑分类（平台管理员）。
// 回填分类列并修正趋势统计（差量搬移，幂等）。审计动作 crash.reclassify。
func (h *CrashSnapshotHandler) Reclassify(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "需要平台管理员权限"})
		return
	}
	res, err := h.svc.ReclassifyAll()
	if err != nil {
		if h.audit != nil {
			h.audit.RecordResultSafe(access.UserID, "crash.reclassify", "crash_snapshot", "", "", c.ClientIP(), false, err.Error())
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "重分类失败"})
		return
	}
	if h.audit != nil {
		detail := "扫描 " + strconv.Itoa(res.Scanned) + "，更新 " + strconv.Itoa(res.Updated) + "，回填 " + strconv.Itoa(res.Backfilled)
		h.audit.RecordResultSafe(access.UserID, "crash.reclassify", "crash_snapshot", "", detail, c.ClientIP(), true, "")
	}
	c.JSON(http.StatusOK, res)
}

// parseDaysQuery 解析 ?days= 查询参数（无效/缺省用 fallback）。
//
// edge m-6：非法值（`days=abc` / 负数）**静默回退** fallback 而非 400，与同 PR 内其它
// 端点的严格风格不同；但静默回退不会放大查询范围（service 侧另有 CrashTrendMaxDays
// 上限兜底），且前端已按「缺省即 30」消费。保持容错，并在 API.md 记录该契约。
// 上限收敛交给 service 的 normalizeCrashDays（引用同一常量，避免两处硬编码 365）。
func parseDaysQuery(c *gin.Context, fallback int) int {
	raw := c.Query("days")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > service.CrashTrendMaxDays {
		return service.CrashTrendMaxDays
	}
	return n
}

// RegisterRoutes 注册崩溃快照路由（加性追加，与 /instances/:id/... 其余参数段共存）。
func (h *CrashSnapshotHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/:id/crash-snapshots", h.List)
	rg.GET("/instances/:id/crash-trend", h.Trend)
	rg.GET("/crash-overview", h.Overview)
	rg.POST("/crash-snapshots/reclassify", h.Reclassify)
}
