// FR-140 保活加固：CP 终端代理给两侧连接装 ping/pong，空闲终端不被中间层断开。
package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	wgrpc "github.com/wcpe/JianManager/internal/worker/grpc"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newProxyKeepaliveFixture 起 Worker 终端服务 + CP 代理，返回代理与签好的终端 token。
func newProxyKeepaliveFixture(t *testing.T) (*TerminalProxy, *TerminalToken) {
	t.Helper()
	const secret = "keepalive-proxy-secret"
	db, instanceID := newTerminalTestDB(t, startWorkerTerminalWS(t, secret))
	svc := NewTerminalService(db, secret, "ws://fallback.invalid")
	proxy := NewTerminalProxy(secret, svc)
	wsrv := &wgrpc.Server{}
	wsrv.SetTerminalWSAddr(startWorkerTerminalWS(t, secret))
	client := startTerminalWorkerGRPC(t, wsrv)
	proxy.SetWorkerClients(func(string) (workerpb.WorkerServiceClient, bool) { return client, true })
	cpHTTP := httptest.NewServer(proxy.Handler())
	t.Cleanup(cpHTTP.Close)

	token, err := svc.IssueToken(instanceID, "write", mustHost(t, cpHTTP.URL), false)
	require.NoError(t, err)
	return proxy, token
}

// TestTerminalProxy_IdleClosesWithoutHeartbeat 复现失败机制：代理设了读超时但不 ping，
// 空闲浏览器连接在 pongWait 后被代理断开。
func TestTerminalProxy_IdleClosesWithoutHeartbeat(t *testing.T) {
	proxy, token := newProxyKeepaliveFixture(t)
	proxy.pongWait = 300 * time.Millisecond
	proxy.pingPeriod = 0 // 关闭主动 ping：模拟无心跳链路

	conn := dialTerminalProxy(t, token.WSURL, token.Token)
	defer conn.Close()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	// 先读掉欢迎消息，之后空闲。
	var welcome map[string]any
	require.NoError(t, conn.ReadJSON(&welcome))

	// 空闲：代理不 ping → 浏览器侧读超时到点，代理关闭桥接 → 客户端读到错误。
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err, "无心跳时空闲连接应被代理按读超时断开")
}

// TestTerminalProxy_HeartbeatKeepsIdleAlive 回归：开启 ping 后空闲连接保活，且随后仍能收数据。
func TestTerminalProxy_HeartbeatKeepsIdleAlive(t *testing.T) {
	proxy, token := newProxyKeepaliveFixture(t)
	proxy.pongWait = 300 * time.Millisecond
	proxy.pingPeriod = 100 * time.Millisecond // < pongWait：定时 ping 保活

	conn := dialTerminalProxy(t, token.WSURL, token.Token)
	defer conn.Close()

	msgCh := make(chan map[string]any, 8)
	errCh := make(chan error, 1)
	go func() {
		for {
			var m map[string]any
			if err := conn.ReadJSON(&m); err != nil {
				errCh <- err
				return
			}
			msgCh <- m
		}
	}()
	// 先消费欢迎消息。
	select {
	case <-msgCh:
	case err := <-errCh:
		t.Fatalf("应先收到欢迎消息，却出错: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到欢迎消息")
	}

	// 空闲远超 pongWait（10×）：若无保活，代理早已断开。
	select {
	case err := <-errCh:
		t.Fatalf("开启心跳后空闲连接不应断开，却收到错误: %v", err)
	case <-time.After(3 * time.Second):
	}
}
