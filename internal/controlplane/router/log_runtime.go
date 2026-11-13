package router

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// LogRuntimeHandler 经反向隧道代理 Worker 管理的日志运行时，不直接连接 VictoriaLogs。
type LogRuntimeHandler struct {
	nodes     *service.NodeService
	pool      *cpgrpc.ClientPool
	authz     *service.AuthzService
	instances *service.InstanceService
	holders   *service.LogTargetHolderService
	// audit 记录「人工应急动作」的审计行（见 MigratePartition / Rehydrate）。
	// 可为 nil（未接线时不落审计，但**不阻断主流程**）。
	audit *service.AuditService
}

func NewLogRuntimeHandler(nodes *service.NodeService, pool *cpgrpc.ClientPool, authz *service.AuthzService, instances *service.InstanceService, holders ...*service.LogTargetHolderService) *LogRuntimeHandler {
	h := &LogRuntimeHandler{nodes: nodes, pool: pool, authz: authz, instances: instances}
	if len(holders) > 0 {
		h.holders = holders[0]
	}
	return h
}

// SetAudit 注入审计服务（装配期调用，与同族 handler 的可选注入同口径）。
// nil 为合法值：审计未接线时人工动作照常执行，仅不留 CP 侧审计行。
func (h *LogRuntimeHandler) SetAudit(audit *service.AuditService) { h.audit = audit }

// recordAudit 记录一条节点级操作审计；audit 未注入时静默跳过（口径同 LogAcqSettingsHandler）。
func (h *LogRuntimeHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	h.audit.RecordSafe(userID, action, "node", c.Param("id"), marshalAuditDetail(detail), c.ClientIP())
}

func (h *LogRuntimeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	if h == nil || h.nodes == nil || h.pool == nil {
		return
	}
	rg.GET("/nodes/:id/log-runtime", h.Status)
	rg.POST("/nodes/:id/log-runtime/:namespace/:action", h.Control)
	rg.POST("/nodes/:id/log-runtime/migrate", h.MigratePartition)
	rg.GET("/nodes/:id/log-archive/status", h.ArchiveStatus)
	rg.POST("/nodes/:id/log-archive/rehydrate", h.Rehydrate)
}

// operatorMetadataKey 是 CP→Worker 承载「操作人认证主体」的 gRPC metadata 键。
//
// 与 Worker 侧 grpcsvc.OperatorMetadataKey 同源（登记为传输契约：操作人必须由传输层
// 携带，请求体可被任意伪造）。这里刻意重复一个字面量而不 import Worker 的内部包：CP 依赖 Worker
// 实现包会引入分层倒挂，而该键本身就是跨进程协议的一部分——改动它属于协议变更，须走 ADR。
const operatorMetadataKey = "x-jm-operator"

// operatorFromRequest 从**认证上下文**（而非请求体）取出可审计的操作人标识。
//
// 为什么必须由 CP 注入（2026-10-02 生产现场缺陷）：Worker 侧的「放弃裁定」要求 incoming
// metadata 带 x-jm-operator，取不到一律拒绝（放弃是唯一允许回收链跨过永久空洞的动作，
// 不可无痕）。而 CP 转发时不透传任何操作人 ⇒ 这条**唯一**能解开「DELIVER_ERROR 未决缺口永久
// 阻塞该源恢复采集」的产品面路径实际不可达（现场 5 个源停在暂停态，运维只能绕过产品面直连
// Worker）。取不到主体时必须在这里就拒绝：把请求下发给 Worker 只会换回一句「请设置
// x-jm-operator」，那条提示对 CP 调用方不可执行。
func (h *LogRuntimeHandler) operatorFromRequest(c *gin.Context) (string, bool) {
	// 已认证主体优先用登录名/密钥名（审计与人可读性最好）：JWT 路径取 claims.Username，
	// Agent（API 密钥）路径由 agent_auth 注入 "agent:<名称>"。
	if name := strings.TrimSpace(c.GetString(middleware.CtxUsername)); name != "" {
		return name, true
	}
	// 兜底：认证主体存在但用户名未注入（自定义认证通道）时用稳定可审计的用户标识，
	// 而不是放弃留痕或编造一个「system」。
	if access := getAccess(c); access != nil && access.UserID > 0 {
		return fmt.Sprintf("user:%d", access.UserID), true
	}
	return "", false
}

type logMigratePartitionRequest struct {
	StorageNamespace string `json:"storageNamespace"`
	UTCDay           string `json:"utcDay"`
}

func (h *LogRuntimeHandler) MigratePartition(c *gin.Context) {
	// 操作人必须在**任何 RPC 之前**解析（对齐同族 resolve-gaps / clear-identity-conflicts /
	// import-archives）：分区下沉把历史日志挪到冷层，事后必须能回答「谁让它动的」。
	// Worker 侧该 RPC 不消费操作人，CP 侧不解析就真的没有任何主体记录。
	operator, ok := h.operatorFromRequest(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "OPERATOR_UNKNOWN",
			"message": "无法确定操作人：认证主体缺失（分区迁移必须可审计）。" +
				"请以已认证用户或 API 密钥身份调用本端点；自定义认证通道需注入 username。",
		})
		return
	}
	var body logMigratePartitionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	body.StorageNamespace = strings.TrimSpace(body.StorageNamespace)
	if body.StorageNamespace == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	if _, err := time.Parse("2006-01-02", body.UTCDay); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_UTC_DAY"})
		return
	}
	// namespace 只是存在性探针，Worker 实际搬运整物理 UTC 日的全部命名空间。
	// 单实例组授权不能代表组外日成员已获授权；沿全量导入判据要求平台管理员，不新增权限节点。
	access := getAccess(c)
	if access == nil || !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "整物理 UTC 日日志迁移仅限平台管理员"})
		return
	}
	// 全范围主体仍须通过原探针格式与节点绑定检查，不能借本节点通道迁移别处目标。
	if !h.authorizeArchiveTarget(c, body.StorageNamespace) {
		return
	}
	client, ok := h.client(c)
	if !ok {
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
	// 冷层仍可按 Catalog 权威位置查询；审计记录触发探针、整日影响范围和操作人。
	// 本路径不由通用审计中间件记录 action，且 Worker RPC 不消费操作人，因此必须在 CP 留痕。
	h.recordAudit(c, "node.log_partition.migrate", map[string]any{
		"migrationScope":   "physical_utc_day",
		"storageNamespace": body.StorageNamespace,
		"utcDay":           body.UTCDay,
		"targetTier":       "cold",
		"operator":         operator,
	})
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

// Control 经反向隧道启停/重启 Worker 上的 VictoriaLogs 运行时。
//
// 为什么必须补操作人与审计：启停/重启会直接中断该节点日志的查询面与写入面，是**有副作用的
// 写操作**；而审计中间件对 POST /nodes/:id/log-runtime/... 不产生 action，Worker 侧
// LogRuntimeControl 也不消费操作人 ⇒ 不在这里留痕，一次误停在生产上就查不到「谁干的」。
//
// 为什么不在这里做目标级授权（与 migrate / rehydrate 的差异，已按实际取值核实，不是想当然）：
// 本端点的 `:namespace` 是 **VictoriaLogs 运行时命名空间**，合法取值只有 hot / cold /
// rehydrate（Worker 侧 LogRuntimeControl 逐个比对 vlsup.NamespaceHot/Cold/Rehydrate 之后才
// 落到 Start/Stop/Restart；前端 LogRuntimeInstance["namespace"] 也是同一组字面量）。它回答的是
// 「动这个节点上的哪个日志库进程」，既不携带 node:/inst: 前缀，也不指向任何租户对象。
// 而同族的 authorizeArchiveTarget 只承认空串 / inst:<id> / node:<id>，其余一律 400 ——
// 硬套在这里会把 hot/start 这类正常调用打成 400，等于用错误的语义换一个「看着补齐」的勾。
// 三个命名空间属于同一节点的同一套日志基础设施，不存在跨实例组或跨节点的可见性差异，
// 故目标面隔离由**节点级权限**承担（见下面的 requireNodes），不做实例级授权。
func (h *LogRuntimeHandler) Control(c *gin.Context) {
	// 操作人必须在**任何 RPC 之前**解析（同族纪律）：取不到主体就不下发请求。
	operator, ok := h.operatorFromRequest(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "OPERATOR_UNKNOWN",
			"message": "无法确定操作人：认证主体缺失（日志运行时启停必须可审计）。" +
				"请以已认证用户或 API 密钥身份调用本端点；自定义认证通道需注入 username。",
		})
		return
	}
	// 节点级权限校验：路由门虽已挂 node.manage，但写操作不应只依赖挂载点——一旦本 handler
	// 被复用到更宽的路由组，静默失去写门是查不出来的（与 asset / node 等 handler 的自查同口径）。
	if !requireNodes(c, "node.manage") {
		return
	}
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
	// 成功受理后落 CP 侧审计行：命名空间 + 动作是复盘「停的是哪一层日志库」的唯一凭据，
	// operator 单列进 detail（Agent 通道下 user_id 写的是 token id，可读主体只在这里）。
	// 只记成功受理：Worker 报错走上面的分支，不落行，避免审计证明一次从未发生的启停。
	h.recordAudit(c, "node.log_runtime.control", map[string]any{
		"namespace": ns,
		"action":    action,
		"operator":  operator,
		"state":     resp.GetState().String(),
	})
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

// rehydrateTarget 把请求里的 target 归一成审计口径：与 archiveQueryBase 的默认值一致，
// 空 target 表示「本节点自身」。
func rehydrateTarget(c *gin.Context, target string) string {
	if trimmed := strings.TrimSpace(target); trimmed != "" {
		return trimmed
	}
	return "node:" + c.Param("id")
}

type logRehydrateRequest struct {
	ArchiveObjectIDs []string `json:"archiveObjectIds"`
	Target           string   `json:"target"`
	FromUTC          string   `json:"fromUtc"`
	ToUTC            string   `json:"toUtc"`
}

// Rehydrate 把冷层归档对象恢复回可查询面（读取 + 校验 + 发布归档事件）。
//
// 为什么必须补操作人与审计：它会把归档数据重新写进查询面，是「谁在什么时候把哪批对象放回了
// 可查范围」必须能回答的高影响动作。目标级授权（authorizeArchiveTarget）此前已补齐，但操作人
// 从未解析、CP 侧也不落行——Worker 侧 LogRehydrate 同样不消费操作人，等于全程无痕。
func (h *LogRuntimeHandler) Rehydrate(c *gin.Context) {
	// 操作人必须在**任何 RPC 之前**解析（同族纪律）：取不到主体就不下发请求。
	operator, ok := h.operatorFromRequest(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "OPERATOR_UNKNOWN",
			"message": "无法确定操作人：认证主体缺失（归档恢复必须可审计）。" +
				"请以已认证用户或 API 密钥身份调用本端点；自定义认证通道需注入 username。",
		})
		return
	}
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
	// 成功受理后落 CP 侧审计行。target 按 archiveQueryBase 的口径归一（空 target 即本节点
	// 自身）：否则同一件事在审计里会出现「空」与「node:<id>」两副面孔，事后无法按目标检索。
	// 归档对象 ID 一并入 detail——「把哪批对象放回了可查范围」正是这次操作的实质。
	// 只记成功受理：Worker 报错走上面两个分支，不落行，避免审计证明一次从未发生的恢复。
	h.recordAudit(c, "node.log_archive.rehydrate", map[string]any{
		"target":           rehydrateTarget(c, body.Target),
		"archiveObjectIds": body.ArchiveObjectIDs,
		"fromUtc":          body.FromUTC,
		"toUtc":            body.ToUTC,
		"operator":         operator,
		"state":            resp.GetState().String(),
	})
	c.JSON(http.StatusAccepted, resp)
}
