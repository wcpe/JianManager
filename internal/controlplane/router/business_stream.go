package router

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ctxBusinessStreamScope 承载「本次订阅可看的实例 id 集合」。
//
// nil 值 = 不受限（平台管理员 / Agent Token）；非 nil（含空切片）= 按此白名单收敛。
// 用 context 传递而不是改 Handler 构造签名：scope 依赖「当前调用者」，
// 只有中间件知道它是谁；handler 不该再引一次 authz 去重算。
const ctxBusinessStreamScope = "businessStreamScope"

// BusinessStreamHandler 业务事件 SSE 订阅端点。
//
// 契约见 docs/openapi/paths/stream.yaml：`GET /business/stream`，以 SSE 推送探针上报的业务事件。
// 帧格式（id 必须等于 seq）：
//
//	id: 1024
//	event: business.event
//	data: {...}
//
// 补读依赖 SSE 原生 `Last-Event-ID`（浏览器 EventSource 断线重连自动携带），
// 窗口外先发 `event: replay_gap`，再由业务台触发全量对账。
type BusinessStreamHandler struct {
	stream *service.BusinessStreamService
}

// NewBusinessStreamHandler 创建业务事件流处理器。
func NewBusinessStreamHandler(stream *service.BusinessStreamService) *BusinessStreamHandler {
	return &BusinessStreamHandler{stream: stream}
}

// sseWriteTimeout 是单帧写出的上限。客户端一直不读（TCP 背压）时，写调用会在这之后失败，
// 从而让订阅者协程退出、队列打满、最终由扇出侧判为慢消费者——**绝不无限期挂在写调用上**。
const sseWriteTimeout = 15 * time.Second

// sseTerminateWriteTimeout 是慢消费者终止帧（`event: error`）的发送窗口。
//
// 比常规写超时短（不让一个已坏的连接长期占住协程），但必须显著大于 0：判为慢消费时
// 对端 socket 缓冲通常已满，终止帧要等对端读走一部分才写得出去。
const sseTerminateWriteTimeout = 10 * time.Second

// businessStreamAuth 是本端点的鉴权组合：Agent Token 优先，人类 JWT 兜底。
//
// 为什么不是单纯挂 AgentAuth：对外契约（业务台）用 Agent Token，但同一份事件流也是
// 运维页面要看的，浏览器手里只有 JWT。二者都失败才 401——不放宽任何一条既有校验，
// 只是把两种合法主体并列。
//
// 实现上按 Token 前缀**二选一**（jmat_ 走 AgentAuth，其余走 JWTAuth），而不是串两条中间件：
// 两个中间件都以 c.Next() 收尾，连续调用会把后续 handler 执行两遍。这里只让选中那条
// 调用 Next，语义与「单独挂某一条」完全一致。
func businessStreamAuth(agentSvc *service.AgentTokenService, authz *service.AuthzService, jwtSecret string) gin.HandlerFunc {
	agentMW := middleware.AgentAuth(agentSvc)
	jwtMW := middleware.JWTAuth(jwtSecret)
	// JWT 分支必须再过一道授权：**认证 ≠ 有权看这条流**。
	//
	// 为什么这条不能省（本端点此前的真实缺陷）：它被从 `/business/events` 改造而来，
	// 而旧端点挂的是 `permRead("instance.read")`。改造时只保留了「谁能进来」，
	// 丢掉了「进来能看什么」——于是任何登录用户（哪怕角色模板被清空、连 instance.read
	// 都没有）都能订阅**全平台所有实例**的业务事件，无需知道实例 id，也无需 scope。
	// 这是相对旧端点的**权限回退**，不是取舍。
	accessMW := middleware.LoadAccess(authz)
	// 事件流的内容是「实例的业务动态」，判据与旧端点一致取 instance.read。
	// 不用 permRead 中间件是因为它按「任一节点」判定，而这里还要把可访问实例集合
	// 传给 handler 做行级收敛（见 resolveStreamScope）。
	requireRead := func(c *gin.Context) {
		access := getAccess(c)
		if access == nil || !access.Can("instance.read") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "需要 instance.read 权限",
			})
			return
		}
		c.Next()
	}
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if strings.HasPrefix(strings.TrimPrefix(auth, "Bearer "), "jmat_") {
			// Agent Token 自带 scope，由 AgentAuth 落实；不需要再叠 RBAC。
			agentMW(c)
			return
		}
		jwtMW(c)
		if c.IsAborted() {
			return
		}
		accessMW(c)
		if c.IsAborted() {
			return
		}
		requireRead(c)
		if c.IsAborted() {
			return
		}
		// 授权通过后把**可访问实例集合**一并算出来交给 handler 做行级收敛。
		// 放在中间件而不是 handler：它要 authz 与当前 access，两者这里都有。
		c.Set(ctxBusinessStreamScope, resolveStreamScope(c, authz))
	}
}

// resolveStreamScope 返回该连接可订阅的实例 id 集合（nil 表示不受限）。
//
// JWT 主体一律按 `AccessibleInstanceIDs` 收敛；Agent Token 由自身 scope 决定，
// 这里返回 nil（其上游调用本来就被 scope 限制在可访问实例内）。
func resolveStreamScope(c *gin.Context, authz *service.AuthzService) []uint {
	access := getAccess(c)
	if access == nil || authz == nil {
		return nil
	}
	if access.IsPlatformAdmin {
		return nil
	}
	ids, _, err := authz.AccessibleInstanceIDs(access)
	if err != nil {
		// 查不到就按「什么都看不到」处理：事件流是只读观测面，
		// 宁可少给也不能因为一次查询失败放开全量。
		return []uint{}
	}
	return ids
}

// RegisterRoutes 注册业务事件流路由。调用方须已挂 businessStreamAuth。
func (h *BusinessStreamHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/business/stream", h.Stream)
}

// Stream 处理 SSE 订阅：限流 → 注册订阅者 → 补读 → 实时流 + 心跳。
func (h *BusinessStreamHandler) Stream(c *gin.Context) {
	if h.stream == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "STREAM_UNAVAILABLE", "message": "业务事件流未启用"})
		return
	}

	filter := service.ParseBusinessStreamFilter(c.Query("domains"), c.Query("topics"))
	// 非管理员按可访问实例收敛（中间件已算好；Agent Token 与平台管理员为 nil = 不受限）。
	if raw, ok := c.Get(ctxBusinessStreamScope); ok {
		if allowed, ok := raw.([]uint); ok {
			filter = filter.WithAllowedInstances(allowed)
		}
	}
	tokenKey := businessStreamTokenKey(c)

	// 先注册订阅者、再取补读快照：二者之间到达的事件会进队列（seq 去重后不会重复下发），
	// 反过来则会出现「补读之后、订阅之前」的真空区，事件必然丢失。
	sub, err := h.stream.Subscribe(tokenKey, filter)
	if err != nil {
		// §4：单 Token/全局并发超限 → 429 + Retry-After。
		c.Header("Retry-After", "5")
		c.JSON(http.StatusTooManyRequests, gin.H{
			"code":    "TOO_MANY_CONNECTIONS",
			"limit":   service.BusinessStreamMaxPerToken,
			"current": h.stream.TokenConnectionCount(tokenKey),
		})
		return
	}
	defer h.stream.Unsubscribe(sub)

	// 响应头：禁用代理缓冲是硬要求（Nginx 默认 60s 无数据断连 + 缓冲会让事件成批延迟）。
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	rc := http.NewResponseController(c.Writer)
	write := func(frame []byte) bool {
		// 写截止时间只影响本次写：慢客户端让写失败而不是让协程永久阻塞。
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		if _, err := c.Writer.Write(frame); err != nil {
			h.stream.IncWriteError()
			return false
		}
		if err := rc.Flush(); err != nil {
			h.stream.IncWriteError()
			return false
		}
		return true
	}

	// 首帧立即 flush：让客户端尽早拿到 200 与响应头，也让「连接已建立」可观测。
	// 这里不写任何帧（不发伪事件），只把已写好的响应头推出去。
	if err := rc.Flush(); err != nil {
		h.stream.IncWriteError()
		return
	}

	// ── 补读 ─────────────────────────────────────────────────────────────
	// 位点来源：Last-Event-ID 头优先，其次 since 查询参数（§3.1）。
	replayUpTo := h.replay(c, write, filter)
	if replayUpTo < 0 {
		return // 补读过程中写失败，直接收尾
	}

	ctx := c.Request.Context()
	ticker := time.NewTicker(h.stream.HeartbeatInterval())
	defer ticker.Stop()

	for {
		// 先做一次「停止信号」的优先检查：慢消费者被判定后不该再多下发一帧实时事件，
		// 而应立刻收尾——否则 select 在 stop 与 frames 同时就绪时会随机选择，
		// 终止帧可能被排在若干实时帧之后，甚至永不下发。
		select {
		case <-sub.Stop():
			h.finish(c, rc, sub)
			return
		default:
		}

		select {
		case <-ctx.Done():
			return
		case <-sub.Stop():
			h.finish(c, rc, sub)
			return
		case frame, ok := <-sub.Frames():
			if !ok {
				return
			}
			// 补读快照已覆盖的位点直接跳过（订阅与快照之间的重叠窗口，靠 seq 单调递增去重）。
			if frame.Seq <= replayUpTo {
				continue
			}
			if !write(frame.Bytes()) {
				// 写失败（客户端已断开或长期不读）：尽力补一帧终止原因后收尾。
				h.finish(c, rc, sub)
				return
			}
		case <-ticker.C:
			// 心跳：防中间设备超时断连，同时让客户端判断连接活性。
			_, _ = fmt.Fprintf(c.Writer, "event: ping\ndata: {\"t\":%d}\n\n", time.Now().Unix())
			if err := rc.Flush(); err != nil {
				h.stream.IncWriteError()
				return
			}
			h.stream.IncHeartbeatSent()
		}
	}
}

// finish 是连接收尾：慢消费者先尽力补一帧 `event: error`（§3.3），再返回（由 defer 断开）。
//
// 这里给终止帧留了一段**较宽的发送窗口**（10s），原因是客观事实：订阅者被判慢消费时，
// 其 socket 发送缓冲几乎必然是满的，终止帧要等对端把内核缓冲读走一部分才写得出去。
// 「慢」不等于「死」——真实客户端（sleep 处理）仍在读，只是慢，所以给足时间能让这一帧真正送达。
// 若对端确实已死，写会在超时后失败，直接断开即可：客户端重连带 Last-Event-ID 照样能补齐，
// 数据完整性由补读机制保证，不依赖这一帧送达。
func (h *BusinessStreamHandler) finish(c *gin.Context, rc *http.ResponseController, sub *service.BusinessSubscriber) {
	if !sub.SlowConsumer() {
		return
	}
	_ = rc.SetWriteDeadline(time.Now().Add(sseTerminateWriteTimeout))
	if _, err := fmt.Fprintf(c.Writer, "event: error\ndata: {\"code\":%q,\"message\":%q}\n\n",
		service.SSEErrorSlowConsumer, "事件处理过慢，连接已关闭，请重连并指定 Last-Event-ID"); err != nil {
		h.stream.IncWriteError()
		return
	}
	if err := rc.Flush(); err != nil {
		h.stream.IncWriteError()
	}
}

// replay 发送补读帧，返回「已覆盖到的最新位点」。返回 -1 表示写失败需立即收尾。
//
// 语义（§3.3）：
//   - 未带位点：只推实时流，不补历史（返回 0）；
//   - 位点在窗口内：从该位点之后补发，然后接续实时流；
//   - 位点早于窗口起点：先发 `event: replay_gap`，再从窗口起点补发；
//   - 位点晚于当前（异常）：忽略，直接进实时流并记 WARN。
func (h *BusinessStreamHandler) replay(c *gin.Context, write func([]byte) bool, filter service.BusinessStreamFilter) int64 {
	since, ok := parseBusinessStreamSince(c)
	if !ok {
		return 0
	}
	buf := h.stream.Buffer()
	if buf == nil {
		return 0
	}
	newest := buf.NewestSeq()
	if since > newest {
		// 异常位点（客户端声称见过尚未发生的事件）：忽略位点，直接进实时流。
		slog.Warn("业务事件流收到超前位点，已忽略并按实时流处理", "since", since, "newest", newest)
		return 0
	}
	if since == newest {
		// 位点已追上最新：不补发，但实时队列里 seq <= since 的帧必须跳过（客户端已收过）。
		return since
	}
	frames, gap, snapshotNewest := buf.SinceWithBoundary(since)
	if gap {
		h.stream.IncGapServed()
		payload, _ := json.Marshal(gin.H{
			"requestedSeq":       since,
			"oldestAvailableSeq": buf.OldestSeq(),
			"reason":             "BUFFER_EXPIRED",
		})
		if !write([]byte("event: replay_gap\ndata: " + string(payload) + "\n\n")) {
			return -1
		}
	}
	for _, frame := range frames {
		// 补读同样遵守订阅过滤：订阅者只该收到它关心的事件（避免无谓流量）。
		if !filter.Match(frame) {
			continue
		}
		if !write(frame.Bytes()) {
			return -1
		}
	}
	return snapshotNewest
}

// parseBusinessStreamSince 取补读位点：Last-Event-ID 头优先，其次 since 查询参数。
// 未带或非法时返回 (0, false)。非法值不报错——按「未带」处理并继续实时流，避免客户端卡死。
func parseBusinessStreamSince(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.GetHeader("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(c.Query("since"))
	}
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// businessStreamTokenKey 构造「单 Token 并发连接」的计数键（§4）。
//
// Agent Token 用 token ID（同一 Token 的多实例部署共享配额）；人类 JWT 用 user ID。
// 两者前缀不同，避免 ID 空间重叠导致互相占用配额。
func businessStreamTokenKey(c *gin.Context) string {
	if p := middleware.GetAgentPrincipal(c); p != nil {
		return "agent:" + strconv.FormatUint(uint64(p.TokenID), 10)
	}
	if v, ok := c.Get(middleware.CtxUserID); ok {
		if uid, ok := v.(uint); ok && uid > 0 {
			return "user:" + strconv.FormatUint(uint64(uid), 10)
		}
	}
	return ""
}
