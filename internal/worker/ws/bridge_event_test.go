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

// TestBridge_PlayerEventFieldsBubbled 验证：探针 event 帧中的结构化玩家字段
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

// TestBridge_DomainDedupBubbled 验证通道信封字段透传（ADR-027ADR-106 后 JM 不再消费语义）：
// 探针 event 帧的 domain/dedupKey 经 Worker 原样冒泡到 ws.PluginEvent，Worker 只透传不解析；
// 监控/治理事件不带 domain → 这两字段为空（继续业务/监控分流）。
func TestBridge_DomainDedupBubbled(t *testing.T) {
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

	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	// 业务事件：带 domain=economy + dedupKey。
	bizData, _ := json.Marshal(map[string]any{"playerName": "alice"})
	require.NoError(t, conn.WriteJSON(bridgeMessage{
		Type: "event", Event: "economy_change", Data: bizData,
		Domain: "economy", DedupKey: "led-123",
	}))
	// 监控事件：无 domain/dedupKey（业务/监控分流）。
	monData, _ := json.Marshal(map[string]any{"playerName": "bob"})
	require.NoError(t, conn.WriteJSON(bridgeMessage{Type: "event", Event: "player_join", Data: monData}))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		var biz, mon bool
		for _, e := range events {
			if e.Type == "economy_change" {
				biz = true
			}
			if e.Type == "player_join" {
				mon = true
			}
		}
		return biz && mon
	}, 2*time.Second, 20*time.Millisecond, "应冒泡业务与监控事件")

	mu.Lock()
	defer mu.Unlock()
	var biz, mon *PluginEvent
	for i := range events {
		switch events[i].Type {
		case "economy_change":
			biz = &events[i]
		case "player_join":
			mon = &events[i]
		}
	}
	require.NotNil(t, biz)
	assert.Equal(t, "economy", biz.Domain)
	assert.Equal(t, "led-123", biz.DedupKey)

	require.NotNil(t, mon)
	assert.Empty(t, mon.Domain, "监控事件 domain 应空（业务/监控分流）")
	assert.Empty(t, mon.DedupKey)
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

// TestBridge_EventTimestampIsMilliseconds 锁定事件时间戳单位为毫秒。
//
// 背景：曾用 time.Now().Unix()（秒）填充 PluginEvent.Timestamp，而 CP 的
// business_event_cursor.OccurredAt 与 Loom 摄入侧都按**毫秒**解析，导致
// 1792957711 被折算成 1970-01-21，经济域日聚合整体落到 1970。
// 这里用「与当前时间差」而不是绝对值断言，避免测试依赖具体时钟。
func TestBridge_EventTimestampIsMilliseconds(t *testing.T) {
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

	conn := dialBridge(t, srv.URL, "inst-ms")
	defer conn.Close()

	var welcome bridgeMessage
	require.NoError(t, conn.ReadJSON(&welcome))

	before := time.Now().UnixMilli()
	data, _ := json.Marshal(map[string]any{"playerName": "carol"})
	require.NoError(t, conn.WriteJSON(bridgeMessage{Type: "event", Event: "player_join", Data: data}))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range events {
			if e.Type == "player_join" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, e := range events {
		if e.Type != "player_join" {
			continue
		}
		// 秒级时间戳约 1.8e9，毫秒级约 1.8e12；用 1e12 作为分界，容错窗口取 1 分钟。
		require.Greater(t, e.Timestamp, int64(1_000_000_000_000),
			"事件时间戳必须是毫秒（秒级会被下游折算成 1970）")
		require.InDelta(t, before, e.Timestamp, 60_000,
			"事件时间戳应与当前毫秒时钟接近")
		return
	}
	t.Fatal("未捕获到 player_join 事件")
}
