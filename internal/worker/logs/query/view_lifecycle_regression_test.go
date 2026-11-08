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
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func queryLeaseCatalog(t *testing.T, journal catalog.Journal) (*catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	key := catalog.PartitionKey{StorageNamespace: "query-lease", UTCDay: "2026-09-22"}
	cat := catalog.New(journal)
	record := queryableRecord(key, catalog.OwnerHot, 1, "hot-1")
	record.PublishedProjection.SourceProjections = []catalog.SourceProjection{{
		SourceGenerationRef:   catalog.SourceGenerationRef{LogSourceID: "source", SourceGeneration: "s1"},
		ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 100}}
	require.NoError(t, cat.PublishProjection(nil, record))
	return cat, key
}

// physicalDayRetirable 直接问 Catalog「这个物理日现在能不能退休」。
//
// 2026-10-29：冷热双层废除后，`lifecycle.DayManager`（迁移状态机）已删除 ——
// 但本文件（以及 physical_budget / view_quality 两个回归）真正要钉的不变量**没有变**：
// **有在途读取时，物理日不得退休/清理**。那个不变量属于 `Catalog.CanRetirePhysicalDay`，
// 与是否真的有第二层可搬无关。
//
// 原实现用 `DayManager.Start(..., OwnerCold, ...)` 作为触发器；现在直接问门禁本身，
// 断言更强也更直接：不再经过一层与本次改造无关的状态机。
func physicalDayRetirable(cat *catalog.Catalog, utcDay string) (bool, string) {
	return cat.CanRetirePhysicalDay(utcDay)
}

func TestQuerySnapshotDeepCopyAndAbsoluteLifetime(t *testing.T) {
	cat, key := queryLeaseCatalog(t, nil)
	var clock atomic.Int64
	base := time.Now().UTC()
	clock.Store(base.UnixNano())
	planner := NewPlannerWithOptions(cat, nil, Options{ViewTTL: time.Hour, MaxViewLifetime: time.Hour,
		Now: func() time.Time { return time.Unix(0, clock.Load()).UTC() }})
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	initial, _ := cat.Get(key)
	first := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
	require.Nil(t, first.Err)
	require.Equal(t, DefaultViewTTL, first.View.ExpiresAt.Sub(first.View.CreatedAt))
	storedID := first.View.ViewID
	first.View.ClosedVisibleSeq[key.String()] = 9999
	first.View.Projections[key.String()].SourceProjections[0].ProjectionGenerations[0] = "external-mutation"
	first.Ranges[0].Projection.SourceProjections[0].ClosedVisibleSeq = 9999
	lookup, ok := planner.GetView(storedID)
	require.True(t, ok)
	lookup.Projections[key.String()].SourceProjections[0].ClosedVisibleSeq = 0
	views := planner.Views()
	views[storedID].Owners[key.String()] = string(catalog.OwnerCold)
	clock.Store(base.Add(4 * time.Minute).UnixNano())
	reuse := planner.Plan(PlanRequest{ViewRef: &ViewRef{ViewID: storedID}})
	require.Nil(t, reuse.Err)
	require.Equal(t, base.Add(DefaultViewTTL), reuse.View.ExpiresAt)
	require.EqualValues(t, 100, reuse.Ranges[0].ClosedVisibleSeq)
	require.Equal(t, "p1", reuse.Ranges[0].Projection.SourceProjections[0].ProjectionGenerations[0])
	require.EqualValues(t, 100, reuse.Ranges[0].Projection.SourceProjections[0].ClosedVisibleSeq)
	require.Equal(t, catalog.OwnerHot, reuse.Ranges[0].Owner)
	after, _ := cat.Get(key)
	require.Equal(t, initial.JournalLastSeq, after.JournalLastSeq, "查询租约不得推进发布 CAS 修订")
	clock.Store(base.Add(DefaultViewTTL + time.Nanosecond).UnixNano())
	expired := planner.Plan(PlanRequest{ViewRef: &ViewRef{ViewID: storedID}})
	require.NotNil(t, expired.Err)
	require.Equal(t, ErrCodeViewStale, expired.Err.Code)
	_, err := planner.PinRead(storedID)
	require.ErrorIs(t, err, catalog.ErrQueryViewStale)
}

func TestQueryRealRangePinSurvivesInvalidationUntilGoroutineExits(t *testing.T) {
	for _, invalidation := range []string{"cancel-new", "ttl", "eviction", "worker-stop"} {
		t.Run(invalidation, func(t *testing.T) {
			cat, key := queryLeaseCatalog(t, nil)
			opts := Options{MaxRetainedViews: 1}
			if invalidation == "ttl" {
				opts.ViewTTL = 40 * time.Millisecond
			}
			planner := NewPlannerWithOptions(cat, nil, opts)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			entered, unblock := make(chan AuthoritativeRange, 1), make(chan struct{})
			client := FuncRangeClient{SearchFn: func(_ context.Context, rng AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
				entered <- rng
				<-unblock
				return RangeResult{Exhausted: true}, nil
			}}
			svc := NewService(planner, client, "pin-fixture")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan SearchResponse, 1)
			go func() { done <- svc.Search(ctx, QueryRequest{AuthorizedTargets: []string{key.String()}}) }()
			require.Equal(t, catalog.OwnerHot, (<-entered).Owner)
			views := planner.Views()
			require.Len(t, views, 1)
			var viewID string
			for id := range views {
				viewID = id
			}
			switch invalidation {
			case "cancel-new":
				cancel()
			case "eviction":
				second := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
				require.Nil(t, second.Err)
				require.NoError(t, planner.ReleaseView(second.View.ViewID))
			case "worker-stop":
				require.NoError(t, svc.Close())
			}
			require.Eventually(t, func() bool { _, ok := planner.GetView(viewID); return !ok }, time.Second, time.Millisecond)
			_, err := planner.PinRead(viewID)
			require.ErrorIs(t, err, catalog.ErrQueryViewStale)
			// 在途读取期间，物理日不得可退休（等价于原来的 Detach 未被调用）。
			if ok, reason := physicalDayRetirable(cat, key.UTCDay); ok {
				t.Fatalf("在途读取期间物理日不得可退休，却放行了：%s", reason)
			}
			select {
			case <-done:
				t.Fatal("真实客户端尚未退出，Service 不得提前交还 pin")
			default:
			}
			close(unblock)
			select {
			case response := <-done:
				if invalidation == "cancel-new" {
					require.Equal(t, ErrCodeCancelled, response.Err.Code)
				} else {
					require.Equal(t, ErrCodeViewStale, response.Err.Code)
					require.Equal(t, EnumStale, response.Coverage.EnumerationState)
					require.False(t, response.Exhausted)
					require.Empty(t, response.NextCursor)
				}
			case <-time.After(time.Second):
				t.Fatal("客户端退出后查询没有收敛")
			}
			// 客户端退出后仍不可退休：该日仍是 HOT 权威且未完成责任转移。
			// 这一条比原来「Detach 被调用一次」更严格——它断言的是**门禁本身**的结论。
			if ok, reason := physicalDayRetirable(cat, key.UTCDay); ok {
				t.Fatalf("HOT 权威仍在时物理日不得可退休：%s", reason)
			}
		})
	}
}

func TestQueryReusedCancelKeepsViewAndOtherReadersPinned(t *testing.T) {
	cat, key := queryLeaseCatalog(t, nil)
	planner := NewPlanner(cat, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	view := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}}).View
	entered := make(chan string, 2)
	firstExit, secondExit := make(chan struct{}), make(chan struct{})
	client := FuncRangeClient{SearchFn: func(_ context.Context, _ AuthoritativeRange, q RangeQuery) (RangeResult, error) {
		entered <- q.RequestID
		if q.RequestID == "first" {
			<-firstExit
		} else {
			<-secondExit
		}
		return RangeResult{Exhausted: true}, nil
	}}
	svc := NewService(planner, client, "concurrent-readers")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan SearchResponse, 2)
	for _, id := range []string{"first", "second"} {
		requestCtx := context.Background()
		if id == "first" {
			requestCtx = ctx
		}
		go func(id string, ctx context.Context) {
			results <- svc.Search(ctx, QueryRequest{RequestID: id, View: &ViewRef{ViewID: view.ViewID}})
		}(id, requestCtx)
	}
	<-entered
	<-entered
	cancel()
	close(firstExit)
	first := <-results
	require.Equal(t, ErrCodeCancelled, first.Err.Code)
	pin, err := planner.PinRead(view.ViewID)
	require.NoError(t, err, "复用请求取消只能释放本 request，不能关闭共享 view")
	pin()
	if ok, reason := physicalDayRetirable(cat, key.UTCDay); ok {
		t.Fatalf("在途读取期间物理日不得可退休：%s", reason)
	}
	// 把该日切到 COLD（模拟「新视图应当看到新归属」）。
	//
	// 2026-10-29：原来靠 `DayManager.Start` 走完整个迁移状态机；迁移链已删，
	// 这里直接按状态机允许的顺序推进 Catalog —— 本用例要验的是**视图复用**，
	// 不是迁移状态机本身（那已不存在）。
	require.NoError(t, func() error {
		if _, err := cat.BeginMigration(key, catalog.OwnerCold, "cold-2"); err != nil {
			return err
		}
		for _, state := range []catalog.MigrationState{catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify, catalog.StateAttachedStaging} {
			if _, err := cat.Advance(key, state); err != nil {
				return err
			}
		}
		if err := cat.RecordVerification(key, "verified-content"); err != nil {
			return err
		}
		current, _ := cat.Get(key)
		proj := current.Clone().PublishedProjection
		proj.QueryLocationDirID = "cold-2"
		proj.QueryGeneration = current.Generation + 1
		_, err := cat.SwitchOwner(key, catalog.OwnerCold, "cold-2", current.Generation+1, proj)
		return err
	}())
	old := planner.Plan(PlanRequest{ViewRef: &ViewRef{ViewID: view.ViewID}})
	require.Nil(t, old.Err)
	require.Equal(t, catalog.OwnerHot, old.Ranges[0].Owner)
	require.Equal(t, "hot-1", old.Ranges[0].DirID)
	fresh := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
	require.Nil(t, fresh.Err)
	require.Equal(t, catalog.OwnerCold, fresh.Ranges[0].Owner)
	require.NoError(t, planner.ReleaseView(view.ViewID))
	close(secondExit)
	<-results
}

func TestQueryWorkerRestartRejectsOldViewAndPrunesOrphanLease(t *testing.T) {
	journal := catalog.NewMemJournal()
	cat, key := queryLeaseCatalog(t, journal)
	oldPlanner := NewPlanner(cat, nil)
	view := oldPlanner.Plan(PlanRequest{TargetIDs: []string{key.String()}}).View
	t.Cleanup(func() { require.NoError(t, oldPlanner.Close()) })
	restarted := catalog.New(journal)
	planner := NewPlanner(restarted, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	res := planner.Plan(PlanRequest{ViewRef: &ViewRef{ViewID: view.ViewID}})
	require.NotNil(t, res.Err)
	require.Equal(t, ErrCodeViewStale, res.Err.Code)
	require.NoError(t, restarted.PruneQueryLeases(view.ExpiresAt.Add(time.Second)))
	record, _ := restarted.Get(key)
	require.Zero(t, record.ActiveLeaseCount(view.ExpiresAt.Add(time.Second)))
	fresh := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
	require.Nil(t, fresh.Err)
	require.NotEqual(t, view.ViewID, fresh.View.ViewID)
}

type queryLeaseFailureJournal struct {
	*catalog.MemJournal
	fail atomic.Bool
}

func (j *queryLeaseFailureJournal) AppendLeaseDelta(delta catalog.LeaseDelta) error {
	if j.fail.Load() {
		return fmt.Errorf("租约落盘失败")
	}
	return j.MemJournal.AppendLeaseDelta(delta)
}

func TestQueryLeaseCreationFailureAndUnsupportedRelease(t *testing.T) {
	journal := &queryLeaseFailureJournal{MemJournal: catalog.NewMemJournal()}
	cat, key := queryLeaseCatalog(t, journal)
	planner := NewPlanner(cat, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	journal.fail.Store(true)
	failed := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
	require.NotNil(t, failed.Err)
	require.Empty(t, planner.Views())
	journal.fail.Store(false)
	response := NewService(planner, nil, "unsupported").Search(context.Background(), QueryRequest{AuthorizedTargets: []string{key.String()}})
	require.Equal(t, ErrCodeUnsupported, response.Err.Code)
	record, _ := cat.Get(key)
	require.Zero(t, record.ActiveLeaseCount(time.Now()))
	require.Empty(t, planner.Views())
}

type queryResourceRecorder struct {
	deny       bool
	reserved   atomic.Int64
	admissions atomic.Int32
}

func (r *queryResourceRecorder) Admit(work vlsup.WorkClass) error {
	r.admissions.Add(1)
	if work != vlsup.WorkForeground || r.deny {
		return errors.New("前台预算拒绝")
	}
	return nil
}
func (r *queryResourceRecorder) Reserve(kind vlsup.ReservationKind, bytes int64) (func(), error) {
	if kind != vlsup.ReservationExport {
		return nil, errors.New("预留类别错误")
	}
	r.reserved.Add(bytes)
	return releaseOnce(func() { r.reserved.Add(-bytes) }), nil
}

type queryObservedCatalog struct {
	*catalog.Catalog
	snapshots atomic.Int32
}

func (c *queryObservedCatalog) SnapshotAndLease(req catalog.QuerySnapshotRequest) ([]catalog.QuerySnapshot, error) {
	c.snapshots.Add(1)
	return c.Catalog.SnapshotAndLease(req)
}

func TestQueryResourceAdmissionAndExportReservationFollowActualIO(t *testing.T) {
	cat, key := queryLeaseCatalog(t, nil)
	observed := &queryObservedCatalog{Catalog: cat}
	planner := NewPlanner(observed, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	var calls atomic.Int32
	entered, exit := make(chan struct{}), make(chan struct{})
	client := FuncRangeClient{SearchFn: func(_ context.Context, _ AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
		calls.Add(1)
		close(entered)
		<-exit
		return RangeResult{Items: []logtypes.Event{{EventID: "one"}}, Exhausted: true}, nil
	}}
	budget := &queryResourceRecorder{deny: true}
	svc := NewServiceWithOptions(planner, client, "budget", Options{ResourceBudget: budget})
	rejected := svc.Search(context.Background(), QueryRequest{AuthorizedTargets: []string{key.String()}})
	require.Equal(t, ErrCodeBudgetExceeded, rejected.Err.Code)
	require.Zero(t, calls.Load())
	require.Zero(t, observed.snapshots.Load(), "资源拒绝必须在深快照分配与租约 sidecar IO 之前")
	budget.deny = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan SearchResponse, 1)
	go func() {
		done <- svc.Search(ctx, QueryRequest{AuthorizedTargets: []string{key.String()}, AllowProjectionDrift: true, Budget: Budget{MaxBytes: 512}})
	}()
	<-entered
	cancel()
	require.EqualValues(t, 512, budget.reserved.Load(), "取消不能在真实 IO 退出前归还导出预留")
	close(exit)
	<-done
	require.Zero(t, budget.reserved.Load())
}

type queryAdmissionCatalog struct {
	*catalog.Catalog
	rejectTicket, rejectSnapshot bool
}

func (c *queryAdmissionCatalog) NewQueryViewID(now time.Time) (string, error) {
	if c.rejectTicket {
		return "", fmt.Errorf("%w: ticket capacity", catalog.ErrQueryAdmission)
	}
	return c.Catalog.NewQueryViewID(now)
}
func (c *queryAdmissionCatalog) SnapshotAndLease(req catalog.QuerySnapshotRequest) ([]catalog.QuerySnapshot, error) {
	if c.rejectSnapshot {
		return nil, fmt.Errorf("%w: snapshot capacity", catalog.ErrQueryAdmission)
	}
	return c.Catalog.SnapshotAndLease(req)
}

func TestQueryLegacyEvidenceUpgradeKeepsOnlyOriginalSet(t *testing.T) {
	for _, exportMode := range []bool{false, true} {
		for _, change := range []string{"append-upgrade", "identity-change", "prefix-zero", "rebuild"} {
			t.Run(fmt.Sprintf("export=%v/%s", exportMode, change), func(t *testing.T) {
				cat := catalog.New(nil)
				key := catalog.PartitionKey{StorageNamespace: "legacy-upgrade", UTCDay: "2026-09-22"}
				record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
				record.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "legacy-v1", ProjectionGeneration: "p1",
					CoveredSourceGenerations: []catalog.SourceGenerationRef{{LogSourceID: "source", SourceGeneration: "s1"}},
					ClosedVisibleSeq:         map[string]uint64{"default": 100}, CoverageComplete: true, QueryLocationDirID: "hot-1", QueryGeneration: 1}
				require.NoError(t, cat.PublishProjection(nil, record))
				planner := NewPlanner(cat, nil)
				t.Cleanup(func() { require.NoError(t, planner.Close()) })
				first := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
				require.Nil(t, first.Err)
				require.Len(t, first.Ranges, 1)
				expected, _ := cat.Get(key)
				next := expected.Clone()
				next.PublishedProjection.ManifestVersion = "modern-v2"
				next.PublishedProjection.ProjectionGenerations = []string{"p1", "p2"}
				next.PublishedProjection.ClosedVisibleSeq["default"] = 200
				next.PublishedProjection.SourceProjections = []catalog.SourceProjection{{
					SourceGenerationRef:   catalog.SourceGenerationRef{LogSourceID: "source", SourceGeneration: "s1"},
					ProjectionGenerations: []string{"p1", "p2"}, ClosedVisibleSeq: 200}}
				switch change {
				case "identity-change":
					next.PublishedProjection.SourceProjections[0].SourceGeneration = "replacement"
				case "prefix-zero":
					next.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 0
				case "rebuild":
					next.PublishedProjection.SourceProjections[0].ProjectionGenerations = []string{"p2"}
				}
				require.NoError(t, cat.PublishProjection(expected, next))
				reused := planner.Plan(PlanRequest{ViewRef: &ViewRef{ViewID: first.View.ViewID}, AllowProjectionDrift: exportMode})
				if change != "append-upgrade" {
					require.NotNil(t, reused.Err)
					require.Equal(t, ErrCodeViewStale, reused.Err.Code)
					return
				}
				require.Nil(t, reused.Err, "有完整旧来源及前缀证据的正常格式升级不能假装成源集合破坏")
				require.Equal(t, "legacy-v1", reused.Ranges[0].Projection.ManifestVersion)
				require.Equal(t, []string{"p1"}, reused.Ranges[0].Projection.SourceProjections[0].ProjectionGenerations)
				require.EqualValues(t, 100, reused.Ranges[0].Projection.SourceProjections[0].ClosedVisibleSeq)
			})
		}
	}
}

func TestQueryCatalogCapacityIsRetryableAndRecovers(t *testing.T) {
	for _, phase := range []string{"ticket", "snapshot"} {
		t.Run(phase, func(t *testing.T) {
			cat, key := queryLeaseCatalog(t, nil)
			limited := &queryAdmissionCatalog{Catalog: cat, rejectTicket: phase == "ticket", rejectSnapshot: phase == "snapshot"}
			planner := NewPlanner(limited, nil)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			failed := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
			require.NotNil(t, failed.Err)
			require.Equal(t, ErrCodeBudgetExceeded, failed.Err.Code)
			require.True(t, failed.Err.Retryable)
			require.Positive(t, failed.Err.RetryAfter)
			require.Contains(t, failed.Coverage.PartialReasons, ReasonBudgetExceeded)
			require.Empty(t, planner.Views())
			limited.rejectTicket, limited.rejectSnapshot = false, false
			recovered := planner.Plan(PlanRequest{TargetIDs: []string{key.String()}})
			require.Nil(t, recovered.Err)
			require.Len(t, recovered.Ranges, 1)
		})
	}
}
