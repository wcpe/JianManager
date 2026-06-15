package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/golang-jwt/jwt/v5"

	"github.com/wxys233/JianManager/internal/worker/daemon"
)

// TerminalMessage 终端消息。
type TerminalMessage struct {
	Type       string `json:"type"`                  // stdin, stdout, stderr, state, resize
	InstanceID string `json:"instanceId,omitempty"`
	Data       string `json:"data,omitempty"`
	Cols       int    `json:"cols,omitempty"`
	Rows       int    `json:"rows,omitempty"`
	State      string `json:"state,omitempty"`
}

// TerminalSession 终端会话。
type TerminalSession struct {
	InstanceID string
	Permission string // read, write
	Output     *daemon.RingBuffer
}

// TerminalServer 终端 WebSocket 服务器。
type TerminalServer struct {
	jwtSecret string
	mu        sync.RWMutex
	sessions  map[string]*TerminalSession // instanceId → session
}

// NewTerminalServer 创建终端服务器。
func NewTerminalServer(jwtSecret string) *TerminalServer {
	return &TerminalServer{
		jwtSecret: jwtSecret,
		sessions:  make(map[string]*TerminalSession),
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

		// 验证 token
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

		slog.Info("终端连接请求", "instanceId", instanceID, "permission", permission, "remote", r.RemoteAddr)

		// TODO: 升级为 WebSocket 连接
		// 当前返回连接信息，实际 WebSocket 需要 gorilla/websocket 或 nhooyr.io/websocket
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":     "connected",
			"instanceId": instanceID,
			"permission": permission,
			"message":    "WebSocket 升级待实现",
		})
	}
}

// RegisterSession 注册终端会话。
func (s *TerminalServer) RegisterSession(instanceID, permission string, buf *daemon.RingBuffer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[instanceID] = &TerminalSession{
		InstanceID: instanceID,
		Permission: permission,
		Output:     buf,
	}
}

// RemoveSession 移除终端会话。
func (s *TerminalServer) RemoveSession(instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, instanceID)
}

// GetSession 获取终端会话。
func (s *TerminalServer) GetSession(instanceID string) *TerminalSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.sessions[instanceID]
}
