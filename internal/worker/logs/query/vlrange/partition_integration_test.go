package vlrange

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// Run explicitly with JM_VL_BIN and JM_VL_SHA256 from the approved asset list.
// No force_flush is used; this checks the actual VL read path after normal visibility.
func TestRealVLPartitionDayIsolation(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("set JM_VL_BIN to run real VictoriaLogs integration")
	}
	sha := os.Getenv("JM_VL_SHA256")
	require.NotEmpty(t, sha, "the real binary must match an explicit approved fingerprint")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	sup, err := vlsup.New(vlsup.Options{BinaryPath: bin, AssetSHA256: sha, DataRoot: t.TempDir(),
		AuthUsername: "fixture", AuthPassword: "fixture-only",
		Ports:              map[vlsup.Namespace]int{vlsup.NamespaceHot: port},
		MemoryAllowedBytes: map[vlsup.Namespace]int64{vlsup.NamespaceHot: 64 << 20}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	require.NoError(t, sup.Start(ctx, vlsup.NamespaceHot))
	t.Cleanup(func() { require.NoError(t, sup.Stop(context.Background(), vlsup.NamespaceHot)) })
	vl, err := sup.ClientFor(vlsup.NamespaceHot)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return vl.Health(ctx) == nil }, 10*time.Second, 100*time.Millisecond)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
	next := day.Add(24 * time.Hour)
	payload := fmt.Sprintf("{\"_time\":%q,\"_msg\":\"first\",\"event_id\":\"E\",\"log_source_id\":\"node:test\",\"source_generation\":\"s1\",\"projection_generation\":\"p1\",\"level\":\"INFO\"}\n"+
		"{\"_time\":%q,\"_msg\":\"second\",\"event_id\":\"F\",\"log_source_id\":\"node:test\",\"source_generation\":\"s1\",\"projection_generation\":\"p1\",\"level\":\"ERROR\"}\n",
		day.Add(12*time.Hour).Format(time.RFC3339Nano), next.Format(time.RFC3339Nano))
	_, err = vl.InsertJSONLines(ctx, []byte(payload))
	require.NoError(t, err)
	client, err := New(vl)
	require.NoError(t, err)
	for i, date := range []time.Time{day, next} {
		rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "node:test", UTCDay: date.Format("2006-01-02")},
			Projection: &catalog.PublishedProjection{ProjectionGenerations: []string{"p1"}}}
		q := query.RangeQuery{Budget: query.Budget{Limit: 10}, GroupBy: []string{"level"}}
		require.Eventually(t, func() bool {
			result, err := client.Search(ctx, rng, q)
			return err == nil && len(result.Items) == 1 && result.Items[0].EventID == []string{"E", "F"}[i]
		}, 15*time.Second, 200*time.Millisecond)
		stats, err := client.Stats(ctx, rng, q)
		require.NoError(t, err)
		require.Len(t, stats.Points, 1)
		require.EqualValues(t, 1, stats.Points[0].Count)
		facets, err := client.Facets(ctx, rng, q, []string{"level"}, 10)
		require.NoError(t, err)
		require.Len(t, facets.Values, 1)
		require.Equal(t, []string{"INFO", "ERROR"}[i], facets.Values[0].Value)
		require.EqualValues(t, 1, facets.Values[0].Count)
	}

	// Two sources independently used p1; B/p1 is an abandoned diagnostic
	// generation, while B/p2 is published. A cross-product selector would leak X.
	var shared string
	for _, row := range []struct{ id, source, generation string }{
		{"A", "source-a", "p1"}, {"X", "source-b", "p1"}, {"B", "source-b", "p2"},
	} {
		shared += fmt.Sprintf("{\"_time\":%q,\"_msg\":%q,\"event_id\":%q,\"log_source_id\":%q,\"source_generation\":\"s1\",\"projection_generation\":%q,\"record_end\":10,\"level\":\"INFO\"}\n",
			day.Add(12*time.Hour).Format(time.RFC3339Nano), row.id, row.id, row.source, row.generation)
	}
	_, err = vl.InsertJSONLines(ctx, []byte(shared))
	require.NoError(t, err)
	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "node:shared", UTCDay: day.Format("2006-01-02")},
		Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{
			{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "source-a", SourceGeneration: "s1"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 10},
			{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "source-b", SourceGeneration: "s1"}, ProjectionGenerations: []string{"p2"}, ClosedVisibleSeq: 10},
		}}}
	q := query.RangeQuery{Budget: query.Budget{Limit: 10}, GroupBy: []string{"level"}}
	require.Eventually(t, func() bool {
		result, err := client.Search(ctx, rng, q)
		return err == nil && len(result.Items) == 2 && result.Items[0].EventID == "A" && result.Items[1].EventID == "B"
	}, 15*time.Second, 200*time.Millisecond)
	stats, err := client.Stats(ctx, rng, q)
	require.NoError(t, err)
	require.Len(t, stats.Points, 1)
	require.EqualValues(t, 2, stats.Points[0].Count)
	cat := catalog.New(nil)
	rec := catalog.NewStableRecord(rng.Key, catalog.OwnerHot, 1, "live-hot")
	rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "live-a", CoverageComplete: true,
		QueryLocationDirID: "live-hot", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{"default": 10},
		SourceProjections: catalog.CloneSourceProjections(rng.Projection.SourceProjections[:1])}
	require.NoError(t, cat.Put(rec))
	liveClient := &publishAfterSearch{Client: client, after: func() {
		next := rec.Clone()
		next.PublishedProjection.ManifestVersion = "live-a-b"
		next.PublishedProjection.SourceProjections = catalog.CloneSourceProjections(rng.Projection.SourceProjections)
		require.NoError(t, cat.Put(next))
	}}
	live := query.NewService(query.NewPlanner(cat, nil), liveClient, "real-vl-live").Tail(ctx, query.QueryRequest{
		AuthorizedTargets: []string{"node:shared"}, Budget: query.Budget{Limit: 2, TimeoutMS: 3000}}, query.TailFollowLive)
	require.Nil(t, live.Err)
	require.True(t, live.Coverage.Complete)
	require.False(t, live.Exhausted)
	require.Equal(t, query.EnumOpen, live.Coverage.EnumerationState)
	require.Len(t, live.Items, 2)
	require.Equal(t, "A", live.Items[0].EventID)
	require.Equal(t, "B", live.Items[1].EventID)
}

type publishAfterSearch struct {
	*Client
	after func()
}

func (c *publishAfterSearch) FollowLive(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	return c.Search(ctx, rng, q)
}

func (c *publishAfterSearch) Search(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	result, err := c.Client.Search(ctx, rng, q)
	if err == nil && len(result.Items) > 0 && c.after != nil {
		publish := c.after
		c.after = nil
		publish()
	}
	return result, err
}
