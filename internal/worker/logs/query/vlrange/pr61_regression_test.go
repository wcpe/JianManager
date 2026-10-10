package vlrange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 仅建模查询明确声明的排序、窗口与上限，不声称复刻真实 VL 未承诺的并列次序。
// 并列输入特意使用最有利的复合全序，稳定保留它仍是合法的仅时间 top-K 输出。
// 所有夹具都属于同一已发布范围，因此选择器过滤与水位过滤不改变结果集合。
func pr61CursorFixture(t *testing.T, tied bool) (*query.Service, query.QueryRequest, []string, func() []string) {
	t.Helper()
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	rows := make([]fakeVLRow, 0, 21)
	want := make([]string, 0, 21)
	for i := 21; i >= 1; i-- {
		stamp := day.Add(10 * time.Hour)
		if !tied {
			stamp = stamp.Add(time.Duration(i) * time.Minute)
		}
		id := fmt.Sprintf("e%02d", i)
		rows = append(rows, fakeVLRow{eventTime: stamp, eventID: id, recordStart: uint64(i * 100), recordEnd: uint64(i*100 + 1)})
		want = append(want, id)
	}
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		start, err := time.Parse(time.RFC3339Nano, values.Get("start"))
		if err != nil {
			t.Errorf("窗口起点非法: %v", err)
			http.Error(w, "起点非法", http.StatusBadRequest)
			return
		}
		end, err := time.Parse(time.RFC3339Nano, values.Get("end"))
		if err != nil {
			t.Errorf("窗口终点非法: %v", err)
			http.Error(w, "终点非法", http.StatusBadRequest)
			return
		}
		limit := len(rows)
		if pipe := pipeLimitRe.FindStringSubmatch(values.Get("query")); len(pipe) == 2 {
			n, err := strconv.Atoi(pipe[1])
			if err != nil {
				t.Errorf("管道上限非法: %v", err)
				return
			}
			if n < limit {
				limit = n
			}
		}
		if raw := values.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				t.Errorf("HTTP 上限非法: %v", err)
				return
			}
			if n < limit {
				limit = n
			}
		}
		matched := make([]fakeVLRow, 0, len(rows))
		for _, row := range rows {
			if !row.eventTime.Before(start) && row.eventTime.Before(end) {
				matched = append(matched, row)
			}
		}
		switch {
		case strings.Contains(values.Get("query"), "| sort by (_time desc, log_source_id, source_generation, record_start desc, record_end desc, event_id)"):
			sort.SliceStable(matched, func(i, j int) bool { return query.LessSortKey(fakeSortKey(matched[i]), fakeSortKey(matched[j])) })
		case strings.Contains(values.Get("query"), "| sort by (_time desc)"):
			sort.SliceStable(matched, func(i, j int) bool { return matched[i].eventTime.After(matched[j].eventTime) })
		default:
			t.Errorf("夹具不支持的排序管道: %s", values.Get("query"))
			http.Error(w, "排序不支持", http.StatusBadRequest)
			return
		}
		if len(matched) > limit {
			matched = matched[:limit]
		}
		ids := make([]string, 0, len(matched))
		for _, row := range matched {
			ids = append(ids, row.eventID)
		}
		mu.Lock()
		calls = append(calls, fmt.Sprintf("end=%s take=%d HTTP-limit=%q 返回=%v", end.Format(time.RFC3339Nano), limit, values.Get("limit"), ids))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(w)
		for _, row := range matched {
			err := encoder.Encode(map[string]string{
				"_time": row.eventTime.Format(time.RFC3339Nano), "_msg": "m-" + row.eventID,
				"event_id": row.eventID, "log_source_id": "inst:7/stdout", "source_generation": "g1",
				"record_start": strconv.FormatUint(row.recordStart, 10), "record_end": strconv.FormatUint(row.recordEnd, 10),
				"level": "INFO", "stream": "stdout", "canonical_content_hash": "h-" + row.eventID,
			})
			if err != nil {
				t.Errorf("编码夹具失败: %v", err)
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(vl)
	if err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:7/stdout", UTCDay: day.Format("2006-01-02")}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion: "pv-1", CoverageComplete: true, QueryLocationDirID: "hot-g1", QueryGeneration: 1,
		ClosedVisibleSeq: map[string]uint64{key.String(): 1 << 40},
	}
	if err := cat.Put(rec); err != nil {
		t.Fatal(err)
	}
	req := query.QueryRequest{
		RequestID: "pr61-cursor", Budget: query.Budget{Limit: 5},
		TimeRange: query.TimeRange{FromUTC: day.Format(time.RFC3339Nano), ToUTC: day.Add(24 * time.Hour).Format(time.RFC3339Nano)},
	}
	return query.NewService(query.NewPlanner(cat, nil), client, "pr61-isolation"), req, want, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func pr61CheckAllCursorPages(t *testing.T, tied bool) {
	t.Helper()
	svc, req, want, calls := pr61CursorFixture(t, tied)
	var got []string
	var sizes []int
	var last query.SearchResponse
	for page := 1; page <= 8; page++ {
		last = svc.Search(context.Background(), req)
		if last.Err != nil {
			t.Fatalf("第 %d 页错误: %v", page, last.Err)
		}
		// 行上限可保持枚举开放；这里先核验跨页集合，不以当前粗筛预算标记掩盖漏行。
		ids := eventIDs(last.Items)
		sizes = append(sizes, len(ids))
		got = append(got, ids...)
		t.Logf("页=%d 返回=%v exhausted=%v next=%v complete=%v enumeration=%s", page, ids, last.Exhausted, last.NextCursor != "", last.Coverage.Complete, last.Coverage.EnumerationState)
		if last.Exhausted {
			break
		}
		if last.NextCursor == "" || last.View == nil {
			t.Fatal("非终页必须携带视图与游标")
		}
		req.View = &query.ViewRef{ViewID: last.View.ViewID, OrderVersion: query.SortVersion, Cursor: last.NextCursor}
	}
	for i, call := range calls() {
		t.Logf("底层请求=%d %s", i+1, call)
	}
	if !last.Exhausted || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(sizes, []int{5, 5, 5, 5, 1}) {
		t.Fatalf("固定视图分页必须覆盖 21 行且无重无漏: 得到 %d 行，页大小=%v，exhausted=%v，next=%v，期望 21 行与 [5 5 5 5 1]", len(got), sizes, last.Exhausted, last.NextCursor != "")
	}
}

// 对抗用例：大于单次 top-K 的同时间戳组必须全部可达。
func TestPR61ReproSameTimestampPagination(t *testing.T) {
	pr61CheckAllCursorPages(t, true)
}

// 反证控制：时间戳互异时，窗口收窄与额外一条前瞻应足以覆盖所有页。
func TestPR61ReproDistinctTimestampControl(t *testing.T) {
	pr61CheckAllCursorPages(t, false)
}
