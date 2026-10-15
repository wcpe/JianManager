package router

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
}

func NewLogRuntimeHandler(nodes *service.NodeService, pool *cpgrpc.ClientPool, authz *service.AuthzService, instances *service.InstanceService, holders ...*service.LogTargetHolderService) *LogRuntimeHandler {
	h := &LogRuntimeHandler{nodes: nodes, pool: pool, authz: authz, instances: instances}
	if len(holders) > 0 {
		h.holders = holders[0]
	}
	return h
}

func (h *LogRuntimeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	if h == nil || h.nodes == nil || h.pool == nil {
		return
	}
	rg.GET("/nodes/:id/log-runtime", h.Status)
	rg.POST("/nodes/:id/log-runtime/:namespace/:action", h.Control)
	rg.POST("/nodes/:id/log-runtime/migrate", h.MigratePartition)
	rg.POST("/nodes/:id/log-runtime/ingest/resolve-gaps", h.ResolveIngestGaps)
	rg.POST("/nodes/:id/log-runtime/ingest/clear-identity-conflicts", h.ClearIdentityConflicts)
	// 手动触发归档（.gz）导入扫描（FR-504）：不受 gz 导入开关影响，应急补历史永远可用。
	rg.POST("/nodes/:id/log-runtime/ingest/import-archives", h.ImportArchives)
	rg.GET("/nodes/:id/log-archive/status", h.ArchiveStatus)
	rg.POST("/nodes/:id/log-archive/rehydrate", h.Rehydrate)
}

// operatorMetadataKey 是 CP→Worker 承载「操作人认证主体」的 gRPC metadata 键。
//
// 与 Worker 侧 grpcsvc.OperatorMetadataKey 同源（ADR-101 登记为传输契约：操作人必须由传输层
// 携带，请求体可被任意伪造）。这里刻意重复一个字面量而不 import Worker 的内部包：CP 依赖 Worker
// 实现包会引入分层倒挂，而该键本身就是跨进程协议的一部分——改动它属于协议变更，须走 ADR。
const operatorMetadataKey = "x-jm-operator"

// operatorFromRequest 从**认证上下文**（而非请求体）取出可审计的操作人标识。
//
// 为什么必须由 CP 注入（2026-10-02 生产现场缺陷）：Worker 侧的「放弃裁定」要求 incoming
// metadata 带 x-jm-operator，取不到一律拒绝（ADR-101：放弃是唯一允许回收链跨过永久空洞的动作，
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

func (h *LogRuntimeHandler) ResolveIngestGaps(c *gin.Context) {
	// 操作人必须在**任何 RPC 之前**解析：取不到就拒绝（不下发请求）。
	operator, ok := h.operatorFromRequest(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "OPERATOR_UNKNOWN",
			"message": "无法确定操作人：认证主体缺失（放弃裁定必须可审计）。" +
				"请以已认证用户或 API 密钥身份调用本端点；自定义认证通道需注入 username。",
		})
		return
	}
	// 可选 body：storageNamespace 必须是完整 LogSourceID/SourceGeneration 源键。
	// 传了：只解该源（显式人工确认，见 Worker 侧 ResolveCoveredGapsForSource——
	// 用于解开「唯一未就绪目标正是持有缺口者」的自我指涉死锁）；
	// 不传：与既有整节点行为逐字一致（向后兼容）。
	var body struct {
		StorageNamespace string `json:"storageNamespace"`
		// Reason 是「放弃裁定」的显式理由（ADR-101 / FR-483 §6）：仅当某源"从未有完整已发布投影"
		// 这类死结需要人工裁定终局时才必填；缺失 ⇒ Worker 侧照旧拒绝（不开静默放宽的口子 ✗）。
		Reason string `json:"reason"`
	}
	if c.Request.ContentLength > 0 {
		if bindErr := c.ShouldBindJSON(&body); bindErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
			return
		}
	}
	body.StorageNamespace = strings.TrimSpace(body.StorageNamespace)
	// 非空源键是永久裁定，须先完成对象授权；空键仍只走既有自动解算，不能扩大其判据。
	if body.StorageNamespace != "" && !h.authorizeResolveSource(c, body.StorageNamespace) {
		return
	}
	client, ok := h.client(c)
	if !ok {
		return
	}
	// 只有已经完成对象授权的显式单源裁定才下传人工凭据。
	// 空键自动解算不得携带操作人/理由，防止全源路径把请求理由解释为永久裁定授权。
	var mdPairs []string
	if body.StorageNamespace != "" {
		mdPairs = append(mdPairs, operatorMetadataKey, operator)
		if reason := strings.TrimSpace(body.Reason); reason != "" {
			mdPairs = append(mdPairs, "x-jm-reason", reason)
		}
	}
	ctx := metadata.NewOutgoingContext(c.Request.Context(), metadata.Pairs(mdPairs...))
	resp, err := client.Worker.LogResolveIngestGaps(ctx, &workerpb.LogResolveIngestGapsRequest{
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

// ClearIdentityConflicts 人工确认并清空「身份冲突」记录，使日志页的质量标记不再为
// conflict / partial（见 Worker planner 的 DupConflict / StatsPartial 派生）。
//
// 语义：运维审阅后确认这些冲突已被正确处理（系统在不可裁定形态下保留先到者，即保留正确
// 版本；冲突行从未进入写入面），同意清空诊断记录以消除质量标记。只移除诊断记录，不改日志内容。
//
// 与 resolve-gaps 同族的纪律（逐条对应）：
//  1. 操作人在**任何 RPC 之前**解析：取不到就拒绝，不下发请求（请求体可被伪造，责任由传输层带）；
//  2. 理由必填：它要进审计凭据，故与操作人同走 metadata（同样不受请求体伪造影响）；
//  3. 指名源须先完成对象授权（与放弃裁定同一判据，防止跨实例/跨组越权）；
//  4. 不传源键 = 全 Worker 范围：那是**批量**动作，只允许平台管理员（见 authorizeClearAllSources），
//     否则任何持有 node.manage 的调用方都能一次性清掉整节点所有源的歧义提示。
func (h *LogRuntimeHandler) ClearIdentityConflicts(c *gin.Context) {
	operator, ok := h.operatorFromRequest(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "OPERATOR_UNKNOWN",
			"message": "无法确定操作人：认证主体缺失（清空身份冲突必须可审计）。" +
				"请以已认证用户或 API 密钥身份调用本端点；自定义认证通道需注入 username。",
		})
		return
	}
	var body struct {
		// StorageNamespace 是完整源键 LogSourceID/SourceGeneration（沿用 resolve-gaps 的字段名与
		// 取值口径）；空 = 本 Worker 全部持有冲突记录的源。
		StorageNamespace string `json:"storageNamespace"`
		// Reason 是本次清空的显式理由：**必填**（凭据必须记录"为什么可以清"）。
		Reason string `json:"reason"`
	}
	if c.Request.ContentLength > 0 {
		if bindErr := c.ShouldBindJSON(&body); bindErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
			return
		}
	}
	body.StorageNamespace = strings.TrimSpace(body.StorageNamespace)
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "REASON_REQUIRED",
			"message": "清空身份冲突必须给出理由（审计凭据需要记录为何可以清）",
		})
		return
	}
	if body.StorageNamespace != "" {
		// 单源是永久性裁定，须先完成对象授权；判据与消失的缺口裁定完全一致。
		if !h.authorizeResolveSource(c, body.StorageNamespace) {
			return
		}
	} else if !h.authorizeClearAllSources(c) {
		return
	}
	client, ok := h.client(c)
	if !ok {
		return
	}
	ctx := metadata.NewOutgoingContext(c.Request.Context(), metadata.Pairs(
		operatorMetadataKey, operator, "x-jm-reason", body.Reason))
	resp, err := client.Worker.LogClearIdentityConflicts(ctx, &workerpb.LogClearIdentityConflictsRequest{
		RequestId: c.GetHeader("X-Request-ID"), ProtocolVersion: "log-query/1",
		StorageNamespace: body.StorageNamespace,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED", "message": err.Error()})
		return
	}
	if resp == nil || resp.GetError() != nil {
		code, message := "LOG_NOT_READY", ""
		if resp != nil && resp.GetError() != nil {
			code = resp.GetError().GetCode().String()
			message = resp.GetError().GetMessage()
		}
		c.JSON(http.StatusConflict, gin.H{"error": "CLEAR_CONFLICTS_FAILED", "code": code, "message": message})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"state":          "cleared",
		"clearedCount":   resp.GetClearedCount(),
		"clearedSources": resp.GetClearedSources(),
		"skippedSources": resp.GetSkippedSources(),
	})
}

// ImportArchives 手动触发归档（.gz）导入扫描（FR-504 §2.1 的「手动入口」）。
//
// 为什么必须补这条 HTTP 通路：Worker 侧的 gRPC `LogImportArchives` 早就存在（并强制校验
// 操作人），但 CP 与前端此前**都没有入口** ⇒「手动触发导入」在运维侧实际不可达。这是生产
// 「814 个 gz 从未被导入」的第三块拼图：① 定时扫描默认关；② 存量源不在采集轮的历史导入
// 范围内（acquire 层不变量：历史段只能前置到**未被读过**的当前段）；③ 手动入口无路可达。
//
// 语义：把「已经躺在源目录里、但还没被导入」的归档走一遍与自动路径完全相同的导入管道。
// 与自动路径的关键差异是**不受 gz 导入开关影响**——应急补历史必须永远可用。
//
// 纪律同 clear-identity-conflicts：操作人在任何 RPC 之前解析；理由必填并进审计凭据；
// 不指名源（全 Worker 范围）属批量动作，仅平台管理员可做。
func (h *LogRuntimeHandler) ImportArchives(c *gin.Context) {
	operator, ok := h.operatorFromRequest(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "OPERATOR_UNKNOWN",
			"message": "无法确定操作人：认证主体缺失（手动导入归档必须可审计）。" +
				"请以已认证用户或 API 密钥身份调用本端点；自定义认证通道需注入 username。",
		})
		return
	}
	var body struct {
		// StorageNamespace 是**实例存储命名空间**（形如 `inst:317`）——与 Worker 侧
		// ImportArchivesNow 的过滤口径一致；空 = 本 Worker 全部可导入源。
		StorageNamespace string `json:"storageNamespace"`
		// Reason 是本次导入的显式理由：必填（审计凭据要记录「为什么可以导」）。
		Reason string `json:"reason"`
	}
	if c.Request.ContentLength > 0 {
		if bindErr := c.ShouldBindJSON(&body); bindErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
			return
		}
	}
	body.StorageNamespace = strings.TrimSpace(body.StorageNamespace)
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "REASON_REQUIRED",
			"message": "手动导入归档必须给出理由（审计凭据需要记录为何可以导入）",
		})
		return
	}
	if body.StorageNamespace != "" {
		// 单实例导入绑定到一个实例，须先完成对象授权（与放弃裁定同一判据的实例级版本）。
		if !h.authorizeImportNamespace(c, body.StorageNamespace) {
			return
		}
	} else if !h.authorizeImportAllSources(c) {
		return
	}
	client, ok := h.client(c)
	if !ok {
		return
	}
	ctx := metadata.NewOutgoingContext(c.Request.Context(), metadata.Pairs(
		operatorMetadataKey, operator, "x-jm-reason", body.Reason))
	resp, err := client.Worker.LogImportArchives(ctx, &workerpb.LogImportArchivesRequest{
		RequestId: c.GetHeader("X-Request-ID"), ProtocolVersion: "log-query/1",
		StorageNamespace: body.StorageNamespace,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "WORKER_RPC_FAILED", "message": err.Error()})
		return
	}
	if resp == nil || resp.GetError() != nil {
		code, message := "LOG_NOT_READY", ""
		if resp != nil && resp.GetError() != nil {
			code = resp.GetError().GetCode().String()
			message = resp.GetError().GetMessage()
		}
		c.JSON(http.StatusConflict, gin.H{"error": "IMPORT_ARCHIVES_FAILED", "code": code, "message": message})
		return
	}
	// 计数不在响应里（LogTaskResponse 只带状态）：导入明细以 Worker 日志
	// 「已执行手动归档导入」（含 scanned/imported）为准；本响应只表示「已触发且无错」。
	c.JSON(http.StatusOK, gin.H{
		"state": "imported",
		"message": "已触发归档导入扫描；导入计数见 Worker 日志。" +
			"批量导入按 WAL 余量分批，可能跨多个采集轮完成。",
	})
}

// authorizeImportNamespace 校验「按实例命名空间导入」的授权（FR-504）。
//
// 为什么粒度是实例（`inst:<id>`）而不是完整源键：Worker 侧 `ImportArchivesNow` 的
// storageNamespace 过滤用的就是**存储命名空间**（即 `inst:<id>`），传完整源键不会匹配到
// 任何源——实测就是这样：传完整源键得到 `scanned=0`，看着像"授权通过、执行成功"，
// 实际一个归档都没扫。这里如实按 Worker 的语义收口，并顺带完成实例级对象授权
// （与放弃裁定同一判据，只是绑定到实例而不是具体源段）。
func (h *LogRuntimeHandler) authorizeImportNamespace(c *gin.Context, ns string) bool {
	id, err := strconv.ParseUint(strings.TrimPrefix(ns, "inst:"), 10, strconv.IntSize)
	if err != nil || id == 0 || ns != fmt.Sprintf("inst:%d", id) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": "须提供实例存储命名空间（形如 inst:317）；留空表示本节点全部源",
		})
		return false
	}
	access := getAccess(c)
	if h.authz == nil || h.instances == nil || access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "归档导入授权未就绪"})
		return false
	}
	// 不区分实例不存在与组外，避免借此枚举实例。
	deny := func() bool {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权导入该实例的日志源"})
		return false
	}
	inst, err := h.instances.GetByID(uint(id))
	if err != nil || inst == nil || inst.UUID == "" {
		return deny()
	}
	allowed, err := h.authz.CanAccessInstance(access, inst.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "归档导入授权校验失败"})
		return false
	}
	if !allowed {
		return deny()
	}
	return true
}

// authorizeImportAllSources 校验「全 Worker 范围导入」的授权：只有平台管理员可做。
//
// 与 authorizeClearAllSources 同判据（批量/跨资源动作 → 平台管理员），消息按本端点语义定制。
func (h *LogRuntimeHandler) authorizeImportAllSources(c *gin.Context) bool {
	access := getAccess(c)
	if h.authz == nil || access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "全量导入的授权未就绪"})
		return false
	}
	if !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN",
			"message": "全 Worker 范围的归档导入仅限平台管理员；请指定单个日志源（storageNamespace）逐源导入",
		})
		return false
	}
	return true
}

// authorizeClearAllSources 校验「清空全部源」的授权：只有平台管理员可做。
//
// 为什么比单源更严：单源清空绑定到一个已授权的实例（越权面最小），而空源键会一次性清掉本
// Worker 上**所有**源的歧义提示。若沿用节点级权限（node.manage），任何能操作该节点的调用方
// 都能借一次调用把整节点的质量标记抹平——那是本功能最不希望出现的越权形态。
//
// 判据用 access.IsPlatformAdmin 而非新增权限节点：与 bot / bot_load 等既有"批量或跨资源"
// 端点的口径一致（组管理员走单源路径即可，不需要为它扩权限树）。
func (h *LogRuntimeHandler) authorizeClearAllSources(c *gin.Context) bool {
	access := getAccess(c)
	if h.authz == nil || access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "清空全部源的授权未就绪"})
		return false
	}
	if !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN",
			"message": "清空全部源的身份冲突仅限平台管理员；请指定单个日志源（storageNamespace）逐源清空",
		})
		return false
	}
	return true
}

// authorizeResolveSource 在裁定 RPC 前绑定完整源键，节点能力不能代替实例组授权。
func (h *LogRuntimeHandler) authorizeResolveSource(c *gin.Context, sourceKey string) bool {
	parts := strings.Split(sourceKey, "/")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "inst:") || parts[2] == "" ||
		(parts[1] != "stdout" && parts[1] != "stderr" && parts[1] != "file") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "须提供完整的实例日志源键及代次"})
		return false
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(parts[0], "inst:"), 10, strconv.IntSize)
	if err != nil || id == 0 || parts[0] != fmt.Sprintf("inst:%d", id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "实例日志源键无效"})
		return false
	}
	access := getAccess(c)
	if h.authz == nil || h.instances == nil || h.nodes == nil || access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "日志源裁定授权未就绪"})
		return false
	}
	deny := func() bool {
		// 不区分源不存在、组外、代次或归属错误，避免借此枚举实例及历史源。
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权裁定该日志源"})
		return false
	}
	// 即便平台管理员在组授权中短路，也必须取得真实实例，不能只信客户端的数字 ID。
	inst, err := h.instances.GetByID(uint(id))
	if err != nil || inst == nil || inst.UUID == "" {
		return deny()
	}
	allowed, err := h.authz.CanAccessInstance(access, inst.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "日志源授权校验失败"})
		return false
	}
	if !allowed {
		return deny()
	}
	nodeID, err := strconv.ParseUint(c.Param("id"), 10, strconv.IntSize)
	if err != nil || nodeID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_NODE_ID"})
		return false
	}
	node, err := h.nodes.GetByID(uint(nodeID))
	if err != nil || node == nil || node.UUID == "" {
		return deny()
	}
	// 当前绑定由 CP 数据库派生，与下发 CreateInstanceRequest 的身份契约一致；不能剥离代次。
	generation := fmt.Sprintf("instance:%s@worker:%s", inst.UUID, node.UUID)
	if inst.NodeID == node.ID && parts[2] == generation {
		return true
	}
	// 实例迁移后，旧 Worker 只可访问已登记的历史 holder；客户端提供的旧 UUID 不是凭证。
	if h.holders != nil {
		holders, err := h.holders.ListForInstance(inst.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "日志源绑定校验失败"})
			return false
		}
		for _, holder := range holders {
			if !holder.Current && holder.InstanceID == inst.ID && holder.StorageNamespace == parts[0] &&
				holder.WorkerUUID == node.UUID && holder.SourceGeneration == parts[2] {
				return true
			}
		}
	}
	return deny()
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
