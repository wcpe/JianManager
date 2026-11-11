package logcoord

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
)

// Exercise the actual CP adapter, protobuf request, grpcsvc and query.Service;
// testing vlrange alone cannot detect Service's default dimension whitelist.
func TestExploreFieldsAdapterServiceDiscoversCustomDocumentFields(t *testing.T) {
	cat, _ := review05Catalog(t)
	client := &review05Range{
		fieldsFn: func(context.Context, workerquery.AuthoritativeRange, workerquery.RangeQuery) (workerquery.FieldsResult, error) {
			return workerquery.FieldsResult{Fields: []string{"trace_id", "latency_ms", "probe_tag", "level", "event_id", "_msg"}}, nil
		},
		facetsFn: func(_ context.Context, _ workerquery.AuthoritativeRange, _ workerquery.RangeQuery, dimensions []string, limit uint32) (workerquery.RangeFacetsResult, error) {
			require.Equal(t, []string{"trace_id"}, dimensions)
			require.Equal(t, uint32(20), limit)
			return workerquery.RangeFacetsResult{Values: []workerquery.FacetValue{{Dimension: "trace_id", Value: "trace-1", Count: 3}}}, nil
		},
	}
	coord, _ := review05Coordinator(t, cat, client)
	q := review05Query()
	// JSON also pins the optional flag's backward compatible zero-value contract.
	require.NoError(t, json.Unmarshal([]byte(`{"explore_fields":true}`), &q))
	view, err := coord.CreateView(context.Background(), q)
	require.NoError(t, err)
	q.ViewID = view.ViewID
	t.Run("fields", func(t *testing.T) {
		out, err := coord.Fields(context.Background(), q)
		require.NoError(t, err)
		require.Contains(t, out.Fields, "trace_id")
		require.Contains(t, out.Fields, "latency_ms")
		require.Contains(t, out.Fields, "probe_tag")
		require.NotContains(t, out.Fields, "event_id")
		require.NotContains(t, out.Fields, "_msg")
	})
	t.Run("facets", func(t *testing.T) {
		out, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: 20})
		require.NoError(t, err)
		require.True(t, out.Coverage.Complete, "%+v", out.Coverage)
		require.Len(t, out.Dimensions, 1)
		require.Equal(t, []FacetValue{{Value: "trace-1", Count: 3}}, out.Dimensions[0].Values)
	})
}
