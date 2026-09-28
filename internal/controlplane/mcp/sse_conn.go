package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
	// ErrSSEConnNotFound SSE 连接不存在或已注销。
	ErrSSEConnNotFound = errors.New("MCP SSE 连接不存在")
	// ErrSSEConnClosed SSE 连接已关闭。
	ErrSSEConnClosed = errors.New("MCP SSE 连接已关闭")
)

// SSEConn 是 SSE 兼容路径的**传输连接**，不是协议会话（ADR-096 决策 5）。
//
// Streamable HTTP 路径已无状态：它不建会话、不下发也不读 Mcp-Session-Id。
// 连接态只服务于 SSE 传输自身的需要——`/message` 必须把工具结果回推到 `/sse`
// 那条已建立的 HTTP 响应流上，因此需要「连接 id → 回推通道」的登记。
// 它不承载空闲/绝对超时、并发上限、能力快照等协议会话语义。
type SSEConn struct {
	// ID 连接标识（mcps_<32hex>），经 /sse 的 endpoint 事件下发给客户端。
	ID string
	// Principal 建连时的鉴权主体；/message 的归属校验需要（防止跨 Token 回推）。
	Principal *service.AgentPrincipal
	// ClientIP 建连客户端 IP。
	ClientIP string
	// ConnectedAt 建连时间。
	ConnectedAt time.Time

	// ctx/cancel 连接关闭（Close/Unregister/Stop）时取消。
	//
	// 它是 SSE 路径进行中 tool call 的生命周期上界：HandleSSEMessage 用
	// context.AfterFunc 把它并入工具调用的 ctx，连接被踢/关闭时工具随之中止——
	// 结果要回推到这条流上，连接没了继续跑只是空转，还可能留下半完成的写操作。
	// Streamable HTTP 路径无连接，工具调用只受请求 ctx 约束。
	ctx    context.Context
	cancel context.CancelFunc

	// SSE 回推通道：sseOpen 为 false 表示已关闭（通道已关闭）。
	sseMu   sync.Mutex
	sseCh   chan []byte
	sseOpen bool
}

// SSEConnRegistry SSE 传输连接登记表（仅 SSE 兼容路径使用）。
type SSEConnRegistry struct {
	mu    sync.Mutex
	conns map[string]*SSEConn
	// ctx 为全部连接的父 context，Stop 时统一取消。
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSSEConnRegistry 创建 SSE 连接登记表。
func NewSSEConnRegistry() *SSEConnRegistry {
	ctx, cancel := context.WithCancel(context.Background())
	return &SSEConnRegistry{
		conns:  make(map[string]*SSEConn),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Register 登记一条 SSE 连接并返回它。
//
// 不再有并发上限（ADR-096 决策 3）：连接数由客户端实际建连数决定，
// 连接断开即注销；故本方法不会因配额拒绝。
// p 须非 nil——调用方（/sse）已在 nil 时回 401。
func (r *SSEConnRegistry) Register(p *service.AgentPrincipal, clientIP string) *SSEConn {
	ctx, cancel := context.WithCancel(r.ctx)
	conn := &SSEConn{
		ID:          newSSEConnID(),
		Principal:   p,
		ClientIP:    clientIP,
		ConnectedAt: time.Now(),
		ctx:         ctx,
		cancel:      cancel,
		sseCh:       make(chan []byte, 32),
		sseOpen:     true,
	}
	r.mu.Lock()
	r.conns[conn.ID] = conn
	r.mu.Unlock()
	fields := []any{"connId", conn.ID, "clientIP", clientIP}
	if p != nil {
		fields = append(fields, "tokenId", p.TokenID, "tokenName", p.Name)
	}
	slog.Info("MCP SSE 连接已建立", fields...)
	return conn
}

// Get 按 id 取连接；不存在返回 false。
func (r *SSEConnRegistry) Get(id string) (*SSEConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	conn, ok := r.conns[id]
	return conn, ok
}

// Unregister 注销连接并关闭其回推通道；重复调用无害。
func (r *SSEConnRegistry) Unregister(id string) {
	r.mu.Lock()
	conn, ok := r.conns[id]
	delete(r.conns, id)
	r.mu.Unlock()
	if !ok {
		return
	}
	conn.Close()
	slog.Info("MCP SSE 连接已关闭", "connId", id)
}

// Stop 进程关闭时注销并关闭全部 SSE 连接。
func (r *SSEConnRegistry) Stop() {
	// 先取消父 context：全部连接 ctx 随之 done，/sse 处理循环得以退出。
	r.cancel()
	r.mu.Lock()
	ids := make([]string, 0, len(r.conns))
	for id := range r.conns {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.Unregister(id)
	}
}

// Context 返回连接 context（连接关闭时取消）。
func (c *SSEConn) Context() context.Context {
	return c.ctx
}

// SendSSE 向 SSE 客户端推送一帧（JSON 字节）；通道满时丢弃并返回错误。
func (c *SSEConn) SendSSE(data []byte) error {
	c.sseMu.Lock()
	defer c.sseMu.Unlock()
	if !c.sseOpen || c.sseCh == nil {
		return ErrSSEConnClosed
	}
	select {
	case c.sseCh <- data:
		return nil
	default:
		return fmt.Errorf("SSE 发送缓冲已满")
	}
}

// SSEChannel 返回只读回推通道（连接关闭后通道被关闭）。
func (c *SSEConn) SSEChannel() <-chan []byte {
	return c.sseCh
}

// Close 关闭连接：取消 context 并关闭回推通道；重复调用无害。
func (c *SSEConn) Close() {
	c.cancel()
	c.sseMu.Lock()
	defer c.sseMu.Unlock()
	if !c.sseOpen {
		return
	}
	c.sseOpen = false
	close(c.sseCh)
}

// newSSEConnID 生成连接 id，保持既有形态 mcps_<32hex>（/message?sessionId= 继续可用）。
func newSSEConnID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 熵源不可用属理论情形（crypto/rand 在支持的平台上不会失败）：
		// 退化为纳秒时间戳，保证 SSE 兼容路径仍能建连而不是整条路径不可用。
		return "mcps_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "mcps_" + hex.EncodeToString(b)
}
