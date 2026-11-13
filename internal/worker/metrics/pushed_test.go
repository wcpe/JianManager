package metrics

import (
	"encoding/json"
	"testing"
)

// TestProbeSnapshotFromPushedUnits 验证单位换算与缺项语义。
//
// 两个最容易接错、且错了不报错的地方：
//  1. MSPT 协议单位是**秒**，内部模型是**毫秒**——漏换会让面板小一千倍；
//  2. 探针对不可用指标**不输出该键**，不能当成 0。
func TestProbeSnapshotFromPushedUnits(t *testing.T) {
	raw := json.RawMessage(`{"tps":19.8,"msptAvg":0.0234,"playersOnline":0,
		"heapUsed":1191000000,"heapMax":4294967296,"threads":212,
		"cpuLoad":0.35,"uptimeSeconds":3600.5,"gcCountTotal":42,"gcTimeMillis":1250,
		"worlds":{"world":{"loadedChunks":1200,"entities":85,"tileEntities":310}}}`)
	snap, ok := ProbeSnapshotFromPushed(raw)
	if !ok {
		t.Fatal("应解析成功")
	}
	if snap.TPS != 19.8 {
		t.Fatalf("TPS 错: %v", snap.TPS)
	}
	// 0.0234 秒 → 23.4 毫秒
	if got := snap.MSPTAvgMillis; got < 23.39 || got > 23.41 {
		t.Fatalf("MSPT 单位换算错（应为 23.4 毫秒）: %v", got)
	}
	if snap.PlayersOnline != 0 {
		// 玩家数为 0 是**真实值**（没人在线），不能被当成缺测
		t.Fatalf("playersOnline 应为 0，实际 %d", snap.PlayersOnline)
	}
	if snap.HeapMaxBytes != 4294967296 {
		t.Fatalf("heapMax 错: %d", snap.HeapMaxBytes)
	}
	if snap.GCCountTotal != 42 {
		t.Fatalf("gcCountTotal 应为 int64 42，实际 %d", snap.GCCountTotal)
	}
	if w, ok := snap.Worlds["world"]; !ok || w.LoadedChunks != 1200 || w.TileEntities != 310 {
		t.Fatalf("世界表错: %+v", snap.Worlds)
	}
}

// TestProbeSnapshotFromPushedMissingFieldsAreNotZero 验证缺项不被伪装成 0。
func TestProbeSnapshotFromPushedMissingFieldsAreNotZero(t *testing.T) {
	// 探针刚启动、TPS/MSPT 还没采样时只给这些字段。
	raw := json.RawMessage(`{"heapUsed":100,"threads":50}`)
	snap, ok := ProbeSnapshotFromPushed(raw)
	if !ok {
		t.Fatal("应解析成功")
	}
	if snap.TPS != -1 {
		t.Fatalf("TPS 缺测应为 -1 哨兵，实际 %v", snap.TPS)
	}
	if snap.MSPTAvgMillis != -1 {
		t.Fatalf("MSPT 缺测应为 -1 哨兵，实际 %v", snap.MSPTAvgMillis)
	}
	if snap.HeapUsedBytes != 100 {
		t.Fatalf("heapUsed 错: %d", snap.HeapUsedBytes)
	}
}

// TestProbeSnapshotFromPushedBadJSON 验证坏载荷不污染快照。
func TestProbeSnapshotFromPushedBadJSON(t *testing.T) {
	if _, ok := ProbeSnapshotFromPushed(json.RawMessage(`{bad`)); ok {
		t.Fatal("坏 JSON 应返回 ok=false")
	}
	if _, ok := ProbeSnapshotFromPushed(nil); ok {
		t.Fatal("空载荷应返回 ok=false")
	}
}

// TestPushedStoreLifecycle 验证存储的存取与清理。
func TestPushedStoreLifecycle(t *testing.T) {
	s := NewPushedSnapshotStore()
	if _, ok := s.Load("a"); ok {
		t.Fatal("未存过不应命中")
	}
	s.Store("a", ProbeSnapshot{TPS: 20})
	if snap, ok := s.Load("a"); !ok || snap.TPS != 20 {
		t.Fatalf("存取不一致: %+v ok=%v", snap, ok)
	}
	s.Forget("a")
	if _, ok := s.Load("a"); ok {
		t.Fatal("Forget 后不应命中")
	}
	// nil 安全：未初始化不应 panic
	var nilStore *PushedSnapshotStore
	nilStore.Store("x", ProbeSnapshot{})
	if _, ok := nilStore.Load("x"); ok {
		t.Fatal("nil store 应返回 false")
	}
}
