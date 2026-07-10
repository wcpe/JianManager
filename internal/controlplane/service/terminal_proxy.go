package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"github.com/wcpe/JianManager/internal/platform/onetimetoken"
)

// TerminalProxyCodeWorkerTokenRejected 是「Worker 拒绝终端令牌」的诊断码（FR-276，见 ADR-061）。
// 前端据此展示「该节点 WS 令牌密钥与平台不一致」的定向诊断，而非裸「连接已断开」。
const TerminalProxyCodeWorkerTokenRejected = "WORKER_TOKEN_REJECTED"

// WS 保活心跳参数（FR-140 加固）：给浏览器侧与 Worker 侧连接都装 ping/pong，
// 让空闲终端不被 CP 与浏览器之间的中间层（反代/LB）按空闲超时断开。参数含义同 worker 侧。
const (
	terminalProxyPongWait   = 70 * time.Second
	terminalProxyPingPeriod = 30 * time.Second
	terminalProxyWriteWait  = 10 * time.Second
)

// terminalProxyStateMessage CP→浏览器的终端代理错误状态消息。
// Code 供前端定向识别（空 = 一般性失败，如网络不可达）；Data 为人话诊断。
type terminalProxyStateMessage struct {
	Type  string `json:"type"`
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
	Data  string `json:"data"`
}

// writeTerminalProxyState 向浏览器连接发送一条错误状态消息（JSON 序列化，转义安全）。
func writeTerminalProxyState(conn *websocket.Conn, code, data string) {
	msg, err := json.Marshal(terminalProxyStateMessage{Type: "state", State: "error", Code: code, Data: data})
	if err != nil {
		return
	}
	_ = conn.WriteMessage(websocket.TextMessage, msg)
}

// TerminalProxy WebSocket 终端代理。
// 浏览器 → CP WebSocket → Worker WebSocket，双向桥接。
type TerminalProxy struct {
	jwtSecret string
	terminal  *TerminalService
	upgrader  websocket.Upgrader
	tokens    *onetimetoken.Store

	// pingPeriod / pongWait 控制保活心跳（FR-140），默认取包级常量；
	// pingPeriod<=0 禁用主动 ping、pongWait<=0 不设读超时（仅测试用于模拟「无心跳」链路）。
	pingPeriod time.Duration
	pongWait   time.Duration
}

// NewTerminalProxy 创建终端代理。
func NewTerminalProxy(jwtSecret string, terminal *TerminalService) *TerminalProxy {
	return &TerminalProxy{
		jwtSecret: jwtSecret,
		terminal:  terminal,
		tokens:    onetimetoken.NewStore(),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		pingPeriod: terminalProxyPingPeriod,
		pongWait:   terminalProxyPongWait,
	}
}

// Handler 返回 HTTP handler，挂载到 /ws/terminal。
func (p *TerminalProxy) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. 验证 JWT token
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}

		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			return []byte(p.jwtSecret), nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, "invalid claims", http.StatusUnauthorized)
			return
		}

		instanceUUID, _ := claims["instanceId"].(string)
		permission, _ := claims["permission"].(string)
		if instanceUUID == "" {
			http.Error(w, "missing instanceId", http.StatusBadRequest)
			return
		}
		expiresAt, ok := terminalTokenExpiresAt(claims)
		if !ok {
			http.Error(w, "invalid token expiration", http.StatusUnauthorized)
			return
		}

		// 2. 查找 Worker WS 地址
		workerWSURL, err := p.terminal.GetWorkerAddr(instanceUUID)
		if err != nil {
			slog.Error("查找 Worker 地址失败", "instanceUUID", instanceUUID, "error", err)
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		if !p.tokens.Consume(tokenStr, expiresAt) {
			http.Error(w, "token already used", http.StatusUnauthorized)
			return
		}

		// 3. 升级浏览器连接
		browserConn, err := p.upgrader.Upgrade(w, r, nil)
		if err != nil {
			p.tokens.Release(tokenStr)
			return
		}
		defer browserConn.Close()

		// 4. 连接 Worker WS（携带同样的 token）
		workerURL := fmt.Sprintf("%s?token=%s", workerWSURL, tokenStr)
		workerConn, dialResp, err := websocket.DefaultDialer.Dial(workerURL, nil)
		if err != nil {
			p.tokens.Release(tokenStr)
			// Worker 以 401/403 拒绝握手 = 令牌被 Worker 主动拒绝（非网络故障）：给出定向诊断
			//（FR-276，见 ADR-061）。常见根因：该节点 WS 令牌密钥与平台不一致（旧 Worker 未升级 /
			// 手动配置漂移）。日志与消息均不含 token（workerWSURL 无 query）。
			if dialResp != nil {
				defer dialResp.Body.Close()
				if dialResp.StatusCode == http.StatusUnauthorized || dialResp.StatusCode == http.StatusForbidden {
					slog.Error("Worker 拒绝终端令牌（疑似节点 WS 令牌密钥与平台不一致）",
						"url", workerWSURL, "status", dialResp.StatusCode)
					writeTerminalProxyState(browserConn, TerminalProxyCodeWorkerTokenRejected,
						fmt.Sprintf("终端令牌被 Worker 拒绝（HTTP %d）：该节点的 WS 令牌密钥与平台不一致。新版 Worker 会在注册时自动获取密钥，请确认节点已升级并重启；手动部署请核对 worker.yml 的 jwt_secret 是否与平台一致。", dialResp.StatusCode))
					return
				}
			}
			slog.Error("连接 Worker WS 失败", "url", workerWSURL, "error", err)
			writeTerminalProxyState(browserConn, "", "连接 Worker 失败: "+err.Error())
			return
		}
		defer workerConn.Close()

		slog.Info("终端代理已建立", "instanceUUID", instanceUUID, "permission", permission)

		// 保活心跳（FR-140）：两侧连接都装 pong 续读超时 + 定时 ping。浏览器侧 ping 让
		// 「浏览器↔CP」这一跳（可能经反代/LB）保持有流量；Worker 侧 ping 保活「CP↔Worker」跳。
		p.setupKeepalive(browserConn)
		p.setupKeepalive(workerConn)

		// 5. 双向桥接
		var wg sync.WaitGroup
		wg.Add(2)

		// browser → worker（stdin、resize）
		go func() {
			defer wg.Done()
			defer workerConn.Close()
			for {
				msgType, msg, err := browserConn.ReadMessage()
				if err != nil {
					return
				}
				p.extendReadDeadline(browserConn)
				if err := workerConn.WriteMessage(msgType, msg); err != nil {
					return
				}
			}
		}()

		// worker → browser（stdout、stderr、state）
		go func() {
			defer wg.Done()
			defer browserConn.Close()
			for {
				msgType, msg, err := workerConn.ReadMessage()
				if err != nil {
					if err != io.EOF {
						slog.Debug("Worker WS 断开", "error", err)
					}
					return
				}
				p.extendReadDeadline(workerConn)
				if err := browserConn.WriteMessage(msgType, msg); err != nil {
					return
				}
			}
		}()

		// 心跳 ping：WriteControl 可与桥接的写并发（gorilla 保证），无需额外互斥。
		if p.pingPeriod > 0 {
			stop := make(chan struct{})
			go pingConn(browserConn, p.pingPeriod, stop)
			go pingConn(workerConn, p.pingPeriod, stop)
			defer close(stop)
		}

		wg.Wait()
		slog.Info("终端代理已关闭", "instanceUUID", instanceUUID)
	}
}

// setupKeepalive 为连接装读超时与 pong 处理器（FR-140）：收到 pong 即续读超时。
func (p *TerminalProxy) setupKeepalive(conn *websocket.Conn) {
	if p.pongWait <= 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(p.pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(p.pongWait))
	})
}

// extendReadDeadline 收到任意数据帧后续读超时（对端存活）。
func (p *TerminalProxy) extendReadDeadline(conn *websocket.Conn) {
	if p.pongWait <= 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(p.pongWait))
}

// pingConn 定时向连接发送 WS ping 帧保活（FR-140），直到 stop 关闭或写失败。
// WriteControl 与其它写并发安全（gorilla 保证）。
func pingConn(conn *websocket.Conn, period time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(terminalProxyWriteWait)); err != nil {
				return
			}
		}
	}
}

func terminalTokenExpiresAt(claims jwt.MapClaims) (time.Time, bool) {
	expiresAt, err := claims.GetExpirationTime()
	if err != nil || expiresAt == nil {
		return time.Time{}, false
	}
	return expiresAt.Time, true
}
