package ws

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBridge_PlayerEventFieldsBubbled 验证 FR-066：探针 event 帧中的结构化玩家字段
// （playerName/playerUuid/message/server/fromServer/toServer）经 Worker 解析后冒泡到
// ws.PluginEvent，供 gRPC 侧填充 workerpb.PluginEvent。Worker 仅解析、不消费语义。
func TestBridge_PlayerEventFieldsBubbled(t *testing.T) {
	s := NewPluginBridgeServer(testSecret)

	var mu sync.Mutex
	var events []PluginEvent
	s.SetEventHandler(func(e PluginEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	conn := dialBridge(t, srv.URL, "inst-1")
	defer conn.Close()

	// 先读 welcome 消化掉。
	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	// 探针上报一个 player_join 业务事件（结构化字段在 data 内）。
	joinData, _ := json.Marshal(map[string]any{
		"playerName": "alice",
		"playerUuid": "uuid-alice",
		"server":     "lobby",
	})
	require.NoError(t, conn.WriteJSON(bridgeMessage{Type: "event", Event: "player_join", Data: joinData}))

	// 探针上报一个 cross_server 路由事件。
	crossData, _ := json.Marshal(map[string]any{
		"playerName": "bob",
		"fromServer": "lobby",
		"toServer":   "survival",
	})
	require.NoError(t, conn.WriteJSON(bridgeMessage{Type: "event", Event: "cross_server", Data: crossData}))

	// 轮询等待两条业务事件冒泡。
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		var join, cross bool
		for _, e := range events {
			if e.Type == "player_join" {
				join = true
			}
			if e.Type == "cross_server" {
				cross = true
			}
		}
		return join && cross
	}, 2*time.Second, 20*time.Millisecond, "应冒泡 player_join 与 cross_server 事件")

	mu.Lock()
	defer mu.Unlock()
	var join, cross *PluginEvent
	for i := range events {
		switch events[i].Type {
		case "player_join":
			join = &events[i]
		case "cross_server":
			cross = &events[i]
		}
	}
	require.NotNil(t, join)
	assert.Equal(t, "alice", join.PlayerName)
	assert.Equal(t, "uuid-alice", join.PlayerUUID)
	assert.Equal(t, "lobby", join.Server)

	require.NotNil(t, cross)
	assert.Equal(t, "bob", cross.PlayerName)
	assert.Equal(t, "lobby", cross.FromServer)
	assert.Equal(t, "survival", cross.ToServer)
}

// TestParseBridgeEventData 直接验证 event 帧载荷解析的纯函数行为（含缺字段/空载荷容错）。
func TestParseBridgeEventData(t *testing.T) {
	full := json.RawMessage(`{"playerName":"alice","playerUuid":"u1","message":"hi","server":"lobby","fromServer":"a","toServer":"b"}`)
	got := parseBridgeEventData(full)
	assert.Equal(t, "alice", got.PlayerName)
	assert.Equal(t, "u1", got.PlayerUUID)
	assert.Equal(t, "hi", got.Message)
	assert.Equal(t, "lobby", got.Server)
	assert.Equal(t, "a", got.FromServer)
	assert.Equal(t, "b", got.ToServer)

	// 空/非法载荷不 panic，返回零值。
	assert.Equal(t, bridgeEventData{}, parseBridgeEventData(nil))
	assert.Equal(t, bridgeEventData{}, parseBridgeEventData(json.RawMessage(`not-json`)))
}
