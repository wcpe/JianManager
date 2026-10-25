package query

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

type physicalBudgetClient struct{ FuncRangeClient }

func (physicalBudgetClient) Fields(context.Context, AuthoritativeRange, RangeQuery) (FieldsResult, error) {
	return FieldsResult{}, fmt.Errorf("%w: fields", ErrRangeQueryBudget)
}
func (physicalBudgetClient) Facets(context.Context, AuthoritativeRange, RangeQuery, []string, uint32) (RangeFacetsResult, error) {
	return RangeFacetsResult{}, fmt.Errorf("%w: facets", ErrRangeQueryBudget)
}

func TestQueryPhysicalReadBudgetCannotBeResetOrDoubleRefunded(t *testing.T) {
	ctx := WithPhysicalReadBudget(context.Background(), Budget{MaxBytes: 10, MaxFanout: 2})
	reserved, release, err := ReservePhysicalRead(ctx, 6)
	require.NoError(t, err)
	require.EqualValues(t, 6, reserved)
	release(2)
	release(0)
	derived := WithPhysicalReadBudget(ctx, Budget{MaxBytes: 1000, MaxFanout: 1000})
	reserved, release, err = ReservePhysicalRead(derived, 1000)
	require.NoError(t, err)
	require.EqualValues(t, 8, reserved, "派生range只能拿到请求剩余额度，重复退款不能凭空扩额")
	release(0)
	_, _, err = ReservePhysicalRead(ctx, 1)
	require.ErrorIs(t, err, ErrRangeQueryBudget, "用过的HTTP扇出不能随字节退款重置")
}

func TestQueryPhysicalBudgetFailureIsRecoverableAndNeverRangeUnavailable(t *testing.T) {
	for _, operation := range []string{"search", "stats", "fields", "facets", "tail"} {
		t.Run(operation, func(t *testing.T) {
			cat, key := queryLeaseCatalog(t, nil)
			planner := NewPlanner(cat, nil)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			client := physicalBudgetClient{FuncRangeClient{
				SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
					return RangeResult{}, fmt.Errorf("%w: search", ErrRangeQueryBudget)
				},
				StatsFn: func(context.Context, AuthoritativeRange, RangeQuery) (StatsResult, error) {
					return StatsResult{}, fmt.Errorf("%w: stats", ErrRangeQueryBudget)
				},
				TailFn: func(context.Context, AuthoritativeRange, RangeQuery, TailMode) (RangeResult, error) {
					return RangeResult{}, fmt.Errorf("%w: tail", ErrRangeQueryBudget)
				},
			}}
			svc := NewService(planner, client, "physical-budget")
			req := QueryRequest{AuthorizedTargets: []string{key.String()}}
			var queryErr *QueryError
			var coverage Coverage
			switch operation {
			case "search":
				response := svc.Search(context.Background(), req)
				queryErr, coverage = response.Err, response.Coverage
				require.Empty(t, response.NextCursor, "预算失败范围不能贡献危险全局游标")
				require.False(t, response.Exhausted)
			case "stats":
				response := svc.Stats(context.Background(), req, nil)
				queryErr, coverage = response.Err, response.Coverage
				require.Equal(t, StatsPartial, response.Quality.StatsQuality)
			case "fields":
				response := svc.Fields(context.Background(), req)
				queryErr, coverage = response.Err, response.Coverage
			case "facets":
				response := svc.Facets(context.Background(), req, []string{"level"}, 10)
				queryErr, coverage = response.Err, response.Coverage
			case "tail":
				response := svc.Tail(context.Background(), req, TailViewBounded)
				queryErr, coverage = response.Err, response.Coverage
				require.False(t, response.Exhausted)
			}
			require.NotNil(t, queryErr)
			require.Equal(t, ErrCodeBudgetExceeded, queryErr.Code)
			require.False(t, coverage.Complete)
			require.Contains(t, coverage.PartialReasons, ReasonBudgetExceeded)
			require.NotContains(t, coverage.PartialReasons, ReasonRangeUnavailable)
		})
	}
}

type queryDeadlineClient struct {
	FuncRangeClient
	wait func(context.Context) error
}

func (c queryDeadlineClient) FollowLive(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error) {
	return c.Search(ctx, rng, q)
}

func (c queryDeadlineClient) Fields(ctx context.Context, _ AuthoritativeRange, _ RangeQuery) (FieldsResult, error) {
	return FieldsResult{}, c.wait(ctx)
}
func (c queryDeadlineClient) Facets(ctx context.Context, _ AuthoritativeRange, _ RangeQuery, _ []string, _ uint32) (RangeFacetsResult, error) {
	return RangeFacetsResult{}, c.wait(ctx)
}

func TestQueryRequestTimeoutAppliesBeforeEveryRangeEntry(t *testing.T) {
	for _, operation := range []string{"search", "stats", "fields", "facets", "tail", "follow-live"} {
		t.Run(operation, func(t *testing.T) {
			cat, key := queryLeaseCatalog(t, nil)
			planner := NewPlanner(cat, nil)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			var calls atomic.Int32
			started := time.Now()
			wait := func(ctx context.Context) error {
				calls.Add(1)
				deadline, ok := ctx.Deadline()
				if !ok || deadline.After(started.Add(50*time.Millisecond)) {
					return errors.New("未施加请求TimeoutMS绝对截止时间")
				}
				<-ctx.Done()
				return ctx.Err()
			}
			client := queryDeadlineClient{wait: wait, FuncRangeClient: FuncRangeClient{
				SearchFn: func(ctx context.Context, _ AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
					return RangeResult{}, wait(ctx)
				},
				StatsFn: func(ctx context.Context, _ AuthoritativeRange, _ RangeQuery) (StatsResult, error) {
					return StatsResult{}, wait(ctx)
				},
				TailFn: func(ctx context.Context, _ AuthoritativeRange, _ RangeQuery, _ TailMode) (RangeResult, error) {
					return RangeResult{}, wait(ctx)
				},
			}}
			svc := NewService(planner, client, "request-timeout")
			parent, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req := QueryRequest{AuthorizedTargets: []string{key.String()}, Budget: Budget{TimeoutMS: 10}}
			var queryErr *QueryError
			var coverage Coverage
			switch operation {
			case "search":
				response := svc.Search(parent, req)
				queryErr, coverage = response.Err, response.Coverage
				require.Empty(t, response.NextCursor)
				require.False(t, response.Exhausted)
			case "stats":
				response := svc.Stats(parent, req, nil)
				queryErr, coverage = response.Err, response.Coverage
				require.NotEqual(t, StatsExact, response.Quality.StatsQuality)
			case "fields":
				response := svc.Fields(parent, req)
				queryErr, coverage = response.Err, response.Coverage
			case "facets":
				response := svc.Facets(parent, req, []string{"level"}, 10)
				queryErr, coverage = response.Err, response.Coverage
			case "tail", "follow-live":
				mode := TailViewBounded
				if operation == "follow-live" {
					mode = TailFollowLive
				}
				response := svc.Tail(parent, req, mode)
				queryErr, coverage = response.Err, response.Coverage
				require.False(t, response.Exhausted)
			}
			require.NotNil(t, queryErr)
			require.Equal(t, ErrCodeCancelled, queryErr.Code)
			require.False(t, coverage.Complete)
			require.Contains(t, coverage.PartialReasons, ReasonCancelled)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

type queryDeadlineCatalog struct {
	*catalog.Catalog
	pins atomic.Int32
	wait func()
}

func (c *queryDeadlineCatalog) SnapshotAndLease(req catalog.QuerySnapshotRequest) ([]catalog.QuerySnapshot, error) {
	if c.wait != nil {
		c.wait()
	}
	return c.Catalog.SnapshotAndLease(req)
}
func (c *queryDeadlineCatalog) PinLeasedRead(viewID string, now time.Time) (func(), error) {
	c.pins.Add(1)
	return c.Catalog.PinLeasedRead(viewID, now)
}

func TestQueryRequestTimeoutPlanningAndQueuedRangeDoNotPin(t *testing.T) {
	cat, key := queryLeaseCatalog(t, nil)
	observed := &queryDeadlineCatalog{Catalog: cat}
	planner := NewPlanner(observed, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	svc := NewService(planner, UnimplementedRangeClient{}, "deadline-plan")
	plan := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
	require.Nil(t, plan.Err)
	observed.pins.Store(0)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, release, err := svc.pinRange(cancelled, &plan)
	release()
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, observed.pins.Load(), "排队任务到期后不得进入实际read pin，更不能调用RangeClient")
	require.NoError(t, planner.ReleaseView(plan.View.ViewID))
	observed.wait = func() { <-time.After(30 * time.Millisecond) }
	response := svc.OpenView(QueryRequest{AuthorizedTargets: []string{key.String()}, Budget: Budget{TimeoutMS: 10}})
	require.NotNil(t, response.Err)
	require.Equal(t, ErrCodeCancelled, response.Err.Code)
	require.False(t, response.Coverage.Complete)
	require.Empty(t, planner.Views())
	record, _ := cat.Get(key)
	require.Zero(t, record.ActiveLeaseCount(time.Now()))
}

func TestQueryRequestTimeoutKeepsExportReservationUntilActualExit(t *testing.T) {
	cat, key := queryLeaseCatalog(t, nil)
	planner := NewPlanner(cat, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	entered, exit := make(chan context.Context, 1), make(chan struct{})
	client := FuncRangeClient{SearchFn: func(ctx context.Context, _ AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
		entered <- ctx
		<-exit
		return RangeResult{Exhausted: true}, nil
	}}
	budget := &queryResourceRecorder{}
	svc := NewServiceWithOptions(planner, client, "deadline-export", Options{ResourceBudget: budget})
	done := make(chan SearchResponse, 1)
	go func() {
		done <- svc.Search(context.Background(), QueryRequest{AuthorizedTargets: []string{key.String()}, AllowProjectionDrift: true, Budget: Budget{MaxBytes: 512, TimeoutMS: 30}})
	}()
	readCtx := <-entered
	<-readCtx.Done()
	require.EqualValues(t, 512, budget.reserved.Load(), "整请求超时也必须等实际忽略ctx的协程退出才归还预留")
	select {
	case <-done:
		t.Fatal("真实读取尚未退出就提前返回并释放保护")
	default:
	}
	var detached atomic.Int32
	require.ErrorIs(t, queryLeaseDayManager(cat, &detached).Start(key.UTCDay, catalog.OwnerCold, "cold-2"), catalog.ErrPhysicalDayNotRetirable)
	require.Zero(t, detached.Load())
	close(exit)
	response := <-done
	require.NotNil(t, response.Err)
	require.Equal(t, ErrCodeCancelled, response.Err.Code)
	require.Empty(t, response.NextCursor)
	require.Zero(t, budget.reserved.Load())
}

func TestQueryPreCancelledAdmissionDoesNotAllocateSnapshot(t *testing.T) {
	cat, key := queryLeaseCatalog(t, nil)
	observed := &queryObservedCatalog{Catalog: cat}
	planner := NewPlanner(observed, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	budget := &queryResourceRecorder{}
	svc := NewServiceWithOptions(planner, UnimplementedRangeClient{}, "pre-cancel", Options{ResourceBudget: budget})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := QueryRequest{AuthorizedTargets: []string{key.String()}}
	for _, queryErr := range []*QueryError{svc.OpenViewContext(ctx, req).Err, svc.Search(ctx, req).Err, svc.Stats(ctx, req, nil).Err, svc.Fields(ctx, req).Err, svc.Facets(ctx, req, []string{"level"}, 10).Err, svc.Tail(ctx, req, TailViewBounded).Err} {
		require.NotNil(t, queryErr)
		require.Equal(t, ErrCodeCancelled, queryErr.Code)
	}
	require.Zero(t, observed.snapshots.Load())
	require.Zero(t, budget.admissions.Load())
	require.Empty(t, planner.Views())
}
