package vlrange

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// The real engine must execute the narrowing pipe: a mock returning full rows
// for both requests cannot detect Search silently losing structured fields.
func TestRealVLSearchHydratesStructuredFields(t *testing.T) {
	ctx, _, _, vl := queryApprovedVL(t)
	client, err := New(vl)
	require.NoError(t, err)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	source := `inst:hydration/file`
	eventID := "event/\"quoted\"\\尾\nOR (*)"
	row := map[string]any{
		"_time": day.Add(12 * time.Hour).Format(time.RFC3339Nano), "_msg": "historical structured marker",
		"event_id": eventID, "log_source_id": source, "source_generation": "g1", "parser_version": "fixture/v1",
		"record_start": 4, "record_end": 5, "canonical_content_hash": "hash-structured",
		"level": "INFO", "stream": "file", "trace_id": "trace-history-1", "latency_ms": 42.5,
		"custom.attribute": "kept", "projection_generation": "historical-copy",
	}
	body, err := json.Marshal(row)
	require.NoError(t, err)
	_, err = vl.InsertJSONLines(ctx, append(body, '\n'))
	require.NoError(t, err)
	_, err = vl.Post(ctx, "/internal/force_flush", nil)
	require.NoError(t, err)
	rng := query.AuthoritativeRange{
		Key:   catalog.PartitionKey{StorageNamespace: "inst:hydration", UTCDay: day.Format("2006-01-02")},
		Owner: catalog.OwnerHot, ClosedVisibleSeq: 5,
		Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{{
			SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: source, SourceGeneration: "g1"}, ClosedVisibleSeq: 5,
		}}},
	}
	q := query.RangeQuery{Budget: query.Budget{Limit: 10, MaxBytes: 1 << 20}, ClosedVisibleSeq: 5, Filter: `level:="INFO"`}
	params, empty, err := client.params(rng, q)
	require.NoError(t, err)
	require.False(t, empty)
	params.Set("query", searchQuery(rng, q))
	params.Set("limit", "11")
	var projected []logtypes.Event
	_, cut, err := client.stream(ctx, params, 1<<20, 0, func(line []byte) error {
		ev, decodeErr := eventFromJSON(line)
		projected = append(projected, ev)
		return decodeErr
	})
	require.NoError(t, err)
	require.False(t, cut)
	require.Len(t, projected, 1)
	require.NotContains(t, projected[0].Fields, "trace_id", "real VL fields pipe must actually prune the fixture")
	require.NotContains(t, projected[0].Fields, "latency_ms")
	t.Log("real VL fixed canonical projection pruned trace_id and latency_ms before hydration")

	for _, merged := range []bool{false, true} {
		name := "single"
		if merged {
			name = "merged"
		}
		t.Run(name, func(t *testing.T) {
			var result query.RangeResult
			if merged {
				result, err = client.SearchMerged(ctx, []query.AuthoritativeRange{rng}, q)
			} else {
				result, err = client.Search(ctx, rng, q)
			}
			require.NoError(t, err)
			require.Len(t, result.Items, 1)
			require.Equal(t, eventID, result.Items[0].EventID)
			require.Equal(t, "trace-history-1", result.Items[0].Fields["trace_id"])
			require.Equal(t, "42.5", result.Items[0].Fields["latency_ms"])
			require.Equal(t, "kept", result.Items[0].Fields["custom.attribute"])
			require.Equal(t, "historical-copy", result.Items[0].Fields["projection_generation"])
			require.Empty(t, result.CoverageReasons)
			require.Equal(t, query.StatsExact, result.StatsQuality)
			t.Logf("structured fields survived %s: trace_id=%s latency_ms=%s bytes=%d", name, result.Items[0].Fields["trace_id"], result.Items[0].Fields["latency_ms"], result.Bytes)
		})
	}
}

// Both public paths must hydrate the selected prefix after fetching a tie group
// larger than merged top-K. Same event_id copies outside source/generation,
// watermark, user filter, or HTTP time scope must never provide custom fields.
func TestRealVLSearchHydrationTiedCursorScopeIsolation(t *testing.T) {
	ctx, _, _, vl := queryApprovedVL(t)
	client, err := New(vl)
	require.NoError(t, err)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	at := day.Add(12*time.Hour + 123*time.Nanosecond)
	var body strings.Builder
	for i := 0; i < 304; i++ {
		body.Write(hydrationFixtureLine(t, hydrationFixtureRow(i, at), false))
	}
	for _, dimension := range []string{"source", "generation", "watermark", "filter", "time"} {
		row := hydrationFixtureRow(303, at)
		row["trace_id"] = "secret-outside-" + dimension
		switch dimension {
		case "source":
			row["log_source_id"] = "inst:hydration-other/file"
		case "generation":
			row["source_generation"] = "old"
		case "watermark":
			row["record_end"] = 2000
		case "filter":
			row["level"] = "WARN"
		case "time":
			row["_time"] = at.Add(time.Nanosecond).Format(time.RFC3339Nano)
		}
		body.Write(hydrationFixtureLine(t, row, false))
	}
	_, err = vl.InsertJSONLines(ctx, []byte(body.String()))
	require.NoError(t, err)
	_, err = vl.Post(ctx, "/internal/force_flush", nil)
	require.NoError(t, err)
	rng := hydrationFixtureRange(day)
	for _, merged := range []bool{false, true} {
		name := "single"
		if merged {
			name = "merged"
		}
		t.Run(name, func(t *testing.T) {
			q := query.RangeQuery{Budget: query.Budget{Limit: 5, MaxBytes: 4 << 20}, ClosedVisibleSeq: 1000, Filter: `level:="INFO"`,
				TimeRange: query.TimeRange{FromUTC: at.Add(-time.Nanosecond).Format(time.RFC3339Nano), ToUTC: at.Add(time.Nanosecond).Format(time.RFC3339Nano)}}
			var ids []string
			for page := 0; page < 2; page++ {
				var result query.RangeResult
				if merged {
					result, err = client.SearchMerged(ctx, []query.AuthoritativeRange{rng}, q)
				} else {
					result, err = client.Search(ctx, rng, q)
				}
				require.NoError(t, err)
				require.Empty(t, result.CoverageReasons)
				require.False(t, result.Exhausted)
				require.GreaterOrEqual(t, len(result.Items), 5)
				require.LessOrEqual(t, len(result.Items), int(lookaheadRows(q)))
				for _, ev := range result.Items[:5] {
					require.NotContains(t, ids, ev.EventID, "cursor refill of a 304-row tie must not repeat page-one events")
					ids = append(ids, ev.EventID)
					require.Equal(t, strings.Replace(ev.EventID, "event-", "trace-", 1), ev.Fields["trace_id"])
					require.Equal(t, "42.5", ev.Fields["latency_ms"])
				}
				cursor := query.SortKeyOf(result.Items[4])
				q.CursorSortKey = &cursor
				t.Logf("real %s page=%d tie=304 selected=%d fields=complete bytes=%d", name, page+1, len(result.Items), result.Bytes)
			}
			require.Equal(t, []string{"event-0303", "event-0302", "event-0301", "event-0300", "event-0299", "event-0298", "event-0297", "event-0296", "event-0295", "event-0294"}, ids)
		})
	}
}
