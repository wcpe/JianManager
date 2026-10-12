package vlrange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 同戳组按合法但任意的时间并列顺序返回，每次请求反转，不能依赖 VL 的隐含次序。
func pr61BoundaryService(t *testing.T, n int, merged, reverse bool) (*query.Service, query.QueryRequest, []string, func() []fakeVLWindow) {
	t.Helper()
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	stamp := day.Add(10 * time.Hour)
	var rows []logtypes.Event
	for i := 1; i <= n; i++ {
		source, gen := "src-a", "g1"
		if merged {
			if i%2 == 0 {
				source = "src-b"
			}
			if i%3 == 0 {
				gen = "g2"
			}
		}
		rows = append(rows, logtypes.Event{EventID: fmt.Sprintf("e%05d", i), EventTimeUTC: stamp.Format(time.RFC3339Nano), CanonicalHash: fmt.Sprint(i), Source: logtypes.SourceIdentity{LogSourceID: source, SourceGeneration: gen}, Record: logtypes.RecordRange{Start: uint64(i), End: uint64(i + 1)}, Fields: map[string]string{projectionGenerationField: "p1"}})
	}
	wantRows := append([]logtypes.Event(nil), rows...)
	query.SortEvents(wantRows)
	want := eventIDs(wantRows)
	if merged {
		// 相同时间的旧投影、水位外行和错误代次不得借边界补查重新进入可见面。
		for _, bad := range []logtypes.Event{
			{EventID: "bad-projection", Source: logtypes.SourceIdentity{LogSourceID: "src-a", SourceGeneration: "g1"}, Fields: map[string]string{projectionGenerationField: "old"}},
			{EventID: "bad-generation", Source: logtypes.SourceIdentity{LogSourceID: "src-a", SourceGeneration: "old"}, Fields: map[string]string{projectionGenerationField: "p1"}},
			{EventID: "bad-watermark", Source: logtypes.SourceIdentity{LogSourceID: "src-b", SourceGeneration: "g2"}, Record: logtypes.RecordRange{End: 1 << 30}, Fields: map[string]string{projectionGenerationField: "p1"}},
		} {
			bad.EventTimeUTC = stamp.Format(time.RFC3339Nano)
			rows = append(rows, bad)
		}
	}
	var mu sync.Mutex
	var windows []fakeVLWindow
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query()
		start, err := time.Parse(time.RFC3339Nano, p.Get("start"))
		require.NoError(t, err)
		end, err := time.Parse(time.RFC3339Nano, p.Get("end"))
		require.NoError(t, err)
		require.Contains(t, p.Get("query"), "| fields")
		require.Contains(t, p.Get("query"), "| sort by (_time desc)")
		require.NotContains(t, p.Get("query"), "sort by (_time desc,")
		limit, err := strconv.Atoi(p.Get("limit"))
		require.NoError(t, err)
		mu.Lock()
		ordinal := len(windows)
		windows = append(windows, fakeVLWindow{start: start, end: end, limit: limit})
		mu.Unlock()
		matched := append([]logtypes.Event(nil), rows...)
		if reverse != (ordinal%2 == 0) {
			sort.SliceStable(matched, func(i, j int) bool {
				return query.LessSortKey(query.SortKeyOf(matched[i]), query.SortKeyOf(matched[j]))
			})
		} else {
			sort.SliceStable(matched, func(i, j int) bool {
				return query.LessSortKey(query.SortKeyOf(matched[j]), query.SortKeyOf(matched[i]))
			})
		}
		enc := json.NewEncoder(w)
		for _, ev := range matched {
			et, _ := time.Parse(time.RFC3339Nano, ev.EventTimeUTC)
			if et.Before(start) || !et.Before(end) {
				continue
			}
			if limit == 0 {
				break
			}
			limit--
			require.NoError(t, enc.Encode(map[string]string{"_time": ev.EventTimeUTC, "_msg": "message", "event_id": ev.EventID, "log_source_id": ev.Source.LogSourceID, "source_generation": ev.Source.SourceGeneration, "record_start": fmt.Sprint(ev.Record.Start), "record_end": fmt.Sprint(ev.Record.End), "canonical_content_hash": ev.CanonicalHash, projectionGenerationField: ev.Fields[projectionGenerationField]}))
		}
	}))
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	cat := catalog.New(nil)
	sources := []string{"src-a"}
	if merged {
		sources = append(sources, "src-b")
	}
	for _, source := range sources {
		key := catalog.PartitionKey{StorageNamespace: source, UTCDay: day.Format("2006-01-02")}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
		rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "pv-1", CoverageComplete: true, QueryLocationDirID: "hot-1", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 1 << 20}}
		if merged {
			for _, gen := range []string{"g1", "g2"} {
				rec.PublishedProjection.SourceProjections = append(rec.PublishedProjection.SourceProjections, catalog.SourceProjection{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: source, SourceGeneration: gen}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 1 << 20})
			}
		}
		require.NoError(t, cat.Put(rec))
	}
	limit := uint32(5)
	if merged {
		limit = 100
	}
	if n > 2000 {
		limit = 1000
	}
	req := query.QueryRequest{Budget: query.Budget{Limit: limit}, TimeRange: query.TimeRange{FromUTC: day.Format(time.RFC3339Nano), ToUTC: day.Add(24 * time.Hour).Format(time.RFC3339Nano)}}
	return query.NewService(query.NewPlanner(cat, nil), client, "pr61"), req, want, func() []fakeVLWindow { mu.Lock(); defer mu.Unlock(); return append([]fakeVLWindow(nil), windows...) }
}

func TestPR61BoundaryGroupsArbitraryOrder(t *testing.T) {
	for _, n := range []int{21, 301, 2101} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", n, reverse), func(t *testing.T) {
				svc, req, want, windows := pr61BoundaryService(t, n, n > 21, reverse)
				var got []string
				exhausted := false
				for page := 0; page < 10; page++ {
					resp := svc.Search(context.Background(), req)
					require.Nil(t, resp.Err)
					require.True(t, resp.Coverage.Complete, "正常页上限不能冒充扫描预算cut")
					require.Equal(t, query.StatsExact, resp.Quality.StatsQuality)
					got = append(got, eventIDs(resp.Items)...)
					if resp.Exhausted {
						exhausted = true
						break
					}
					require.Len(t, resp.Items, int(req.Budget.Limit))
					require.NotEmpty(t, resp.NextCursor)
					req.View = &query.ViewRef{ViewID: resp.View.ViewID, OrderVersion: query.SortVersion, Cursor: resp.NextCursor}
				}
				require.True(t, exhausted)
				require.Equal(t, want, got, "任意合法并列顺序跨页无漏无重")
				var narrow int
				for _, window := range windows() {
					if window.end.Sub(window.start) == time.Nanosecond {
						narrow++
					}
				}
				require.Positive(t, narrow, "只补边界时间组")
			})
		}
	}
}

func TestPR61BoundaryBudgetCannotAdvance(t *testing.T) {
	svc, req, _, windows := pr61BoundaryService(t, 301, true, true)
	req.Budget.MaxBytes = 128 << 10
	// 初查足够读满 300 行，剩余额度不足完整重读同戳组。
	resp := svc.Search(context.Background(), req)
	require.False(t, resp.Coverage.Complete)
	require.Contains(t, resp.Coverage.PartialReasons, query.ReasonBudgetExceeded)
	require.False(t, resp.Exhausted)
	require.Empty(t, resp.NextCursor, "不得越过未证明完整的组")
	require.Empty(t, resp.Items, "该组内任意部分都不能当安全前缀")
	require.GreaterOrEqual(t, len(windows()), 2)
}

// 高时间分辨率控制：互异时间页不额外补查，保留正常一次查询路径。
func TestPR61DistinctQueryHasOneRequestPerPage(t *testing.T) {
	svc, req, _, calls := pr61CursorFixture(t, false)
	resp := svc.Search(context.Background(), req)
	require.Nil(t, resp.Err)
	require.True(t, resp.Coverage.Complete)
	require.Len(t, calls(), 1)
	require.False(t, strings.Contains(calls()[0], "take=21"))
}
