package service

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 业务事件纯透传通道（见 ADR-106）与 SSE 连接治理。
//
// 职责边界（**铁律**）：JM 只做通道。
//   - 不解析 payload：`data` 段以 json.RawMessage 原样透传，不 unmarshal 业务内容；
//   - 不落库：环形缓冲全在内存，进程重启即失；
//   - 不聚合、不按业务语义分类：指标只统计通道维度（条数/丢弃/缺口）。
//
// 与（退役业务能力）**完全解耦**：本服务不引用 BusinessEventService / BusinessEvent 模型
// 等任何已删除或待删除的旧业务持久化设施，只消费 workerpb.PluginEvent 的 domain/topic 信封字段。

const (
	// BusinessSubscriberQueueSize 每个 SSE 订阅者的有界发送队列容量（§3.1）。
	BusinessSubscriberQueueSize = 1024
	// BusinessStreamDefaultHeartbeat 心跳间隔（§3.4§5：15–30s，默认 20s）。
	BusinessStreamDefaultHeartbeat = 20 * time.Second
	// BusinessStreamMaxPerToken 单 Token 并发 SSE 连接上限（§4）。
	BusinessStreamMaxPerToken = 5
	// BusinessStreamMaxGlobal 全局并发 SSE 连接上限（§4）。
	BusinessStreamMaxGlobal = 200
)

// ErrBusinessStreamTooManyConnections 单 Token 或全局并发连接超限。
var ErrBusinessStreamTooManyConnections = errors.New("SSE 并发连接超限")

// 慢消费者/写失败的稳定错误码（SSE `event: error` 的 code 字段）。
const (
	// SSEErrorSlowConsumer 订阅者处理过慢、队列已满，连接被主动断开。
	SSEErrorSlowConsumer = "SLOW_CONSUMER"
)

// BusinessStreamFilter 是订阅侧的过滤条件（§3.1）。
//
// 两者同时存在时取交集（域匹配且主题匹配才下发）；空集合表示不过滤。
type BusinessStreamFilter struct {
	// domains 精确匹配的域名集合（小写比较）。
	domains map[string]struct{}
	// topicPatterns 主题模式；尾部 `*` 表示前缀通配（如 activity.*），其余精确匹配。
	topicPatterns []string

	// allowedInstances 可订阅的实例 id 集合；nil 表示不受限（平台管理员 / Agent Token）。
	//
	// 为什么过滤要落在**服务层**而不是路由层：事件在 Subscribe 之后由服务端主动推送，
	// 路由层拿不到「下一条是什么」，只能在出口处逐条丢弃——那既浪费带宽也容易漏。
	// 放在 Match 里，越界事件从一开始就不进这个订阅者的队列。
	//
	// 空**非 nil**切片表示「一个实例都看不到」（权限查询失败时的安全默认）。
	allowedInstances map[uint]struct{}
}

// WithAllowedInstances 返回一个额外按实例白名单收敛的过滤器副本。
//
// allowed 为 nil 表示不受限（保持原行为）；空切片表示什么都看不到。
// 返回副本而不是原地改：filter 可能被同一次请求里多处复用，
// 原地改会让「谁在什么时候收窄了它」变得不可追踪。
func (f BusinessStreamFilter) WithAllowedInstances(allowed []uint) BusinessStreamFilter {
	if allowed == nil {
		return f
	}
	set := make(map[uint]struct{}, len(allowed))
	for _, id := range allowed {
		set[id] = struct{}{}
	}
	f.allowedInstances = set
	return f
}

// ParseBusinessStreamFilter 解析逗号分隔的 domains / topics 查询参数。
// 空白项忽略；空串表示不过滤。topics 支持尾部单级通配（§9 待定项 2：只支持单级后缀）。
func ParseBusinessStreamFilter(domains, topics string) BusinessStreamFilter {
	f := BusinessStreamFilter{}
	for _, raw := range strings.Split(domains, ",") {
		if d := strings.TrimSpace(raw); d != "" {
			if f.domains == nil {
				f.domains = make(map[string]struct{})
			}
			f.domains[strings.ToLower(d)] = struct{}{}
		}
	}
	for _, raw := range strings.Split(topics, ",") {
		if t := strings.TrimSpace(raw); t != "" {
			f.topicPatterns = append(f.topicPatterns, t)
		}
	}
	return f
}

// Empty 表示无任何过滤条件（全部事件下发）。
func (f BusinessStreamFilter) Empty() bool {
	return len(f.domains) == 0 && len(f.topicPatterns) == 0
}

// Match 判断一帧是否满足过滤条件。
func (f BusinessStreamFilter) Match(frame *BusinessFrame) bool {
	if frame == nil {
		return false
	}
	// 实例白名单优先：越界事件不必再比对域名/主题（也避免后续分支提前 return true 漏掉它）。
	//
	// 两种「空」的语义必须区分清楚：
	//  - `nil`（未设置）= 不受限，保持原行为（平台管理员 / Agent Token）；
	//  - 空**非 nil**切片 = 什么都看不到（权限查询失败时的安全默认）。
	//    这一档连未绑定实例的事件也挡——「查不到权限」不该等于「至少还能看全局」。
	//
	// 非空白名单时，`frame.Instance == 0`（节点级/全局事件）**放行**：它不属于任何实例，
	// 若按白名单裁剪，「没有实例归属」会被误判成「无权看」，节点级事件对所有非管理员消失。
	if f.allowedInstances != nil {
		// 空白名单（非 nil）是「什么都看不到」的显式信号（权限查询失败时的安全默认），
		// 它连未绑定实例的节点级事件也一并挡住——「查不到权限」不该等于「至少还能看全局」。
		if len(f.allowedInstances) == 0 {
			return false
		}
		// 未绑定实例的事件按「与实例无关」放行（见上方说明）。
		if frame.Instance != 0 {
			if _, ok := f.allowedInstances[frame.Instance]; !ok {
				return false
			}
		}
	}
	if len(f.domains) > 0 {
		if _, ok := f.domains[strings.ToLower(frame.Domain)]; !ok {
			return false
		}
	}
	if len(f.topicPatterns) > 0 {
		for _, p := range f.topicPatterns {
			if matchTopicPattern(p, frame.Topic) {
				return true
			}
		}
		return false
	}
	return true
}

// matchTopicPattern 单级后缀通配匹配：`activity.*` 匹配 `activity.spring-2026`（也匹配 `activity.` 前缀下任意串）。
// 不含 `*` 的模式按精确匹配处理。
func matchTopicPattern(pattern, topic string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(topic, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == topic
}

// BusinessStreamMetrics 是 SSE 通道维度的内存计数快照（§6§7）。
//
// 全部为**通道**指标，不按业务语义分类（边界）；不逐条写库，读取方按需取快照。
type BusinessStreamMetrics struct {
	// Connections 当前 SSE 连接数。
	Connections int
	// QueueDepthMax 各订阅者队列观察到的最大深度。
	QueueDepthMax int
	// SlowConsumerDisconnects 因队列满而断开订阅者的次数。
	SlowConsumerDisconnects uint64
	// WriteErrors SSE 写失败次数。
	WriteErrors uint64
	// HeartbeatSent 心跳帧发送数。
	HeartbeatSent uint64
	// EventsReceived 收到并写入缓冲的业务事件总数。
	EventsReceived uint64
	// EventsForwarded 至少成功推给一个订阅者的事件数。
	EventsForwarded uint64
	// EventsDropped 缓冲因容量打满而丢弃的最旧条数。
	EventsDropped uint64
	// GapServed 返回 replay_gap 的次数。
	GapServed uint64
	// BufferSize 当前缓冲条数。
	BufferSize int
	// BufferOldestSeq / BufferNewestSeq 缓冲位点边界。
	BufferOldestSeq int64
	BufferNewestSeq int64
}

// BusinessSubscriber 是一个 SSE 订阅者：独立有界队列 + 停止信号（§3.1）。
//
// 队列满时不阻塞、不丢最旧，而是标记为慢消费者并关闭 stop：由 SSE 写协程在收尾时补发
// `event: error`（SLOW_CONSUMER）后断开。断开是安全语义——重连带 Last-Event-ID 即可补齐窗口内事件。
type BusinessSubscriber struct {
	ch     chan *BusinessFrame
	stop   chan struct{}
	stopON sync.Once
	filter BusinessStreamFilter
	token  string
	// slow 标记本次停止是否由慢消费触发（决定收尾是否发 SLOW_CONSUMER 错误帧）。
	slow atomic.Bool
}

// Frames 返回订阅者的事件接收队列（只读消费）。
func (s *BusinessSubscriber) Frames() <-chan *BusinessFrame { return s.ch }

// Stop 返回连接终止信号（服务端主动断开或服务停止时关闭）。
func (s *BusinessSubscriber) Stop() <-chan struct{} { return s.stop }

// SlowConsumer 表示连接是否因慢消费被服务端断开。
func (s *BusinessSubscriber) SlowConsumer() bool { return s.slow.Load() }

// halt 幂等关闭停止信号（可被写入方并发调用）。
func (s *BusinessSubscriber) halt() {
	s.stopON.Do(func() { close(s.stop) })
}

// BusinessStreamService 承载业务事件环形缓冲与 SSE 扇出。
type BusinessStreamService struct {
	buffer    *BusinessStreamBuffer
	heartbeat time.Duration
	maxPerTok int
	maxGlobal int
	// db 仅用于解析实例信封元数据（UUID → ID/Name），从不写入；测试中可为 nil。
	db *gorm.DB

	mu   sync.Mutex
	subs map[*BusinessSubscriber]struct{}
	// perToken 是「单 Token 并发连接数」，key 由调用方给出（agent:<id> / user:<id>）。
	perToken map[string]int

	// meta 实例 UUID → (ID, Name) 的只读缓存：转发路径上不做任何同步 IO，
	// 未命中时以空值先转发，并异步整表刷新（实例表规模有界，约百行量级）。
	metaMu   sync.RWMutex
	meta     map[string]instanceMeta
	metaAt   time.Time
	metaBusy atomic.Bool

	received  atomic.Uint64
	forwarded atomic.Uint64
	gapServed atomic.Uint64
	hbSent    atomic.Uint64
	writeErr  atomic.Uint64
	slowDisc  atomic.Uint64
	queueMax  atomic.Int64

	// cursorWriter 是 ADR-107 的位点账本旁路写入器；nil 表示未装配账本（位点不落库，投递不受影响）。
	// 只在装配期由 SetCursorWriter 写入，之后只读，故不需要额外同步。
	cursorWriter *BusinessCursorWriter
}

// instanceMeta 是转发用的实例信封元数据（JM 侧补充，便于业务台定位来源）。
type instanceMeta struct {
	ID   uint
	Name string
}

// NewBusinessStreamService 创建业务事件流服务。buffer 为 nil 时按默认容量/窗口创建。
// db 可为 nil（测试）：此时实例元数据解析降级为空，事件仍原样透传。
func NewBusinessStreamService(db *gorm.DB, buffer *BusinessStreamBuffer) *BusinessStreamService {
	if buffer == nil {
		buffer = NewBusinessStreamBuffer(BusinessStreamBufferCapacity, BusinessStreamBufferWindow)
	}
	s := &BusinessStreamService{
		buffer:    buffer,
		heartbeat: BusinessStreamDefaultHeartbeat,
		maxPerTok: BusinessStreamMaxPerToken,
		maxGlobal: BusinessStreamMaxGlobal,
		db:        db,
		subs:      make(map[*BusinessSubscriber]struct{}),
		perToken:  make(map[string]int),
	}
	if db != nil {
		s.reloadMeta(db)
	}
	return s
}

// SetCursorWriter 注入位点账本写入器（ADR-107）。由 CP 装配期调用；传 nil 关闭账本。
//
// 必须在 StartWorkerStream 之前调用：装配后不再变更，使热路径读取无需加锁。
func (s *BusinessStreamService) SetCursorWriter(w *BusinessCursorWriter) {
	if s == nil {
		return
	}
	s.cursorWriter = w
}

// SetHeartbeatInterval 覆盖心跳间隔（默认 20s，规格允许 15–30s）。
func (s *BusinessStreamService) SetHeartbeatInterval(d time.Duration) {
	if s == nil || d <= 0 {
		return
	}
	s.heartbeat = d
}

// HeartbeatInterval 返回当前心跳间隔（SSE 处理器使用）。
func (s *BusinessStreamService) HeartbeatInterval() time.Duration {
	if s == nil {
		return BusinessStreamDefaultHeartbeat
	}
	return s.heartbeat
}

// Buffer 暴露底层环形缓冲（SSE 处理器做补读）。
func (s *BusinessStreamService) Buffer() *BusinessStreamBuffer {
	if s == nil {
		return nil
	}
	return s.buffer
}

// Ingest 接收一条探针业务事件：写入环形缓冲并扇出给订阅者（§2 的 ①②③）。
//
// 触发条件：`domain` 非空即业务事件（monitoring/governance 事件不进入本通道）。
// **转发路径不做同步 IO**：不写库、不写盘、不阻塞调用方（gRPC 事件流读循环）；
// 实例元数据未命中时异步整表刷新，本次以空元数据转发（不丢事件）。
//
// workerpb.PluginEvent.RawJson 是探针上报的**原始帧**；这里只拆信封取 topic 与 data，
// data 以 json.RawMessage 直接透传，不解析业务内容。
func (s *BusinessStreamService) Ingest(nodeUUID string, evt *workerpb.PluginEvent) {
	if s == nil || evt == nil {
		return
	}
	domain := strings.TrimSpace(evt.Domain)
	if domain == "" {
		return
	}
	frame := &BusinessFrame{
		Node:       nodeUUID,
		Domain:     domain,
		DedupKey:   evt.DedupKey,
		OccurredAt: evt.Timestamp,
	}
	if meta, ok := s.lookupMeta(evt.InstanceUuid); ok {
		frame.Instance = meta.ID
		frame.InstanceName = meta.Name
	}
	env := splitRawEnvelope(evt.RawJson)
	frame.Topic, frame.Payload, frame.Action = env.Topic, env.Payload, env.Action

	s.received.Add(1)
	streamSeq := s.buffer.Append(frame)
	s.publish(frame)
	// ADR-107 位点旁路：把本条事件的投递位点投给异步账本写入器。
	// 放在投递之后且**非阻塞**（有界队列满即丢最旧），因此不改变上面任何一行的语义与性能。
	s.recordCursor(frame, streamSeq, env.Seq)
}

// splitRawEnvelope 从原始探针帧里取信封字段：topic、data 与探针 per-domain 序号。
//
// 只做一层浅解（data 保持 RawMessage，不深入解析业务载荷）；非法/空帧容错返回零值。
//
// 为什么要在这里顺手取 `seq`：Worker 的 workerpb.PluginEvent 只透传 domain/dedupKey，
// 探针的 per-domain `seq` 仅存在于原始帧顶层（RawJson 就是整帧原文）。ADR-107 的位点账本
// 需要它做缺口比对，若另外再 Parse 一次 RawJson，等于给每条事件多加一次 JSON 解码——
// 合进同一次浅解里，热路径零额外开销。
func splitRawEnvelope(raw string) rawEnvelope {
	if raw == "" {
		return rawEnvelope{}
	}
	var env struct {
		Topic string          `json:"topic"`
		Data  json.RawMessage `json:"data"`
		Seq   int64           `json:"seq"`
		/*
			Action 是业务事件的「域内动作」（如 deposit / mail_sent）。

			【为什么在这里取】探针的 BusinessEventSink 签名是
			`emit(domain, dedupKey, topic, fields)` —— 没有 action 形参。
			插件把 action 放在 fields 里，最终落进探针帧的 `data.action`
			（与 payload 同级）。JM 不理解 action 的语义，只是把它从 payload
			顶层**提升**到帧信封，让下游（Loom）不必自己挖 payload。

			【为什么合进同一次浅解】与 Seq 同理：另开一次 Parse 等于给每条事件
			多加一次 JSON 解码。这里只多读一个字符串字段，热路径零额外开销。

			【边界】JM 不校验、不枚举 action，拿不到就留空——它不是 JM 的业务概念。
		*/
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return rawEnvelope{}
	}
	// data 是嵌套的，action 在 data 里面而不是帧顶层；再浅解一次 data。
	action := strings.TrimSpace(env.Action)
	if action == "" && len(env.Data) > 0 {
		var inner struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(env.Data, &inner) == nil {
			action = strings.TrimSpace(inner.Action)
		}
	}
	return rawEnvelope{
		Topic:   strings.TrimSpace(env.Topic),
		Payload: env.Data,
		Seq:     env.Seq,
		Action:  action,
	}
}

// rawEnvelope 是原始探针帧的浅解结果（仅传输层信封字段，不含业务语义）。
type rawEnvelope struct {
	Topic   string
	Payload json.RawMessage
	// Seq 探针分配的 per-domain 序号；探针未上报时为 0（合法状态，不是错误）。
	Seq int64
	// Action 域内动作（deposit / mail_sent …）；插件未在 fields 里带时为 ""。
	// JM 只透传，不校验、不枚举——它不是 JM 的业务概念。
	Action string
}

// recordCursor 把一条已投递事件的位点投给异步账本写入器（ADR-107 §2）。
//
// 三条硬约束：
//   - **不阻塞**：只做一次有界队列入队，队列满由写入器丢最旧，热路径永不等待 DB；
//   - **不解析 payload**：只用信封字段（domain / dedupKey / 探针 seq / 时间）；
//   - **不因缺字段而丢位点**：实例元数据缓存未命中时 instanceID=0 仍记账，
//     否则重启后首屏事件的位点会成段缺失，反而扩大业务台的保守对账范围。
func (s *BusinessStreamService) recordCursor(frame *BusinessFrame, streamSeq, probeSeq int64) {
	if s == nil || frame == nil || s.cursorWriter == nil {
		return
	}
	s.cursorWriter.Enqueue(model.BusinessEventCursor{
		StreamSeq:  streamSeq,
		InstanceID: frame.Instance,
		Domain:     frame.Domain,
		Seq:        probeSeq,
		DedupKey:   frame.DedupKey,
		OccurredAt: frame.OccurredAt,
	})
}

// publish 把一帧非阻塞地扇出给匹配的订阅者（§3.1）。
//
// 队列满 → 该订阅者被判为慢消费者：标记 slow、关闭 stop 让 SSE 写协程收尾（先发 error 帧再断开），
// 并从订阅表移除以免继续投递。**绝不阻塞**主链路与其他订阅者——这是本函数唯一的硬约束。
func (s *BusinessStreamService) publish(frame *BusinessFrame) {
	s.mu.Lock()
	var slow []*BusinessSubscriber
	delivered := 0
	for sub := range s.subs {
		if !sub.filter.Match(frame) {
			continue
		}
		select {
		case sub.ch <- frame:
			delivered++
			if depth := len(sub.ch); int64(depth) > s.queueMax.Load() {
				s.queueMax.Store(int64(depth))
			}
		default:
			sub.slow.Store(true)
			slow = append(slow, sub)
		}
	}
	for _, sub := range slow {
		delete(s.subs, sub)
		if sub.token != "" {
			if n := s.perToken[sub.token]; n > 1 {
				s.perToken[sub.token] = n - 1
			} else {
				delete(s.perToken, sub.token)
			}
		}
	}
	s.mu.Unlock()

	// 停止信号在锁外关闭：close 是即时操作，但放到锁外可确保不会被任何持锁路径阻塞。
	for _, sub := range slow {
		sub.halt()
		s.slowDisc.Add(1)
		slog.Warn("SSE 慢消费者被断开（队列已满）",
			"token", sub.token, "queueCapacity", cap(sub.ch))
	}
	if delivered > 0 {
		s.forwarded.Add(1)
	}
}

// Subscribe 注册一个 SSE 订阅者（§3.1 / §4）。
//
// tokenKey 为空表示不参与单 Token 限流（仅受全局上限约束）。超出上限返回
// ErrBusinessStreamTooManyConnections，调用方据此回 429 + Retry-After。
func (s *BusinessStreamService) Subscribe(tokenKey string, filter BusinessStreamFilter) (*BusinessSubscriber, error) {
	if s == nil {
		return nil, errors.New("业务事件流服务未初始化")
	}
	sub := &BusinessSubscriber{
		ch:     make(chan *BusinessFrame, BusinessSubscriberQueueSize),
		stop:   make(chan struct{}),
		filter: filter,
		token:  tokenKey,
	}
	s.mu.Lock()
	if s.maxGlobal > 0 && len(s.subs) >= s.maxGlobal {
		s.mu.Unlock()
		return nil, ErrBusinessStreamTooManyConnections
	}
	if tokenKey != "" && s.maxPerTok > 0 && s.perToken[tokenKey] >= s.maxPerTok {
		s.mu.Unlock()
		return nil, ErrBusinessStreamTooManyConnections
	}
	s.subs[sub] = struct{}{}
	if tokenKey != "" {
		s.perToken[tokenKey]++
	}
	s.mu.Unlock()
	return sub, nil
}

// Unsubscribe 注销订阅者（幂等）。SSE 处理器 defer 调用。
func (s *BusinessStreamService) Unsubscribe(sub *BusinessSubscriber) {
	if s == nil || sub == nil {
		return
	}
	s.mu.Lock()
	if _, ok := s.subs[sub]; ok {
		delete(s.subs, sub)
		if sub.token != "" {
			if n := s.perToken[sub.token]; n > 1 {
				s.perToken[sub.token] = n - 1
			} else {
				delete(s.perToken, sub.token)
			}
		}
	}
	s.mu.Unlock()
	sub.halt()
}

// TokenConnectionCount 返回某 Token 当前并发 SSE 连接数（诊断/测试）。
func (s *BusinessStreamService) TokenConnectionCount(tokenKey string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.perToken[tokenKey]
}

// IncGapServed / IncWriteError / IncHeartbeatSent 供 SSE 处理器上报通道指标。
func (s *BusinessStreamService) IncGapServed() {
	if s != nil {
		s.gapServed.Add(1)
	}
}

func (s *BusinessStreamService) IncWriteError() {
	if s != nil {
		s.writeErr.Add(1)
	}
}

func (s *BusinessStreamService) IncHeartbeatSent() {
	if s != nil {
		s.hbSent.Add(1)
	}
}

// Metrics 返回通道维度指标快照（内存计数，不写库）。
func (s *BusinessStreamService) Metrics() BusinessStreamMetrics {
	if s == nil {
		return BusinessStreamMetrics{}
	}
	s.mu.Lock()
	conns := len(s.subs)
	s.mu.Unlock()
	m := BusinessStreamMetrics{
		Connections:             conns,
		QueueDepthMax:           int(s.queueMax.Load()),
		SlowConsumerDisconnects: s.slowDisc.Load(),
		WriteErrors:             s.writeErr.Load(),
		HeartbeatSent:           s.hbSent.Load(),
		EventsReceived:          s.received.Load(),
		EventsForwarded:         s.forwarded.Load(),
		EventsDropped:           s.buffer.Dropped(),
		GapServed:               s.gapServed.Load(),
		BufferSize:              s.buffer.Size(),
		BufferOldestSeq:         s.buffer.OldestSeq(),
		BufferNewestSeq:         s.buffer.NewestSeq(),
	}
	return m
}

// lookupMeta 查实例信封元数据；未命中触发异步刷新并返回 false（不阻塞转发）。
func (s *BusinessStreamService) lookupMeta(uuid string) (instanceMeta, bool) {
	if uuid == "" {
		return instanceMeta{}, false
	}
	s.metaMu.RLock()
	meta, ok := s.meta[uuid]
	s.metaMu.RUnlock()
	if ok {
		return meta, true
	}
	if s.db != nil {
		s.markMetaReload()
	}
	return instanceMeta{}, false
}

// setDB 供测试注入（构造时 db 为 nil 的情形）。
func (s *BusinessStreamService) setDB(db *gorm.DB) { s.db = db }

// markMetaReload 触发一次异步整表刷新（单飞，避免事件洪峰打爆 DB）。
func (s *BusinessStreamService) markMetaReload() {
	if s.db == nil {
		return
	}
	if !s.metaBusy.CompareAndSwap(false, true) {
		return
	}
	db := s.db
	go func() {
		defer s.metaBusy.Store(false)
		s.reloadMeta(db)
	}()
}

// reloadMeta 整表加载实例 UUID → (ID, Name)。
// 只读平台元数据（非业务数据），实例表规模有界（100+ 量级），代价可忽略。
func (s *BusinessStreamService) reloadMeta(db *gorm.DB) {
	if s == nil || db == nil {
		return
	}
	var rows []model.Instance
	if err := db.Select("id", "uuid", "name").Find(&rows).Error; err != nil {
		slog.Warn("加载实例元数据缓存失败（业务事件将暂缺 instance/instanceName）", "error", err)
		return
	}
	next := make(map[string]instanceMeta, len(rows))
	for _, inst := range rows {
		if inst.UUID != "" {
			next[inst.UUID] = instanceMeta{ID: inst.ID, Name: inst.Name}
		}
	}
	s.metaMu.Lock()
	s.meta = next
	s.metaAt = time.Now()
	s.metaMu.Unlock()
}

// MetaAge 返回实例元数据缓存年龄（诊断/测试）。
func (s *BusinessStreamService) MetaAge() time.Duration {
	if s == nil {
		return 0
	}
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	if s.metaAt.IsZero() {
		return 0
	}
	return time.Since(s.metaAt)
}
