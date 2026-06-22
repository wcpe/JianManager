package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestPlayerRoster_ApplyEvent 验证在线名册随事件演进（FR-066）：
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
