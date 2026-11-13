package ws

import (
	"encoding/json"
	"testing"
)

// 用真实帧形态验证解析（与探针 MetricSnapshotFrame.of 的输出逐字段对齐）。
//
// 帧是从探针源码里抄出来的真实结构：外层 type/event/instance/dedupKey，
// 载荷在 `payload` 下（不是 command_result 那种 `data`），metrics 是一层对象。
func TestParseMetricSnapshotRealShape(t *testing.T) {
	const raw = `{"type":"event","event":"metric_snapshot","instance":"170d8d98-c236-420c-a990-f8d572f8ca16","dedupKey":"metric:170d8d98:1793575000","payload":{"ts":1793575000,"sequence":7,"metrics":{"tps":19.8,"msptAvg":0.0234,"playersOnline":0,"heapUsed":1191000000,"heapMax":4294967296,"threads":212,"cpuLoad":0.35,"uptimeSeconds":3600.5,"gcCountTotal":42,"gcTimeMillis":1250,"worlds":{"world":{"loadedChunks":1200,"entities":85,"tileEntities":310}}}}}`

	var msg bridgeMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("帧本身不合法: %v", err)
	}
	if msg.Event != metricSnapshotEventName {
		t.Fatalf("event 解析错: %q", msg.Event)
	}

	frame, ok := parseMetricSnapshot(&PluginSession{InstanceID: "fallback"}, msg, []byte(raw))
	if !ok {
		t.Fatal("parseMetricSnapshot 应成功")
	}
	if frame.InstanceUUID != "170d8d98-c236-420c-a990-f8d572f8ca16" {
		t.Fatalf("instance 错: %s", frame.InstanceUUID)
	}
	if frame.TS != 1793575000 || frame.Sequence != 7 {
		t.Fatalf("ts/seq 错: %d/%d", frame.TS, frame.Sequence)
	}
	if len(frame.Metrics) == 0 {
		t.Fatal("metrics 不应为空")
	}
}

// TestParseMetricSnapshotFallbackInstance 验证帧未自报实例时回退到会话实例。
func TestParseMetricSnapshotFallbackInstance(t *testing.T) {
	const raw = `{"type":"event","event":"metric_snapshot","payload":{"ts":1,"sequence":1,"metrics":{}}}`
	var msg bridgeMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	frame, ok := parseMetricSnapshot(&PluginSession{InstanceID: "sess-uuid"}, msg, []byte(raw))
	if !ok {
		t.Fatal("应解析成功")
	}
	if frame.InstanceUUID != "sess-uuid" {
		t.Fatalf("应回退到会话实例，实际 %q", frame.InstanceUUID)
	}
}

// TestParseMetricSnapshotBadJSON 验证坏帧只被丢弃、不 panic（best-effort 通道语义）。
func TestParseMetricSnapshotBadJSON(t *testing.T) {
	var msg bridgeMessage
	frame, ok := parseMetricSnapshot(&PluginSession{InstanceID: "x"}, msg, []byte(`{not json`))
	if ok {
		t.Fatal("坏帧应返回 ok=false")
	}
	if frame.InstanceUUID != "" {
		t.Fatal("坏帧不应产生数据")
	}
}

// TestEmitMetricDropNotifiesHandler 验证会话结束时会把实例 UUID 交给作废回调。
//
// 回归防线：这个回调曾经根本不存在。探针断开后 pushedStore 里留着旧快照，
// 心跳拼装时 Load 拿到 pushed=true，于是一个已经停掉的实例仍被当作有数据源
// 去采集，把过期 TPS/堆/桥指标一路报到 CP。
func TestEmitMetricDropNotifiesHandler(t *testing.T) {
	server := NewPluginBridgeServer("secret")
	var got []string
	server.SetMetricSnapshotDropHandler(func(uuid string) { got = append(got, uuid) })

	server.emitMetricDrop("inst-1")

	if len(got) != 1 || got[0] != "inst-1" {
		t.Fatalf("应收到一次作废通知，实际 %v", got)
	}
}

// TestEmitMetricDropIgnoresEmptyAndMissingHandler 验证未注入与空 UUID 都不 panic。
func TestEmitMetricDropIgnoresEmptyAndMissingHandler(t *testing.T) {
	server := NewPluginBridgeServer("secret")
	server.emitMetricDrop("inst-1") // 未注入 handler：不应 panic

	called := false
	server.SetMetricSnapshotDropHandler(func(string) { called = true })
	server.emitMetricDrop("") // 空 UUID：不应回调
	if called {
		t.Fatal("空 UUID 不应触发回调")
	}
}
