package vlrange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 回归（P0-1）：游标页必须把窗口收窄到「不晚于游标时刻」（HTTP `end` 开区间 → 取 游标+1ns
// 等价于 `_time <= 游标时刻`），并为收窄窗口多要 1 行预算。否则服务端
// `| sort by (_time desc) | limit K` 会把游标页的取数截断成**第一页的同一批 top-K**：
// 客户端 filterAfterCursor 过滤后只剩 ≤1 行 → 服务端判 exhausted 且 next="" →
// 第一页之后的行全部不可达（用户表现为「日志查不到」）。
//
// 用「忠实执行 top-K 截断」的假 VL（对窗口内全部行排序后截断）驱动真 Service 分页。
// 现有 planner_test 的桩恒返回全量行，抓不到这个截断。三条判据各有独立的转红路径：
//
//	判据 1「非终页必须取满 limit 条」   ← 不收窄窗口（旧实现）即红；
//	判据 2「跨页并集 == 全部行、无重」  ← 第 3 页起不可达（游标页仍只请求 N+1）即红；
//	                                     并列组跨页时用 `<` 收窄会整组漏掉，同样即红；
//	判据 3「窗口上界 >= 游标时刻」      ← 上界收紧到游标时刻之前（`<` 语义）即红。
func TestSearchCursorPagesFillLimitWithoutGapsOrDuplicates(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	tenth := day.Add(10*time.Hour + 10*time.Minute)

	// 夹具 21 行（limit=5）：
	//   序列位 1..9 ：10:19 → 10:11（时间戳互不相同）
	//   序列位 10..11：10:10 **并列 2 行**（record_start 2000 / 1000，全键 DESC 内部次序 big → small）
	//   序列位 12..21：10:09 → 10:00
	// 10:10 并列组恰好跨页（第 2 页止于组内第 1 行）：只有收窄成 `<= 游标时刻`，第 3 页
	// 才能重新取回组内剩余行；用 `<` 会整组漏掉。
	rows := make([]fakeVLRow, 0, 21)
	for minute := 19; minute >= 11; minute-- {
		rows = append(rows, fakeVLRow{
			eventTime:   day.Add(10*time.Hour + time.Duration(minute)*time.Minute),
			eventID:     fmt.Sprintf("e%02d", minute),
			recordStart: uint64(1000 + minute),
			recordEnd:   uint64(1001 + minute),
		})
	}
	rows = append(rows,
		fakeVLRow{eventTime: tenth, eventID: "e10-big", recordStart: 2000, recordEnd: 2001},
		fakeVLRow{eventTime: tenth, eventID: "e10-small", recordStart: 1000, recordEnd: 1001},
	)
	for minute := 9; minute >= 0; minute-- {
		rows = append(rows, fakeVLRow{
			eventTime:   day.Add(10*time.Hour + time.Duration(minute)*time.Minute),
			eventID:     fmt.Sprintf("e%02d", minute),
			recordStart: uint64(1000 + minute),
			recordEnd:   uint64(1001 + minute),
		})
	}

	store := &fakeVLStore{rows: rows}
	srv := httptest.NewServer(store.handler(t))
	defer srv.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	key := catalog.PartitionKey{StorageNamespace: "inst:7/stdout", UTCDay: day.Format("2006-01-02")}
	cat := catalog.New(nil)
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:    "pv-1",
		CoverageComplete:   true,
		QueryLocationDirID: "hot-g1",
		QueryGeneration:    1,
		// 视图前缀远大于夹具 record_end：等价于「前缀内全部行都可见」。
		ClosedVisibleSeq: map[string]uint64{key.String(): 1 << 40},
	}
	require.NoError(t, cat.Put(rec))
	svc := query.NewService(query.NewPlanner(cat, nil), client, "cursor-pagination-test")

	const limit = 5
	req := query.QueryRequest{
		RequestID: "page-1",
		TimeRange: query.TimeRange{
			FromUTC: day.Format(time.RFC3339Nano),
			ToUTC:   day.Add(24 * time.Hour).Format(time.RFC3339Nano),
		},
		Budget: query.Budget{Limit: limit},
	}

	var (
		collected []logtypes.Event
		pages     [][]string
		viewID    string
		cursor    string
		exhausted bool
	)
	// 上限 8 页只用于防止坏实现下死循环；夹具应 5 页收敛（5+5+5+5+1）。
	for page := 1; page <= 8 && !exhausted; page++ {
		r := req
		r.RequestID = fmt.Sprintf("page-%d", page)
		if cursor != "" {
			r.View = &query.ViewRef{ViewID: viewID, OrderVersion: query.SortVersion, Cursor: cursor}
		}
		resp := svc.Search(context.Background(), r)
		require.Nil(t, resp.Err, "第 %d 页不得报错", page)
		if viewID == "" {
			require.NotNil(t, resp.View)
			viewID = resp.View.ViewID
		}
		pages = append(pages, eventIDs(resp.Items))
		collected = append(collected, resp.Items...)
		exhausted = resp.Exhausted
		cursor = resp.NextCursor
		if !exhausted {
			require.Len(t, resp.Items, limit,
				"第 %d 页必须取满 %d 条：游标页窗口未收窄时服务端 top-K 会取回第一页同一批行，客户端过滤后只剩 ≤1 条", page, limit)
			require.NotEmpty(t, cursor, "第 %d 页非终页必须给出下一页游标", page)
		}
	}
	require.True(t, exhausted, "分页必须收敛到 exhausted，实际页数 %d", len(pages))

	// 判据 2：跨页无漏 + 无重 + 全序 = 全键 DESC。
	sorted := append([]fakeVLRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return query.LessSortKey(fakeSortKey(sorted[i]), fakeSortKey(sorted[j]))
	})
	wantIDs := make([]string, 0, len(sorted))
	for _, row := range sorted {
		wantIDs = append(wantIDs, row.eventID)
	}
	gotIDs := eventIDs(collected)
	require.Len(t, pages, 5, "21 行 / 每页 5 行 = 4 个满页 + 1 个余数页，实际: %v", pages)
	for i := 0; i < 4; i++ {
		require.Len(t, pages[i], limit, "第 %d 页应满页", i+1)
	}
	require.Equal(t, wantIDs, gotIDs, "跨页并集必须等于全键 DESC 全序（无漏无重）")
	require.Len(t, gotIDs, len(rows))

	// 判据 3：游标页窗口确实被收窄到「不晚于游标时刻」，并保留 1 行前瞻预算。
	windows := store.snapshot()
	require.Len(t, windows, 5)
	require.NotEqual(t, windows[0].end, windows[1].end,
		"游标页必须收窄窗口上界；与第一页逐字相同的窗口正是 P0-1 的根因")
	cursorTime := sorted[limit-1].eventTime // 第一页第 5 行即第一页游标所在行
	require.True(t, windows[1].end.After(cursorTime),
		"上界必须包含游标时刻本身（<=）；收紧到游标时刻之前会把同一时间戳的并列行整组漏掉")
	require.False(t, windows[1].end.After(cursorTime.Add(time.Second)),
		"上界放宽到 1s 以上会让「未来时间戳」行挤占 top-K 预算，反而挤掉本页应返回的行")
	require.Equal(t, limit+1, windows[0].limit, "首页请求 N+1 行：N 页内 + 1 条前瞻")
	require.Equal(t, limit+2, windows[1].limit,
		"游标页请求 N+2 行：收窄后的窗口必含游标行本身，它会被客户端全键过滤丢弃，仍需留下 1 条前瞻")
}

// fakeVLRow 是假 VL 中的一行日志。
type fakeVLRow struct {
	eventTime   time.Time
	eventID     string
	recordStart uint64
	recordEnd   uint64
}

func fakeSortKey(row fakeVLRow) query.SortKey {
	return query.SortKey{
		EventTimeUTC:     row.eventTime.Format(time.RFC3339Nano),
		LogSourceID:      "inst:7/stdout",
		SourceGeneration: "g1",
		RecordStart:      row.recordStart,
		RecordEnd:        row.recordEnd,
		EventID:          row.eventID,
	}
}

func eventIDs(items []logtypes.Event) []string {
	out := make([]string, 0, len(items))
	for _, ev := range items {
		out = append(out, ev.EventID)
	}
	return out
}

// fakeVLWindow 记录一次查询请求实际生效的窗口与 top-K 上限。
type fakeVLWindow struct {
	start time.Time
	end   time.Time
	limit int
}

// fakeVLStore 是「忠实执行 `| sort by (_time desc) | limit K`」的假 VictoriaLogs：
// 按 [start,end) 过滤窗口内的全部行，做 top-K 截断后返回——K 之外的行**不会**返回，
// 这正是 P0-1 截断的来源。请求侧只解析窗口与 limit；夹具不含用户过滤，
// 故不实现 LogsQL 选择器（选择器越界防护由其它用例覆盖）。
type fakeVLStore struct {
	rows []fakeVLRow

	mu      sync.Mutex
	windows []fakeVLWindow
}

var pipeLimitRe = regexp.MustCompile(`\| limit (\d+)`)

func (s *fakeVLStore) snapshot() []fakeVLWindow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeVLWindow(nil), s.windows...)
}

func (s *fakeVLStore) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		start, err := time.Parse(time.RFC3339Nano, values.Get("start"))
		require.NoError(t, err)
		end, err := time.Parse(time.RFC3339Nano, values.Get("end"))
		require.NoError(t, err)
		pipe := pipeLimitRe.FindStringSubmatch(values.Get("query"))
		require.Len(t, pipe, 2, "query 必须带 `| limit` 管道: %s", values.Get("query"))
		limit, err := strconv.Atoi(pipe[1])
		require.NoError(t, err)
		if httpLimit := values.Get("limit"); httpLimit != "" {
			n, err := strconv.Atoi(httpLimit)
			require.NoError(t, err)
			if n < limit {
				limit = n
			}
		}
		s.mu.Lock()
		s.windows = append(s.windows, fakeVLWindow{start: start, end: end, limit: limit})
		s.mu.Unlock()

		matched := make([]fakeVLRow, 0, len(s.rows))
		for _, row := range s.rows {
			if row.eventTime.Before(start) || !row.eventTime.Before(end) {
				continue
			}
			matched = append(matched, row)
		}
		sort.SliceStable(matched, func(i, j int) bool { return matched[i].eventTime.After(matched[j].eventTime) })
		if len(matched) > limit {
			matched = matched[:limit]
		}

		w.Header().Set("Content-Type", "application/json")
		for _, row := range matched {
			line, err := json.Marshal(map[string]string{
				"_time":                  row.eventTime.Format(time.RFC3339Nano),
				"_msg":                   "m-" + row.eventID,
				"event_id":               row.eventID,
				"log_source_id":          "inst:7/stdout",
				"source_generation":      "g1",
				"record_start":           strconv.FormatUint(row.recordStart, 10),
				"record_end":             strconv.FormatUint(row.recordEnd, 10),
				"level":                  "INFO",
				"stream":                 "stdout",
				"canonical_content_hash": "h-" + row.eventID,
			})
			require.NoError(t, err)
			_, _ = w.Write(append(line, '\n'))
		}
	}
}
