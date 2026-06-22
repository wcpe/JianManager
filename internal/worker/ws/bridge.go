package ws

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

// PluginBridgeScope 是插件桥 token 要求的 scope claim 值。
// CP 签发插件桥 token 时写入 scope=plugin-bridge，Worker 握手时据此与终端 token 区分，
// 拒绝拿终端 token（无此 scope）冒连插件桥（见 ADR-016）。
const PluginBridgeScope = "plugin-bridge"

// 插件桥心跳与超时参数。探针周期性发 ping，Worker 回 pong；
// 读超时（pongWait）内未再收到任何帧即判定断线、关闭会话并冒泡 disconnected。
// pongWait 取心跳周期的数倍，容忍单次心跳丢失而不误判断线。
const (
	bridgePongWait  = 90 * time.Second
	bridgeWriteWait = 10 * time.Second
)

// 校验错误，供握手与单测区分失败原因。
var (
	errBridgeNoToken      = errors.New("缺少 token")
	errBridgeBadToken     = errors.New("token 无效或已过期")
	errBridgeBadScope     = errors.New("token scope 非 plugin-bridge")
	errBridgeNoInstance   = errors.New("token 缺少 instanceId")
	errBridgeInstMismatch = errors.New("token instanceId 与 query instance 不一致")
)

// PluginEventKind 是插件桥冒泡事件的类型，与 proto PluginEvent.type 对应。
// 地基（FR-065）真实产生 connected/disconnected/heartbeat；其余子类型留 FR-066/067。
type PluginEventKind = string

const (
	PluginEventConnected    PluginEventKind = "connected"
	PluginEventDisconnected PluginEventKind = "disconnected"
	PluginEventHeartbeat    PluginEventKind = "heartbeat"
)

// PluginEvent 是插件桥向上（gRPC StreamPluginEvents）冒泡的一条事件。
// 平台无关：Worker 侧负责会话/握手/心跳与连接状态冒泡，并把探针 event 帧里的结构化
// 玩家字段解析出来（FR-066：join/quit/chat/cross_server），供 gRPC 侧填充 workerpb.PluginEvent。
// Worker 只解析、不消费语义（治理执行在 FR-067，跨服感知聚合在 CP）。
type PluginEvent struct {
	InstanceUUID string
	Type         PluginEventKind
	Timestamp    int64
	Platform     string // bukkit | bungee（来自探针 hello）
	Version      string // 探针版本（来自探针 hello）
	// 以下为探针业务事件（FR-066）携带的结构化字段；连接/心跳事件为空。
	PlayerName string // 玩家事件：玩家名
	PlayerUUID string // 玩家事件：玩家 UUID
	Message    string // chat 内容 / 事件描述
	Server     string // 子服名（玩家所在/事件发生）
	FromServer string // cross_server：来源子服
	ToServer   string // cross_server：目标子服
	Raw        string // 透传原始消息载荷（下游按需解析）
}

// PluginEventHandler 接收插件桥冒泡的事件，由 Worker 注入以桥接到 gRPC 事件流。
type PluginEventHandler func(PluginEvent)

// bridgeMessage 是探针 ↔ Worker 之间的 WS 文本帧（JSON）。
// type 区分语义：探针上行 hello/ping/event；Worker 下行 welcome/pong/command。
type bridgeMessage struct {
	Type     string          `json:"type"`
	Instance string          `json:"instance,omitempty"`
	Platform string          `json:"platform,omitempty"`
	Version  string          `json:"version,omitempty"`
	Event    string          `json:"event,omitempty"` // type=event 时的事件子类型
	Data     json.RawMessage `json:"data,omitempty"`  // 透传业务载荷
}

// bridgeEventData 是探针 event 帧 data 字段里的结构化玩家载荷（FR-066）。
// 字段命名与探针侧（ServerProbe BridgeClient.eventJson）约定一致；缺字段为零值。
type bridgeEventData struct {
	PlayerName string `json:"playerName"`
	PlayerUUID string `json:"playerUuid"`
	Message    string `json:"message"`
	Server     string `json:"server"`
	FromServer string `json:"fromServer"`
	ToServer   string `json:"toServer"`
}

// parseBridgeEventData 解析 event 帧的 data 载荷为结构化玩家字段。
// 空/非法 JSON 容错返回零值（不断连、不 panic）：Worker 是透传层，载荷异常由下游降级。
func parseBridgeEventData(data json.RawMessage) bridgeEventData {
	var d bridgeEventData
	if len(data) == 0 {
		return d
	}
	_ = json.Unmarshal(data, &d) // 失败即零值，容错
	return d
}

// PluginSession 是一个实例当前活动的探针会话。
type PluginSession struct {
	InstanceID string
	Platform   string
	Version    string
	Conn       *websocket.Conn
	writeMu    sync.Mutex // 串行化写，避免 ping/pong 与下发指令并发写同一连接
}

// writeJSON 串行化地向探针写一帧 JSON，带写超时。
func (s *PluginSession) writeJSON(v interface{}) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.Conn.SetWriteDeadline(time.Now().Add(bridgeWriteWait))
	return s.Conn.WriteJSON(v)
}

// PluginBridgeServer 是 ServerProbe 探针反向 WS 连入的服务端（端点 /ws/plugin-bridge，FR-065）。
// 维护「实例 UUID → 探针会话」表：同一实例同时仅一活动会话，新连顶替旧连（见 ADR-016）。
// token 校验复用 JIANMANAGER_JWT_SECRET，要求 scope=plugin-bridge 且 instanceId 与 query 一致。
type PluginBridgeServer struct {
	jwtSecret string
	upgrader  websocket.Upgrader
	mu        sync.Mutex
	sessions  map[string]*PluginSession
	onEvent   PluginEventHandler
}

// NewPluginBridgeServer 创建插件桥服务端。
func NewPluginBridgeServer(jwtSecret string) *PluginBridgeServer {
	return &PluginBridgeServer{
		jwtSecret: jwtSecret,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		sessions: make(map[string]*PluginSession),
	}
}

// SetEventHandler 注入事件回调，把 connected/disconnected/业务事件桥接到 gRPC StreamPluginEvents。
func (s *PluginBridgeServer) SetEventHandler(h PluginEventHandler) { s.onEvent = h }

// validateBridgeToken 校验插件桥握手参数：HS256 签名有效 + scope=plugin-bridge +
// token 内 instanceId 与 query instance 一致；通过则返回实例 UUID。
// 抽为纯函数便于单测（不依赖 HTTP/WS）。
func validateBridgeToken(secret, tokenStr, queryInstance string) (string, error) {
	if tokenStr == "" {
		return "", errBridgeNoToken
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return "", errBridgeBadToken
	}
	scope, _ := claims["scope"].(string)
	if scope != PluginBridgeScope {
		return "", errBridgeBadScope
	}
	instanceID, _ := claims["instanceId"].(string)
	if instanceID == "" {
		return "", errBridgeNoInstance
	}
	if queryInstance != "" && queryInstance != instanceID {
		return "", errBridgeInstMismatch
	}
	return instanceID, nil
}

// Handler 返回 /ws/plugin-bridge 的 HTTP handler。
func (s *PluginBridgeServer) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		instanceID, err := validateBridgeToken(s.jwtSecret, r.URL.Query().Get("token"), r.URL.Query().Get("instance"))
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, errBridgeNoInstance) || errors.Is(err, errBridgeInstMismatch) {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}

		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("插件桥 WebSocket 升级失败", "instanceId", instanceID, "error", err)
			return
		}

		session := &PluginSession{InstanceID: instanceID, Conn: conn}
		s.addSession(session) // 单活动会话：顶替并关闭旧连
		slog.Info("插件桥已连接", "instanceId", instanceID, "remote", r.RemoteAddr)

		go s.handleSession(session)
	}
}

// addSession 注册会话；同实例已有活动会话时顶替旧连（关闭旧 conn），保证单活动会话。
func (s *PluginBridgeServer) addSession(session *PluginSession) {
	s.mu.Lock()
	old := s.sessions[session.InstanceID]
	s.sessions[session.InstanceID] = session
	s.mu.Unlock()
	if old != nil {
		slog.Info("插件桥旧会话被新连顶替", "instanceId", session.InstanceID)
		_ = old.Conn.Close() // 旧 handleSession 读出错退出，自身负责清理与冒泡 disconnected
	}
}

// removeSession 仅当表中当前会话正是 session 时移除（避免误删已被顶替的旧会话指针）。
// 返回是否真正移除（true 表示本会话是当时的活动会话，需冒泡 disconnected）。
func (s *PluginBridgeServer) removeSession(session *PluginSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[session.InstanceID] == session {
		delete(s.sessions, session.InstanceID)
		return true
	}
	return false
}

// handleSession 处理一个探针会话的生命周期：连接冒泡 connected、心跳超时管理、
// 读循环（ping→pong、hello 记录平台/版本、event 冒泡），退出时冒泡 disconnected。
func (s *PluginBridgeServer) handleSession(session *PluginSession) {
	defer func() {
		_ = session.Conn.Close()
		if s.removeSession(session) {
			s.emit(PluginEvent{
				InstanceUUID: session.InstanceID,
				Type:         PluginEventDisconnected,
				Timestamp:    time.Now().Unix(),
				Platform:     session.Platform,
				Version:      session.Version,
			})
			slog.Info("插件桥已断开", "instanceId", session.InstanceID)
		}
	}()

	// 连接建立即冒泡 connected（platform/version 此时可能未知，hello 到达后业务事件会带上）。
	s.emit(PluginEvent{
		InstanceUUID: session.InstanceID,
		Type:         PluginEventConnected,
		Timestamp:    time.Now().Unix(),
	})
	// 回执 welcome，确认会话建立。
	_ = session.writeJSON(bridgeMessage{Type: "welcome", Instance: session.InstanceID})

	// 心跳超时：任意帧到达刷新读 deadline；超时未收到帧即读出错、退出循环。
	_ = session.Conn.SetReadDeadline(time.Now().Add(bridgePongWait))

	for {
		_, msgBytes, err := session.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("插件桥连接异常关闭", "instanceId", session.InstanceID, "error", err)
			}
			return
		}
		_ = session.Conn.SetReadDeadline(time.Now().Add(bridgePongWait))

		var msg bridgeMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			continue // 容忍非法帧，不断连
		}

		switch msg.Type {
		case "ping":
			_ = session.writeJSON(bridgeMessage{Type: "pong"})
			s.emit(PluginEvent{
				InstanceUUID: session.InstanceID,
				Type:         PluginEventHeartbeat,
				Timestamp:    time.Now().Unix(),
			})
		case "hello":
			// 记录探针自报的平台/版本，供后续事件与连接状态展示携带。
			session.Platform = msg.Platform
			session.Version = msg.Version
			slog.Info("插件桥握手 hello", "instanceId", session.InstanceID, "platform", msg.Platform, "version", msg.Version)
		case "event":
			// 业务事件冒泡（FR-066）：解析 data 里的结构化玩家字段填充事件，原始载荷一并透传（raw）。
			// Worker 只解析、不消费语义（跨服感知聚合在 CP，治理执行在 FR-067）。
			d := parseBridgeEventData(msg.Data)
			s.emit(PluginEvent{
				InstanceUUID: session.InstanceID,
				Type:         msg.Event,
				Timestamp:    time.Now().Unix(),
				Platform:     session.Platform,
				Version:      session.Version,
				PlayerName:   d.PlayerName,
				PlayerUUID:   d.PlayerUUID,
				Message:      d.Message,
				Server:       d.Server,
				FromServer:   d.FromServer,
				ToServer:     d.ToServer,
				Raw:          string(msgBytes),
			})
		}
	}
}

// emit 把事件交给注入的回调（若有），桥接到 gRPC 事件流。
func (s *PluginBridgeServer) emit(evt PluginEvent) {
	if s.onEvent != nil {
		s.onEvent(evt)
	}
}

// SendCommand 向指定实例的活动探针会话下发一帧指令（command）。
// 地基（FR-065）提供通道：CP 经 gRPC SendPluginCommand 调到此处；探针侧具体执行留 FR-067。
// 实例当前无活动会话时返回 false。
func (s *PluginBridgeServer) SendCommand(instanceID string, payload interface{}) (bool, error) {
	s.mu.Lock()
	session := s.sessions[instanceID]
	s.mu.Unlock()
	if session == nil {
		return false, nil
	}
	if err := session.writeJSON(payload); err != nil {
		return false, err
	}
	return true, nil
}

// IsConnected 返回指定实例当前是否有活动探针会话。
func (s *PluginBridgeServer) IsConnected(instanceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[instanceID] != nil
}

// SessionCount 返回当前活动探针会话总数（用于观测/测试）。
func (s *PluginBridgeServer) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}
