package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/**
游标分页（FR-419，spec §4.2）单测。

本文件的存在理由就是 offset 漂移：控制台向上回溯时新日志正在往表头插，
OFFSET/LIMIT 的「跳过前 N 行」在两次请求间指向不同的行 → 重复行或丢行。
故并发插入用例不只验「游标对」，还顺手把「offset 错在哪」断言出来，
免得后人把游标改回 offset 时测试仍然全绿。
*/

// seedLogRows 按时间递增写入 n 条实例日志，返回写入的行（时间从 base 起每条 +step）。
func seedLogRows(t *testing.T, db *gorm.DB, base time.Time, step time.Duration, n int, prefix string) []model.LogEntry {
	t.Helper()
	rows := make([]model.LogEntry, 0, n)
	for i := 0; i < n; i++ {
		e := model.LogEntry{
			Source:     model.LogSourceInstance,
			Level:      model.LogLevelInfo,
			InstanceID: 1,
			NodeID:     9,
			Message:    fmt.Sprintf("%s-%d", prefix, i),
			Time:       base.Add(time.Duration(i) * step),
		}
		require.NoError(t, db.Create(&e).Error)
		rows = append(rows, e)
	}
	return rows
}

// drainCursor 从 start 起一路翻到最早，返回按返回顺序拼接的全部 message 与实际页数。
func drainCursor(t *testing.T, svc *LogService, filter LogFilter, start *LogCursor) ([]string, int) {
	t.Helper()
	cursor := start
	var got []string
	pages := 0
	for {
		f := filter
		f.Cursor = cursor
		page, err := svc.QueryCursor(f)
		require.NoError(t, err)
		pages++
		for _, item := range page.Items {
			got = append(got, item.Message)
		}
		if page.NextCursor == nil {
			return got, pages
		}
		parsed, err := ParseLogCursor(*page.NextCursor)
		require.NoError(t, err)
		cursor = &parsed
		require.Less(t, pages, 100, "翻页未终止，游标可能未推进")
	}
}

func TestLogCursor_EmptyCursorStartsFromNewest(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	seedLogRows(t, db, base, time.Second, 5, "row")

	// 空游标 = 从最新开始，且顺序与页码分页一致（time DESC, id DESC）。
	page, err := svc.QueryCursor(LogFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, "row-4", page.Items[0].Message)
	require.Equal(t, "row-3", page.Items[1].Message)
	require.Equal(t, 2, page.Limit)
	// 还有更早的 → nextCursor 非空，且指向本页最后一行的位置。
	require.NotNil(t, page.NextCursor)
	cursor, err := ParseLogCursor(*page.NextCursor)
	require.NoError(t, err)
	require.Equal(t, page.Items[1].ID, cursor.ID)
	require.True(t, cursor.Time.Equal(page.Items[1].Time))
}

func TestLogCursor_LastPageHasNilNextCursor(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	seedLogRows(t, db, base, time.Second, 6, "row")

	// 6 行 / 每页 2 行 → 恰好 3 页，第 3 页必须自报「已到最早」而不是再给一个游标
	// 换来一次返回空数组的往返。
	got, pages := drainCursor(t, svc, LogFilter{Limit: 2}, nil)
	require.Equal(t, 3, pages)
	require.Equal(t, []string{"row-5", "row-4", "row-3", "row-2", "row-1", "row-0"}, got)
}

func TestLogCursor_RowCountEqualToLimitIsLastPage(t *testing.T) {
	// 边界：总行数正好等于 limit 时也必须是末页（探测多取一行拿不满 → nextCursor 为 nil）。
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	seedLogRows(t, db, base, time.Second, 3, "exact")

	page, err := svc.QueryCursor(LogFilter{Limit: 3})
	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	require.Nil(t, page.NextCursor)
}

func TestLogCursor_EmptyTableAndPastEarliest(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())

	// 空表：items 是空切片（非 nil，序列化成 [] 而不是 null），nextCursor 为 nil。
	page, err := svc.QueryCursor(LogFilter{Limit: 10})
	require.NoError(t, err)
	require.NotNil(t, page.Items)
	require.Empty(t, page.Items)
	require.Nil(t, page.NextCursor)

	// 游标已越过最早一行：同样是空页 + nil，不报错。
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	rows := seedLogRows(t, db, base, time.Second, 2, "row")
	beyond := LogCursor{Time: rows[0].Time, ID: rows[0].ID}
	page, err = svc.QueryCursor(LogFilter{Limit: 10, Cursor: &beyond})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Nil(t, page.NextCursor)
}

func TestLogCursor_TiesBrokenByID(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	// 时间完全相同的一批（log_ingest 批量 INSERT 的真实形态）：只按 time 比较会在相等处
	// 反复返回同一批或整批跳过，故 id 必须参与游标。
	same := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	seedLogRows(t, db, same, 0, 5, "tie")

	got, _ := drainCursor(t, svc, LogFilter{Limit: 2}, nil)
	require.Equal(t, []string{"tie-4", "tie-3", "tie-2", "tie-1", "tie-0"}, got)
}

func TestLogCursor_NoDuplicateNoLossUnderConcurrentInsert(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	old := seedLogRows(t, db, base, time.Second, 6, "old")
	require.Len(t, old, 6)

	// 第 1 页：拿到最新的 old-5 / old-4。
	first, err := svc.QueryCursor(LogFilter{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"old-5", "old-4"}, messages(first.Items))
	require.NotNil(t, first.NextCursor)
	cursor, err := ParseLogCursor(*first.NextCursor)
	require.NoError(t, err)

	// 翻页「期间」表头涌入 5 条更新的日志——这正是控制台一边回溯一边收实时输出的形态。
	seedLogRows(t, db, base.Add(time.Hour), time.Second, 5, "fresh")

	// 游标续翻：只应继续往更早走，一条不重、一条不漏。
	rest, _ := drainCursor(t, svc, LogFilter{Limit: 2}, &cursor)
	require.Equal(t, []string{"old-3", "old-2", "old-1", "old-0"}, rest)

	all := append(messages(first.Items), rest...)
	require.Equal(t, []string{"old-5", "old-4", "old-3", "old-2", "old-1", "old-0"}, all)
	require.Len(t, uniq(all), len(all), "游标翻页出现重复行")

	// 反证：同样的时序下 OFFSET 分页会把已读过的行再吐一遍——
	// 表头插了 5 行，offset=2 的第 2 页从 fresh 段里取，old-3/old-2 被整段跳过，
	// 而 fresh-2/fresh-1 是第 1 页语义上「更新」的行，本不该出现在向更早翻的第 2 页里。
	offsetPage2, err := svc.Query(LogFilter{Page: 2, PageSize: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"fresh-2", "fresh-1"}, messages(offsetPage2.Items))
	require.NotContains(t, messages(offsetPage2.Items), "old-3", "offset 漂移已丢行（本用例的存在理由）")
}

func TestLogCursor_CarriesFilters(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	for i := 0; i < 6; i++ {
		level := model.LogLevelInfo
		if i%2 == 1 {
			level = model.LogLevelError
		}
		require.NoError(t, db.Create(&model.LogEntry{
			Source:     model.LogSourceInstance,
			Level:      level,
			InstanceID: uint(1 + i%2),
			NodeID:     9,
			Message:    fmt.Sprintf("m-%d", i),
			Time:       base.Add(time.Duration(i) * time.Second),
		}).Error)
	}

	// 级别过滤带进游标查询（spec §4.3：先按级别过滤再回溯，数据量骤降）。
	lvl := model.LogLevelError
	got, _ := drainCursor(t, svc, LogFilter{Limit: 2, Level: &lvl}, nil)
	require.Equal(t, []string{"m-5", "m-3", "m-1"}, got)

	// 实例过滤同样生效（控制台回溯恒带 instanceId）。
	iid := uint(1)
	got, _ = drainCursor(t, svc, LogFilter{Limit: 10, InstanceID: &iid}, nil)
	require.Equal(t, []string{"m-4", "m-2", "m-0"}, got)

	// 时间上界（控制台用它把回溯窗口卡在会话开始之前，避免与内存缓冲重复）。
	to := base.Add(2 * time.Second)
	got, _ = drainCursor(t, svc, LogFilter{Limit: 10, To: &to}, nil)
	require.Equal(t, []string{"m-2", "m-1", "m-0"}, got)

	// 资源级隔离（非平台管理员的可访问实例集）在游标模式同样收敛。
	got, _ = drainCursor(t, svc, LogFilter{Limit: 10, InstanceIDs: []uint{}}, nil)
	require.Empty(t, got)
}

func TestLogCursor_LimitDefaultAndClamp(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	seedLogRows(t, db, base, time.Millisecond, 3, "row")

	// 未传 limit → 默认值；超上限 → 钳到 500（与页码分页的 pageSize 上限同源）。
	page, err := svc.QueryCursor(LogFilter{})
	require.NoError(t, err)
	require.Equal(t, logCursorDefaultLimit, page.Limit)

	page, err = svc.QueryCursor(LogFilter{Limit: 5000})
	require.NoError(t, err)
	require.Equal(t, logCursorMaxLimit, page.Limit)

	page, err = svc.QueryCursor(LogFilter{Limit: -3})
	require.NoError(t, err)
	require.Equal(t, logCursorDefaultLimit, page.Limit)
}

func TestLogCursor_ParseRoundTripAndRejects(t *testing.T) {
	original := LogCursor{Time: time.Date(2026, 8, 27, 10, 20, 30, 123456789, time.UTC), ID: 4242}
	parsed, err := ParseLogCursor(original.String())
	require.NoError(t, err)
	require.True(t, parsed.Time.Equal(original.Time))
	require.Equal(t, original.ID, parsed.ID)

	for _, bad := range []string{
		"",
		"_",
		"1234",
		"not-a-time_1",
		"2026-08-27T10:20:30Z_",
		"_12",
		"2026-08-27T10:20:30Z_abc",
	} {
		_, err := ParseLogCursor(bad)
		require.Error(t, err, "游标 %q 应被拒绝", bad)
	}
}

func TestLogCursor_PageModeUnchanged(t *testing.T) {
	// 回归：游标模式落地后页码分页（日志中心调用方）行为一字不改。
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	seedLogRows(t, db, base, time.Second, 5, "row")

	page, err := svc.Query(LogFilter{Page: 1, PageSize: 2})
	require.NoError(t, err)
	require.EqualValues(t, 5, page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 2, page.PageSize)
	require.Equal(t, []string{"row-4", "row-3"}, messages(page.Items))
}

func messages(items []model.LogEntry) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Message)
	}
	return out
}

func uniq(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
