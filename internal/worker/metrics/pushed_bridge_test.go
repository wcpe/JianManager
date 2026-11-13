package metrics

import (
	"encoding/json"
	"testing"
)

/**
 * 桥中转指标解析的契约测试。
 *
 * 这一组用例保护的是一件很容易被“优化掉”的事：**老探针不发字段时必须保持 nil**。
 * 如果哪天有人图方便把这里改成“默认建一个全零 BridgeSnapshot”，
 * CP 侧就会把“不知道有没丢”画成“队列空、零丢失”——恰好与本项监控要防的事相反。
 * 这种错误不会报错、不会崩，只会让监控静默变成摆设。
 */

func TestProbeSnapshotFromPushedBridgeMetrics(t *testing.T) {
	raw := json.RawMessage(`{
		"bridgeReliableQueue": 1024,
		"bridgeBestEffortQueue": 37,
		"bridgeReliableRejectedTotal": 3880,
		"bridgeBestEffortDroppedTotal": 12,
		"bridgeReceiptRejectedTotal": 3,
		"bridgeSpillDroppedTotal": 0,
		"bridgeWriteFailuresTotal": 7,
		"bridgeWriteTimeoutsTotal": 1
	}`)

	snap, ok := ProbeSnapshotFromPushed(raw)
	if !ok {
		t.Fatal("解析应成功")
	}
	if snap.Bridge == nil {
		t.Fatal("新探针上报了 bridge 字段，Bridge 不应为 nil")
	}

	b := snap.Bridge
	if b.ReliableQueue != 1024 {
		t.Errorf("ReliableQueue 应为 1024，实际=%d", b.ReliableQueue)
	}
	if b.BestEffortQueue != 37 {
		t.Errorf("BestEffortQueue 应为 37，实际=%d", b.BestEffortQueue)
	}
	if b.ReliableRejectedTotal != 3880 {
		t.Errorf("ReliableRejectedTotal 应为 3880，实际=%d", b.ReliableRejectedTotal)
	}
	if b.BestEffortDroppedTotal != 12 {
		t.Errorf("BestEffortDroppedTotal 应为 12，实际=%d", b.BestEffortDroppedTotal)
	}
	if b.ReceiptRejectedTotal != 3 {
		t.Errorf("ReceiptRejectedTotal 应为 3，实际=%d", b.ReceiptRejectedTotal)
	}
	if b.WriteFailuresTotal != 7 {
		t.Errorf("WriteFailuresTotal 应为 7，实际=%d", b.WriteFailuresTotal)
	}
	if b.WriteTimeoutsTotal != 1 {
		t.Errorf("WriteTimeoutsTotal 应为 1，实际=%d", b.WriteTimeoutsTotal)
	}
}

// 老探针（或任何没上报 bridge 的探针）必须得到 nil，而不是全零快照。
func TestProbeSnapshotFromPushedBridgeAbsent(t *testing.T) {
	raw := json.RawMessage(`{"tps": 20.0, "msptAvg": 0.003}`)

	snap, ok := ProbeSnapshotFromPushed(raw)
	if !ok {
		t.Fatal("解析应成功")
	}
	if snap.Bridge != nil {
		t.Fatalf(
			"未上报 bridge 字段时 Bridge 必须为 nil（代表“不知道”），实际拿到了 %+v；"+
				"若这里改成全零快照，CP 会把“不知道有没丢”画成“健康”",
			snap.Bridge,
		)
	}
}

// 只上报部分字段也算已上报（同源于一次 SenderStatus 快照，整组可信）。
func TestProbeSnapshotFromPushedBridgePartial(t *testing.T) {
	raw := json.RawMessage(`{"bridgeReliableQueue": 5}`)

	snap, ok := ProbeSnapshotFromPushed(raw)
	if !ok {
		t.Fatal("解析应成功")
	}
	if snap.Bridge == nil {
		t.Fatal("只要任一 bridge 字段出现，就应认为整组可信")
	}
	if snap.Bridge.ReliableQueue != 5 {
		t.Errorf("ReliableQueue 应为 5，实际=%d", snap.Bridge.ReliableQueue)
	}
	// 缺失项零值即可：它们与“确实为 0”在本场景下不构成歧义，
	// 因为“是否上报”已由 Bridge 是否为 nil 表达。
	if snap.Bridge.BestEffortQueue != 0 {
		t.Errorf("未上报的 BestEffortQueue 应为零值，实际=%d", snap.Bridge.BestEffortQueue)
	}
}
