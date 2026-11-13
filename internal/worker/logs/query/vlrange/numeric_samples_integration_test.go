package vlrange

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

func TestRealVLStatsNumericSamples(t *testing.T) {
	ctx, _, _, vl := queryApprovedVL(t)
	client, err := New(vl)
	require.NoError(t, err)
	at := time.Now().UTC().Truncate(time.Minute).Add(-5 * time.Minute)
	const tag = "numeric-samples"
	var payload []byte
	appendRow := func(id, source, probeTag string, eventTime time.Time, metric any) {
		row := map[string]any{
			"_time": eventTime.Format(time.RFC3339Nano), "_msg": id, "event_id": id,
			"log_source_id": source, "probe_tag": probeTag, "stream": "probe",
		}
		if metric != nil {
			row["latency_ms"] = metric
		}
		encoded, err := json.Marshal(row)
		require.NoError(t, err)
		payload = append(payload, encoded...)
		payload = append(payload, '\n')
	}
	for i, metric := range []any{10, "20", "notnum", nil, -2} {
		appendRow(fmt.Sprintf("sample-%d", i), "inst:557/file", tag, at.Add(time.Duration(i+1)*time.Second), metric)
	}
	appendRow("other-filter", "inst:557/file", "other-tag", at.Add(time.Second), 1000)
	appendRow("outside-window", "inst:557/file", tag, at.Add(2*time.Minute), 2000)
	appendRow("other-source", "inst:5571/file", tag, at.Add(time.Second), 3000)
	_, err = vl.InsertJSONLines(ctx, payload)
	require.NoError(t, err)

	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{
		StorageNamespace: "inst:557", UTCDay: at.Format("2006-01-02"),
	}}
	q := query.RangeQuery{
		Filter: `probe_tag:="` + tag + `"`,
		TimeRange: query.TimeRange{
			FromUTC: at.Format(time.RFC3339Nano), ToUTC: at.Add(time.Minute).Format(time.RFC3339Nano),
		},
		Budget: query.Budget{Limit: 10, TimeoutMS: 5000},
	}
	// Wait for normal ingestion visibility through the same Stats transport.
	require.Eventually(t, func() bool {
		result, err := client.Stats(ctx, rng, q)
		return err == nil && len(result.Points) == 1 && result.Points[0].Count == 5
	}, 15*time.Second, 100*time.Millisecond)

	q.MetricField = "latency_ms"
	for _, bucket := range []string{"", "1m"} {
		name := "scalar"
		if bucket != "" {
			name = "bucket-and-group"
		}
		t.Run(name, func(t *testing.T) {
			current := q
			current.TimeBucket = bucket
			if bucket != "" {
				current.GroupBy = []string{"stream"}
			}
			result, err := client.Stats(ctx, rng, current)
			require.NoError(t, err)
			require.Equal(t, query.DupExact, result.DuplicateQuality)
			require.Equal(t, query.StatsExact, result.StatsQuality)
			require.Empty(t, result.CoverageReasons)
			require.Len(t, result.Points, 1)
			point := result.Points[0]
			require.EqualValues(t, 3, point.Count)
			require.Equal(t, float64(28), point.Sum)
			require.Equal(t, float64(-2), point.Min)
			require.Equal(t, float64(20), point.Max)
			require.InDelta(t, 28.0/3.0, point.Sum/float64(point.Count), 1e-12)
			if bucket != "" {
				require.Equal(t, "probe", point.Dimensions["stream"])
				require.NotEmpty(t, point.Dimensions["_time"])
			}
			t.Logf("numeric samples=%d sum=%g min=%g max=%g average=%.12f", point.Count, point.Sum, point.Min, point.Max, point.Sum/float64(point.Count))
		})
	}
}
