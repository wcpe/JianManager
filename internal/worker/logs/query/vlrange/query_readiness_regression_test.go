package vlrange

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestQueryTieredReadinessRechecksFixedClientWithoutHotFallback(t *testing.T) {
	var hotCalls, coldCalls, checks atomic.Int32
	hot := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hotCalls.Add(1) }))
	t.Cleanup(hot.Close)
	cold := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		coldCalls.Add(1)
		_, _ = w.Write([]byte(`{"_time":"2026-09-22T12:00:00Z","event_id":"cold-row","log_source_id":"source","source_generation":"s1","record_end":"1"}` + "\n"))
	}))
	t.Cleanup(cold.Close)
	makeClient := func(url string) *Client {
		vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: url})
		require.NoError(t, err)
		client, err := New(vl)
		require.NoError(t, err)
		return client
	}
	var ready atomic.Bool
	tiered := &Tiered{Hot: makeClient(hot.URL), Cold: makeClient(cold.URL), Ready: func(_ context.Context, owner catalog.Owner) error {
		checks.Add(1)
		if owner != catalog.OwnerCold || !ready.Load() {
			return fmt.Errorf("冷层尚未就绪")
		}
		return nil
	}}
	rng := query.AuthoritativeRange{Owner: catalog.OwnerCold, Key: catalog.PartitionKey{StorageNamespace: "source", UTCDay: "2026-09-22"}, ClosedVisibleSeq: 1}
	q := query.RangeQuery{ClosedVisibleSeq: 1, Budget: query.Budget{Limit: 10}}
	_, err := tiered.Search(context.Background(), rng, q)
	require.Error(t, err)
	require.Zero(t, coldCalls.Load())
	ready.Store(true)
	response, err := tiered.Search(context.Background(), rng, q)
	require.NoError(t, err)
	require.Len(t, response.Items, 1)
	ready.Store(false)
	_, err = tiered.Search(context.Background(), rng, q)
	require.Error(t, err)
	require.EqualValues(t, 3, checks.Load())
	require.EqualValues(t, 1, coldCalls.Load(), "恢复后复用原固定地址 client，停机后不再发读")
	require.Zero(t, hotCalls.Load(), "权威冷层不可用不能回退热点副本")
}
