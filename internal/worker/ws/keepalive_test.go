// FR-140 保活加固：空闲终端不被中间层按空闲超时断开。
package ws

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialKeepaliveClient 以短有效期 token 连上 server 的 /ws/terminal，并读掉欢迎消息。
func dialKeepaliveClient(t *testing.T, server *TerminalServer, instanceID string) (*websocket.Conn, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/terminal", server.Handler())
	httpServer := httptest.NewServer(mux)

	now := time.Now()
	token := signToken(t, testSecret, jwt.MapClaims{
		"instanceId": instanceID,
		"permission": "write",
		"exp":        now.Add(30 * time.Second).Unix(),
		"iat":        now.Unix(),
	})
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/terminal"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token="+url.QueryEscape(token), nil)
	if resp != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err)
	// 读掉欢迎消息，之后进入空闲。
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	var welcome TerminalMessage
	require.NoError(t, conn.ReadJSON(&welcome))
	require.NoError(t, conn.SetReadDeadline(time.Time{}))
	return conn, func() { _ = conn.Close(); httpServer.Close() }
}

// TestWorkerWS_IdleClosesWithoutHeartbeat 复现失败机制：服务端设了读超时但不发 ping（模拟
// 中间层空闲超时且无保活），空闲客户端在 pongWait 后被断开。
func TestWorkerWS_IdleClosesWithoutHeartbeat(t *testing.T) {
	server := NewTerminalServer(testSecret)
	server.pongWait = 300 * time.Millisecond
	server.pingPeriod = 0 // 关闭主动 ping：模拟无心跳链路

	conn, cleanup := dialKeepaliveClient(t, server, "inst-idle-noping")
	defer cleanup()

	// 客户端持续读但保持空闲。因服务端不 ping，客户端不会自动 pong，
	// 服务端读超时到点后关闭连接 → 客户端读到关闭/错误。
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err, "无心跳时空闲连接应被服务端按读超时断开")
}

// TestWorkerWS_HeartbeatKeepsIdleAlive 回归：开启 ping 后，空闲客户端（自动 pong）
// 在远超 pongWait 的时长内保持连接，且随后仍能正常收数据。
func TestWorkerWS_HeartbeatKeepsIdleAlive(t *testing.T) {
	const instanceID = "inst-idle-heartbeat"
	server := NewTerminalServer(testSecret)
	server.pongWait = 300 * time.Millisecond
	server.pingPeriod = 100 * time.Millisecond // < pongWait：定时 ping 保活

	conn, cleanup := dialKeepaliveClient(t, server, instanceID)
	defer cleanup()

	// 后台持续读：处理服务端 ping 并自动回 pong，同时收集消息/错误。
	msgCh := make(chan TerminalMessage, 4)
	errCh := make(chan error, 1)
	go func() {
		for {
			var m TerminalMessage
			if err := conn.ReadJSON(&m); err != nil {
				errCh <- err
				return
			}
			msgCh <- m
		}
	}()

	// 空闲远超 pongWait（10×）：若无保活，服务端早已断开、errCh 会先响。
	select {
	case err := <-errCh:
		t.Fatalf("开启心跳后空闲连接不应断开，却收到错误: %v", err)
	case <-time.After(3 * time.Second):
	}

	// 连接仍活：服务端广播能送达客户端。
	server.Broadcast(instanceID, "stdout", "still-alive")
	select {
	case m := <-msgCh:
		assert.Equal(t, "still-alive", m.Data)
	case err := <-errCh:
		t.Fatalf("空闲保活后连接应仍可收数据，却收到错误: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("空闲保活后未在超时内收到广播消息")
	}
}
