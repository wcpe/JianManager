package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 传输类型常量。
const (
	TransportStreamableHTTP = "streamable_http"
	TransportSSE            = "sse"
)

var (
	// ErrSessionLimitGlobal 全局会话数已达上限。
	ErrSessionLimitGlobal = errors.New("MCP 全局并发会话已达上限")
	// ErrSessionLimitToken 单 Token 会话数已达上限。
	ErrSessionLimitToken = errors.New("该 Token 的 MCP 并发会话已达上限")
	// ErrSessionNotFound 会话不存在或已关闭。
	ErrSessionNotFound = errors.New("MCP 会话不存在")
	// ErrSessionClosed 会话已关闭（踢线/超时）。
	ErrSessionClosed = errors.New("MCP 会话已关闭")
)

// Session 内存中的 MCP 会话。
type Session struct {
	ID              string
	TokenID         uint
	TokenName       string
	TokenPrefix     string
	ClientIP        string
	Transport       string
	ConnectedAt     time.Time
	LastActivityAt  time.Time
	LastTool        string
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration

	// Principal 鉴权主体（工具调用用）。
	Principal *service.AgentPrincipal

	// ctx/cancel 踢线或超时时取消进行中的 tool call。
	ctx    context.Context
	cancel context.CancelFunc

	// SSE 兼容：可选消息通道（nil 表示非 SSE 或已关闭）。
	sseMu   sync.Mutex
	sseCh   chan []byte
	sseOpen bool

	mu     sync.Mutex
	closed bool
}

// Snapshot 会话对外只读视图（管理员列表 / JSON）。
type Snapshot struct {
	ID              string    `json:"sessionId"`
	TokenID         uint      `json:"tokenId"`
	TokenName       string    `json:"tokenName"`
	TokenPrefix     string    `json:"tokenPrefix"`
	ClientIP        string    `json:"clientIP"`
	Transport       string    `json:"transport"`
	ConnectedAt     time.Time `json:"connectedAt"`
	LastActivityAt  time.Time `json:"lastActivityAt"`
	LastTool        string    `json:"lastTool,omitempty"`
	IdleTimeout     string    `json:"idleTimeout"`
	AbsoluteTimeout string    `json:"absoluteTimeout"`
}

// CreateParams 创建会话参数。
type CreateParams struct {
	Principal *service.AgentPrincipal
	ClientIP  string
	Transport string
}

// SessionManager 内存会话表 + 超时巡检。
type SessionManager struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*Session
	// perToken 计数（tokenID → 活跃会话数）。
	perToken map[uint]int

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// now 可注入以便单测控制时间。
	now func() time.Time
}

// NewSessionManager 创建管理器；调用 Start 启动超时清理。
func NewSessionManager(cfg Config) *SessionManager {
	cfg = cfg.Normalize()
	ctx, cancel := context.WithCancel(context.Background())
	return &SessionManager{
		cfg:      cfg,
		sessions: make(map[string]*Session),
		perToken: make(map[uint]int),
		ctx:      ctx,
		cancel:   cancel,
		now:      time.Now,
	}
}

// Start 启动空闲/绝对超时巡检 goroutine。
func (m *SessionManager) Start() {
	m.wg.Add(1)
	go m.reapLoop()
}

// Stop 停止巡检并关闭全部会话。
func (m *SessionManager) Stop() {
	m.cancel()
	m.wg.Wait()
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		if err := m.Kick(id, "服务关闭"); err != nil && !errors.Is(err, ErrSessionNotFound) {
			slog.Warn("服务停止时关闭 MCP 会话失败", "sessionId", id, "error", err)
		}
	}
}

// Create 校验并发上限后创建会话。
func (m *SessionManager) Create(p CreateParams) (*Session, error) {
	if p.Principal == nil {
		return nil, fmt.Errorf("缺少 Agent 主体")
	}
	transport := p.Transport
	if transport == "" {
		transport = TransportStreamableHTTP
	}
	if transport != TransportStreamableHTTP && transport != TransportSSE {
		return nil, fmt.Errorf("不支持的传输类型: %s", transport)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sessions) >= m.cfg.MaxGlobalSessions {
		return nil, ErrSessionLimitGlobal
	}
	if m.perToken[p.Principal.TokenID] >= m.cfg.MaxSessionsPerToken {
		return nil, ErrSessionLimitToken
	}

	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	now := m.now()
	sctx, scancel := context.WithCancel(m.ctx)
	s := &Session{
		ID:              id,
		TokenID:         p.Principal.TokenID,
		TokenName:       p.Principal.Name,
		TokenPrefix:     p.Principal.TokenPrefix,
		ClientIP:        p.ClientIP,
		Transport:       transport,
		ConnectedAt:     now,
		LastActivityAt:  now,
		IdleTimeout:     m.cfg.IdleTimeout,
		AbsoluteTimeout: m.cfg.AbsoluteTimeout,
		Principal:       p.Principal,
		ctx:             sctx,
		cancel:          scancel,
	}
	if transport == TransportSSE {
		s.sseCh = make(chan []byte, 32)
		s.sseOpen = true
	}
	m.sessions[id] = s
	m.perToken[p.Principal.TokenID]++
	slog.Info("MCP 会话已建立",
		"sessionId", id,
		"tokenId", p.Principal.TokenID,
		"tokenName", p.Principal.Name,
		"transport", transport,
		"clientIP", p.ClientIP,
	)
	return s, nil
}

// Get 按 id 取会话；已关闭或不存在返回 ErrSessionNotFound。
func (m *SessionManager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.IsClosed() {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

// Touch 刷新 lastActivityAt；可选记录最近 tool。
func (m *SessionManager) Touch(id, lastTool string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return ErrSessionNotFound
	}
	return s.Touch(m.now(), lastTool)
}

// Kick 强制关闭会话（管理员踢线 / 超时）。
func (m *SessionManager) Kick(id, reason string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	delete(m.sessions, id)
	if m.perToken[s.TokenID] > 0 {
		m.perToken[s.TokenID]--
		if m.perToken[s.TokenID] == 0 {
			delete(m.perToken, s.TokenID)
		}
	}
	m.mu.Unlock()

	s.close(reason)
	slog.Info("MCP 会话已关闭",
		"sessionId", id,
		"tokenId", s.TokenID,
		"reason", reason,
	)
	return nil
}

// List 返回全部会话快照（按 connectedAt 倒序）。
func (m *SessionManager) List() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ConnectedAt.After(out[j].ConnectedAt)
	})
	return out
}

// Count 当前活跃会话数。
func (m *SessionManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Config 返回当前配置副本。
func (m *SessionManager) Config() Config {
	return m.cfg
}

func (m *SessionManager) reapLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.reapOnce()
		}
	}
}

func (m *SessionManager) reapOnce() {
	now := m.now()
	type victim struct {
		id     string
		reason string
	}
	var victims []victim

	m.mu.Lock()
	for id, s := range m.sessions {
		s.mu.Lock()
		idleDeadline := s.LastActivityAt.Add(s.IdleTimeout)
		absDeadline := s.ConnectedAt.Add(s.AbsoluteTimeout)
		s.mu.Unlock()
		if now.After(absDeadline) {
			victims = append(victims, victim{id, "绝对超时"})
			continue
		}
		if now.After(idleDeadline) {
			victims = append(victims, victim{id, "空闲超时"})
		}
	}
	m.mu.Unlock()

	for _, v := range victims {
		if err := m.Kick(v.id, v.reason); err != nil && !errors.Is(err, ErrSessionNotFound) {
			slog.Warn("清理超时 MCP 会话失败", "sessionId", v.id, "error", err)
		}
	}
}

// ---- Session methods ----

// Context 返回会话 context（踢线后取消）。
func (s *Session) Context() context.Context {
	return s.ctx
}

// IsClosed 会话是否已关闭。
func (s *Session) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Touch 刷新活动时间。
func (s *Session) Touch(now time.Time, lastTool string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionClosed
	}
	s.LastActivityAt = now
	if lastTool != "" {
		s.LastTool = lastTool
	}
	return nil
}

// Snapshot 导出只读视图。
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		ID:              s.ID,
		TokenID:         s.TokenID,
		TokenName:       s.TokenName,
		TokenPrefix:     s.TokenPrefix,
		ClientIP:        s.ClientIP,
		Transport:       s.Transport,
		ConnectedAt:     s.ConnectedAt,
		LastActivityAt:  s.LastActivityAt,
		LastTool:        s.LastTool,
		IdleTimeout:     s.IdleTimeout.String(),
		AbsoluteTimeout: s.AbsoluteTimeout.String(),
	}
}

// SendSSE 向 SSE 客户端推送一帧（JSON 字节）；通道满时丢弃并返回错误。
func (s *Session) SendSSE(data []byte) error {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	if !s.sseOpen || s.sseCh == nil {
		return ErrSessionClosed
	}
	select {
	case s.sseCh <- data:
		return nil
	default:
		return fmt.Errorf("SSE 发送缓冲已满")
	}
}

// SSEChannel 返回只读 SSE 通道（可能为 nil）。
func (s *Session) SSEChannel() <-chan []byte {
	return s.sseCh
}

func (s *Session) close(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.sseMu.Lock()
	if s.sseOpen {
		s.sseOpen = false
		close(s.sseCh)
	}
	s.sseMu.Unlock()
	_ = reason // 日志由 Kick 统一打
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成 sessionId 失败: %w", err)
	}
	return "mcps_" + hex.EncodeToString(b), nil
}
