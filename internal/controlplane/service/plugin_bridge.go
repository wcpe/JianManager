package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// pluginTokenScope 插件桥 token 的 scope claim 固定值（与 Worker 侧 ws 包常量一致）。
// 用于区分终端 token，防止终端 token 被误用于插件桥握手。参见 ADR-012。
const pluginTokenScope = "plugin-bridge"

// pluginTokenTTL 插件桥 token 有效期。仅握手时校验一次，连上后长期有效；
// 取数分钟，覆盖「签发 token → 写入插件配置 → 插件启动连入」的窗口。
const pluginTokenTTL = 10 * time.Minute

// PluginEvent 插件桥事件（前端消费）。data 为事件载荷 JSON 原文。
type PluginEvent struct {
	InstanceUUID string `json:"instanceUuid"`
	Type         string `json:"type"`
	Data         string `json:"data"`
	Timestamp    int64  `json:"timestamp"`
}

// PluginToken 插件桥连接 token 响应。运维把 token + wsUrl 写入实例的平台插件配置。
type PluginToken struct {
	Token        string `json:"token"`
	WSURL        string `json:"wsUrl"`
	InstanceUUID string `json:"instanceUuid"`
	ExpiresIn    int    `json:"expiresIn"`
}

// PluginConnection 某实例的插件连接状态（前端「已连插件列表」用）。
type PluginConnection struct {
	InstanceUUID string `json:"instanceUuid"`
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	NodeUUID     string `json:"nodeUuid"`
	Connected    bool   `json:"connected"`
	LastEventAt  int64  `json:"lastEventAt"`
}

// PluginBridgeService 插件桥的 Control Plane 侧服务（FR-103 / ADR-012）：
//   - 为实例签发插件桥连接 token；
//   - 订阅各 Worker 的 StreamPluginEvents，扇出给前端 SSE，并维护连接状态；
//   - 把指令经 gRPC 下发给实例当前连入的插件。
// CP 仍是唯一 DB 入口与唯一面向浏览器入口；插件只与 Worker 通信。
type PluginBridgeService struct {
	db        *gorm.DB
	pool      *cpgrpc.ClientPool
	jwtSecret string

	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.RWMutex
	subs []chan PluginEvent
	// conns：实例 UUID → 连接状态，由 connected/disconnected 事件维护。
	conns map[string]*PluginConnection
}

// NewPluginBridgeService 创建插件桥服务。
func NewPluginBridgeService(db *gorm.DB, pool *cpgrpc.ClientPool, jwtSecret string) *PluginBridgeService {
	ctx, cancel := context.WithCancel(context.Background())
	return &PluginBridgeService{
		db:        db,
		pool:      pool,
		jwtSecret: jwtSecret,
		ctx:       ctx,
		cancel:    cancel,
		conns:     make(map[string]*PluginConnection),
	}
}

// IssueToken 为实例签发插件桥连接 token（HS256，scope=plugin-bridge，10min 有效）。
// wsBaseHost 非空时按浏览器访问的 Host 与协议构造 wsUrl（支持非 localhost），为空时回退实例所在节点地址。
func (s *PluginBridgeService) IssueToken(instanceID uint, wsBaseHost string, secure bool) (*PluginToken, error) {
	var instance model.Instance
	if err := s.db.Preload("Node").First(&instance, instanceID).Error; err != nil {
		return nil, ErrInstanceNotFound
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"instanceId": instance.UUID,
		"scope":      pluginTokenScope,
		"exp":        now.Add(pluginTokenTTL).Unix(),
		"iat":        now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return nil, fmt.Errorf("签发插件桥 token 失败: %w", err)
	}

	// 插件运行于游戏服 JVM，与 Worker 同机，直接连 Worker 的 WS 端口（不经 CP 代理）。
	// 默认用实例所在节点 host + wsPort；同机插件可用回环，跨机部署用节点对外 host。
	wsURL := fmt.Sprintf("ws://%s:%d/ws/plugin-bridge", instance.Node.Host, instance.Node.WSPort)

	return &PluginToken{
		Token:        tokenStr,
		WSURL:        wsURL,
		InstanceUUID: instance.UUID,
		ExpiresIn:    int(pluginTokenTTL.Seconds()),
	}, nil
}

// SendCommand 把指令下发给某实例当前连入的插件。实例无插件连入或 Worker 未连接时返回错误。
func (s *PluginBridgeService) SendCommand(instanceID uint, action, argsJSON string) error {
	var instance model.Instance
	if err := s.db.Preload("Node").First(&instance, instanceID).Error; err != nil {
		return ErrInstanceNotFound
	}
	client, ok := s.pool.Get(instance.Node.UUID)
	if !ok {
		return fmt.Errorf("Worker %s 未连接", instance.Node.UUID)
	}

	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	resp, err := client.Worker.SendPluginCommand(ctx, &workerpb.SendPluginCommandRequest{
		InstanceUuid: instance.UUID,
		Action:       action,
		ArgsJson:     argsJSON,
	})
	if err != nil {
		return fmt.Errorf("gRPC SendPluginCommand 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// Subscribe 订阅插件桥事件，返回只读 channel。取消时调用返回的函数。
func (s *PluginBridgeService) Subscribe() (<-chan PluginEvent, func()) {
	ch := make(chan PluginEvent, 128)
	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()

	unsub := func() {
		s.mu.Lock()
		for i, sub := range s.subs {
			if sub == ch {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		close(ch)
	}
	return ch, unsub
}

// Connections 返回当前已知的插件连接状态快照（前端「已连插件列表」用）。
func (s *PluginBridgeService) Connections() []PluginConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PluginConnection, 0, len(s.conns))
	for _, c := range s.conns {
		out = append(out, *c)
	}
	return out
}

// StartWorkerStream 启动到指定 Worker 的插件事件流订阅（由 onWorkerConnect 回调触发）。
func (s *PluginBridgeService) StartWorkerStream(nodeUUID string) {
	client, ok := s.pool.Get(nodeUUID)
	if !ok {
		slog.Warn("PluginBridgeService: 无法获取 Worker 客户端", "nodeUUID", nodeUUID)
		return
	}
	go s.streamFromWorker(nodeUUID, client)
}

// streamFromWorker 从单个 Worker 拉取插件事件并扇出 + 维护连接状态。
func (s *PluginBridgeService) streamFromWorker(nodeUUID string, client *cpgrpc.Client) {
	slog.Info("PluginBridgeService: 开始订阅 Worker 插件事件流", "nodeUUID", nodeUUID)

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		stream, err := client.Worker.StreamPluginEvents(s.ctx, &workerpb.StreamPluginEventsRequest{})
		if err != nil {
			slog.Warn("PluginBridgeService: 订阅 Worker 插件事件流失败", "nodeUUID", nodeUUID, "err", err)
			return
		}

		for {
			evt, err := stream.Recv()
			if err != nil {
				slog.Info("PluginBridgeService: Worker 插件事件流断开", "nodeUUID", nodeUUID, "err", err)
				break
			}
			s.handleEvent(nodeUUID, evt)
		}
	}
}

// handleEvent 维护连接状态并扇出事件给 SSE 订阅者。
func (s *PluginBridgeService) handleEvent(nodeUUID string, evt *workerpb.PluginEvent) {
	s.updateConnection(nodeUUID, evt)
	s.broadcast(PluginEvent{
		InstanceUUID: evt.InstanceUuid,
		Type:         evt.Type,
		Data:         evt.Data,
		Timestamp:    evt.Timestamp,
	})
}

// updateConnection 按 connected/disconnected 事件维护连接状态表。
// 实例名经 DB 解析一次后缓存进状态项，避免逐事件查库。
func (s *PluginBridgeService) updateConnection(nodeUUID string, evt *workerpb.PluginEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn := s.conns[evt.InstanceUuid]
	if conn == nil {
		conn = &PluginConnection{InstanceUUID: evt.InstanceUuid, NodeUUID: nodeUUID}
		// 解析实例 id/name（一次）；查不到也不致命，仅状态展示用。
		var instance model.Instance
		if err := s.db.Where("uuid = ?", evt.InstanceUuid).First(&instance).Error; err == nil {
			conn.InstanceID = instance.ID
			conn.InstanceName = instance.Name
		}
		s.conns[evt.InstanceUuid] = conn
	}
	conn.NodeUUID = nodeUUID
	conn.LastEventAt = evt.Timestamp
	switch evt.Type {
	case "connected", "hello":
		conn.Connected = true
	case "disconnected":
		conn.Connected = false
	}
}

// broadcast 扇出事件给所有订阅者（消费过慢则丢弃，不阻塞事件流）。
func (s *PluginBridgeService) broadcast(evt PluginEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

// Stop 停止服务并关闭所有订阅。
func (s *PluginBridgeService) Stop() {
	s.cancel()
	s.mu.Lock()
	for _, ch := range s.subs {
		close(ch)
	}
	s.subs = nil
	s.mu.Unlock()
}
