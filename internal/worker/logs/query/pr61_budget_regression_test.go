package query

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

func pr61BudgetPlanner(t *testing.T, n int) *Planner {
	t.Helper()
	cat := catalog.New(nil)
	for i := 0; i < n; i++ {
		key := catalog.PartitionKey{StorageNamespace: fmt.Sprintf("src-%02d", i), UTCDay: "2026-09-22"}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
		rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "pv-1", CoverageComplete: true, QueryLocationDirID: "hot-1", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 100}}
		require.NoError(t, cat.Put(rec))
	}
	return NewPlanner(cat, nil)
}

// 八个在途任务由屏障保持；剩余四个不能偷偷获得同一份整额预算。
func TestPR61BudgetReservationsAndIncrementalStop(t *testing.T) {
	for _, stats := range []bool{false, true} {
		t.Run(fmt.Sprint(stats), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered := make(chan uint64, 12)
			release := make(chan struct{})
			var calls atomic.Int32
			call := func(q RangeQuery) uint64 {
				calls.Add(1)
				entered <- q.Budget.MaxBytes
				select {
				case <-release:
				case <-ctx.Done():
				}
				return q.Budget.MaxBytes
			}
			client := FuncRangeClient{
				SearchFn: func(_ context.Context, _ AuthoritativeRange, q RangeQuery) (RangeResult, error) {
					return RangeResult{Bytes: call(q), Exhausted: true}, nil
				},
				StatsFn: func(_ context.Context, _ AuthoritativeRange, q RangeQuery) (StatsResult, error) {
					return StatsResult{Bytes: call(q)}, nil
				},
			}
			svc := NewService(pr61BudgetPlanner(t, 12), client, "test")
			done := make(chan Coverage, 1)
			go func() {
				req := QueryRequest{Budget: Budget{MaxBytes: 128}}
				if stats {
					done <- svc.Stats(ctx, req, nil).Coverage
				} else {
					done <- svc.Search(ctx, req).Coverage
				}
			}()
			var reserved uint64
			for i := 0; i < 8; i++ {
				select {
				case b := <-entered:
					reserved += b
					require.Positive(t, b)
				case <-ctx.Done():
					t.Fatal("未达到八任务屏障")
				}
			}
			if reserved > 128 {
				t.Errorf("真实在途预留超总额：%d > 128", reserved)
			}
			close(release)
			var cov Coverage
			select {
			case cov = <-done:
			case <-ctx.Done():
				t.Fatal("预算调度未终止")
			}
			require.LessOrEqual(t, calls.Load(), int32(8), "额度耗尽后不得发出第九个任务")
			require.False(t, cov.Complete)
			require.Contains(t, cov.PartialReasons, ReasonBudgetExceeded)
		})
	}
}

// 完成一个任务即释放未用预留，排队任务无需等待其它七个慢任务全部结束。
func TestPR61UnusedReservationReleasedIncrementally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan int, 12)
	releaseOne, releaseRest := make(chan struct{}), make(chan struct{})
	done := make(chan bool, 1)
	var applied int
	go func() {
		done <- runBudgetTasks(ctx, Budget{MaxBytes: 128}, 12, func(workCtx context.Context, idx int, b Budget) (int, uint64) {
			entered <- idx
			if b.MaxBytes == 0 {
				t.Error("有限预算不得传零额度")
			}
			if idx < 8 {
				barrier := releaseRest
				if idx == 0 {
					barrier = releaseOne
				}
				select {
				case <-barrier:
				case <-workCtx.Done():
				}
			}
			return idx, 1
		}, func(int) bool { applied++; return true })
	}()
	for i := 0; i < 8; i++ {
		select {
		case idx := <-entered:
			require.Less(t, idx, 8)
		case <-ctx.Done():
			t.Fatal("未达到八任务屏障")
		}
	}
	close(releaseOne)
	for i := 0; i < 4; i++ {
		select {
		case idx := <-entered:
			require.GreaterOrEqual(t, idx, 8)
		case <-ctx.Done():
			t.Fatal("未用预留必须随完成释放，不能等全体任务结束")
		}
	}
	close(releaseRest)
	select {
	case cut := <-done:
		require.False(t, cut)
	case <-ctx.Done():
		t.Fatal("调度未结束")
	}
	require.Equal(t, 12, applied)
}

// 取消等待中的任务不应在信号量释放后迟到发出请求。
func TestPR61BudgetCancellationDoesNotLaunchQueuedTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 12)
	var calls atomic.Int32
	client := FuncRangeClient{SearchFn: func(ctx context.Context, _ AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		return RangeResult{}, ctx.Err()
	}}
	svc := NewService(pr61BudgetPlanner(t, 12), client, "test")
	done := make(chan SearchResponse, 1)
	go func() { done <- svc.Search(ctx, QueryRequest{}) }()
	for i := 0; i < 8; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("未达到八任务屏障")
		}
	}
	cancel()
	select {
	case resp := <-done:
		require.NotNil(t, resp.Err)
	case <-time.After(5 * time.Second):
		t.Fatal("取消未结束")
	}
	require.Equal(t, int32(8), calls.Load(), "排队任务不得取消后启动")
}
