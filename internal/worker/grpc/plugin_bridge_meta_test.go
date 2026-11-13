package grpc

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 桥能力接入的 Worker gRPC 侧用例。
//
// 复刻 plugin_bridge_test.go 的形态：起真实 /ws/plugin-bridge 服务端 + 模拟探针 WS 连接，
// 验证四条 RPC 的下发形态、回执映射与降级分支。

// newMetaBridge 起插件桥 + httptest 服务，返回桥服务端与一个已连入（消化 welcome）的探针连接。
func newMetaBridge(t *testing.T, instance string) (*Server, *websocket.Conn) {
	t.Helper()
	bridge := ws.NewPluginBridgeServer(bridgeTestSecret)
	srv := httptest.NewServer(bridge.Handler())
	t.Cleanup(srv.Close)

	conn := dialStateBridge(t, srv.URL, instance)
	t.Cleanup(func() { _ = conn.Close() })

	var welcome map[string]any
	require.NoError(t, conn.ReadJSON(&welcome))
	return &Server{bridge: bridge}, conn
}

// replyProbe 在后台读取一条命令帧并按 action 回执（output 由 respond 决定）。
//
// 返回一个 channel，收到后表示回执已写出。
func replyProbe(t *testing.T, conn *websocket.Conn, respond func(action string, cmd map[string]any) (string, bool)) <-chan string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		var cmd map[string]any
		if err := conn.ReadJSON(&cmd); err != nil {
			done <- ""
			return
		}
		action, _ := cmd["action"].(string)
		reqID, _ := cmd["requestId"].(string)
		output, success := respond(action, cmd)
		data, _ := json.Marshal(map[string]any{
			"requestId": reqID,
			"success":   success,
			"output":    output,
			"error":     "",
		})
		_ = conn.WriteJSON(map[string]any{"type": "event", "event": "command_result", "data": json.RawMessage(data)})
		done <- action
	}()
	return done
}

// TestQueryBridgeStatus_OK 桥状态查询下发 `core.bridge_status` 并透传快照。
func TestQueryBridgeStatus_OK(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	snapshot := `{"connected":true,"reconnects":2,"version":"2"}`
	var gotDomain string
	done := replyProbe(t, conn, func(action string, cmd map[string]any) (string, bool) {
		gotDomain, _ = cmd["domain"].(string)
		assert.Equal(t, "bridge_status", action)
		return snapshot, true
	})

	resp, err := s.QueryBridgeStatus(t.Context(), &workerpb.QueryBridgeStatusRequest{InstanceUuid: "inst-1"})
	require.NoError(t, err)
	<-done
	assert.Equal(t, "core", gotDomain, "桥能力命令必须走 core 治理域")
	assert.True(t, resp.Success)
	assert.True(t, resp.Connected)
	assert.JSONEq(t, snapshot, resp.StatusJson)
	assert.Positive(t, resp.FetchedAtMs)
}

// TestQueryBridgeStatus_ProbeOffline 探针未连：connected=false 且不下发命令（不白等到超时）。
func TestQueryBridgeStatus_ProbeOffline(t *testing.T) {
	bridge := ws.NewPluginBridgeServer(bridgeTestSecret)
	s := &Server{bridge: bridge}

	resp, err := s.QueryBridgeStatus(t.Context(), &workerpb.QueryBridgeStatusRequest{InstanceUuid: "nobody"})
	require.NoError(t, err)
	assert.True(t, resp.Success, "探针不在不是执行失败")
	assert.False(t, resp.Connected)
	assert.Empty(t, resp.StatusJson)
}

// TestBridgeRPC_BridgeDisabled 未启用插件桥：success=false + 明确 error。
func TestBridgeRPC_BridgeDisabled(t *testing.T) {
	s := &Server{} // bridge == nil
	resp, err := s.QueryBridgeStatus(t.Context(), &workerpb.QueryBridgeStatusRequest{InstanceUuid: "x"})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "未启用插件桥")

	sub, err := s.SetBridgeSubscription(t.Context(), &workerpb.SetBridgeSubscriptionRequest{InstanceUuid: "x"})
	require.NoError(t, err)
	assert.False(t, sub.Success)

	biz, err := s.QueryBusinessStatus(t.Context(), &workerpb.QueryBusinessStatusRequest{InstanceUuid: "x"})
	require.NoError(t, err)
	assert.False(t, biz.Success)

	reload, err := s.ReloadProbeConfig(t.Context(), &workerpb.ReloadProbeConfigRequest{InstanceUuid: "x"})
	require.NoError(t, err)
	assert.False(t, reload.Success)
}

// TestSetBridgeSubscription_TopicsPayload 订阅下发 `core.subscribe`，payload 是 {"topics":[...]}。
func TestSetBridgeSubscription_TopicsPayload(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	var payload map[string]any
	done := replyProbe(t, conn, func(action string, cmd map[string]any) (string, bool) {
		assert.Equal(t, "subscribe", action)
		raw, _ := cmd["payloadJson"].(string)
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		return `["activity.*"]`, true
	})

	resp, err := s.SetBridgeSubscription(t.Context(), &workerpb.SetBridgeSubscriptionRequest{
		InstanceUuid: "inst-1",
		Topics:       []string{"activity.*"},
	})
	require.NoError(t, err)
	<-done
	assert.True(t, resp.Success)
	assert.Equal(t, []any{"activity.*"}, payload["topics"])
	assert.Equal(t, []string{"activity.*"}, resp.AppliedTopics, "回传探针的生效集合")
}

// TestSetBridgeSubscription_DomainExpanded domains 展开成 `<domain>.*` 并入 topics。
func TestSetBridgeSubscription_DomainExpanded(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	var topics []any
	done := replyProbe(t, conn, func(_ string, cmd map[string]any) (string, bool) {
		raw, _ := cmd["payloadJson"].(string)
		var p map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &p))
		topics, _ = p["topics"].([]any)
		return `["activity.*","economy.*"]`, true
	})

	_, err := s.SetBridgeSubscription(t.Context(), &workerpb.SetBridgeSubscriptionRequest{
		InstanceUuid: "inst-1",
		Domains:      []string{"activity", "economy"},
	})
	require.NoError(t, err)
	<-done
	assert.Equal(t, []any{"activity.*", "economy.*"}, topics,
		"探针侧只有 topic 维度，domain 必须展开成通配")
}

// TestSetBridgeSubscription_EmptyMeansAll 空数组 = 订阅全部：payload 里 topics 为空列表，
// 且**不能**补裸 `*`（探针的匹配器只认 `expr` 或 `expr.*`，裸 * 匹配不到任何主题）。
func TestSetBridgeSubscription_EmptyMeansAll(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	var topics []any
	done := replyProbe(t, conn, func(_ string, cmd map[string]any) (string, bool) {
		raw, _ := cmd["payloadJson"].(string)
		var p map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &p))
		topics, _ = p["topics"].([]any)
		return `[]`, true
	})

	_, err := s.SetBridgeSubscription(t.Context(), &workerpb.SetBridgeSubscriptionRequest{InstanceUuid: "inst-1"})
	require.NoError(t, err)
	<-done
	assert.Empty(t, topics, "空数组下发空列表（探针据此 clear → 全量上报）")
}

// TestSetBridgeSubscription_ProbeOffline 探针未连：connected=false（CP 据此回 409）。
func TestSetBridgeSubscription_ProbeOffline(t *testing.T) {
	s := &Server{bridge: ws.NewPluginBridgeServer(bridgeTestSecret)}
	resp, err := s.SetBridgeSubscription(t.Context(), &workerpb.SetBridgeSubscriptionRequest{InstanceUuid: "x"})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.False(t, resp.Connected)
	assert.Contains(t, resp.Error, "探针未连接")
}

// TestQueryBusinessStatus_PayloadAndOutput 域状态查询：payload 带上可选 domain，output 原样透传。
func TestQueryBusinessStatus_PayloadAndOutput(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	var payload map[string]any
	statusJSON := `{"domains":{"activity":{"available":true}}}`
	done := replyProbe(t, conn, func(action string, cmd map[string]any) (string, bool) {
		assert.Equal(t, "business_status", action)
		raw, _ := cmd["payloadJson"].(string)
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		return statusJSON, true
	})

	resp, err := s.QueryBusinessStatus(t.Context(), &workerpb.QueryBusinessStatusRequest{
		InstanceUuid: "inst-1", Domain: "activity",
	})
	require.NoError(t, err)
	<-done
	assert.Equal(t, "activity", payload["domain"])
	assert.True(t, resp.Success)
	assert.JSONEq(t, statusJSON, resp.StatusJson)
}

// TestQueryBusinessStatus_ProbeOffline 探针未连：connected=false，不算失败。
func TestQueryBusinessStatus_ProbeOffline(t *testing.T) {
	s := &Server{bridge: ws.NewPluginBridgeServer(bridgeTestSecret)}
	resp, err := s.QueryBusinessStatus(t.Context(), &workerpb.QueryBusinessStatusRequest{InstanceUuid: "x"})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.False(t, resp.Connected)
}

// TestReloadProbeConfig_IndependentTimeout 重载超时**独立于**业务命令：
// 默认 10s、可覆盖，且下发的是 `core.config_reload` 而非业务域命令。
func TestReloadProbeConfig_IndependentTimeout(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	var payload map[string]any
	var gotDomain string
	done := replyProbe(t, conn, func(action string, cmd map[string]any) (string, bool) {
		assert.Equal(t, "config_reload", action)
		gotDomain, _ = cmd["domain"].(string)
		raw, _ := cmd["payloadJson"].(string)
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		return `{"reloaded":["plugins/a.yml"],"applied":true}`, true
	})

	resp, err := s.ReloadProbeConfig(t.Context(), &workerpb.ReloadProbeConfigRequest{InstanceUuid: "inst-1"})
	require.NoError(t, err)
	<-done
	assert.Equal(t, "core", gotDomain)
	assert.Equal(t, []any{}, payload["paths"], "空 paths = 全量重载")
	assert.True(t, resp.Success)
	assert.Contains(t, resp.ReceiptJson, "reloaded")
	assert.GreaterOrEqual(t, resp.DurationMs, int64(0))
}

// TestReloadProbeConfig_TimeoutDoesNotBlockBusiness 重载超时不影响业务命令：
// 一个不回执的重载会超时，随后的普通命令仍能正常往返。
//
// 这是 spec §3 验收 2「reload 超时不阻塞业务命令」的通道级证据：两者不共用超时预算，
// 也不共用等待队列。
func TestReloadProbeConfig_TimeoutDoesNotBlockBusiness(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")

	// 探针收到 reload 后**故意不回执**（模拟重载卡住）。必须并发读：读取要等命令到达。
	gotReload := make(chan string, 1)
	go func() {
		var cmd map[string]any
		if err := conn.ReadJSON(&cmd); err != nil {
			gotReload <- ""
			return
		}
		action, _ := cmd["action"].(string)
		gotReload <- action
	}()

	// 用一个很短的超时让它尽快返回（模拟"默认 10s 到了"）。
	start := time.Now()
	resp, err := s.ReloadProbeConfig(t.Context(), &workerpb.ReloadProbeConfigRequest{
		InstanceUuid: "inst-1", TimeoutMs: 200,
	})
	elapsed := time.Since(start)
	assert.Equal(t, "config_reload", <-gotReload)
	require.NoError(t, err)
	assert.False(t, resp.Success, "未回执的重载应超时失败")
	assert.Contains(t, resp.Error, "超时")
	assert.Less(t, elapsed, 2*time.Second, "超时必须按配置生效，不能等默认的 10s")

	// 紧接着一条业务命令仍能正常往返（探针回执）。
	go func() {
		var c map[string]any
		if err := conn.ReadJSON(&c); err != nil {
			return
		}
		reqID, _ := c["requestId"].(string)
		data, _ := json.Marshal(map[string]any{"requestId": reqID, "success": true, "output": "ok"})
		_ = conn.WriteJSON(map[string]any{"type": "event", "event": "command_result", "data": json.RawMessage(data)})
	}()
	biz, err := s.SendPluginCommand(t.Context(), &workerpb.SendPluginCommandRequest{
		InstanceUuid: "inst-1",
		Command:      &workerpb.PluginCommand{Action: "list", RequestId: "biz-1"},
		Wait:         true,
	})
	require.NoError(t, err)
	assert.True(t, biz.Success, "重载超时不得影响后续业务命令")
	assert.Equal(t, "ok", biz.Output)
}

// TestReloadTimeout_Clamp 超时解析：0 用默认、负值用默认、过大夹到上限。
func TestReloadTimeout_Clamp(t *testing.T) {
	assert.Equal(t, ProbeConfigReloadTimeout, reloadTimeout(0))
	assert.Equal(t, ProbeConfigReloadTimeout, reloadTimeout(-5))
	assert.Equal(t, 2*time.Second, reloadTimeout(2000))
	assert.Equal(t, 60*time.Second, reloadTimeout(10*60*1000), "过大的超时应夹到上限")
	assert.NotEqual(t, 5*time.Second, ProbeConfigReloadTimeout, "重载默认超时必须独立于业务命令的 5s")
}

// TestExpandSubscriptionTopics 订阅表达式合并：去重保序、domain 展开、已带通配的不重复展开。
func TestExpandSubscriptionTopics(t *testing.T) {
	cases := []struct {
		name    string
		topics  []string
		domains []string
		want    []string
	}{
		{"仅 topics", []string{"activity.spring", "activity.*"}, nil, []string{"activity.spring", "activity.*"}},
		{"仅 domains", nil, []string{"activity", "economy"}, []string{"activity.*", "economy.*"}},
		{"混合", []string{"a.b"}, []string{"c"}, []string{"a.b", "c.*"}},
		{"去重", []string{"a.*", "a.*"}, []string{"a"}, []string{"a.*"}},
		{"已带通配的 domain 不重复展开", nil, []string{"a.*"}, []string{"a.*"}},
		{"裸星号原样保留", nil, []string{"*"}, []string{"*"}},
		{"空与空白忽略", []string{"", "  "}, []string{"", " "}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandSubscriptionTopics(tc.topics, tc.domains)
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestParseAppliedTopics 生效集合解析：合法数组解析、空/非法返回 nil。
func TestParseAppliedTopics(t *testing.T) {
	assert.Equal(t, []string{"a.*", "b"}, parseAppliedTopics(`["a.*","b"]`))
	assert.Nil(t, parseAppliedTopics(""))
	assert.Nil(t, parseAppliedTopics("  "))
	assert.Nil(t, parseAppliedTopics("not-json"))
}

// TestQueryBridgeStatus_ProbeRejects 探针在线但拒绝查询（老版本无该路由）：connected=true + error。
func TestQueryBridgeStatus_ProbeRejects(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	done := replyProbe(t, conn, func(string, map[string]any) (string, bool) {
		return "", false
	})

	resp, err := s.QueryBridgeStatus(t.Context(), &workerpb.QueryBridgeStatusRequest{InstanceUuid: "inst-1"})
	require.NoError(t, err)
	<-done
	assert.False(t, resp.Success)
	assert.True(t, resp.Connected, "探针确实在线，只是拒绝了查询")
}

// TestQueryBridgeStatus_ActionNaming 四条命令的 action 名与 ServerProbe 路由常量逐字一致。
//
// 这是跨仓契约的对齐点：改名不会有编译错误，只会在运行期静默失败。
func TestQueryBridgeStatus_ActionNaming(t *testing.T) {
	assert.Equal(t, "bridge_status", probeActionBridgeStatus)
	assert.Equal(t, "business_status", probeActionBusinessStatus)
	assert.Equal(t, "subscribe", probeActionSubscribe)
	assert.Equal(t, "config_reload", probeActionConfigReload)
	assert.Equal(t, "core", probeCoreDomain)
}

// TestReloadProbeConfig_PathsForwarded paths 原样下发（需要定向重载）。
func TestReloadProbeConfig_PathsForwarded(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	var payload map[string]any
	done := replyProbe(t, conn, func(_ string, cmd map[string]any) (string, bool) {
		raw, _ := cmd["payloadJson"].(string)
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		return `{"applied":true}`, true
	})

	resp, err := s.ReloadProbeConfig(t.Context(), &workerpb.ReloadProbeConfigRequest{
		InstanceUuid: "inst-1",
		Paths:        []string{"plugins/a.yml", "plugins/b.yml"},
	})
	require.NoError(t, err)
	<-done
	assert.Equal(t, []any{"plugins/a.yml", "plugins/b.yml"}, payload["paths"])
	assert.True(t, resp.Success)
}

// TestReloadProbeConfig_FailureKeepsReceipt 探针失败时仍带 receipt_json
// （探针用 ofFailure 保住机器可读明细，丢了它 CP 就只能看到一句中文）。
func TestReloadProbeConfig_FailureKeepsReceipt(t *testing.T) {
	s, conn := newMetaBridge(t, "inst-1")
	done := replyProbe(t, conn, func(_ string, cmd map[string]any) (string, bool) {
		// 失败回执：success=false 但 output 仍有结构化明细。
		return `{"reloaded":[],"failed":[{"path":"a.yml","code":"TIMEOUT"}]}`, false
	})

	resp, err := s.ReloadProbeConfig(t.Context(), &workerpb.ReloadProbeConfigRequest{InstanceUuid: "inst-1"})
	require.NoError(t, err)
	<-done
	assert.False(t, resp.Success)
	assert.Contains(t, resp.ReceiptJson, "TIMEOUT", "失败回执的结构化明细必须透传")
}
