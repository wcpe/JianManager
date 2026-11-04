package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// eventUTCDays 决定「为哪几个分区补建 Catalog 记录」。它错了，日志页就空。
//
// # 它曾经错在哪（2026-11-05 真机）
//
// 补建时用的是源级 `source.UTCDay`（= 源**登记当天**，由 runtime_lifecycle 填一次后固定）。
// 而事件按各自的 EventTimeUTC 落进分区。跨天后两者不一致：
//
//	18 个源的 Catalog 记录 utcDay=2026-10-30（登记日）
//	实际数据落点 2026-11-04，VL 直查 239 条
//	查询 11-04 → Planner.selectKeys 按时间范围过滤掉 10-30 的记录 → target 无记录 → 空
//
// 既有代码里早有正确模式（hot_retention_window 的 daySource）——
//「必须把 UTCDay 设成该事件所属天」，本处只是没照做。
func TestEventUTCDaysUsesEventTimeNotSourceDay(t *testing.T) {
	events := []logtypes.Event{
		{EventTimeUTC: "2026-11-04T17:23:19Z"},
		{EventTimeUTC: "2026-11-04T23:59:59Z"},
		{EventTimeUTC: "2026-11-05T00:00:01Z"}, // 跨天
		{EventTimeUTC: "2026-11-04T01:02:03Z"},
	}
	require.Equal(t, []string{"2026-11-04", "2026-11-05"}, eventUTCDays(events),
		"必须返回本批涉及的**所有** UTC 天（升序去重），不能只取第一天")
}

// 单天批次只返回一天（不要凭空多建）。
func TestEventUTCDaysSingleDay(t *testing.T) {
	events := []logtypes.Event{
		{EventTimeUTC: "2026-11-04T00:00:00Z"},
		{EventTimeUTC: "2026-11-04T23:59:59Z"},
	}
	require.Equal(t, []string{"2026-11-04"}, eventUTCDays(events))
}

// 时区换算：UTC 日以 **UTC** 为准，不是本地日。
// 2026-11-04T16:30:00Z 在 HKT(+8) 是 11-05 00:30，但分区必须归 11-04。
func TestEventUTCDaysIsUTCNotLocal(t *testing.T) {
	events := []logtypes.Event{{EventTimeUTC: "2026-11-04T16:30:00Z"}}
	require.Equal(t, []string{"2026-11-04"}, eventUTCDays(events),
		"分区按 UTC 日归属；若误用本地时区会归到 11-05，落错天")
}

// 解析不了的时间不猜：返回空，调用方因此跳过补建。
// 宁可晚一拍可见，也不写一条错天的记录——错天记录会让查询选不中，比没有记录更难查。
func TestEventUTCDaysSkipsUnparsable(t *testing.T) {
	events := []logtypes.Event{
		{EventTimeUTC: ""},
		{EventTimeUTC: "not-a-time"},
		{EventTimeUTC: "2026-11-04T00:00:00Z"},
	}
	require.Equal(t, []string{"2026-11-04"}, eventUTCDays(events))

	require.Nil(t, eventUTCDays(nil))
	require.Nil(t, eventUTCDays([]logtypes.Event{{EventTimeUTC: "garbage"}}))
}

// RFC3339Nano 带纳秒也要能解析（生产时间戳带纳秒）。
func TestEventUTCDaysParsesNanoTimestamps(t *testing.T) {
	events := []logtypes.Event{{EventTimeUTC: "2026-11-04T17:23:19.123456789Z"}}
	require.Equal(t, []string{"2026-11-04"}, eventUTCDays(events))
}
