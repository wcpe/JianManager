package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret"

// signPluginToken 构造一个测试用插件桥 token。
func signPluginToken(t *testing.T, secret, instanceUUID, scope string, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"instanceId": instanceUUID,
		"scope":      scope,
		"iat":        now.Unix(),
		"exp":        now.Add(ttl).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	require.NoError(t, err)
	return s
}

// eventCollector 线程安全地收集冒泡事件，供断言用。
type eventCollector struct {
	mu     sync.Mutex
	events []PluginEvent
}

// PluginEvent 仅测试内部使用的事件聚合结构（生产代码已改用扁平回调）。
type PluginEvent struct {
	InstanceUUID string
	Type         string
	Data         string
	Timestamp    int64
}

func (c *eventCollector) handler(instanceUUID, eventType, data string, ts int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, PluginEvent{instanceUUID, eventType, data, ts})
}

func (c *eventCollector) snapshot() []PluginEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]PluginEvent, len(c.events))
	copy(out, c.events)
	return out
}

// waitForEvent 轮询等待某 type 的事件出现，避免对异步冒泡用固定 sleep。
func (c *eventCollector) waitForEvent(t *testing.T, eventType string) PluginEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range c.snapshot() {
			if e.Type == eventType {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("超时未收到事件 type=%s，已收到 %+v", eventType, c.snapshot())
	return PluginEvent{}
}

// dialBridge 用给定查询参数连接插件桥测试服务器。
func dialBridge(t *testing.T, srv *httptest.Server, query string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/plugin-bridge?" + query
	return websocket.DefaultDialer.Dial(wsURL, nil)
}

func newTestBridge(t *testing.T) (*PluginBridgeServer, *httptest.Server, *eventCollector) {
	t.Helper()
	bridge := NewPluginBridgeServer(testSecret)
	col := &eventCollector{}
	bridge.SetEventHandler(col.handler)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/plugin-bridge", bridge.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return bridge, srv, col
}

func TestPluginBridge_RejectsBadToken(t *testing.T) {
	_, srv, _ := newTestBridge(t)

	tests := []struct {
		name  string
		query string
	}{
		{"缺少 token", "instance=inst-1"},
		{"签名错误", "instance=inst-1&token=" + signPluginToken(t, "wrong-secret", "inst-1", pluginTokenScope, time.Minute)},
		{"scope 非 plugin-bridge", "instance=inst-1&token=" + signPluginToken(t, testSecret, "inst-1", "terminal", time.Minute)},
		{"已过期", "instance=inst-1&token=" + signPluginToken(t, testSecret, "inst-1", pluginTokenScope, -time.Minute)},
		{"instance 与 token 不一致", "instance=inst-OTHER&token=" + signPluginToken(t, testSecret, "inst-1", pluginTokenScope, time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, resp, err := dialBridge(t, srv, tt.query)
			if conn != nil {
				conn.Close()
			}
			assert.Error(t, err, "应拒绝握手")
			if resp != nil {
				assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			}
		})
	}
}

func TestPluginBridge_ConnectEmitsConnectedAndTracksSession(t *testing.T) {
	bridge, srv, col := newTestBridge(t)
	token := signPluginToken(t, testSecret, "inst-1", pluginTokenScope, time.Minute)

	conn, _, err := dialBridge(t, srv, "instance=inst-1&token="+token)
	require.NoError(t, err)
	defer conn.Close()

	evt := col.waitForEvent(t, "connected")
	assert.Equal(t, "inst-1", evt.InstanceUUID)

	assert.True(t, bridge.HasSession("inst-1"), "连接后应有会话")
	assert.ElementsMatch(t, []string{"inst-1"}, bridge.ConnectedInstances())
}

func TestPluginBridge_DisconnectEmitsDisconnected(t *testing.T) {
	bridge, srv, col := newTestBridge(t)
	token := signPluginToken(t, testSecret, "inst-1", pluginTokenScope, time.Minute)

	conn, _, err := dialBridge(t, srv, "instance=inst-1&token="+token)
	require.NoError(t, err)
	col.waitForEvent(t, "connected")

	conn.Close()
	evt := col.waitForEvent(t, "disconnected")
	assert.Equal(t, "inst-1", evt.InstanceUUID)

	// 断开后会话应被清理
	deadline := time.Now().Add(time.Second)
	for bridge.HasSession("inst-1") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.False(t, bridge.HasSession("inst-1"), "断开后会话应清理")
}

func TestPluginBridge_BubblesPlayerEvent(t *testing.T) {
	_, srv, col := newTestBridge(t)
	token := signPluginToken(t, testSecret, "inst-1", pluginTokenScope, time.Minute)

	conn, _, err := dialBridge(t, srv, "instance=inst-1&token="+token)
	require.NoError(t, err)
	defer conn.Close()
	col.waitForEvent(t, "connected")

	// 插件上报玩家加入事件
	require.NoError(t, conn.WriteJSON(map[string]interface{}{
		"type":  "event",
		"event": "player_join",
		"data":  map[string]string{"player": "Steve"},
		"ts":    1718870000,
	}))

	evt := col.waitForEvent(t, "player_join")
	assert.Equal(t, "inst-1", evt.InstanceUUID)
	assert.Equal(t, int64(1718870000), evt.Timestamp)
	// data 应透传 JSON
	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(evt.Data), &payload))
	assert.Equal(t, "Steve", payload["player"])
}

func TestPluginBridge_SendCommandDeliversToPlugin(t *testing.T) {
	bridge, srv, col := newTestBridge(t)
	token := signPluginToken(t, testSecret, "inst-1", pluginTokenScope, time.Minute)

	conn, _, err := dialBridge(t, srv, "instance=inst-1&token="+token)
	require.NoError(t, err)
	defer conn.Close()
	col.waitForEvent(t, "connected")

	// 等待会话登记完成
	deadline := time.Now().Add(time.Second)
	for !bridge.HasSession("inst-1") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	ok := bridge.SendCommand("inst-1", "cmd-1", "kick", json.RawMessage(`{"player":"Steve","reason":"afk"}`))
	assert.True(t, ok, "有会话时下发应成功")

	// 插件应收到指令
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var got pluginOutbound
	require.NoError(t, conn.ReadJSON(&got))
	assert.Equal(t, "command", got.Type)
	assert.Equal(t, "kick", got.Action)
	assert.Equal(t, "cmd-1", got.ID)
	var args map[string]string
	require.NoError(t, json.Unmarshal(got.Args, &args))
	assert.Equal(t, "Steve", args["player"])
}

func TestPluginBridge_SendCommandNoSession(t *testing.T) {
	bridge, _, _ := newTestBridge(t)
	ok := bridge.SendCommand("inst-absent", "cmd-1", "kick", nil)
	assert.False(t, ok, "无插件连入时应返回 false")
}

func TestPluginBridge_NewConnectionReplacesOld(t *testing.T) {
	bridge, srv, col := newTestBridge(t)
	token := signPluginToken(t, testSecret, "inst-1", pluginTokenScope, time.Minute)

	conn1, _, err := dialBridge(t, srv, "instance=inst-1&token="+token)
	require.NoError(t, err)
	defer conn1.Close()
	col.waitForEvent(t, "connected")

	// 第二个连接顶替第一个
	conn2, _, err := dialBridge(t, srv, "instance=inst-1&token="+token)
	require.NoError(t, err)
	defer conn2.Close()

	// 等到第二个会话生效（仍只有一个实例）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(bridge.ConnectedInstances()) == 1 && bridge.HasSession("inst-1") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.ElementsMatch(t, []string{"inst-1"}, bridge.ConnectedInstances())

	// 旧连应被服务端关闭：读取应最终报错
	_ = conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, readErr := conn1.ReadMessage()
	assert.Error(t, readErr, "旧连接应被顶替关闭")
}
