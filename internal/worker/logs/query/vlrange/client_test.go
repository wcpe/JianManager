package vlrange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestClientSearchMapsNDJSONAndEnforcesClosedVisibleSeq(t *testing.T) {
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.Query().Get("query")
		if r.URL.Path != "/select/logsql/query" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"_time":"2026-09-22T01:02:03Z","_msg":"kept","event_id":"e1","log_source_id":"src-1","source_generation":"g1","record_start":"1","record_end":"5","level":"INFO","stream":"stdout","canonical_content_hash":"h1"}
{"_time":"2026-09-22T01:02:04Z","_msg":"beyond","event_id":"e2","log_source_id":"src-1","source_generation":"g1","record_start":"6","record_end":"12","level":"INFO","stream":"stdout","canonical_content_hash":"h2"}
`))
	}))
	defer srv.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, AllowNonLoopback: true})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	res, err := client.Search(context.Background(), query.AuthoritativeRange{
		Key: queryKey(),
		Projection: &catalog.PublishedProjection{CoveredSourceGenerations: []catalog.SourceGenerationRef{{
			LogSourceID: "src-1", SourceGeneration: "g1",
		}}},
	}, query.RangeQuery{
		TimeRange:        query.TimeRange{FromUTC: "2026-09-22T00:00:00Z", ToUTC: "2026-09-23T00:00:00Z"},
		Budget:           query.Budget{Limit: 10, MaxBytes: 1 << 20},
		ClosedVisibleSeq: 5,
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	require.Equal(t, "e1", res.Items[0].EventID)
	require.Contains(t, seenQuery, `log_source_id:="src-1"`)
	require.Contains(t, seenQuery, `source_generation:="g1"`)
	require.Contains(t, seenQuery, "fields _time")
}

func TestTieredArchiveSearchUsesRehydrateClientAndCoveredSourceSelector(t *testing.T) {
	hotCalls, deepCalls := 0, 0
	hot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hotCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer hot.Close()
	deep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deepCalls++
		selector := r.URL.Query().Get("query")
		require.Contains(t, selector, `log_source_id:="source-a"`)
		require.Contains(t, selector, `log_source_id:="source-b"`)
		require.Contains(t, selector, " OR ")
		require.Contains(t, selector, `projection_generation:="rehydrate-g2"`)
		require.NotContains(t, selector, `log_source_id:="shared-namespace"`)
		_, _ = w.Write([]byte(`{"_time":"2026-09-22T12:00:00Z","_msg":"deep","event_id":"e1","log_source_id":"source-a","source_generation":"g1","record_start":1,"record_end":2}` + "\n"))
	}))
	defer deep.Close()
	hotVL, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: hot.URL})
	require.NoError(t, err)
	deepVL, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: deep.URL})
	require.NoError(t, err)
	hotClient, err := New(hotVL)
	require.NoError(t, err)
	deepClient, err := New(deepVL)
	require.NoError(t, err)
	tiered := &Tiered{Hot: hotClient, Rehydrate: deepClient}
	res, err := tiered.Search(context.Background(), query.AuthoritativeRange{
		Key:   catalog.PartitionKey{StorageNamespace: "shared-namespace", UTCDay: "2026-09-22"},
		Owner: catalog.OwnerArchive,
		Projection: &catalog.PublishedProjection{
			ProjectionGeneration: "rehydrate-g2",
			CoveredSourceGenerations: []catalog.SourceGenerationRef{
				{LogSourceID: "source-a", SourceGeneration: "g1"},
				{LogSourceID: "source-b", SourceGeneration: "g7"},
			},
		},
	}, query.RangeQuery{Budget: query.Budget{Limit: 10}})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	require.Zero(t, hotCalls)
	require.Equal(t, 1, deepCalls)
}

func TestClientUsesEveryPublishedProjectionGeneration(t *testing.T) {
	var selector string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selector = r.URL.Query().Get("query")
	}))
	defer srv.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	_, err = client.Search(context.Background(), query.AuthoritativeRange{
		Key:        catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22"},
		Projection: &catalog.PublishedProjection{ProjectionGeneration: "g2", ProjectionGenerations: []string{"g1", "g2"}},
	}, query.RangeQuery{Budget: query.Budget{Limit: 10}})
	require.NoError(t, err)
	require.Contains(t, selector, `projection_generation:="g1"`)
	require.Contains(t, selector, `projection_generation:="g2"`)
	require.Contains(t, selector, " OR ")
}

func TestClientStatsMapsStatsRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("query"), "stats") {
			t.Fatalf("query = %q", r.URL.Query().Get("query"))
		}
		_, _ = w.Write([]byte(`{"level":"ERROR","_jm_count":"3","_jm_sum":"12.5","_jm_min":"1.5","_jm_max":"7.0"}
`))
	}))
	defer srv.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL, AllowNonLoopback: true})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)

	res, err := client.Stats(context.Background(), query.AuthoritativeRange{Key: queryKey()}, query.RangeQuery{GroupBy: []string{"level"}, MetricField: "duration"})
	require.NoError(t, err)
	require.Len(t, res.Points, 1)
	require.Equal(t, uint64(3), res.Points[0].Count)
	require.Equal(t, "ERROR", res.Points[0].Dimensions["level"])
	require.Equal(t, 12.5, res.Points[0].Sum)
}

func TestClientSearchFetchesLookaheadBeforeDeclaringExhausted(t *testing.T) {
	var limit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit = r.URL.Query().Get("limit")
		_, _ = w.Write([]byte(`{"_time":"2026-09-22T01:02:04Z","_msg":"newer","event_id":"e2","record_start":"6","record_end":"7"}
{"_time":"2026-09-22T01:02:03Z","_msg":"older","event_id":"e1","record_start":"1","record_end":"2"}
`))
	}))
	defer srv.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	_, err = client.Search(context.Background(), query.AuthoritativeRange{Key: queryKey()}, query.RangeQuery{Budget: query.Budget{Limit: 1}})
	require.NoError(t, err)
	require.Equal(t, "2", limit, "first page needs a lookahead row to produce a cursor")
}

func TestClientStatsConstrainInputToClosedVisiblePrefix(t *testing.T) {
	var selector string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selector = r.URL.Query().Get("query")
		_, _ = w.Write([]byte("{\"_jm_count\":\"1\"}\n"))
	}))
	defer srv.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	_, err = client.Stats(context.Background(), query.AuthoritativeRange{Key: queryKey()}, query.RangeQuery{ClosedVisibleSeq: 42})
	require.NoError(t, err)
	require.Contains(t, selector, "record_end:<=42")
}

func queryKey() catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "src-1", UTCDay: "2026-09-22"}
}

func TestPartitionDayBoundsApplyToSearchStatsAndFacets(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "2026-09-22T12:00:00Z", r.URL.Query().Get("start"))
		require.Equal(t, "2026-09-23T00:00:00Z", r.URL.Query().Get("end"))
	}))
	defer srv.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	rng := query.AuthoritativeRange{Key: queryKey()}
	q := query.RangeQuery{TimeRange: query.TimeRange{FromUTC: "2026-09-22T20:00:00+08:00", ToUTC: "2026-09-24T00:00:00Z"}}
	_, err = client.Search(context.Background(), rng, q)
	require.NoError(t, err)
	_, err = client.Stats(context.Background(), rng, q)
	require.NoError(t, err)
	_, err = client.Facets(context.Background(), rng, q, []string{"level"}, 10)
	require.NoError(t, err)
	require.Equal(t, 3, calls)

	q.TimeRange.FromUTC = "2026-09-23T00:00:00Z"
	res, err := client.Search(context.Background(), rng, q)
	require.NoError(t, err)
	require.Empty(t, res.Items)
	require.True(t, res.Exhausted)
	require.Equal(t, 3, calls, "empty partition intersection must not query a different day")

	rng.Key.UTCDay = "invalid"
	_, err = client.Search(context.Background(), rng, query.RangeQuery{})
	require.Error(t, err)
	require.Equal(t, 3, calls)
}
