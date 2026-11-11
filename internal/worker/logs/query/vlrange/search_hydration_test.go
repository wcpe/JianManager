package vlrange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func isSearchHydrationRequest(r *http.Request) bool {
	return strings.Contains(r.URL.Query().Get("query"), "event_id:=") && !strings.Contains(r.URL.Query().Get("query"), " | fields ")
}

var fixtureHydrationEventIDRE = regexp.MustCompile(`event_id:=("(?:[^"\\]|\\.)*")`)

func searchFixtureHydrationIDs(t *testing.T, r *http.Request) map[string]bool {
	t.Helper()
	ids := make(map[string]bool)
	for _, match := range fixtureHydrationEventIDRE.FindAllStringSubmatch(r.URL.Query().Get("query"), -1) {
		id, err := strconv.Unquote(match[1])
		require.NoError(t, err)
		ids[id] = true
	}
	return ids
}

// Older fixtures model top-K and scopes separately. Their hydration branch must
// still honor the complete identity instead of returning unrelated static rows.
func searchFixtureLineMatches(t *testing.T, r *http.Request, line []byte) bool {
	t.Helper()
	if !isSearchHydrationRequest(r) {
		return true
	}
	ev, err := eventFromJSON(line)
	require.NoError(t, err)
	id, err := hydrationIdentity(ev)
	require.NoError(t, err)
	return strings.Contains(r.URL.Query().Get("query"), id.selector())
}

func writeSearchFixtureNDJSON(t *testing.T, w http.ResponseWriter, r *http.Request, body string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line != "" && searchFixtureLineMatches(t, r, []byte(line)) {
			_, err := fmt.Fprintln(w, line)
			require.NoError(t, err)
		}
	}
}

func hydrationFixtureRow(i int, at time.Time) map[string]any {
	return map[string]any{"_time": at.Format(time.RFC3339Nano), "_msg": "same search marker", "event_id": fmt.Sprintf("event-%04d", i),
		"log_source_id": "inst:hydration/file", "source_generation": "g1", "parser_version": "fixture/v1",
		"record_start": i * 2, "record_end": i*2 + 1, "canonical_content_hash": fmt.Sprintf("hash-%04d", i),
		"level": "INFO", "stream": "file", "trace_id": fmt.Sprintf("trace-%04d", i), "latency_ms": 42.5}
}

func hydrationFixtureLine(t *testing.T, row map[string]any, projected bool) []byte {
	t.Helper()
	if projected {
		narrow := make(map[string]any)
		for _, key := range strings.Fields("_time _msg event_id log_source_id source_generation parser_version record_start record_end ingest_time_utc level stream instance_id canonical_content_hash") {
			if value, ok := row[key]; ok {
				narrow[key] = value
			}
		}
		row = narrow
	}
	line, err := json.Marshal(row)
	require.NoError(t, err)
	return append(line, '\n')
}

func hydrationFixtureRange(day time.Time) query.AuthoritativeRange {
	return query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "inst:hydration", UTCDay: day.Format("2006-01-02")},
		Owner: catalog.OwnerHot, ClosedVisibleSeq: 1000,
		Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{{
			SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "inst:hydration/file", SourceGeneration: "g1"}, ClosedVisibleSeq: 1000,
		}}}}
}

func newHydrationFixtureClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	return client
}

func TestSearchHydrationOnlyReadsBoundedFinalTiedCursorItems(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	at := day.Add(10 * time.Hour)
	for _, merged := range []bool{false, true} {
		t.Run(fmt.Sprint(merged), func(t *testing.T) {
			var selected, hydrated int
			var hydratedIDs []string
			client := newHydrationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.Query()
				require.Equal(t, `level:="INFO"`, p.Get("extra_filters"))
				require.Contains(t, p.Get("query"), `log_source_id`)
				limit, err := strconv.Atoi(p.Get("limit"))
				require.NoError(t, err)
				if isSearchHydrationRequest(r) {
					hydrated++
					require.NotContains(t, p.Get("query"), " | sort ")
					require.Contains(t, p.Get("query"), "record_end:<=1000")
				} else {
					selected++
					require.Contains(t, p.Get("query"), " | fields ")
					require.Contains(t, p.Get("query"), " | sort by (_time desc) | limit ")
				}
				for i := 0; i < 320 && limit > 0; i++ {
					row := hydrationFixtureRow(i, at)
					line := hydrationFixtureLine(t, row, !isSearchHydrationRequest(r))
					if !searchFixtureLineMatches(t, r, line) {
						continue
					}
					if isSearchHydrationRequest(r) {
						hydratedIDs = append(hydratedIDs, row["event_id"].(string))
					}
					_, _ = w.Write(line)
					limit--
				}
			})
			rng := hydrationFixtureRange(day)
			q := query.RangeQuery{Budget: query.Budget{Limit: 5, MaxBytes: 4 << 20}, ClosedVisibleSeq: 1000, Filter: `level:="INFO"`}
			for page := 0; page < 2; page++ {
				hydratedIDs = nil
				var result query.RangeResult
				var err error
				if merged {
					result, err = client.SearchMerged(context.Background(), []query.AuthoritativeRange{rng}, q)
				} else {
					result, err = client.Search(context.Background(), rng, q)
				}
				require.NoError(t, err)
				require.Empty(t, result.CoverageReasons)
				require.False(t, result.Exhausted)
				require.LessOrEqual(t, len(result.Items), int(lookaheadRows(q)), "complete 320-row tie must be narrowed before full-width reads")
				require.ElementsMatch(t, eventIDs(result.Items), hydratedIDs, "no scanned or discarded candidate may be hydrated")
				for _, ev := range result.Items {
					require.Equal(t, strings.Replace(ev.EventID, "event-", "trace-", 1), ev.Fields["trace_id"])
					require.Equal(t, "42.5", ev.Fields["latency_ms"])
				}
				cursor := query.SortKeyOf(result.Items[4])
				q.CursorSortKey = &cursor
			}
			require.Equal(t, 4, selected, "each page keeps a projected initial top-K and one projected tie-group read")
			require.Equal(t, 2, hydrated, "only the final page prefixes need full fields")
		})
	}
}

func TestSearchHydrationRejectsMissingOrChangedIdentityAndHTTPFailure(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	for _, change := range []string{"missing", "source", "generation", "parser", "record", "time", "hash", "message", "http", "decode"} {
		t.Run(change, func(t *testing.T) {
			row := hydrationFixtureRow(2, day.Add(time.Hour))
			client := newHydrationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if !isSearchHydrationRequest(r) {
					_, _ = w.Write(hydrationFixtureLine(t, row, true))
					return
				}
				switch change {
				case "missing":
					return
				case "http":
					http.Error(w, "fields unavailable", http.StatusInternalServerError)
					return
				case "decode":
					_, _ = w.Write([]byte("not-json\n"))
					return
				case "source":
					row["log_source_id"] = "inst:other/file"
				case "generation":
					row["source_generation"] = "g2"
				case "parser":
					row["parser_version"] = "other/v1"
				case "record":
					row["record_end"] = 6
				case "time":
					row["_time"] = day.Add(2 * time.Hour).Format(time.RFC3339Nano)
				case "hash":
					row["canonical_content_hash"] = "different"
				case "message":
					row["_msg"] = "different"
				}
				_, _ = w.Write(hydrationFixtureLine(t, row, false))
			})
			result, err := client.Search(context.Background(), hydrationFixtureRange(day), query.RangeQuery{Budget: query.Budget{Limit: 10, MaxBytes: 1 << 20}, ClosedVisibleSeq: 1000})
			require.Error(t, err)
			require.Contains(t, result.CoverageReasons, query.ReasonRangeUnavailable)
			require.Equal(t, query.StatsPartial, result.StatsQuality)
			require.False(t, result.Exhausted)
			require.Len(t, result.Items, 1)
			require.NotContains(t, result.Items[0].Fields, "trace_id", "unverified full-width rows must never be attached by event_id alone")
		})
	}
}

func TestSearchHydrationSharesBytesFanoutAndDeadline(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	row := hydrationFixtureRow(2, day.Add(time.Hour))
	projected := hydrationFixtureLine(t, row, true)
	for _, remaining := range []uint64{0, 20, 1 << 20} {
		t.Run(fmt.Sprint(remaining), func(t *testing.T) {
			var calls int
			var actual uint64
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			client := pr61ByteClient(t, func(r *http.Request) (*http.Response, error) {
				gotDeadline, hasDeadline := r.Context().Deadline()
				require.True(t, hasDeadline)
				require.Equal(t, deadline, gotDeadline, "hydration must not extend the original request deadline")
				calls++
				line := projected
				if isSearchHydrationRequest(r) {
					line = hydrationFixtureLine(t, row, false)
				}
				return &http.Response{StatusCode: 200, Body: &hydrationCountingBody{Reader: strings.NewReader(string(line)), read: &actual}}, nil
			})
			result, err := client.Search(ctx, hydrationFixtureRange(day), query.RangeQuery{Budget: query.Budget{Limit: 10, MaxBytes: uint64(len(projected)) + remaining}, ClosedVisibleSeq: 1000})
			require.NoError(t, err)
			require.Equal(t, actual, result.Bytes)
			require.LessOrEqual(t, result.Bytes, uint64(len(projected))+remaining)
			if remaining == 1<<20 {
				require.Equal(t, 2, calls)
				require.Equal(t, "trace-0002", result.Items[0].Fields["trace_id"])
				require.Empty(t, result.CoverageReasons)
			} else {
				require.Contains(t, result.CoverageReasons, query.ReasonBudgetExceeded)
				require.Equal(t, query.StatsPartial, result.StatsQuality)
				require.False(t, result.Exhausted)
				if remaining == 0 {
					require.Equal(t, 1, calls, "zero remaining bytes cannot become the default scan budget")
				}
			}
		})
	}
	var calls int
	client := newHydrationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write(projected)
	})
	result, err := client.Search(context.Background(), hydrationFixtureRange(day), query.RangeQuery{Budget: query.Budget{Limit: 10, MaxBytes: 1 << 20, MaxFanout: 1}, ClosedVisibleSeq: 1000})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Contains(t, result.CoverageReasons, query.ReasonBudgetExceeded)
	require.False(t, result.Exhausted)
}

type hydrationCountingBody struct {
	*strings.Reader
	read *uint64
}

func (b *hydrationCountingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	*b.read += uint64(n)
	return n, err
}

func (b *hydrationCountingBody) Close() error { return nil }

func TestSearchHydrationSplitsActualPhysicalTextAndEncodedRequest(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	var rows []map[string]any
	for i := 0; i < 48; i++ {
		row := hydrationFixtureRow(i, day.Add(time.Hour+time.Duration(i)*time.Second))
		row["event_id"] = fmt.Sprintf("event-%04d-", i) + strings.Repeat("\"\\尾\nOR (*)", 24)
		rows = append(rows, row)
	}
	var batches, hydrated int
	var wireBytes uint64
	client := newHydrationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query()
		if isSearchHydrationRequest(r) {
			batches++
			require.True(t, physicalQueryFits(p.Get("query"), p.Get("extra_filters")))
			require.LessOrEqual(t, len("GET "+r.URL.RequestURI()+" HTTP/1.1\r\n"), physicalRequestLineMaxBytes)
			require.Equal(t, `level:="INFO"`, p.Get("extra_filters"))
			require.Contains(t, p.Get("query"), `record_end:<=1000`)
		}
		for _, row := range rows {
			line := hydrationFixtureLine(t, row, !isSearchHydrationRequest(r))
			if !searchFixtureLineMatches(t, r, line) {
				continue
			}
			if isSearchHydrationRequest(r) {
				hydrated++
			}
			n, _ := w.Write(line)
			wireBytes += uint64(n)
		}
	})
	result, err := client.Search(context.Background(), hydrationFixtureRange(day), query.RangeQuery{Budget: query.Budget{Limit: 100, MaxBytes: 4 << 20}, Filter: `level:="INFO"`, ClosedVisibleSeq: 1000})
	require.NoError(t, err)
	require.Empty(t, result.CoverageReasons)
	require.Len(t, result.Items, len(rows))
	require.Equal(t, len(rows), hydrated)
	require.Greater(t, batches, 1)
	require.Equal(t, wireBytes, result.Bytes, "all full-width batches and the canonical top-K share one byte account")
	for _, ev := range result.Items {
		require.NotEmpty(t, ev.Fields["trace_id"])
	}
}

func TestMergedHydrationSelectorKeepsPerRangeGenerationAndWatermark(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	rng := hydrationFixtureRange(day)
	rng.ClosedVisibleSeq = 8
	rng.Projection.SourceProjections[0].ClosedVisibleSeq = 10
	ev, err := eventFromJSON(hydrationFixtureLine(t, hydrationFixtureRow(2, day.Add(time.Hour)), true))
	require.NoError(t, err)
	selector := mergedHydrationSelector([]query.AuthoritativeRange{rng}, query.RangeQuery{ClosedVisibleSeq: 999})(ev)
	require.Contains(t, selector, `source_generation:="g1"`)
	require.Contains(t, selector, "record_end:<=8")
	require.Contains(t, selector, "record_end:<=10")
	require.NotContains(t, selector, "999")
	params := url.Values{"query": {"ignored"}, "extra_filters": {`level:="INFO"`}}
	id, err := hydrationIdentity(ev)
	require.NoError(t, err)
	batches, err := splitHydrationTerms([]string{"((" + selector + ") AND " + id.selector() + ")"}, params)
	require.NoError(t, err)
	require.Len(t, batches, 1)
}

func TestSearchHydrationOversizedIdentityFailsWithoutUnboundedQuery(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	row := hydrationFixtureRow(2, day.Add(time.Hour))
	row["event_id"] = strings.Repeat("long-identity", physicalMaxQueryBytes)
	var calls int
	client := newHydrationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.False(t, isSearchHydrationRequest(r), "an indivisible identity must fail before sending an oversized query")
		_, _ = w.Write(hydrationFixtureLine(t, row, true))
	})
	result, err := client.Search(context.Background(), hydrationFixtureRange(day), query.RangeQuery{Budget: query.Budget{Limit: 10, MaxBytes: 1 << 20}, ClosedVisibleSeq: 1000})
	require.ErrorIs(t, err, query.ErrRangeQueryBudget)
	require.Equal(t, 1, calls)
	require.Positive(t, result.Bytes)
	require.Contains(t, result.CoverageReasons, query.ReasonBudgetExceeded)
	require.Equal(t, query.StatsPartial, result.StatsQuality)
}

func TestSearchHydrationHTTPErrorBodyKeepsPhysicalByteAccount(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	row := hydrationFixtureRow(2, day.Add(time.Hour))
	projected := hydrationFixtureLine(t, row, true)
	var actual uint64
	client := pr61ByteClient(t, func(r *http.Request) (*http.Response, error) {
		code, body := http.StatusOK, string(projected)
		if isSearchHydrationRequest(r) {
			code, body = http.StatusInternalServerError, strings.Repeat("unavailable", 1000)
		}
		return &http.Response{StatusCode: code, Body: &hydrationCountingBody{Reader: strings.NewReader(body), read: &actual}}, nil
	})
	budget := uint64(len(projected)) + 50
	result, err := client.Search(context.Background(), hydrationFixtureRange(day), query.RangeQuery{Budget: query.Budget{Limit: 10, MaxBytes: budget}, ClosedVisibleSeq: 1000})
	require.Error(t, err)
	require.Equal(t, budget, actual)
	require.Equal(t, actual, result.Bytes, "the field-query HTTP error body cannot refund consumed bytes")
	require.Contains(t, result.CoverageReasons, query.ReasonRangeUnavailable)
	require.Equal(t, query.StatsPartial, result.StatsQuality)
}

func TestSearchHydrationRowAllowanceIsSharedAcrossBatches(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	var rows []map[string]any
	for i := 0; i < 4; i++ {
		row := hydrationFixtureRow(i, day.Add(time.Hour+time.Duration(i)*time.Second))
		row["event_id"] = fmt.Sprint(i) + strings.Repeat("x", 4000)
		rows = append(rows, row)
	}
	var batches int
	client := newHydrationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !isSearchHydrationRequest(r) {
			for _, row := range rows {
				_, _ = w.Write(hydrationFixtureLine(t, row, true))
			}
			return
		}
		batches++
		var selected [][]byte
		for _, row := range rows {
			line := hydrationFixtureLine(t, row, false)
			if searchFixtureLineMatches(t, r, line) {
				selected = append(selected, line)
			}
		}
		require.Len(t, selected, 2)
		// Deliberately violate the server's limit to exercise the client's
		// shared row guard. A fresh allowance in batch two would hide this.
		for i := 0; i < 5; i++ {
			_, _ = w.Write(selected[i%len(selected)])
		}
	})
	result, err := client.Search(context.Background(), hydrationFixtureRange(day), query.RangeQuery{Budget: query.Budget{Limit: 10, MaxBytes: 1 << 20}, ClosedVisibleSeq: 1000})
	require.NoError(t, err)
	require.Equal(t, 2, batches)
	require.Contains(t, result.CoverageReasons, query.ReasonBudgetExceeded)
	require.Equal(t, query.StatsPartial, result.StatsQuality)
	require.False(t, result.Exhausted)
}
