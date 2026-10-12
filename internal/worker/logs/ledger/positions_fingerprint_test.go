package ledger

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 段 1（w18 漂移防护）：位置指纹及其对应位置必须随 Entry 一起持久化与恢复。
//
// 现场：外部原地重写（保头重写/截断）后旧偏移指向不同内容，恢复续读按旧偏移回读 ⇒ 同内容以
// 新身份（漂移后的偏移）再摄取 ⇒ 同一批日志在 VL 里出现两遍（本轮实测 16/60 台、+211 079 行）。
// 本段只落「凭证」：字段＋迁移兼容；采样在段 2（tailer）、校验与回退在段 3（acquire）。
func TestPositionsFingerprintRoundTrip(t *testing.T) {
	led := New()
	key := keyOf("fp", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "fp", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceDurable(key, 4096))
	require.NoError(t, led.RecordDelivery(key, 0, 4096, logtypes.DeliveryRequestDone))

	// 段 1 仅验证字段随条目存取；写入方（tailer）在段 2 接线。
	led.mu.Lock()
	entry := led.entries[key]
	require.NotNil(t, entry)
	entry.PositionsFingerprint = "sha256:deadbeef"
	entry.PositionsFingerprintAt = 4096
	led.mu.Unlock()

	// 存取往返：Snapshot → JSON → 反序列化必须一致（随身位成对，不得只留其一半）。
	snapshot := led.Snapshot()
	require.Len(t, snapshot, 1)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var restored []Entry
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Len(t, restored, 1)
	require.Equal(t, "sha256:deadbeef", restored[0].PositionsFingerprint)
	require.Equal(t, uint64(4096), restored[0].PositionsFingerprintAt)
}

// 旧账本（无指纹字段）必须可加载：零值即「缺指纹」，段 3 判定语义里视作不一致（宁可走对账，不回读历史）。
func TestPositionsFingerprintLegacyEntryLoadsWithZeroValue(t *testing.T) {
	legacy := []byte(`{"key":{"log_source_id":"fp","source_generation":"g1"},` +
		`"identity":{"log_source_id":"fp","source_generation":"g1","parser_version":"v1"},` +
		`"positions":{"read_position":10,"durable_position":10,"delivery_position":10,"reclaim_position":10},` +
		`"segments":[],"rotations":[],"gaps":[],"recovery_refs":[],"error_count":0,"ingest_seq":0,"acquire_paused":false}`)
	var entry Entry
	require.NoError(t, json.Unmarshal(legacy, &entry), "旧账本缺指纹字段不得导致加载失败")
	require.Empty(t, entry.PositionsFingerprint)
	require.Zero(t, entry.PositionsFingerprintAt)
	require.Equal(t, uint64(10), entry.Positions.Reclaim, "旧字段语义不得受影响")
}
