package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"github.com/wxys233/JianManager/internal/worker/daemon"
)

// TerminalMessage 终端消息。
type TerminalMessage struct {
	Type       string `json:"type"`
	InstanceID string `json:"instanceId,omitempty"`
	Data       string `json:"data,omitempty"`
	Cols       int    `json:"cols,omitempty"`
	Rows       int    `json:"rows,omitempty"`
	State      string `json:"state,omitempty"`
}

// TerminalSession 终端会话。
type TerminalSession struct {
	InstanceID string
	Permission string
	Output     *daemon.RingBuffer
	Conn       *websocket.Conn
}

// TerminalServer 终端 WebSocket 服务器。
type TerminalServer struct {
	jwtSecret string
	upgrader  websocket.Upgrader
	mu        sync.RWMutex
	sessions  map[string][]*TerminalSession
}

// NewTerminalServer 创建终端服务器。
func NewTerminalServer(jwtSecret string) *TerminalServer {
	return &TerminalServer{
		jwtSecret: jwtSecret,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		sessions: make(map[string][]*TerminalSession),
	}
}

// Handler 返回 HTTP handler。
func (s *TerminalServer) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			http.Error(w, "缺少 token", http.StatusUnauthorized)
			return
		}

		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(s.jwtSecret), nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "token 无效或已过期", http.StatusUnauthorized)
			return
		}

		instanceID, _ := claims["instanceId"].(string)
		permission, _ := claims["permission"].(string)

		if instanceID == "" {
			http.Error(w, "token 缺少 instanceId", http.StatusBadRequest)
			return
		}

		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("WebSocket 升级失败", "error", err)
			return
		}

		session := &TerminalSession{
			InstanceID: instanceID,
			Permission: permission,
			Output:     daemon.NewRingBuffer(64 * 1024),
			Conn:       conn,
		}

		s.addSession(instanceID, session)
		slog.Info("终端已连接", "instanceId", instanceID, "permission", permission, "remote", r.RemoteAddr)

		// 发送欢迎消息
		conn.WriteJSON(TerminalMessage{
			Type:       "stdout",
			InstanceID: instanceID,
			Data:       "已连接到实例 " + instanceID + "\r\n",
		})

		// 处理消息循环
		go s.handleSession(session)
	}
}

// handleSession 处理终端会话消息。
func (s *TerminalServer) handleSession(session *TerminalSession) {
	defer func() {
		s.removeSession(session.InstanceID, session)
		session.Conn.Close()
		slog.Info("终端已断开", "instanceId", session.InstanceID)
	}()

	for {
		_, msgBytes, err := session.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("终端连接异常关闭", "instanceId", session.InstanceID, "error", err)
			}
			return
		}

		var msg TerminalMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "stdin":
			if session.Permission == "write" {
				// TODO: 转发 stdin 到实例进程
				slog.Debug("终端输入", "instanceId", session.InstanceID, "data", msg.Data)
			}
		case "resize":
			// TODO: 调整 PTY 大小
			slog.Debug("终端调整大小", "instanceId", session.InstanceID, "cols", msg.Cols, "rows", msg.Rows)
		}
	}
}

// Broadcast 向指定实例的所有观察者广播消息。
func (s *TerminalServer) Broadcast(instanceID string, msgType, data string) {
	s.mu.RLock()
	sessions := s.sessions[instanceID]
	s.mu.RUnlock()

	msg := TerminalMessage{
		Type:       msgType,
		InstanceID: instanceID,
		Data:       data,
	}

	for _, session := range sessions {
		if err := session.Conn.WriteJSON(msg); err != nil {
			slog.Warn("广播消息失败", "instanceId", instanceID, "error", err)
		}
		// 同时写入环形缓冲区
		if session.Output != nil {
			session.Output.Write([]byte(data))
		}
	}
}

func (s *TerminalServer) addSession(instanceID string, session *TerminalSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[instanceID] = append(s.sessions[instanceID], session)
}

func (s *TerminalServer) removeSession(instanceID string, session *TerminalSession) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions := s.sessions[instanceID]
	for i, sess := range sessions {
		if sess == session {
			s.sessions[instanceID] = append(sessions[:i], sessions[i+1:]...)
			break
		}
	}
	if len(s.sessions[instanceID]) == 0 {
		delete(s.sessions, instanceID)
	}
}

// GetSessionCount 获取指定实例的终端会话数。
func (s *TerminalServer) GetSessionCount(instanceID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions[instanceID])
}
