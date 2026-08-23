package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/internal/platform/onetimetoken"
	"github.com/wcpe/JianManager/proto/workerpb"
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
		slog.Warn("序列化终端代理状态消息失败", "error", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		slog.Debug("写入终端代理状态消息失败", "error", err)
	}
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

	// workerClients 按节点取 WorkerServiceClient（FR-281 M2，见 ADR-066）。
	// 由 ClientPool 适配注入；终端只经 gRPC 反向隧道桥接，无可用隧道即提示重试。
	workerClients func(nodeUUID string) (workerpb.WorkerServiceClient, bool)
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
		if instanceUUID == "" {
			http.Error(w, "missing instanceId", http.StatusBadRequest)
			return
		}
		expiresAt, ok := terminalTokenExpiresAt(claims)
		if !ok {
			http.Error(w, "invalid token expiration", http.StatusUnauthorized)
			return
		}

		// 2. 查找实例所在节点，由反向隧道承载后续终端会话。
		nodeUUID, err := p.terminal.GetWorkerSession(instanceUUID)
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

		// 4. 经 gRPC TerminalSession 反向隧道桥接，令牌校验方仍是 Worker。
		if p.workerClients != nil {
			if worker, ok := p.workerClients(nodeUUID); ok {
				if p.bridgeViaGRPC(browserConn, worker, tokenStr, instanceUUID) {
					return
				}
			}
		}
		p.tokens.Release(tokenStr)
		writeTerminalProxyState(browserConn, "", "节点反向隧道不可用，请等待节点重新连接后重试")
	}
}

// SetWorkerClients 注入按节点取 WorkerServiceClient 的来源（main 装配自 ClientPool，FR-281 M2）。
func (p *TerminalProxy) SetWorkerClients(get func(nodeUUID string) (workerpb.WorkerServiceClient, bool)) {
	p.workerClients = get
}

// bridgeViaGRPC 经 gRPC TerminalSession 双向流桥接浏览器终端。
// 返回 true 表示会话已由本路径处理（含已向浏览器发出诊断的失败）；
// 返回 false 表示反向隧道不可用，由调用方保留一次性令牌并提示重试。
func (p *TerminalProxy) bridgeViaGRPC(browserConn *websocket.Conn, worker workerpb.WorkerServiceClient, tokenStr, instanceUUID string) bool {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := worker.TerminalSession(ctx)
	if err != nil {
		return false
	}
	if err := stream.Send(&workerpb.TerminalFrame{
		Kind: &workerpb.TerminalFrame_Open{Open: &workerpb.TerminalOpen{Token: tokenStr}},
	}); err != nil {
		return false
	}

	// 等 Worker 的就绪 ack：确定性区分就绪、令牌被拒和隧道不可用。
	first, err := stream.Recv()
	if err != nil {
		st, _ := status.FromError(err)
		switch st.Code() {
		case codes.Unimplemented:
			return false
		case codes.PermissionDenied:
			// 令牌被 Worker 拒绝：FR-276 定向诊断在 gRPC 桥路同语义成立（见 ADR-061）。
			slog.Error("Worker 拒绝终端令牌（疑似节点 WS 令牌密钥与平台不一致）",
				"instanceUUID", instanceUUID, "detail", st.Message())
			writeTerminalProxyState(browserConn, TerminalProxyCodeWorkerTokenRejected,
				fmt.Sprintf("终端令牌被 Worker 拒绝：该节点的 WS 令牌密钥与平台不一致。新版 Worker 会在注册时自动获取密钥，请确认节点已升级并重启；手动部署请核对 worker.yml 的 jwt_secret 是否与平台一致。（%s）", st.Message()))
			return true
		default:
			slog.Error("gRPC 终端桥建立失败", "instanceUUID", instanceUUID, "error", err)
			writeTerminalProxyState(browserConn, "", "连接 Worker 终端失败: "+st.Message())
			return true
		}
	}
	if first.GetOpen() == nil {
		// 协议期望首帧为就绪 ack；容错：若对端直接发数据帧则透传后继续。
		if f := first.GetFrame(); f != nil {
			if err := browserConn.WriteMessage(int(f.MsgType), f.Payload); err != nil {
				slog.Debug("透传终端首帧失败", "instanceUUID", instanceUUID, "error", err)
				return true
			}
		}
	}

	slog.Info("终端代理已建立（gRPC 桥）", "instanceUUID", instanceUUID)
	p.setupKeepalive(browserConn)

	var wg sync.WaitGroup
	wg.Add(2)

	// browser → gRPC（stdin、resize）
	go func() {
		defer wg.Done()
		defer cancel() // 浏览器断开 → 取消流，另一侧泵随之退出
		for {
			msgType, msg, err := browserConn.ReadMessage()
			if err != nil {
				return
			}
			p.extendReadDeadline(browserConn)
			if err := stream.Send(&workerpb.TerminalFrame{
				Kind: &workerpb.TerminalFrame_Frame{Frame: &workerpb.TerminalWSFrame{MsgType: int32(msgType), Payload: msg}},
			}); err != nil {
				return
			}
		}
	}()

	// gRPC → browser（stdout、stderr、state）
	go func() {
		defer wg.Done()
		defer browserConn.Close() // 流断开 → 关浏览器连接，另一侧泵随之退出
		for {
			in, err := stream.Recv()
			if err != nil {
				return
			}
			f := in.GetFrame()
			if f == nil {
				continue
			}
			if err := browserConn.WriteMessage(int(f.MsgType), f.Payload); err != nil {
				return
			}
		}
	}()

	// 浏览器侧保活 ping（FR-140）；Worker 侧保活由 gRPC/隧道传输层承担，无需应用层 ping。
	if p.pingPeriod > 0 {
		stop := make(chan struct{})
		go pingConn(browserConn, p.pingPeriod, stop)
		defer close(stop)
	}

	wg.Wait()
	slog.Info("终端代理已关闭（gRPC 桥）", "instanceUUID", instanceUUID)
	return true
}

// setupKeepalive 为连接装读超时与 pong 处理器（FR-140）：收到 pong 即续读超时。
func (p *TerminalProxy) setupKeepalive(conn *websocket.Conn) {
	if p.pongWait <= 0 {
		return
	}
	if err := conn.SetReadDeadline(time.Now().Add(p.pongWait)); err != nil {
		slog.Debug("设置终端读取超时失败", "error", err)
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(p.pongWait))
	})
}

// extendReadDeadline 收到任意数据帧后续读超时（对端存活）。
func (p *TerminalProxy) extendReadDeadline(conn *websocket.Conn) {
	if p.pongWait <= 0 {
		return
	}
	if err := conn.SetReadDeadline(time.Now().Add(p.pongWait)); err != nil {
		slog.Debug("续期终端读取超时失败", "error", err)
	}
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
