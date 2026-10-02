package normalize

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 缺陷 C 回归（时间戳偏移 8 小时）。转红：改动前没有任何配置面能把源时区接进来，
// Location 恒为 UTC——本地时间被直接当成 UTC 落库（现场：`[23:54:02]` HKT 存成 23:54:02Z）。

func tzEvent(t *testing.T, line string, loc *time.Location, base time.Time) logtypes.Event {
	t.Helper()
	res := ProcessLines([]string{line, "[23:59:58] [Server thread/INFO]: closer"}, Options{
		Source:   logtypes.SourceIdentity{LogSourceID: "src-tz", SourceGeneration: "g1"},
		Stream:   "stdout",
		Location: loc,
		BaseTime: base,
	})
	require.NotEmpty(t, res.Events)
	return res.Events[0]
}

// TestSourceTimeZoneConvertsLocalClockToUTC 是缺陷 C 的核心转红闸门：
// 本地 HKT 时间必须换算成 UTC，而不是原样当作 UTC。
func TestSourceTimeZoneConvertsLocalClockToUTC(t *testing.T) {
	hkt, err := time.LoadLocation("Asia/Hong_Kong")
	require.NoError(t, err)
	// 基准取本地 20:00（HKT），保证 [23:54:02] 落在同一天而不触发跨日回拨。
	base := time.Date(2026, 10, 1, 20, 0, 0, 0, hkt)

	ev := tzEvent(t, "[23:54:02] [Server thread/INFO]: 香港时间", hkt, base)
	require.Equal(t, "2026-10-01T15:54:02Z", ev.EventTimeUTC,
		"HKT 23:54:02 必须换算为 15:54:02Z（现场正是这里存成了 23:54:02Z）")
	require.Equal(t, "Asia/Hong_Kong", ev.Fields[FieldEventTimeZone],
		"非 UTC 源必须留下解释时区，便于排查历史时间轴")
}

// TestSourceTimeZoneCrossMidnightBoundary 覆盖跨日边界：本地凌晨的时刻应换算到前一天 UTC，
// 且不得把时间「猜」到未来（applyClock 的 ±12h 回拨必须仍按源时区工作）。
func TestSourceTimeZoneCrossMidnightBoundary(t *testing.T) {
	hkt, err := time.LoadLocation("Asia/Hong_Kong")
	require.NoError(t, err)
	base := time.Date(2026, 10, 1, 23, 30, 0, 0, hkt)

	ev := tzEvent(t, "[00:10:00] [Server thread/INFO]: 跨午夜", hkt, base)
	require.Equal(t, "2026-10-01T16:10:00Z", ev.EventTimeUTC,
		"本地次日 00:10 应换算为前一日 16:10Z（跨日边界不能偏一天）")
}

// TestDefaultTimeZoneStaysUTC 守住「未配置的源零行为变化」。
func TestDefaultTimeZoneStaysUTC(t *testing.T) {
	base := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	ev := tzEvent(t, "[23:54:02] [Server thread/INFO]: 无时区配置", nil, base)
	require.Equal(t, "2026-10-01T23:54:02Z", ev.EventTimeUTC,
		"未配置时区时保持既有 UTC 语义（向后兼容）")
	require.NotContains(t, ev.Fields, FieldEventTimeZone, "UTC 事件不写时区字段（零变化）")
}
