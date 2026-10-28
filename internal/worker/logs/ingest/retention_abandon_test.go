package ingest

// 超出热层保留期的前缀必须被如实放弃、而不是无限重试（2026-10-29 生产停采最后一环）。
//
// ## 现场
//
// 补段路径要把 `[0, 1784453)` 登记成恢复段，其中 20000 条事件的 UTC 天是 9/20-9/21，
// 而 VL hot 只有 30 天保留期（边界约 9/28）。这些天在 VL 里**物理上不存在**
// （VL 对超期数据返回 200 但立即丢弃，已实测），所以：
//
//	`capturePublishedPrefixEvidence` 取不到证据（Catalog 无 PublishedProjection）
//	  → proof-coverage-short → 不建段 → reclaim 卡 0
//	  → WAL 删不掉 → 撞 20000 条上限 → 整源停采
//
// 用户裁定（2026-10-29）：「丢失就丢失，旧的数据要来干什么，承认永久丢失，放行 reclaim」。
//
// ## 本文件钉住三条性质
//
//  1. 全部事件都超窗口 ⇒ `beyondHotRetentionForEvents` 为真（触发放弃路径）；
//  2. 只要有一条在窗口内 ⇒ 为假（走正常补段，不能误伤）；
//  3. 未配置 retention（零值）⇒ 恒假（保持改动前行为，不放弃任何数据）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// retentionTestEvent 造一个指定 UTC 天的事件。
func retentionTestEvent(day string) logtypes.Event {
	return logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "inst:ret/file", SourceGeneration: "g1", ParserVersion: "v1"},
		logtypes.RecordRange{Start: 0, End: 9},
		day+"T02:00:00Z", day+"T02:00:01Z", "INFO", "stdout", "line",
	)
}

// TestBeyondHotRetentionForEventsAllBeyond 是本修复的正例：
// 全部事件都早于 `now-retention` ⇒ 必须判为「超出窗口」。
func TestBeyondHotRetentionForEventsAllBeyond(t *testing.T) {
	m := &Manager{hotRetention: 30 * 24 * time.Hour}
	source := SourceConfig{LogSourceID: "inst:ret/file", SourceGeneration: "g1"}

	beyondDay := time.Now().UTC().AddDate(0, 0, -45).Format("2006-01-02")
	events := []logtypes.Event{retentionTestEvent(beyondDay), retentionTestEvent(beyondDay)}

	require.True(t, m.beyondHotRetentionForEvents(source, events),
		"全部事件都在保留期外 ⇒ 必须触发放弃路径（否则 reclaim 永远卡死）")
}

// TestBeyondHotRetentionForEventsMixedStaysFalse 守住「不误伤」：
// 只要有一天在窗口内，就必须走正常补段。
func TestBeyondHotRetentionForEventsMixedStaysFalse(t *testing.T) {
	m := &Manager{hotRetention: 30 * 24 * time.Hour}
	source := SourceConfig{LogSourceID: "inst:ret/file", SourceGeneration: "g1"}

	beyondDay := time.Now().UTC().AddDate(0, 0, -45).Format("2006-01-02")
	insideDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	events := []logtypes.Event{retentionTestEvent(beyondDay), retentionTestEvent(insideDay)}

	require.False(t, m.beyondHotRetentionForEvents(source, events),
		"窗口内还有数据 ⇒ 不得放弃，必须走正常补段")
}

// TestBeyondHotRetentionForEventsNoRetentionDisabled 守住向后兼容：
// 未配置 retention（零值）⇒ 恒假，不放弃任何数据。
func TestBeyondHotRetentionForEventsNoRetentionDisabled(t *testing.T) {
	m := &Manager{} // hotRetention 零值
	source := SourceConfig{LogSourceID: "inst:ret/file", SourceGeneration: "g1"}
	beyondDay := time.Now().UTC().AddDate(0, 0, -400).Format("2006-01-02")

	require.False(t, m.beyondHotRetentionForEvents(source, []logtypes.Event{retentionTestEvent(beyondDay)}),
		"retention 未配置 ⇒ 不启用放弃（保持改动前行为）")
}

// TestBeyondHotRetentionForEventsEmptyBatch 守住空批次：无事可做，不触发放弃。
func TestBeyondHotRetentionForEventsEmptyBatch(t *testing.T) {
	m := &Manager{hotRetention: 30 * 24 * time.Hour}
	source := SourceConfig{LogSourceID: "inst:ret/file", SourceGeneration: "g1"}
	require.False(t, m.beyondHotRetentionForEvents(source, nil))
}
