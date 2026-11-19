package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestPlayerRoster_ApplyEvent 验证在线名册随事件演进：
// connected 重置该实例名册、player_join 加入、player_quit 移除、disconnected 清空该实例、
// cross_server 在目标子服记为在线。名册以「实例 UUID + 子服名」为活动维度（BC 跨服感知）。
func TestPlayerRoster_ApplyEvent(t *testing.T) {
	r := newPlayerRoster()

	// connected：清空该实例旧名册（探针重连后以新一轮 join 为准）。
	r.apply(&workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "connected"})
	assert.Empty(t, r.snapshot("inst-1"))

	// player_join：alice 进入 lobby 子服。
	r.apply(&workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "player_join", PlayerName: "alice", Server: "lobby"})
	r.apply(&workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "player_join", PlayerName: "bob", Server: "lobby"})
	snap := r.snapshot("inst-1")
	require.Len(t, snap, 2)
	names := map[string]string{}
	for _, p := range snap {
		names[p.Name] = p.Server
	}
	assert.Equal(t, "lobby", names["alice"])
	assert.Equal(t, "lobby", names["bob"])

	// cross_server：bob 从 lobby 切到 survival，名册里其所在子服应更新为 survival。
	r.apply(&workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "cross_server", PlayerName: "bob", FromServer: "lobby", ToServer: "survival"})
	for _, p := range r.snapshot("inst-1") {
		if p.Name == "bob" {
			assert.Equal(t, "survival", p.Server)
		}
	}

	// player_quit：alice 退出。
	r.apply(&workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "player_quit", PlayerName: "alice"})
	snap = r.snapshot("inst-1")
	require.Len(t, snap, 1)
	assert.Equal(t, "bob", snap[0].Name)

	// disconnected：探针断开，清空该实例名册（在线状态不可知，降级为空 + 由前端提示未连入）。
	r.apply(&workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "disconnected"})
	assert.Empty(t, r.snapshot("inst-1"))
}

// TestPlayerRoster_Isolation 验证不同实例名册互不干扰。
func TestPlayerRoster_Isolation(t *testing.T) {
	r := newPlayerRoster()
	r.apply(&workerpb.PluginEvent{InstanceUuid: "inst-1", Type: "player_join", PlayerName: "alice"})
	r.apply(&workerpb.PluginEvent{InstanceUuid: "inst-2", Type: "player_join", PlayerName: "carol"})
	assert.Len(t, r.snapshot("inst-1"), 1)
	assert.Len(t, r.snapshot("inst-2"), 1)
	assert.Empty(t, r.snapshot("inst-3"))
}

// TestPlayerEvent_FanoutFilter 验证 SSE 订阅按实例 UUID 过滤：
// 订阅 inst-1 只收到 inst-1 的事件，订阅空字符串收到全部。
func TestPlayerEvent_FanoutFilter(t *testing.T) {
	svc := NewPlayerEventService(nil, nil)

	chFiltered, unsub1 := svc.Subscribe("inst-1")
	defer unsub1()
	chAll, unsub2 := svc.Subscribe("")
	defer unsub2()

	svc.broadcast(PlayerEvent{InstanceUUID: "inst-1", Type: "player_join", PlayerName: "alice"})
	svc.broadcast(PlayerEvent{InstanceUUID: "inst-2", Type: "player_join", PlayerName: "bob"})

	// 过滤订阅只应收到 inst-1。
	got := drainPlayerEvents(chFiltered)
	require.Len(t, got, 1)
	assert.Equal(t, "inst-1", got[0].InstanceUUID)
	assert.Equal(t, "alice", got[0].PlayerName)

	// 全量订阅应收到两条。
	gotAll := drainPlayerEvents(chAll)
	assert.Len(t, gotAll, 2)
}

// drainPlayerEvents 非阻塞读光一个事件 channel 当前缓冲。
func drainPlayerEvents(ch <-chan PlayerEvent) []PlayerEvent {
	var out []PlayerEvent
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// TestManagedEventPayload_ExtractsNestedPayload 验证托管桥事件的载荷提取。
//
// 回归的是**真实缺陷**：探针把 config_applied/config_state 的结构化字段放在 `payload` 下
// （与 metric_snapshot 一致），早期直接把整帧当载荷解析，于是 configVersion 恒为 0、
// result 恒为空串——每条回执都被判成 failed，且失败原因永远显示"探针未给出失败原因"。
func TestManagedEventPayload_ExtractsNestedPayload(t *testing.T) {
	t.Run("config_applied 取 payload 段", func(t *testing.T) {
		frame := map[string]any{
			"type":     "event",
			"event":    "config_applied",
			"instance": "inst-1",
			"dedupKey": "cfg:7:applied",
			"payload": map[string]any{
				"configVersion": float64(7),
				"result":        "applied",
				"effective":     map[string]any{"collect-period-ticks": float64(40)},
			},
		}
		got, ok := managedEventPayload(frame)
		require.True(t, ok)
		assert.Equal(t, "applied", got["result"], "result 必须来自 payload 段而不是信封")
		assert.Equal(t, float64(7), got["configVersion"])
	})

	t.Run("config_state 取 payload 段", func(t *testing.T) {
		frame := map[string]any{
			"type":  "event",
			"event": "config_state",
			"payload": map[string]any{
				"configVersion":   float64(9),
				"mode":            "managed",
				"connection":      "connected",
				"cacheAgeSeconds": nil, // 从未应用过：null 而不是 0
			},
		}
		got, ok := managedEventPayload(frame)
		require.True(t, ok)
		assert.Equal(t, "connected", got["connection"])
		assert.Nil(t, got["cacheAgeSeconds"])
	})

	t.Run("回退 data 段", func(t *testing.T) {
		frame := map[string]any{
			"type": "event",
			"data": map[string]any{"configVersion": float64(3), "result": "rejected"},
		}
		got, ok := managedEventPayload(frame)
		require.True(t, ok)
		assert.Equal(t, "rejected", got["result"])
	})

	t.Run("平铺载荷也认", func(t *testing.T) {
		frame := map[string]any{"configVersion": float64(4), "result": "applied"}
		got, ok := managedEventPayload(frame)
		require.True(t, ok)
		assert.Equal(t, "applied", got["result"])
	})

	t.Run("无载荷段的帧被拒", func(t *testing.T) {
		frame := map[string]any{"type": "event", "event": "config_applied", "instance": "inst-1"}
		_, ok := managedEventPayload(frame)
		assert.False(t, ok, "既无 payload/data 也无协议字段时应拒收，而不是当成空回执")
	})

	t.Run("空 payload 段视为无载荷", func(t *testing.T) {
		frame := map[string]any{"type": "event", "payload": map[string]any{}}
		_, ok := managedEventPayload(frame)
		assert.False(t, ok)
	})
}

// TestManagedConfigApplied_AppliedAtBothForms 验证 config_applied 的 appliedAt 两种形态都收。
//
// 回归的是**真机发现的协议缺陷**：探针给的是 ISO-8601 字符串（ConfigPushOutcome.appliedAt），
// 而 CP 早期把它声明成 int64，于是每条回执都因
// `json: cannot unmarshal string into Go struct field ... AppliedAt of type int64`
// 整条反序列化失败被丢弃——目标全部走 ACK 超时，矩阵显示"探针没回执"，
// 而探针其实早就应用成功了。
func TestManagedConfigApplied_AppliedAtBothForms(t *testing.T) {
	t.Run("ISO-8601 字符串（当前探针）", func(t *testing.T) {
		// 与探针 BridgeFrames.configApplied 的真实载荷逐字一致（含 error:null）。
		raw := `{"configVersion":1,"result":"applied","effective":{"collect-period-ticks":40},` +
			`"selfCheck":{"collectorAlive":true},"error":null,"appliedAt":"2026-11-16T14:57:29Z"}`
		var applied ManagedConfigApplied
		require.NoError(t, json.Unmarshal([]byte(raw), &applied),
			"ISO 字符串形式的 appliedAt 必须能解析整条回执")
		assert.Equal(t, int64(1), applied.ConfigVersion)
		assert.Equal(t, "applied", applied.Result)

		got := applied.AppliedAtTime()
		require.False(t, got.IsZero(), "ISO 时间应能解析出来")
		assert.Equal(t, 2026, got.UTC().Year())
		assert.Equal(t, 11, int(got.UTC().Month()))
		assert.Equal(t, 14, got.UTC().Hour())
	})

	t.Run("epoch 毫秒数字（早期草案）", func(t *testing.T) {
		raw := `{"configVersion":2,"result":"applied","appliedAt":1794841049442}`
		var applied ManagedConfigApplied
		require.NoError(t, json.Unmarshal([]byte(raw), &applied))
		got := applied.AppliedAtTime()
		require.False(t, got.IsZero())
		assert.Equal(t, int64(1794841049442), got.UnixMilli())
	})

	t.Run("null 与缺失都当作未生效", func(t *testing.T) {
		for _, raw := range []string{
			`{"configVersion":3,"result":"failed","appliedAt":null}`,
			`{"configVersion":3,"result":"failed"}`,
		} {
			var applied ManagedConfigApplied
			require.NoError(t, json.Unmarshal([]byte(raw), &applied), raw)
			assert.True(t, applied.AppliedAtTime().IsZero(), "未生效时时间应为零值: %s", raw)
		}
	})

	t.Run("非法时间不拖垮回执", func(t *testing.T) {
		raw := `{"configVersion":4,"result":"applied","appliedAt":"不是时间"}`
		var applied ManagedConfigApplied
		require.NoError(t, json.Unmarshal([]byte(raw), &applied),
			"时间戳只是展示用，格式不认识也不该让整条回执作废")
		assert.True(t, applied.AppliedAtTime().IsZero())
		assert.Equal(t, "applied", applied.Result)
	})
}
