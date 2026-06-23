package grpc

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 插件桥全状态查询的 Worker gRPC 侧测试（FR-076，见 ADR-016）。
// 复刻 ws 包的桥连入测试形态：起一个真实 /ws/plugin-bridge 服务端 + 模拟探针 WS 连接，
// 验证 QueryServerState 的下发-回执往返与各降级分支。

const bridgeTestSecret = "test-state-secret"

// dialStateBridge 以合法实例级 token 反向连入插件桥服务端（模拟探针侧连接）。
func dialStateBridge(t *testing.T, srvURL, instance string) *websocket.Conn {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"instanceId": instance,
		"scope":      ws.PluginBridgeScope,
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
	})
	signed, err := tok.SignedString([]byte(bridgeTestSecret))
	require.NoError(t, err)
	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/ws/plugin-bridge?token=" + signed + "&instance=" + instance
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	return conn
}

// newStateBridgeServer 起一个插件桥服务端 + httptest WS 服务，返回二者与一个连入的探针连接（已消化 welcome）。
func newStateBridgeServer(t *testing.T, instance string) (*ws.PluginBridgeServer, *httptest.Server, *websocket.Conn) {
	t.Helper()
	bridge := ws.NewPluginBridgeServer(bridgeTestSecret)
	srv := httptest.NewServer(bridge.Handler())
	t.Cleanup(srv.Close)

	conn := dialStateBridge(t, srv.URL, instance)
	t.Cleanup(func() { _ = conn.Close() })
	// 消化握手 welcome。
	var welcome map[string]any
	require.NoError(t, conn.ReadJSON(&welcome))
	require.Eventually(t, func() bool { return bridge.IsConnected(instance) }, time.Second, 10*time.Millisecond)
	return bridge, srv, conn
}

// TestQueryServerState_NoBridge 本节点未启用插件桥时返回 success=false + error（不 panic）。
func TestQueryServerState_NoBridge(t *testing.T) {
	s := &Server{} // bridge 为 nil
	resp, err := s.QueryServerState(context.Background(), &workerpb.QueryServerStateRequest{InstanceUuid: "inst-1"})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.False(t, resp.Connected)
	assert.NotEmpty(t, resp.Error)
}

// TestQueryServerState_NotConnected 探针未连入时连接状态降级（不下发指令、不阻塞到超时）。
func TestQueryServerState_NotConnected(t *testing.T) {
	bridge := ws.NewPluginBridgeServer(bridgeTestSecret)
	s := &Server{bridge: bridge}
	start := time.Now()
	resp, err := s.QueryServerState(context.Background(), &workerpb.QueryServerStateRequest{InstanceUuid: "nope"})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.False(t, resp.Connected)
	assert.Empty(t, resp.StateJson)
	assert.Less(t, time.Since(start), time.Second) // 未连入即返回，未走 5s 等待
}

// TestQueryServerState_RoundTrip 探针连入时下发 query_state、探针回带 state_json 的 command_result，
// Worker 把 output 透传到 state_json（不解析）。
func TestQueryServerState_RoundTrip(t *testing.T) {
	const instance = "inst-state-1"
	bridge, _, conn := newStateBridgeServer(t, instance)
	s := &Server{bridge: bridge}

	const stateJSON = `{"server":{"version":"git-Paper-123"},"jvm":{"threads":42},"classloader":{"loadedClasses":12345}}`

	// 模拟探针：读到 query_state 指令后回一个带相同 requestId、output=stateJSON 的 command_result。
	go func() {
		var cmd map[string]any
		if err := conn.ReadJSON(&cmd); err != nil {
			return
		}
		if cmd["action"] != "query_state" {
			return
		}
		reqID, _ := cmd["requestId"].(string)
		data, _ := json.Marshal(map[string]any{"requestId": reqID, "success": true, "output": stateJSON})
		_ = conn.WriteJSON(map[string]any{"type": "event", "event": "command_result", "data": json.RawMessage(data)})
	}()

	resp, err := s.QueryServerState(context.Background(), &workerpb.QueryServerStateRequest{InstanceUuid: instance})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.True(t, resp.Connected)
	assert.Equal(t, stateJSON, resp.StateJson)
	// state_json 必须是 Worker 原样透传（合法 JSON，未被改写）。
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.StateJson), &parsed))
	assert.Contains(t, parsed, "classloader")
}

// TestQueryServerState_Timeout 探针在线但不回 command_result 时降级为 N/A（connected=true、state_json 空 + error）。
func TestQueryServerState_Timeout(t *testing.T) {
	const instance = "inst-state-timeout"
	bridge, _, conn := newStateBridgeServer(t, instance)
	s := &Server{bridge: bridge}

	// 探针读掉指令但不回执（模拟采集卡住）。
	go func() { var m map[string]any; _ = conn.ReadJSON(&m) }()

	// 用一个短超时上下文的等价物：直接复用生产 5s 超时，但探针不回 → 走 Worker 侧超时分支。
	// 为不让单测真等 5s，这里只断言降级语义（connected=true + state 空），并允许至多 ~6s。
	done := make(chan *workerpb.QueryServerStateResponse, 1)
	go func() {
		resp, _ := s.QueryServerState(context.Background(), &workerpb.QueryServerStateRequest{InstanceUuid: instance})
		done <- resp
	}()
	select {
	case resp := <-done:
		assert.True(t, resp.Success)
		assert.True(t, resp.Connected) // 探针在线，仅本次采集超时
		assert.Empty(t, resp.StateJson)
		assert.NotEmpty(t, resp.Error)
	case <-time.After(7 * time.Second):
		t.Fatal("QueryServerState 未在超时窗口内返回（应由 Worker 侧 5s 兜底降级）")
	}
}
