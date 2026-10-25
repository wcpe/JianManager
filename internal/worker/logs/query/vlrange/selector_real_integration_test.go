package vlrange

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

type selectorEngineFixture struct {
	row   selectorCandidate
	id    string
	at    time.Time
	level string
}

func selectorLegacySearch(ctx context.Context, client *Client, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	ctx, cancel := query.WithQueryDeadline(ctx, q.Budget)
	defer cancel()
	ctx = query.WithPhysicalReadBudget(ctx, q.Budget)
	params, empty, err := client.params(rng, q)
	if err != nil || empty {
		return query.RangeResult{Exhausted: true, DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}, err
	}
	pipe := strings.TrimPrefix(searchQuery(rng, q), selectorQuery(rng, q))
	params.Set("query", selectorBeforeFactoring(rng, q)+pipe)
	params.Set("limit", strconv.FormatUint(lookaheadRows(q), 10))
	return client.searchPage(ctx, params, q, lookaheadRows(q), func(event logtypes.Event) bool {
		return rng.Projection != nil && len(rng.Projection.SourceProjections) > 0 && q.ClosedVisibleSeq == 0 || event.Record.End <= q.ClosedVisibleSeq
	})
}

// 原、新谓词都交给批准真引擎；完整候选行集包含交叉代次、各水位两侧和相邻时间点。
// 沿用隔离根、随机回环端口的现有 helper，不接触生产或提高引擎查询上限。
func TestRealVLSelectorFactoringSetOrderAndPages(t *testing.T) {
	ctx, _, _, hot, _ := queryApprovedVL(t, false)
	client, err := New(hot)
	require.NoError(t, err)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	from, to := day.Add(12*time.Hour), day.Add(13*time.Hour)
	rng := selectorSixtySources(true)
	rng.Key.UTCDay = day.Format("2006-01-02")
	// 原60源完整保留；负样本和重复scope只能追加，不能靠减少来源获得通过。
	rng.Projection.SourceProjections = append(rng.Projection.SourceProjections,
		selectorScope(`node:标点/子"\路径`, "fixture-1", 26052, "projection-1"),
		selectorScope("node:1/bench-000/extra", "fixture-1", 26052, "projection-1"),
		selectorScope("(a) OR (*)", "fixture-1", 26052, "projection-1"),
		selectorScope("node:1/bench-000", "fixture-2", 1, "projection-2"),
		selectorScope("node:1/bench-000", "fixture-1", 2, "projection-2"),
		selectorScope("zero-prefix", "fixture-1", 0, "projection-1"),
		rng.Projection.SourceProjections[0])
	scopes := rng.Projection.SourceProjections
	sources := make(map[string]bool)
	for _, scope := range scopes {
		sources[scope.LogSourceID] = true
	}
	sources["node:1/bench-000x"] = true
	sourceNames := make([]string, 0, len(sources))
	for source := range sources {
		sourceNames = append(sourceNames, source)
	}
	sort.Strings(sourceNames)
	var fixtures []selectorEngineFixture
	var body strings.Builder
	for _, source := range sourceNames {
		for _, generation := range []string{"fixture-1", "fixture-2"} {
			for _, projection := range []string{"projection-1", "projection-2", ""} {
				for _, end := range []uint64{0, 1, 2, 26052, 26053, 27396, 27397} {
					index := len(fixtures)
					at := []time.Time{from.Add(-time.Nanosecond), from, from.Add(time.Minute), to.Add(-time.Nanosecond), to}[index%5]
					level := []string{"INFO", "ERROR"}[index%2]
					id := fmt.Sprintf("selector-%05d", index)
					fixture := selectorEngineFixture{row: selectorCandidate{source, generation, projection, end}, id: id, at: at, level: level}
					fixtures = append(fixtures, fixture)
					row := map[string]any{"_time": at.Format(time.RFC3339Nano), "_msg": id, "event_id": id,
						"log_source_id": source, "source_generation": generation, "projection_generation": projection,
						"record_start": uint64(0), "record_end": end, "level": level, "stream": "stdout",
						"canonical_content_hash": "hash-" + id, "parser_version": "selector/v1", "instance_id": "9"}
					encoded, marshalErr := json.Marshal(row)
					require.NoError(t, marshalErr)
					body.Write(encoded)
					body.WriteByte('\n')
				}
			}
		}
	}
	_, err = hot.InsertJSONLines(ctx, []byte(body.String()))
	require.NoError(t, err)
	_, err = hot.Post(ctx, "/internal/force_flush", nil)
	require.NoError(t, err)
	t.Logf("固定候选全集=%d 唯一来源=%d 原始scope=%d NDJSON字节=%d SHA256=%x 含精确标点、前缀邻接、跨代次、零水位和半开时间边界", len(fixtures), len(sourceNames), len(scopes), body.Len(), sha256.Sum256([]byte(body.String())))

	unknown := rng
	unknownProjection := *rng.Projection
	unknownProjection.SourceProjections = []catalog.SourceProjection{selectorScope(scopes[0].LogSourceID, "fixture-1", 27396)}
	unknown.Projection = &unknownProjection
	for _, tc := range []struct {
		name   string
		rangeQ query.AuthoritativeRange
		cutoff uint64
		filter string
	}{
		{"scoped-per-source", rng, 0, ""},
		{"global-cutoff-and-filter", rng, 26052, `level:="INFO"`},
		{"unknown-projection-deny", unknown, 0, ""},
		{"legacy-zero-prefix", query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: scopes[0].LogSourceID, UTCDay: rng.Key.UTCDay}}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := query.RangeQuery{ClosedVisibleSeq: tc.cutoff, Filter: tc.filter,
				TimeRange: query.TimeRange{FromUTC: from.Format(time.RFC3339Nano), ToUTC: to.Format(time.RFC3339Nano)},
				Budget:    query.Budget{Limit: 19, MaxBytes: 32 << 20, TimeoutMS: 10000}}
			var wantIDs []string
			for _, fixture := range fixtures {
				if selectorOracle(tc.rangeQ, q, fixture.row) && !fixture.at.Before(from) && fixture.at.Before(to) && (q.Filter == "" || fixture.level == "INFO") {
					wantIDs = append(wantIDs, fixture.id)
				}
			}
			sort.Strings(wantIDs)
			if wantIDs == nil {
				wantIDs = []string{}
			}
			var beforeSet []logtypes.Event
			for _, selector := range []string{selectorBeforeFactoring(tc.rangeQ, q), selectorQuery(tc.rangeQ, q)} {
				params, empty, paramsErr := client.params(tc.rangeQ, q)
				require.NoError(t, paramsErr)
				require.False(t, empty)
				text := selector + " | sort by (_time desc) | limit 10000"
				require.True(t, physicalQueryFits(text, q.Filter), "保留原12KiB物理字节界")
				params.Set("query", text)
				params.Set("limit", "10000")
				var events []logtypes.Event
				_, cut, streamErr := client.stream(ctx, params, 32<<20, 10000, func(line []byte) error {
					event, decodeErr := eventFromJSON(line)
					if decodeErr == nil {
						events = append(events, event)
					}
					return decodeErr
				})
				require.NoError(t, streamErr)
				require.False(t, cut)
				ids := make([]string, 0, len(events))
				for _, event := range events {
					ids = append(ids, event.EventID)
				}
				sort.Strings(ids)
				require.Equal(t, wantIDs, ids, "真引擎命中集合必须与结构化真值逐条相等")
				query.SortEvents(events)
				if beforeSet == nil {
					beforeSet = events
				} else {
					require.Equal(t, beforeSet, events, "新旧完整行字段与冻结复合序一致")
				}
			}
			var allIDs []string
			pages := 0
			for ; pages < 200; pages++ {
				before, beforeErr := selectorLegacySearch(ctx, client, tc.rangeQ, q)
				after, afterErr := client.Search(ctx, tc.rangeQ, q)
				require.NoError(t, beforeErr)
				require.NoError(t, afterErr)
				require.Equal(t, before.Items, after.Items, "同一页完整复合序必须一致")
				require.Equal(t, before.Exhausted, after.Exhausted)
				require.Equal(t, before.CoverageReasons, after.CoverageReasons)
				require.Equal(t, before.SafeBeforeUTC, after.SafeBeforeUTC)
				require.Equal(t, before.DuplicateQuality, after.DuplicateQuality)
				require.Equal(t, before.StatsQuality, after.StatsQuality)
				require.Empty(t, after.CoverageReasons)
				require.Equal(t, query.DupExact, after.DuplicateQuality)
				require.Equal(t, query.StatsExact, after.StatsQuality)
				items := after.Items
				if len(items) > int(q.Budget.Limit) {
					items = items[:q.Budget.Limit]
				}
				for _, event := range items {
					allIDs = append(allIDs, event.EventID)
				}
				if len(items) == 0 || after.Exhausted && len(after.Items) <= int(q.Budget.Limit) {
					break
				}
				cursor := query.SortKeyOf(items[len(items)-1])
				q.CursorSortKey = &cursor
			}
			require.Less(t, pages, 200, "分页必须自然终止")
			sort.Strings(allIDs)
			if allIDs == nil {
				allIDs = []string{}
			}
			require.Equal(t, wantIDs, allIDs, "并列时间组大于页长时跨页仍必须不重不漏")
			t.Logf("候选全集=%d 命中=%d 页=%d 页长=19 新旧完整字段/排序/游标/质量逐页相等", len(fixtures), len(wantIDs), pages+1)
		})
	}
}
