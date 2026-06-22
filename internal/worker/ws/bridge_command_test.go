package ws

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendCommandAndWait_RoundTrip 验证 FR-067 治理同步往返：Worker 下发 command 帧、
// 探针执行后回 command_result（携带相同 requestId），Worker 把结果同步返回给调用方。
func TestSendCommandAndWait_RoundTrip(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()

	// 消化 welcome。
	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	// 模拟探针：读到 command 后回一个匹配 requestId 的 command_result。
	go func() {
		var cmd map[string]any
		if err := conn.ReadJSON(&cmd); err != nil {
			return
		}
		reqID, _ := cmd["requestId"].(string)
		data, _ := json.Marshal(map[string]any{
			"requestId": reqID,
			"success":   true,
			"output":    "已踢出 alice",
		})
		_ = conn.WriteJSON(bridgeMessage{Type: "event", Event: "command_result", Data: data})
	}()

	// 等会话建立。
	require.Eventually(t, func() bool { return s.IsConnected("inst-1") }, time.Second, 10*time.Millisecond)

	frame := map[string]interface{}{"type": "command", "action": "kick", "target": "alice", "requestId": "req-1"}
	res, err := s.SendCommandAndWait("inst-1", "req-1", frame, 2*time.Second)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, "已踢出 alice", res.Output)
}

// TestSendCommandAndWait_NotConnected 实例无活动会话时立即返回错误（不阻塞到超时）。
func TestSendCommandAndWait_NotConnected(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)
	_, err := s.SendCommandAndWait("nope", "req-1", map[string]interface{}{"type": "command"}, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBridgeNotConnected)
}

// TestSendCommandAndWait_Timeout 探针不回 command_result 时按超时返回（不永久阻塞）。
func TestSendCommandAndWait_Timeout(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()
	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))
	// 探针读掉 command 但不回 result。
	go func() { var m map[string]any; _ = conn.ReadJSON(&m) }()

	require.Eventually(t, func() bool { return s.IsConnected("inst-1") }, time.Second, 10*time.Millisecond)

	start := time.Now()
	_, err := s.SendCommandAndWait("inst-1", "req-1", map[string]interface{}{"type": "command", "requestId": "req-1"}, 300*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBridgeCommandTimeout)
	assert.Less(t, time.Since(start), 2*time.Second) // 确实在超时附近返回，未永久阻塞
}

// 确保 command_result 也作为 PluginEvent 冒泡（观测用），不被 pending 路由吞掉。
func TestCommandResult_AlsoBubbles(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)
	got := make(chan PluginEvent, 8)
	s.SetEventHandler(func(e PluginEvent) { got <- e })
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()
	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	require.Eventually(t, func() bool { return s.IsConnected("inst-1") }, time.Second, 10*time.Millisecond)

	// 无人等待的 command_result（如重发/超时后到达）：仍应作为事件冒泡，不 panic。
	data, _ := json.Marshal(map[string]any{"requestId": "orphan", "success": true})
	require.NoError(t, conn.WriteJSON(bridgeMessage{Type: "event", Event: "command_result", Data: data}))

	require.Eventually(t, func() bool {
		for {
			select {
			case e := <-got:
				if e.Type == "command_result" {
					return true
				}
			default:
				return false
			}
		}
	}, 2*time.Second, 20*time.Millisecond)
}

var _ = websocket.TextMessage // keep gorilla import stable across edits
