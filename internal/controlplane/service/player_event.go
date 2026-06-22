package service

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// PlayerEvent 是推送给前端（SSE）的一条玩家/探针事件（FR-066，见 ADR-016）。
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

// playerRoster 维护「实例 UUID → 在线玩家集合」的实时名册（FR-066）。
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
// 并把玩家事件扇出给前端 SSE 客户端（FR-066）。与 EventService（实例状态/输出流）刻意分开：
// 二者消费方、过滤维度、订阅生命周期均不同（见 ADR-016）。
type PlayerEventService struct {
	pool   *cpgrpc.ClientPool
	db     *gorm.DB
	roster *playerRoster

	mu   sync.RWMutex
	subs []playerSub

	ctx    context.Context
	cancel context.CancelFunc
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
		for i, sub := range s.subs {
			if sub.ch == ch {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		close(ch)
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

// StartWorkerStream 启动到指定 Worker 的插件事件流订阅。
func (s *PlayerEventService) StartWorkerStream(nodeUUID string) {
	client, ok := s.pool.Get(nodeUUID)
	if !ok {
		slog.Warn("PlayerEventService: 无法获取 Worker 客户端", "nodeUUID", nodeUUID)
		return
	}
	go s.streamFromWorker(nodeUUID, client)
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
			// 先演进名册（连接/在线集合），再翻译扇出。
			s.roster.apply(evt)
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
func (s *PlayerEventService) Stop() {
	s.cancel()
	s.mu.Lock()
	for _, sub := range s.subs {
		close(sub.ch)
	}
	s.subs = nil
	s.mu.Unlock()
}
