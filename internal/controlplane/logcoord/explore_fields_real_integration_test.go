package logcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/query/vlrange"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// This test only starts the approved executable on a random loopback port with
// t.TempDir storage. It never connects to a running DEV/production VL instance.
func TestRealVLExploreFieldsThroughCPAdapterAndWorkerService(t *testing.T) {
	binary := os.Getenv("JM_VL_BIN")
	if binary == "" {
		t.Skip("JM_VL_BIN is not set to a local approved VL executable")
	}
	require.NoError(t, vlsup.VerifyAsset(binary, vlsup.CurrentApprovedExeSHA256()))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	root := t.TempDir()
	supervisor, err := vlsup.New(vlsup.Options{BinaryPath: binary, AssetSHA256: vlsup.CurrentApprovedExeSHA256(), DataRoot: root,
		AuthUsername: "exploration-fixture", AuthPassword: "isolated-only", RetentionPeriod: "730d",
		Ports: map[vlsup.Namespace]int{vlsup.NamespaceHot: port}, MemoryAllowedBytes: map[vlsup.Namespace]int64{vlsup.NamespaceHot: 64 << 20}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	require.NoError(t, supervisor.Start(ctx, vlsup.NamespaceHot))
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		supervisor.StopAll(stop)
	})
	require.Eventually(t, func() bool { return supervisor.Health(ctx, vlsup.NamespaceHot) == nil }, 10*time.Second, 25*time.Millisecond)
	vl, err := supervisor.ClientFor(vlsup.NamespaceHot)
	require.NoError(t, err)
	var body strings.Builder
	for i := 1; i <= 20; i++ {
		row := map[string]any{"_time": "2026-09-22T12:00:00Z", "_msg": "explore fixture", "event_id": fmt.Sprintf("event-%d", i),
			"log_source_id": "src-a", "source_generation": "g1", "record_start": i - 1, "record_end": i, "canonical_content_hash": "fixture-content",
			"stream": "stdout", "level": "INFO", "projection_generation": "p1", "trace_id": fmt.Sprintf("trace-%02d", i), "latency_ms": float64(i), "probe_tag": "accepted"}
		row["http.status_code"], row["trace-id"] = 200, "edge-a"
		if i%2 == 0 {
			row["http.status_code"], row["trace-id"] = 500, "edge-b"
		}
		encoded, err := json.Marshal(row)
		require.NoError(t, err)
		body.Write(encoded)
		body.WriteByte('\n')
	}
	for _, kind := range []string{"source", "generation", "watermark", "filter", "time"} {
		row := map[string]any{"_time": "2026-09-22T12:00:00Z", "_msg": "outside frozen scope", "event_id": "outside-" + kind,
			"log_source_id": "src-a", "source_generation": "g1", "record_start": 0, "record_end": 1, "level": "INFO", "stream": "stdout", "projection_generation": "p1",
			"outside_" + kind: "must not be discovered", "trace_id": "outside-" + kind}
		switch kind {
		case "source":
			row["log_source_id"] = "src-other"
		case "generation":
			row["source_generation"] = "g2"
		case "watermark":
			row["record_end"] = 21
		case "filter":
			row["level"] = "WARN"
		case "time":
			row["_time"] = "2026-09-23T00:00:00Z"
		}
		encoded, err := json.Marshal(row)
		require.NoError(t, err)
		body.Write(encoded)
		body.WriteByte('\n')
	}
	_, err = vl.InsertJSONLines(ctx, []byte(body.String()))
	require.NoError(t, err)
	_, err = vl.Post(ctx, "/internal/force_flush", nil)
	require.NoError(t, err)
	rawFields, err := vl.Post(ctx, "/select/logsql/field_names", url.Values{"query": {`log_source_id:="src-a" AND source_generation:="g1" AND record_end:<=20`}, "extra_filters": {`level:="INFO"`}, "start": {"2026-09-22T00:00:00Z"}, "end": {"2026-09-23T00:00:00Z"}})
	require.NoError(t, err)
	t.Logf("raw scoped field_names: %s", rawFields)
	ranges, err := vlrange.New(vl)
	require.NoError(t, err)
	cat, _ := review05Catalog(t)
	coord, _ := review05Coordinator(t, cat, ranges)
	q := review05Query()
	q.ExploreFields = true
	q.Filter = `level:="INFO"`
	view, err := coord.CreateView(ctx, q)
	require.NoError(t, err)
	q.ViewID = view.ViewID
	fields, err := coord.Fields(ctx, q)
	require.NoError(t, err)
	require.True(t, fields.ExploreApplied)
	require.True(t, fields.Coverage.Complete, "%+v", fields.Coverage)
	for _, field := range []string{"trace_id", "latency_ms", "probe_tag", "http.status_code", "trace-id"} {
		require.Contains(t, fields.Fields, field)
	}
	for _, field := range []string{"event_id", "canonical_content_hash", "record_end", "_msg", "_time", "source_generation", "outside_source", "outside_generation", "outside_watermark", "outside_filter", "outside_time"} {
		require.NotContains(t, fields.Fields, field)
	}
	facet, err := coord.Facets(ctx, FacetsQuery{Query: q, Dimensions: []string{"probe_tag"}, DimensionLimit: 20})
	require.NoError(t, err)
	require.True(t, facet.ExploreApplied)
	require.True(t, facet.Coverage.Complete, "%+v", facet.Coverage)
	require.Len(t, facet.Dimensions, 1)
	require.Equal(t, []FacetValue{{Value: "accepted", Count: 20}}, facet.Dimensions[0].Values)
	truncated, err := coord.Facets(ctx, FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: 5})
	require.NoError(t, err)
	require.True(t, truncated.Truncated)
	require.False(t, truncated.Coverage.Complete)
	require.Equal(t, StatsQPartial, truncated.Quality.StatsQuality)
	require.Len(t, truncated.Dimensions[0].Values, 5)
	for _, test := range []struct {
		field  string
		values []FacetValue
	}{
		{"http.status_code", []FacetValue{{Value: "200", Count: 10}, {Value: "500", Count: 10}}},
		{"trace-id", []FacetValue{{Value: "edge-a", Count: 10}, {Value: "edge-b", Count: 10}}},
	} {
		t.Run("punctuated_facet/"+test.field, func(t *testing.T) {
			out, err := coord.Facets(ctx, FacetsQuery{Query: q, Dimensions: []string{test.field}, DimensionLimit: 20})
			require.NoError(t, err)
			require.True(t, out.Coverage.Complete, "%+v", out.Coverage)
			require.Len(t, out.Dimensions, 1)
			require.Equal(t, test.field, out.Dimensions[0].Dimension)
			require.Equal(t, test.values, out.Dimensions[0].Values)
		})
	}
	for _, test := range []struct{ expression, field, value string }{
		{`("http.status_code":="200")`, "http.status_code", "200"},
		{`NOT ("http.status_code":="200")`, "http.status_code", "500"},
		{`("trace-id":="edge-a")`, "trace-id", "edge-a"},
		{`NOT ("trace-id":="edge-a")`, "trace-id", "edge-b"},
	} {
		t.Run("punctuated_filter/"+test.expression, func(t *testing.T) {
			filtered := q
			filtered.ViewID = ""
			filtered.Filter = `level:="INFO" AND ` + test.expression
			out, err := coord.Facets(ctx, FacetsQuery{Query: filtered, Dimensions: []string{test.field}, DimensionLimit: 20})
			require.NoError(t, err)
			require.True(t, out.Coverage.Complete, "%+v", out.Coverage)
			require.Len(t, out.Dimensions, 1)
			require.Equal(t, []FacetValue{{Value: test.value, Count: 10}}, out.Dimensions[0].Values)
		})
	}
	t.Run("punctuated_numeric_metric_and_group", func(t *testing.T) {
		statsQuery := q
		statsQuery.ExploreFields = false
		out, err := coord.Stats(ctx, StatsQuery{Query: statsQuery, MetricField: "http.status_code", GroupBy: []string{"trace-id"}})
		require.NoError(t, err)
		require.True(t, out.Coverage.Complete, "%+v", out.Coverage)
		require.Equal(t, StatsQExact, out.Quality.StatsQuality)
		require.Len(t, out.Points, 2)
		for _, point := range out.Points {
			want := float64(200)
			if point.Dimensions["trace-id"] == "edge-b" {
				want = 500
			} else {
				require.Equal(t, "edge-a", point.Dimensions["trace-id"])
			}
			require.Equal(t, uint64(10), point.Agg.Count)
			require.Equal(t, want*10, point.Agg.Sum)
			require.Equal(t, want, point.Agg.Min)
			require.Equal(t, want, point.Agg.Max)
			require.Equal(t, want, point.Agg.Avg())
		}
	})
	q.ExploreFields = false
	legacy, err := coord.Fields(ctx, q)
	require.NoError(t, err)
	require.NotContains(t, legacy.Fields, "trace_id")
	t.Logf("isolated approved VL port=%d dataRoot=%s discovered=%v scoped probe_tag=20 high-cardinality truncated=true", port, root, fields.Fields)
}
