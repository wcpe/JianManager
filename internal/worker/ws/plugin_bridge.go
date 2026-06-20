package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

// pluginTokenScope 是插件桥 token 的 scope claim 固定值，用于区分终端 token，
// 避免某实例的终端 token 被误用于插件桥连接。CP 签发时写入此值。参见 ADR-012。
const pluginTokenScope = "plugin-bridge"

// PluginEventHandler 插件事件回调，由 grpc 层注入以把事件扇出到 StreamPluginEvents 订阅者。
// 用扁平标量参数（而非结构体）以便直接满足 grpc 层的 broker 接口，保持 ws 包对 proto 无依赖。
// eventType：connected / disconnected / player_join / player_quit / player_chat / server_status / hello / 其它；
// data 为事件载荷 JSON 原文；ts 为秒级时间戳。
type PluginEventHandler func(instanceUUID, eventType, data string, ts int64)

// pluginInbound 是插件 → Worker 的 WS 消息（JSON 行），见 ARCHITECTURE 6.2.1。
type pluginInbound struct {
	Type    string          `json:"type"`    // hello / event / pong / command_result
	Event   string          `json:"event"`   // type=event 时的事件名（player_join 等）
	ID      string          `json:"id"`      // command_result 回执对应的指令 id
	Data    json.RawMessage `json:"data"`    // 事件/回执载荷（透传 JSON）
	TS      int64           `json:"ts"`      // 插件侧时间戳（秒），可空
}

// pluginOutbound 是 Worker → 插件的 WS 消息（指令下发 / ping），见 ARCHITECTURE 6.2.1。
type pluginOutbound struct {
	Type   string          `json:"type"`             // command / ping
	ID     string          `json:"id,omitempty"`     // 指令 id（便于插件回执）
	Action string          `json:"action,omitempty"` // kick / ban / whitelist_add 等
	Args   json.RawMessage `json:"args,omitempty"`   // 指令参数 JSON
}

// pluginSession 单个插件连接会话。conn 的写入需经 writeMu 串行化
// （gorilla/websocket 不允许并发写同一连接）。
type pluginSession struct {
	instanceUUID string
	conn         *websocket.Conn
	writeMu      sync.Mutex
}

// writeJSON 串行化地向插件写一条消息。
func (ps *pluginSession) writeJSON(v interface{}) error {
	ps.writeMu.Lock()
	defer ps.writeMu.Unlock()
	return ps.conn.WriteJSON(v)
}

// PluginBridgeServer 平台插件桥 WebSocket 服务器（FR-103 / ADR-012）。
// 与终端 WS 并列、复用同一 WS 监听端口；插件经 /ws/plugin-bridge 连入，
// token 鉴权后建会话：上行事件经 onEvent 冒泡给 CP，下行指令经 SendCommand 写给插件。
type PluginBridgeServer struct {
	jwtSecret string
	upgrader  websocket.Upgrader

	mu sync.RWMutex
	// sessions：实例 UUID → 当前活动会话。同一实例同时仅一活动会话，新连顶替旧连。
	sessions map[string]*pluginSession

	onEvent PluginEventHandler
}

// NewPluginBridgeServer 创建插件桥服务器。jwtSecret 与终端 token 使用同一 CP 签名密钥。
func NewPluginBridgeServer(jwtSecret string) *PluginBridgeServer {
	return &PluginBridgeServer{
		jwtSecret: jwtSecret,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		sessions: make(map[string]*pluginSession),
	}
}

// SetEventHandler 注入事件回调（由 grpc 层调用）。
// 参数用 PluginEventHandler 的底层字面量类型，使本方法签名与 grpc 层 broker 接口逐字匹配
// （Go 接口方法匹配要求签名相同，命名类型与其底层字面量不视为同一签名）。
func (s *PluginBridgeServer) SetEventHandler(h func(instanceUUID, eventType, data string, ts int64)) {
	s.onEvent = h
}

// Handler 返回 /ws/plugin-bridge 的 HTTP handler。
// 握手参数：token（CP 签发的插件桥 JWT）+ instance（实例 UUID，需与 token 内一致）。
func (s *PluginBridgeServer) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			http.Error(w, "缺少 token", http.StatusUnauthorized)
			return
		}

		instanceUUID, ok := s.verifyToken(tokenStr, r.URL.Query().Get("instance"))
		if !ok {
			http.Error(w, "token 无效或与实例不匹配", http.StatusUnauthorized)
			return
		}

		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("插件桥 WebSocket 升级失败", "error", err)
			return
		}

		session := &pluginSession{instanceUUID: instanceUUID, conn: conn}
		s.addSession(session)
		slog.Info("插件已连接", "instanceId", instanceUUID, "remote", r.RemoteAddr)
		s.emit(instanceUUID, "connected", "{}", time.Now().Unix())

		go s.handleSession(session)
	}
}

// verifyToken 校验插件桥 token：签名有效、scope=plugin-bridge、instanceId 与握手参数一致。
// 返回 token 内的实例 UUID。queryInstance 为空时不强制比对（以 token 内实例为准）。
func (s *PluginBridgeServer) verifyToken(tokenStr, queryInstance string) (string, bool) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(s.jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return "", false
	}
	if scope, _ := claims["scope"].(string); scope != pluginTokenScope {
		return "", false
	}
	instanceUUID, _ := claims["instanceId"].(string)
	if instanceUUID == "" {
		return "", false
	}
	// 握手参数携带 instance 时必须与 token 内一致，防止用 A 实例 token 连 B 实例。
	if queryInstance != "" && queryInstance != instanceUUID {
		return "", false
	}
	return instanceUUID, true
}

// handleSession 读取插件上行消息并冒泡为事件，连接结束时清理会话并发 disconnected。
func (s *PluginBridgeServer) handleSession(session *pluginSession) {
	defer func() {
		s.removeSession(session)
		session.conn.Close()
		slog.Info("插件已断开", "instanceId", session.instanceUUID)
		s.emit(session.instanceUUID, "disconnected", "{}", time.Now().Unix())
	}()

	for {
		_, msgBytes, err := session.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("插件桥连接异常关闭", "instanceId", session.instanceUUID, "error", err)
			}
			return
		}

		var msg pluginInbound
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "pong", "command_result":
			// pong 用于保活探测；command_result 暂作日志（下行指令为 fire-and-forget），不冒泡。
			slog.Debug("插件回执", "instanceId", session.instanceUUID, "type", msg.Type, "id", msg.ID)
		case "hello":
			s.emit(session.instanceUUID, "hello", rawOrEmpty(msg.Data), tsOrNow(msg.TS))
		case "event":
			if msg.Event == "" {
				continue
			}
			s.emit(session.instanceUUID, msg.Event, rawOrEmpty(msg.Data), tsOrNow(msg.TS))
		}
	}
}

// SendCommand 向某实例当前连入的插件下发一条指令。实例无插件连入时返回 false。
// args 为指令参数的 JSON（可空）。
func (s *PluginBridgeServer) SendCommand(instanceUUID, id, action string, args json.RawMessage) bool {
	s.mu.RLock()
	session := s.sessions[instanceUUID]
	s.mu.RUnlock()
	if session == nil {
		return false
	}
	out := pluginOutbound{Type: "command", ID: id, Action: action, Args: args}
	if err := session.writeJSON(out); err != nil {
		slog.Warn("插件指令下发失败", "instanceId", instanceUUID, "action", action, "error", err)
		return false
	}
	return true
}

// HasSession 报告某实例是否有插件连入。
func (s *PluginBridgeServer) HasSession(instanceUUID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.sessions[instanceUUID]
	return ok
}

// ConnectedInstances 返回当前有插件连入的实例 UUID 列表。
func (s *PluginBridgeServer) ConnectedInstances() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.sessions))
	for uuid := range s.sessions {
		out = append(out, uuid)
	}
	return out
}

// addSession 登记会话；同一实例已有会话则关闭旧连（新连顶替）。
func (s *PluginBridgeServer) addSession(session *pluginSession) {
	s.mu.Lock()
	old := s.sessions[session.instanceUUID]
	s.sessions[session.instanceUUID] = session
	s.mu.Unlock()
	if old != nil && old != session {
		slog.Info("插件重复连接，顶替旧会话", "instanceId", session.instanceUUID)
		old.conn.Close()
	}
}

// removeSession 仅当 map 中仍是该会话时删除（避免顶替后误删新会话）。
func (s *PluginBridgeServer) removeSession(session *pluginSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[session.instanceUUID] == session {
		delete(s.sessions, session.instanceUUID)
	}
}

// emit 把事件冒泡给注入的回调（grpc 层）。回调未设时丢弃。
func (s *PluginBridgeServer) emit(instanceUUID, eventType, data string, ts int64) {
	if s.onEvent != nil {
		s.onEvent(instanceUUID, eventType, data, ts)
	}
}

// rawOrEmpty 把可空的 JSON 原文转为字符串，nil 时返回空对象。
func rawOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// tsOrNow 插件未带时间戳时用当前秒级时间。
func tsOrNow(ts int64) int64 {
	if ts > 0 {
		return ts
	}
	return time.Now().Unix()
}
