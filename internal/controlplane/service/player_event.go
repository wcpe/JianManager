package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 托管模式桥事件类型（与探针侧的帧 event 名逐字一致，协议规格 §3.4.2 / §3.4.3）。
const (
	// ManagedBridgeEventConfigApplied 探针应用配置后的回执。
	ManagedBridgeEventConfigApplied = "config_applied"
	// ManagedBridgeEventConfigState 探针周期性上报的托管状态。
	ManagedBridgeEventConfigState = "config_state"
)

// PlayerEvent 是推送给前端（SSE）的一条玩家/探针事件。
// 由 Worker 经 gRPC StreamPluginEvents 上报的 workerpb.PluginEvent 翻译而来，
// 并按需补充实例 DB ID 与名称（前端按实例/群组订阅、展示所在子服）。
type PlayerEvent struct {
	InstanceUUID string `json:"instanceUuid"`
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	// Type 取值与探针事件一致：connected | disconnected | heartbeat | player_join | player_quit | chat | cross_server。
	Type       string `json:"type"`
	Timestamp  int64  `json:"timestamp"`
	PlayerName string `json:"playerName,omitempty"`
	PlayerUUID string `json:"playerUuid,omitempty"`
	Message    string `json:"message,omitempty"`
	Server     string `json:"server,omitempty"`     // 子服名（玩家所在/事件发生）
	FromServer string `json:"fromServer,omitempty"` // cross_server：来源子服
	ToServer   string `json:"toServer,omitempty"`   // cross_server：目标子服
	Platform   string `json:"platform,omitempty"`   // bukkit | bungee
}

// rosterPlayer 在线名册中的一名玩家及其当前所在子服（BC 跨服感知）。
type rosterPlayer struct {
	Name   string `json:"name"`
	Server string `json:"server"`
}

// playerRoster 维护「实例 UUID → 在线玩家集合」的实时名册。
//
// 由探针经反向 WS 实时推送的 join/quit/cross_server 事件演进而来，给前端提供「实时在线列表」，
// 与 RCON/list 的轮询快照互补（探针在位时以名册为准，更实时）。探针 connected 时重置该实例名册
// （以新一轮 join 为准），disconnected 时清空（在线状态不可知，降级为空 + 前端提示未连入）。
type playerRoster struct {
	mu sync.RWMutex
	// byInstance[instanceUUID][playerName] = 当前所在子服名。
	byInstance map[string]map[string]string
}

// newPlayerRoster 创建空名册。
func newPlayerRoster() *playerRoster {
	return &playerRoster{byInstance: make(map[string]map[string]string)}
}

// apply 据一条探针事件演进名册。仅处理影响在线集合的事件，其余（heartbeat/chat）忽略。
func (r *playerRoster) apply(evt *workerpb.PluginEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch evt.Type {
	case "connected":
		// 探针（重）连入：重置该实例名册，避免残留上一会话的陈旧在线。
		r.byInstance[evt.InstanceUuid] = make(map[string]string)
	case "disconnected":
		// 探针断开：在线状态不可知，清空（前端据 connected 标志提示未连入）。
		delete(r.byInstance, evt.InstanceUuid)
	case "player_join":
		if evt.PlayerName == "" {
			return
		}
		r.ensure(evt.InstanceUuid)[evt.PlayerName] = evt.Server
	case "player_quit":
		if m := r.byInstance[evt.InstanceUuid]; m != nil {
			delete(m, evt.PlayerName)
		}
	case "cross_server":
		// 跨服路由：玩家切到 toServer（进入/切换记为在线于目标子服）。
		if evt.PlayerName == "" {
			return
		}
		dest := evt.ToServer
		if dest == "" {
			dest = evt.Server
		}
		r.ensure(evt.InstanceUuid)[evt.PlayerName] = dest
	}
}

// ensure 返回（必要时创建）某实例的名册 map。调用方须持写锁。
func (r *playerRoster) ensure(instanceUUID string) map[string]string {
	m := r.byInstance[instanceUUID]
	if m == nil {
		m = make(map[string]string)
		r.byInstance[instanceUUID] = m
	}
	return m
}

// snapshot 返回某实例当前在线名册（按玩家名排序，便于稳定展示）。
func (r *playerRoster) snapshot(instanceUUID string) []rosterPlayer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := r.byInstance[instanceUUID]
	out := make([]rosterPlayer, 0, len(m))
	for name, server := range m {
		out = append(out, rosterPlayer{Name: name, Server: server})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PlayerEventService 订阅所有 Worker 的插件事件流（StreamPluginEvents），维护实时在线名册，
// 并把玩家事件扇出给前端 SSE 客户端。与 EventService（实例状态/输出流）刻意分开：
// 二者消费方、过滤维度、订阅生命周期均不同。
type PlayerEventService struct {
	pool   *cpgrpc.ClientPool
	db     *gorm.DB
	roster *playerRoster

	mu   sync.RWMutex
	subs []playerSub
	// closed 表示 Stop() 已把订阅表清空并关过全部 channel。
	// 有了它，unsub 才能判断「channel 是我摘下的（我有权关）」还是「Stop 已经关了（不能再关）」。
	closed bool

	ctx    context.Context
	cancel context.CancelFunc

	// streamMu/streaming 保证每节点只起一条插件事件流订阅（F4）：重复订阅会导致事件重复
	// 扇出与业务事件重复落库。订阅 goroutine 退出时清除标记，使节点真正重连后仍能重建订阅。
	streamMu  sync.Mutex
	streaming map[string]struct{}

	// businessStream 是业务事件（domain 非空）的纯透传出口：本服务作为
	// StreamPluginEvents 的唯一消费点，把业务帧交给它写环形缓冲并扇出，绝不在本服务内解析业务语义。
	// nil 时业务事件被静默丢弃（未装配该能力，不影响监控/玩家事件链路）。
	businessStream *BusinessStreamService

	// probeConnected 是探针（重）连入时的旁路钩子（§8.2）。
	//
	// 为什么挂在这里：StreamPluginEvents 是全平台唯一消费探针 `connected` 事件的地方，
	// 而「离线目标上线后自动补推」需要的正是这一瞬间的信号。另起一条订阅会重复消费同一条
	// 长驻流（F4 明确禁止），轮询则更晚发现且要为每台离线实例反复打库。
	//
	// 契约：回调必须是**非阻塞**的（本方法在事件流的读循环里调用），且不得 panic。
	probeConnected func(instanceUUID string)

	// managedEvent 是托管模式桥事件（config_applied / config_state）的旁路钩子。
	//
	// 为什么也挂在这里：这两类事件同样只经 StreamPluginEvents 到达，另起订阅会重复消费
	// 同一条长驻流（F4 禁止）。由装配期注入 handlers，本服务不解帧、不落库，
	// 只做「谁该收到」的路由——托管语义归 ManagedPushService 所有。
	//
	// 契约同 probeConnected：**非阻塞**、不得 panic（在读循环里同步调用）。
	managedEvent func(instanceUUID, eventType string, raw map[string]any)

	// bridgeState 是桥连接态变化回调（connected / disconnected 事实本身）。
	// 供对账矩阵判定实例是否真的在线——config_state 走同一条桥，断连时发不出去，
	// 只信自报会把掉线实例显示成在线。契约同 probeConnected：非阻塞、不得 panic。
	bridgeState func(instanceUUID string, connected bool)
}

// SetManagedEventHook 注入托管桥事件回调（config_applied / config_state）。传 nil 关闭。
//
// 回调在事件流读循环内被同步调用，实现方必须自己起 goroutine——落库动作一旦阻塞
// 读循环，会把该节点所有实例的实时事件一起拖住。
func (s *PlayerEventService) SetManagedEventHook(fn func(instanceUUID, eventType string, raw map[string]any)) {
	s.managedEvent = fn
}

// SetProbeBridgeStateHook 注入桥连接态变化回调（connected=true / disconnected=false）。传 nil 关闭。
//
// 与 SetProbeConnectedHook 的分工：那个是「探针连入 → 触发补推」的业务钩子，
// 这个是**连接事实**本身，供对账矩阵判定实例是否真的在线上。
// 两者语义不同，不要合并——合并会让「只想补推」和「只想知道通断」互相牵连。
func (s *PlayerEventService) SetProbeBridgeStateHook(fn func(instanceUUID string, connected bool)) {
	s.bridgeState = fn
}

// SetProbeConnectedHook 注入探针连入回调（自动补推入口）。传 nil 关闭。
//
// 回调在事件流读循环内被同步调用，因此实现方必须自己起 goroutine——
// 反序列化/落库这类动作一旦阻塞读循环，会把该节点所有实例的实时事件一起拖住。
func (s *PlayerEventService) SetProbeConnectedHook(fn func(instanceUUID string)) {
	s.probeConnected = fn
}

// SetBusinessStream 注入业务事件纯透传通道。由 CP 装配期调用。
func (s *PlayerEventService) SetBusinessStream(stream *BusinessStreamService) {
	s.businessStream = stream
}

// playerSub 一个 SSE 订阅：channel + 实例 UUID 过滤（空=全部）。
type playerSub struct {
	ch     chan PlayerEvent
	filter string
}

// NewPlayerEventService 创建玩家事件服务。db 用于把实例 UUID 解析为 DB ID/名称（可为 nil，仅测试）。
func NewPlayerEventService(pool *cpgrpc.ClientPool, db *gorm.DB) *PlayerEventService {
	ctx, cancel := context.WithCancel(context.Background())
	return &PlayerEventService{
		pool:   pool,
		db:     db,
		roster: newPlayerRoster(),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Subscribe 订阅玩家事件，filter 为实例 UUID（空=全部）。取消时调用返回的函数。
func (s *PlayerEventService) Subscribe(filter string) (<-chan PlayerEvent, func()) {
	ch := make(chan PlayerEvent, 128)
	s.mu.Lock()
	s.subs = append(s.subs, playerSub{ch: ch, filter: filter})
	s.mu.Unlock()

	unsub := func() {
		s.mu.Lock()
		removed := false
		for i, sub := range s.subs {
			if sub.ch == ch {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				removed = true
				break
			}
		}
		closed := s.closed
		s.mu.Unlock()
		// Stop() 已经把订阅表清空并关过 channel：此时不能再 close 一次（会 panic）。
		// 只有「本次 unsub 真正把 channel 从表里摘掉」才拥有关闭权；进程已停（closed）也不关。
		if removed && !closed {
			close(ch)
		}
	}
	return ch, unsub
}

// OnlineSnapshot 返回某实例当前的实时在线名册（探针在位时由事件流维护）。
func (s *PlayerEventService) OnlineSnapshot(instanceUUID string) []rosterPlayer {
	return s.roster.snapshot(instanceUUID)
}

// InstanceUUIDByID 把实例 DB ID 解析为 UUID（SSE 路由按 :id 订阅时用）。db 为 nil 或未找到返回空串。
func (s *PlayerEventService) InstanceUUIDByID(id uint) string {
	if s.db == nil {
		return ""
	}
	var inst model.Instance
	if err := s.db.Select("uuid").First(&inst, id).Error; err != nil {
		return ""
	}
	return inst.UUID
}

// IsProbeConnected 返回某实例是否有探针在线（名册存在即视为在线，connected 时建、disconnected 时删）。
func (s *PlayerEventService) IsProbeConnected(instanceUUID string) bool {
	s.roster.mu.RLock()
	defer s.roster.mu.RUnlock()
	_, ok := s.roster.byInstance[instanceUUID]
	return ok
}

// StartWorkerStream 启动到指定 Worker 的插件事件流订阅（每节点单飞F4）。
func (s *PlayerEventService) StartWorkerStream(nodeUUID string) {
	client, ok := s.pool.Get(nodeUUID)
	if !ok {
		slog.Warn("PlayerEventService: 无法获取 Worker 客户端", "nodeUUID", nodeUUID)
		return
	}
	// 每节点单飞：已在订阅则忽略重复触发（瞬时 active=2 的 over-trigger 不得起重复长驻流）。
	s.streamMu.Lock()
	if s.streaming == nil {
		s.streaming = make(map[string]struct{})
	}
	if _, busy := s.streaming[nodeUUID]; busy {
		s.streamMu.Unlock()
		slog.Debug("PlayerEventService: 插件事件流已订阅，忽略重复触发", "nodeUUID", nodeUUID)
		return
	}
	s.streaming[nodeUUID] = struct{}{}
	s.streamMu.Unlock()

	go func() {
		defer func() {
			s.streamMu.Lock()
			delete(s.streaming, nodeUUID)
			s.streamMu.Unlock()
		}()
		s.streamFromWorker(nodeUUID, client)
	}()
}

// isManagedBridgeEventType 判断是否为托管模式桥事件（回执 / 周期状态）。
//
// 只认这两种：其它事件走名册或业务透传，不该进托管处理器。
func isManagedBridgeEventType(eventType string) bool {
	switch eventType {
	case ManagedBridgeEventConfigApplied, ManagedBridgeEventConfigState:
		return true
	}
	return false
}

func (s *PlayerEventService) dispatchManagedEvent(evt *workerpb.PluginEvent) {
	raw := evt.RawJson
	if strings.TrimSpace(raw) == "" {
		raw = evt.Message
	}
	if strings.TrimSpace(raw) == "" {
		slog.Debug("托管桥事件缺少载荷，已跳过", "instanceUuid", evt.InstanceUuid, "type", evt.Type)
		return
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		slog.Warn("托管桥事件载荷非法，已跳过", "instanceUuid", evt.InstanceUuid, "type", evt.Type, "err", err)
		return
	}
	payload, ok := managedEventPayload(frame)
	if !ok {
		slog.Warn("托管桥事件缺少 payload 段，已跳过",
			"instanceUuid", evt.InstanceUuid, "type", evt.Type)
		return
	}
	s.managedEvent(evt.InstanceUuid, evt.Type, payload)
}

// managedEventPayload 从探针上行的整帧里取出托管事件的实际载荷。
//
// ## 为什么不能把整帧当载荷
//
// 探针的 `config_applied` / `config_state` 帧是这种信封结构（ServerProbe BridgeFrames）：
//
//	{"type":"event","event":"config_applied","instance":"<uuid>","timestamp":<ms>,
//	 "dedupKey":"cfg:7:applied","payload":{"configVersion":7,"result":"applied",...}}
//
// 结构化字段一律在 `payload` 下，与 `metric_snapshot` 一致。早期把整帧直接交给处理器，
// `configVersion` 就恒为 0、`result` 恒为空串——每条回执都被判成 failed，
// 而失败原因永远显示"探针未给出失败原因"，看起来像探针的问题。
//
// 顺序即协议优先级：
//  1. `payload` 段（规范形态）；
//  2. `data` 段（早期草案把字段放这里，仍有存量探针）；
//  3. 整帧（兜底：载荷本身就是平铺对象时，例如测试或将来新增的扁平事件）。
func managedEventPayload(frame map[string]any) (map[string]any, bool) {
	for _, key := range []string{"payload", "data"} {
		if nested, ok := frame[key].(map[string]any); ok && len(nested) > 0 {
			return nested, true
		}
	}
	// 兜底：整帧里已有协议要求的字段，说明是平铺载荷而非信封。
	if _, ok := frame["configVersion"]; ok {
		return frame, true
	}
	if _, ok := frame["connection"]; ok {
		return frame, true
	}
	return nil, false
}

// streamFromWorker 从单个 Worker 拉取插件事件流：演进名册 + 翻译 + 扇出。
func (s *PlayerEventService) streamFromWorker(nodeUUID string, client *cpgrpc.Client) {
	slog.Info("PlayerEventService: 开始订阅 Worker 插件事件流", "nodeUUID", nodeUUID)
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		stream, err := client.Worker.StreamPluginEvents(s.ctx, &workerpb.StreamPluginEventsRequest{})
		if err != nil {
			slog.Warn("PlayerEventService: 订阅 Worker 插件事件流失败", "nodeUUID", nodeUUID, "err", err)
			return
		}

		for {
			evt, err := stream.Recv()
			if err != nil {
				slog.Info("PlayerEventService: Worker 插件事件流断开", "nodeUUID", nodeUUID, "err", err)
				break
			}
			// 带业务域（domain 非空）的事件不在本服务内做业务处理：JM 不做任何业务语义
			//。它们只走纯透传通道：写内存环形缓冲 + 扇出给 SSE 订阅者，
			// 不落库、不聚合、也不演进在线名册。
			if evt.Domain != "" {
				s.businessStream.Ingest(nodeUUID, evt)
				continue
			}
			// 先演进名册（连接/在线集合），再翻译扇出。
			s.roster.apply(evt)
			// 探针（重）连入：通知旁路钩子（的离线目标自动补推）。
			// 钩子必须非阻塞，否则会卡住本节点所有实例的实时事件。
			if evt.Type == "connected" && s.probeConnected != nil {
				s.probeConnected(evt.InstanceUuid)
			}
			// 桥连接事实：独立通知，供对账矩阵判定在线与否（与上面的补推钩子语义不同）。
			if s.bridgeState != nil && (evt.Type == "connected" || evt.Type == "disconnected") {
				s.bridgeState(evt.InstanceUuid, evt.Type == "connected")
			}
			// 托管模式回执/状态：同样经本流到达，路由给托管服务的处理器。
			// 只在 type 命中时解析 payload——托管帧几百毫秒一条，为主流路径省一次 JSON 解析。
			if s.managedEvent != nil && isManagedBridgeEventType(evt.Type) {
				s.dispatchManagedEvent(evt)
			}
			s.broadcast(s.translate(evt))
		}
	}
}

// translate 把 workerpb.PluginEvent 翻译为前端事件，并补充实例 DB ID/名称（按 UUID 查 DB）。
func (s *PlayerEventService) translate(evt *workerpb.PluginEvent) PlayerEvent {
	out := PlayerEvent{
		InstanceUUID: evt.InstanceUuid,
		Type:         evt.Type,
		Timestamp:    evt.Timestamp,
		PlayerName:   evt.PlayerName,
		PlayerUUID:   evt.PlayerUuid,
		Message:      evt.Message,
		Server:       evt.Server,
		FromServer:   evt.FromServer,
		ToServer:     evt.ToServer,
		Platform:     evt.Platform,
	}
	if s.db != nil && evt.InstanceUuid != "" {
		var inst model.Instance
		if err := s.db.Select("id", "name").Where("uuid = ?", evt.InstanceUuid).First(&inst).Error; err == nil {
			out.InstanceID = inst.ID
			out.InstanceName = inst.Name
		}
	}
	return out
}

// broadcast 按订阅过滤扇出事件给所有订阅者（消费过慢则丢弃，绝不阻塞产生方）。
func (s *PlayerEventService) broadcast(evt PlayerEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sub := range s.subs {
		if sub.filter != "" && sub.filter != evt.InstanceUUID {
			continue
		}
		select {
		case sub.ch <- evt:
		default:
		}
	}
}

// Stop 停止服务。
//
// 关闭权归这里：关闭全部 channel 并置 closed，之后仍在世上的 unsub 回调会因
// 「removed=false 或 closed=true」而不再 close 第二次（那是 panic：close of closed channel）。
func (s *PlayerEventService) Stop() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for _, sub := range s.subs {
		close(sub.ch)
	}
	s.subs = nil
}
