package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"github.com/wcpe/JianManager/internal/platform/onetimetoken"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// WS 保活心跳参数（FR-140 加固）：空闲终端（如 Paper 长时间无输出）经反代/LB 时，
// 会被中间层按空闲超时（常见 60s）断开。定时 ping 让连接保持有流量、pong 到达即续读超时，
// 据此既保活又能检测真正的死连。pingPeriod 须显著小于 pongWait 与常见中间层空闲超时。
const (
	// wsPongWait 读超时：超过此时长未收到对端任何帧（含 pong）判定连接已死。
	wsPongWait = 70 * time.Second
	// wsPingPeriod 主动 ping 间隔。
	wsPingPeriod = 30 * time.Second
	// wsWriteWait 单次控制帧（ping）写超时。
	wsWriteWait = 10 * time.Second
)

// StdinHandler stdin 输入处理函数。
type StdinHandler func(instanceID, data string)

// ResizeHandler 终端大小调整处理函数。
type ResizeHandler func(instanceID string, cols, rows int)
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
	Conn       *websocket.Conn
}

// TerminalServer 终端 WebSocket 服务器。
type TerminalServer struct {
	// jwtSecret 终端一次性 token 校验密钥，secretMu 保护：CP 经注册/心跳下发新值时
	// 由 SetJWTSecret 热更新（FR-275，见 ADR-061），与握手校验读并发安全。
	jwtSecret string
	secretMu  sync.RWMutex
	upgrader  websocket.Upgrader
	tokens    *onetimetoken.Store
	mu        sync.RWMutex
	sessions  map[string][]*TerminalSession
	buffers   map[string]*daemon.RingBuffer // per-instance 环形缓冲区
	onStdin   StdinHandler
	onResize  ResizeHandler

	// pingPeriod / pongWait 控制保活心跳（FR-140），默认取包级常量；
	// pingPeriod<=0 禁用主动 ping、pongWait<=0 不设读超时（仅测试用于模拟「无心跳」链路）。
	pingPeriod time.Duration
	pongWait   time.Duration
}

// NewTerminalServer 创建终端服务器。
func NewTerminalServer(jwtSecret string) *TerminalServer {
	return &TerminalServer{
		jwtSecret: jwtSecret,
		tokens:    onetimetoken.NewStore(),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		sessions:   make(map[string][]*TerminalSession),
		buffers:    make(map[string]*daemon.RingBuffer),
		pingPeriod: wsPingPeriod,
		pongWait:   wsPongWait,
	}
}

// SetJWTSecret 热更新 token 校验密钥（FR-275，见 ADR-061）：CP 经注册/心跳下发新密钥时调用。
// 仅影响后续握手；已建立的会话不受影响（握手只校验一次）。
func (s *TerminalServer) SetJWTSecret(secret string) {
	s.secretMu.Lock()
	s.jwtSecret = secret
	s.secretMu.Unlock()
}

// currentJWTSecret 并发安全读取当前校验密钥。
func (s *TerminalServer) currentJWTSecret() string {
	s.secretMu.RLock()
	defer s.secretMu.RUnlock()
	return s.jwtSecret
}

// SetStdinHandler 设置 stdin 输入处理函数。
func (s *TerminalServer) SetStdinHandler(handler StdinHandler) {
	s.onStdin = handler
}

// SetResizeHandler 设置终端大小调整处理函数。
func (s *TerminalServer) SetResizeHandler(handler ResizeHandler) {
	s.onResize = handler
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
			return []byte(s.currentJWTSecret()), nil
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
		expiresAt, ok := terminalWSExpiresAt(claims)
		if !ok {
			http.Error(w, "token 缺少过期时间", http.StatusUnauthorized)
			return
		}
		if !s.tokens.Consume(tokenStr, expiresAt) {
			http.Error(w, "token 已被使用", http.StatusUnauthorized)
			return
		}

		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			s.tokens.Release(tokenStr)
			slog.Error("WebSocket 升级失败", "error", err)
			return
		}

		session := &TerminalSession{
			InstanceID: instanceID,
			Permission: permission,
			Conn:       conn,
		}

		// 获取或创建该实例的共享环形缓冲区
		buf := s.getOrCreateBuffer(instanceID)

		s.addSession(instanceID, session)
		slog.Info("终端已连接", "instanceId", instanceID, "permission", permission, "remote", r.RemoteAddr)

		// 发送欢迎消息
		conn.WriteJSON(TerminalMessage{
			Type:       "stdout",
			InstanceID: instanceID,
			Data:       "已连接到实例 " + instanceID + "\r\n",
		})

		// 回放环形缓冲区中的历史输出
		if history := buf.ReadAll(); len(history) > 0 {
			conn.WriteJSON(TerminalMessage{
				Type:       "stdout",
				InstanceID: instanceID,
				Data:       string(history),
			})
		}

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

	conn := session.Conn
	// 保活心跳（FR-140）：pong 到达即续读超时，配合定时 ping 让空闲连接不被中间层判超时断开。
	if s.pongWait > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(s.pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(s.pongWait))
		})
	}
	if s.pingPeriod > 0 {
		stop := make(chan struct{})
		defer close(stop)
		// WriteControl 可与其它写并发（gorilla 保证），无需与 Broadcast 的写互斥。
		go s.pingLoop(conn, stop)
	}

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("终端连接异常关闭", "instanceId", session.InstanceID, "error", err)
			}
			return
		}
		// 收到任意数据帧也视为对端存活，续读超时。
		if s.pongWait > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.pongWait))
		}

		var msg TerminalMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "stdin":
			if session.Permission == "write" && s.onStdin != nil {
				s.onStdin(session.InstanceID, msg.Data)
			}
		case "resize":
			if s.onResize != nil {
				s.onResize(session.InstanceID, msg.Cols, msg.Rows)
			}
		}
	}
}

// pingLoop 定时向连接发送 WS ping 帧保活（FR-140），直到 stop 关闭或写失败。
func (s *TerminalServer) pingLoop(conn *websocket.Conn, stop <-chan struct{}) {
	ticker := time.NewTicker(s.pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
				return
			}
		}
	}
}

// Broadcast 向指定实例的所有观察者广播消息，同时写入共享环形缓冲区。
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
	}

	// 写入共享环形缓冲区（无论是否有连接）
	buf := s.getOrCreateBuffer(instanceID)
	buf.Write([]byte(data))
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

// getOrCreateBuffer 获取或创建实例的共享环形缓冲区。
func (s *TerminalServer) getOrCreateBuffer(instanceID string) *daemon.RingBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf, ok := s.buffers[instanceID]
	if !ok {
		buf = daemon.NewRingBuffer(64 * 1024)
		s.buffers[instanceID] = buf
	}
	return buf
}

// GetSessionCount 获取指定实例的终端会话数。
func (s *TerminalServer) GetSessionCount(instanceID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions[instanceID])
}

func terminalWSExpiresAt(claims jwt.MapClaims) (time.Time, bool) {
	expiresAt, err := claims.GetExpirationTime()
	if err != nil || expiresAt == nil {
		return time.Time{}, false
	}
	return expiresAt.Time, true
}
