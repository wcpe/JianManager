package router

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

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

