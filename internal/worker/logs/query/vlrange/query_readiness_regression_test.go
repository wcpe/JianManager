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

// TestQueryTieredReadinessRechecksFixedClientWithoutFallback
//
// 2026-10-29：冷热双层废除后只剩一个受管实例，`Tiered` 不再「按 Owner 选层」，
// 也不再存在「回退到热点副本」这条路 —— 但本用例钉的两个不变量仍然成立且在今天就生效：
//  1. `Ready` 每次读取都被**重新调用**（不是缓存一次就放行），未就绪时必须失败；
//  2. 未就绪时**一个读请求都不发**（coldCalls 不增），恢复后复用同一个固定地址客户端。
//
// 原用例里「不能回退到 hot」的断言，在单实例架构下退化成「请求只发往唯一实例」，
// 因此用 hotCalls（那个从不被选中的 server）保持同一个意思。
func TestQueryTieredReadinessRechecksFixedClientWithoutFallback(t *testing.T) {
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
	tiered := &Tiered{Main: makeClient(cold.URL), Ready: func(_ context.Context, owner catalog.Owner) error {
		checks.Add(1)
		if !ready.Load() {
			return fmt.Errorf("受管实例尚未就绪")
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
	require.EqualValues(t, 2, coldCalls.Load(), "恢复后selection与hydration复用原固定地址client，停机后不再发读")
	require.Zero(t, hotCalls.Load(), "请求只发往唯一的受管实例，不得发到其他地址")
}
