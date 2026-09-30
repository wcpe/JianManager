package query

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 变异验证：把逐分区取数改回串行，本测试即转红。
//
// 判据有两条，任一条都足以让串行实现失败：
//  1. 并发度峰值必须 >= 2（串行实现下恒为 1）；
//  2. 总耗时必须显著低于「n × 单 range 耗时」（串行实现下必然超过）。
//
// 同时它也是并行化的实效证据：并发取数时总耗时约等于单 range 耗时。
func TestSearch_FetchesRangesInParallel(t *testing.T) {
	const n = 8
	const perRange = 40 * time.Millisecond

	c := catalog.New(nil)
	from := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		key := catalog.PartitionKey{
			StorageNamespace: "ns/game-1",
			UTCDay:           from.AddDate(0, 0, i).Format("2006-01-02"),
		}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
		rec.PublishedProjection = &catalog.PublishedProjection{
			ManifestVersion:    "pv-1",
			CoverageComplete:   true,
			QueryLocationDirID: "hot-g1",
			QueryGeneration:    1,
			ClosedVisibleSeq:   map[string]uint64{key.String(): 10},
		}
		require.NoError(t, c.Put(rec))
	}

	var inflight, peak int32
	client := FuncRangeClient{
		SearchFn: func(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error) {
			cur := atomic.AddInt32(&inflight, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
					break
				}
			}
			time.Sleep(perRange)
			atomic.AddInt32(&inflight, -1)
			return RangeResult{
				Items:     []logtypes.Event{buildEvent("src", "g1", 1, 2, from.Format(time.RFC3339), "m")},
				Exhausted: true,
			}, nil
		},
	}
	svc := NewService(NewPlanner(c, nil), client, "test-build")

	start := time.Now()
	resp := svc.Search(context.Background(), QueryRequest{
		RequestID: "parallel-1",
		TimeRange: TimeRange{
			FromUTC: from.Format(time.RFC3339),
			ToUTC:   from.AddDate(0, 0, n+1).Format(time.RFC3339),
		},
		Budget: Budget{Limit: 100},
	})
	elapsed := time.Since(start)

	require.Nil(t, resp.Err)
	// 每个 range 各返回 1 条，合并后应恰好 n 条：顺序无关的合并语义未被并发破坏。
	require.Len(t, resp.Items, n)
	require.GreaterOrEqual(t, int(atomic.LoadInt32(&peak)), 2,
		"应观察到并发的 range 取数；若实现退回串行，峰值为 1 而转红")
	require.Less(t, elapsed, time.Duration(n-2)*perRange,
		"并发取数总耗时应显著低于串行总时长；若退回串行则必然超时转红")
}
