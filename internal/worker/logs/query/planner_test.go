package query

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func testKey() catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}
}

func testKeyOld() catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-13"}
}

func newCatalogWithHot(t *testing.T) *catalog.Catalog {
	t.Helper()
	c := catalog.New(nil)
	require.NoError(t, c.Put(catalog.NewStableRecord(testKey(), catalog.OwnerHot, 1, "hot-g1")))
	return c
}

func buildEvent(sourceID, gen string, start, end uint64, eventTime, msg string) logtypes.Event {
	src := logtypes.SourceIdentity{LogSourceID: sourceID, SourceGeneration: gen, ParserVersion: "p1"}
	return logtypes.BuildEvent(src, logtypes.RecordRange{Start: start, End: end},
		eventTime, eventTime, "INFO", "stdout", msg)
}

// staging 排除：ATTACHED_STAGING 时 planner 只返回切换前 HOT 权威，绝不返回 staging 目录。
func TestPlanner_StagingExcludedFromQueryPlan(t *testing.T) {
	c := newCatalogWithHot(t)
	_, err := c.BeginMigration(testKey(), catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	for _, st := range []catalog.MigrationState{
		catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify, catalog.StateAttachedStaging,
	} {
		_, err = c.Advance(testKey(), st)
		require.NoError(t, err)
	}

	p := NewPlanner(c, nil)
	res := p.Plan(PlanRequest{RequestID: "r1"})
	require.Nil(t, res.Err)
	require.Len(t, res.Ranges, 1, "exactly one authoritative range per partition")
	require.Equal(t, "hot-g1", res.Ranges[0].DirID)
	require.Equal(t, catalog.OwnerHot, res.Ranges[0].Owner)
	require.NotEqual(t, "cold-g2", res.Ranges[0].DirID, "staging dir must never be planned")

	// Catalog 侧不变量与 planner 一致。
	require.False(t, c.IsQueryDirVisible(testKey(), "cold-g2"))
	require.True(t, c.IsQueryDirVisible(testKey(), "hot-g1"))
}

// OWNER_SWITCHED 后查询侧翻转到新权威；旧目录 residual 仍排除。
func TestPlanner_OwnerSwitchFlipsQuerySide(t *testing.T) {
	c := newCatalogWithHot(t)
	_, err := c.BeginMigration(testKey(), catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	for _, st := range []catalog.MigrationState{
		catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify, catalog.StateAttachedStaging,
	} {
		_, err = c.Advance(testKey(), st)
		require.NoError(t, err)
	}

	p := NewPlanner(c, nil)
	before := p.Plan(PlanRequest{RequestID: "before"})
	require.Len(t, before.Ranges, 1)
	require.Equal(t, "hot-g1", before.Ranges[0].DirID)
	require.EqualValues(t, 1, before.Ranges[0].Generation)

	proj := &catalog.PublishedProjection{
		ManifestVersion:    "pv-2",
		CoverageComplete:   true,
		ClosedVisibleSeq:   map[string]uint64{testKey().String(): 200},
		QueryLocationDirID: "cold-g2",
		QueryGeneration:    2,
	}
	_, err = c.SwitchOwner(testKey(), catalog.OwnerCold, "cold-g2", 2, proj)
	require.NoError(t, err)

	after := p.Plan(PlanRequest{RequestID: "after"})
	require.Nil(t, after.Err)
	require.Len(t, after.Ranges, 1)
	require.Equal(t, "cold-g2", after.Ranges[0].DirID)
	require.Equal(t, catalog.OwnerCold, after.Ranges[0].Owner)
	require.Equal(t, TierCold, after.Ranges[0].Tier)
	require.EqualValues(t, 2, after.Ranges[0].Generation)
	require.EqualValues(t, 200, after.Ranges[0].ClosedVisibleSeq)
	require.False(t, c.IsQueryDirVisible(testKey(), "hot-g1"))

	// 旧 view 携带 gen=1 → VIEW_STALE
	stale := p.Plan(PlanRequest{
		RequestID: "stale",
		ViewRef:   &ViewRef{ViewID: before.View.ViewID, OrderVersion: SortVersion},
	})
	require.NotNil(t, stale.Err)
	require.Equal(t, ErrCodeViewStale, stale.Err.Code)
}

// 同一 partition 绝不同时给出 hot+cold range。
func TestPlanner_NeverBothHotAndColdForSamePartition(t *testing.T) {
	c := newCatalogWithHot(t)
	// 人为制造“两边都像权威”的记录字段：Owner 仍是 hot，但 dirs 同时登记 cold staging。
	rec, ok := c.Get(testKey())
	require.True(t, ok)
	rec.Dirs = append(rec.Dirs, catalog.PhysicalDir{ID: "cold-g2", Role: catalog.DirStaging})
	rec.TargetDirID = "cold-g2"
	rec.TargetOwner = catalog.OwnerCold
	require.NoError(t, c.Put(rec))

	p := NewPlanner(c, nil)
	res := p.Plan(PlanRequest{})
	require.Len(t, res.Ranges, 1)
	require.Equal(t, "hot-g1", res.Ranges[0].DirID)
	require.Equal(t, catalog.OwnerHot, res.Ranges[0].Owner)
}

// 冷层缺失：RequireCold + ColdReady=false → partial coverage，且不额外并行 cold range。
func TestPlanner_PartialCoverageWhenColdMissing(t *testing.T) {
	c := catalog.New(nil)
	require.NoError(t, c.Put(catalog.NewStableRecord(testKeyOld(), catalog.OwnerHot, 1, "hot-old")))
	require.NoError(t, c.Put(catalog.NewStableRecord(testKey(), catalog.OwnerHot, 1, "hot-g1")))

	status := func(key catalog.PartitionKey, _ *catalog.Record, ref catalog.QueryRef) PartitionStatus {
		st := PartitionStatus{
			Queryable:      ref.OK,
			HotReady:       ref.OK && ref.Owner == catalog.OwnerHot,
			ColdConfigured: true,
			ColdReady:      false, // 冷层配置了但不可用/未就绪
		}
		return st
	}
	p := NewPlanner(c, status)
	res := p.Plan(PlanRequest{RequestID: "cold-miss", RequireCold: true})
	require.Nil(t, res.Err)

	// 两个分区仍各只有一个 range（catalog owner=hot），但 coverage 为 partial。
	require.Len(t, res.Ranges, 2)
	for _, r := range res.Ranges {
		require.Equal(t, catalog.OwnerHot, r.Owner)
		require.Equal(t, TierHot, r.Tier)
	}
	require.False(t, res.Coverage.Complete)
	require.Contains(t, res.Coverage.PartialReasons, ReasonColdMissing)
	for _, tc := range res.Coverage.Targets {
		require.Equal(t, CoveragePartial, tc.State)
		require.Contains(t, tc.Reasons, ReasonColdMissing)
	}
}

// 归档未恢复：owner=archive 且 ArchiveRestored=false → ARCHIVE_NOT_RESTORED partial。
func TestPlanner_ArchiveNotRestoredPartial(t *testing.T) {
	key := catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-01"}
	c := catalog.New(nil)
	require.NoError(t, c.Put(catalog.NewStableRecord(key, catalog.OwnerArchive, 5, "arch-1")))

	status := func(_ catalog.PartitionKey, _ *catalog.Record, _ catalog.QueryRef) PartitionStatus {
		return PartitionStatus{
			Queryable:         false,
			ArchiveConfigured: true,
			ArchiveRestored:   false,
			NotReadyReasons:   []string{ReasonArchiveNotRestored},
		}
	}
	p := NewPlanner(c, status)
	res := p.Plan(PlanRequest{RequestID: "arch", RequireArchive: true})
	require.Nil(t, res.Err)
	require.Empty(t, res.Ranges, "unrestored archive must not yield a queryable range")
	require.False(t, res.Coverage.Complete)
	require.Contains(t, res.Coverage.PartialReasons, ReasonArchiveNotRestored)
	require.Len(t, res.Coverage.Targets, 1)
	require.Equal(t, CoverageArchiveMiss, res.Coverage.Targets[0].State)
}

func TestPlannerArchiveOwnerRequiresPublishedRehydrateProjection(t *testing.T) {
	key := catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-10"}
	cat := catalog.New(nil)
	rec := catalog.NewStableRecord(key, catalog.OwnerArchive, 1, "deep-g1")
	require.NoError(t, cat.Put(rec))
	planner := NewPlanner(cat, nil)
	before := planner.Plan(PlanRequest{TargetIDs: []string{"node:1"}})
	require.Empty(t, before.Ranges)
	require.False(t, before.Coverage.Complete)
	require.Contains(t, before.Coverage.PartialReasons, ReasonArchiveNotRestored)
	reused := planner.Plan(PlanRequest{TargetIDs: []string{"node:1"}, ViewRef: &ViewRef{ViewID: before.View.ViewID}})
	require.Empty(t, reused.Ranges, "a missing archive cannot become queryable on a reused view")
	require.False(t, reused.Coverage.Complete)

	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion: "manifest-rh-1", ProjectionGeneration: "rehydrate-rh-1",
		QueryLocationDirID: "deep-g1", QueryGeneration: 1, CoverageComplete: true,
		ClosedVisibleSeq: map[string]uint64{key.String(): 8},
	}
	require.NoError(t, cat.Put(rec))
	after := planner.Plan(PlanRequest{TargetIDs: []string{"node:1"}})
	require.Len(t, after.Ranges, 1)
	require.True(t, after.Coverage.Complete)
	stale := planner.Plan(PlanRequest{TargetIDs: []string{"node:1"}, ViewRef: &ViewRef{ViewID: before.View.ViewID}})
	require.NotNil(t, stale.Err)
	require.Equal(t, ErrCodeViewStale, stale.Err.Code)
}

// VIEW_STALE：view_id 未知。
func TestPlanner_ViewStaleUnknownView(t *testing.T) {
	p := NewPlanner(newCatalogWithHot(t), nil)
	res := p.Plan(PlanRequest{ViewRef: &ViewRef{ViewID: "nope"}})
	require.NotNil(t, res.Err)
	require.Equal(t, ErrCodeViewStale, res.Err.Code)
}

// closed_visible_seq 来自 PublishedProjection，不拆开组合。
func TestPlanner_ClosedVisibleSeqFromProjection(t *testing.T) {
	c := newCatalogWithHot(t)
	rec, _ := c.Get(testKey())
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:    "pv-1",
		CoverageComplete:   false,
		ClosedVisibleSeq:   map[string]uint64{testKey().String(): 42},
		QueryLocationDirID: "hot-g1",
		QueryGeneration:    1,
		ConflictCount:      1,
	}
	require.NoError(t, c.Put(rec))

	p := NewPlanner(c, nil)
	res := p.Plan(PlanRequest{})
	require.Len(t, res.Ranges, 1)
	require.EqualValues(t, 42, res.Ranges[0].ClosedVisibleSeq)
	require.False(t, res.Coverage.Complete)
	require.Contains(t, res.Coverage.PartialReasons, ReasonProjectionIncomplete)
	require.Equal(t, DupConflict, res.Quality.DuplicateQuality)
	require.Equal(t, StatsPartial, res.Quality.StatsQuality)
}

func TestPlanner_ViewStaleWhenPublishedProjectionChanges(t *testing.T) {
	cat := newCatalogWithHot(t)
	rec, ok := cat.Get(testKey())
	require.True(t, ok)
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion: "manifest-1", ProjectionGeneration: "projection-1",
		CoverageComplete: true, QueryLocationDirID: rec.OwnerDirID,
		QueryGeneration:  rec.Generation,
		ClosedVisibleSeq: map[string]uint64{testKey().String(): 10},
	}
	require.NoError(t, cat.Put(rec))
	planner := NewPlanner(cat, nil)
	view := planner.Plan(PlanRequest{TargetIDs: []string{testKey().StorageNamespace}}).View
	require.NotNil(t, view)
	rec, ok = cat.Get(testKey())
	require.True(t, ok)
	rec.PublishedProjection.ManifestVersion = "manifest-2"
	rec.PublishedProjection.ProjectionGeneration = "projection-2"
	rec.PublishedProjection.ClosedVisibleSeq[testKey().String()] = 20
	require.NoError(t, cat.Put(rec))
	result := planner.Plan(PlanRequest{ViewRef: &ViewRef{ViewID: view.ViewID}})
	require.NotNil(t, result.Err)
	require.Equal(t, ErrCodeViewStale, result.Err.Code)
}

// 授权目标过滤：未授权分区不得进入 ranges。
func TestPlanner_AuthorizedTargetsFilter(t *testing.T) {
	c := catalog.New(nil)
	require.NoError(t, c.Put(catalog.NewStableRecord(testKey(), catalog.OwnerHot, 1, "hot-g1")))
	require.NoError(t, c.Put(catalog.NewStableRecord(testKeyOld(), catalog.OwnerHot, 1, "hot-old")))

	p := NewPlanner(c, nil)
	res := p.Plan(PlanRequest{TargetIDs: []string{testKey().String()}})
	require.Len(t, res.Ranges, 1)
	require.Equal(t, testKey().String(), res.Ranges[0].TargetID)
	require.Len(t, res.Coverage.Targets, 1)
}

// SortKey 契约：event_time DESC → log_source_id → generation → record_start DESC → event_id。
func TestSortKey_ContractOrder(t *testing.T) {
	a := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "s1", SourceGeneration: "g1", RecordStart: 10, RecordEnd: 11, EventID: "e1"}
	b := SortKey{EventTimeUTC: "2026-09-20T09:00:00Z", LogSourceID: "s0", SourceGeneration: "g9", RecordStart: 99, EventID: "e0"}
	require.True(t, LessSortKey(a, b), "newer event_time first")

	// 同 event_time：log_source_id ASC
	c1 := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "aaa", SourceGeneration: "g2", RecordStart: 1, EventID: "z"}
	c2 := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "bbb", SourceGeneration: "g1", RecordStart: 9, EventID: "a"}
	require.True(t, LessSortKey(c1, c2), "log_source_id ASC")

	// 同 source：generation ASC
	d1 := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "s", SourceGeneration: "g1", RecordStart: 5, EventID: "a"}
	d2 := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "s", SourceGeneration: "g2", RecordStart: 1, EventID: "b"}
	require.True(t, LessSortKey(d1, d2), "source_generation ASC")

	// 同 generation：record_start DESC
	e1 := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "s", SourceGeneration: "g1", RecordStart: 20, EventID: "a"}
	e2 := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "s", SourceGeneration: "g1", RecordStart: 10, EventID: "b"}
	require.True(t, LessSortKey(e1, e2), "record_start DESC")

	// 同 record_start：event_id ASC
	f1 := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "s", SourceGeneration: "g1", RecordStart: 1, RecordEnd: 2, EventID: "aaa"}
	f2 := SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "s", SourceGeneration: "g1", RecordStart: 1, RecordEnd: 2, EventID: "bbb"}
	require.True(t, LessSortKey(f1, f2), "event_id ASC")

	items := []logtypes.Event{
		buildEvent("s", "g1", 1, 2, "2026-09-20T09:00:00Z", "old"),
		buildEvent("s", "g1", 3, 4, "2026-09-20T11:00:00Z", "new"),
		buildEvent("s", "g1", 2, 3, "2026-09-20T10:00:00Z", "mid"),
	}
	SortEvents(items)
	require.Equal(t, "new", items[0].Message)
	require.Equal(t, "mid", items[1].Message)
	require.Equal(t, "old", items[2].Message)
}

// Search：limit/budget 受尊重；注入 client 返回事件。
func TestSearch_RespectsLimitAndBudget(t *testing.T) {
	c := newCatalogWithHot(t)
	proj := &catalog.PublishedProjection{
		ManifestVersion:  "pv-1",
		CoverageComplete: true,
		ClosedVisibleSeq: map[string]uint64{testKey().String(): 10},
	}
	rec, _ := c.Get(testKey())
	rec.PublishedProjection = proj
	require.NoError(t, c.Put(rec))

	client := FuncRangeClient{
		SearchFn: func(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error) {
			require.Equal(t, "hot-g1", rng.DirID)
			require.EqualValues(t, 10, q.ClosedVisibleSeq)
			var out []logtypes.Event
			for i := 0; i < 5; i++ {
				out = append(out, buildEvent("src", "g1", uint64(i), uint64(i+1),
					time.Date(2026, 9, 20, 10, i, 0, 0, time.UTC).Format(time.RFC3339), "m"))
			}
			return RangeResult{Items: out, Exhausted: true, Bytes: 100}, nil
		},
	}
	svc := NewService(NewPlanner(c, nil), client, "test-build")

	// limit=2 → 只返回 2 条，next_cursor 非空，exhausted=false
	resp := svc.Search(context.Background(), QueryRequest{
		RequestID: "s1",
		Budget:    Budget{Limit: 2},
	})
	require.Nil(t, resp.Err)
	require.Len(t, resp.Items, 2)
	require.False(t, resp.Exhausted)
	require.NotEmpty(t, resp.NextCursor)
	require.True(t, resp.Coverage.Complete)

	// max_fanout=0 且 limit 足够 → 全部返回
	resp2 := svc.Search(context.Background(), QueryRequest{
		RequestID: "s2",
		Budget:    Budget{Limit: 100},
	})
	require.Len(t, resp2.Items, 5)
	require.True(t, resp2.Exhausted)

	// max_bytes 过小 → budget incomplete
	resp3 := svc.Search(context.Background(), QueryRequest{
		RequestID: "s3",
		Budget:    Budget{Limit: 100, MaxBytes: 10},
	})
	require.Nil(t, resp3.Err)
	require.False(t, resp3.Coverage.Complete)
	require.Contains(t, resp3.Coverage.PartialReasons, ReasonBudgetExceeded)
}

// Unimplemented 能力路径：client 未实现时不得返回空完整结果。
func TestSearch_UnimplementedCapabilityPath(t *testing.T) {
	c := newCatalogWithHot(t)
	svc := NewService(NewPlanner(c, nil), nil, "old-worker") // nil → UnimplementedRangeClient

	caps := svc.GetCapabilities(CapabilitiesRequest{ProtocolVersion: DefaultProtocolVersion})
	require.True(t, caps.Supported)
	require.NotContains(t, caps.Capabilities, "search")

	resp := svc.Search(context.Background(), QueryRequest{RequestID: "u1", Budget: Budget{Limit: 10}})
	require.NotNil(t, resp.Err)
	require.Equal(t, ErrCodeUnsupported, resp.Err.Code)
	require.False(t, resp.Coverage.Complete, "unsupported must not look like complete empty success")
	require.Contains(t, resp.Coverage.PartialReasons, ReasonUnsupported)

	// 协议版本不匹配：旧 worker 风格显式 unsupported
	bad := svc.GetCapabilities(CapabilitiesRequest{ProtocolVersion: "log-query/0"})
	require.False(t, bad.Supported)
	require.NotEmpty(t, bad.UnsupportedReasons)
	require.Equal(t, ErrCodeUnsupported, bad.UnsupportedReasons[0].Code)

	// Stats / Tail FOLLOW_LIVE 同样走 unsupported
	st := svc.Stats(context.Background(), QueryRequest{RequestID: "u2"}, nil)
	require.NotNil(t, st.Err)
	require.Equal(t, ErrCodeUnsupported, st.Err.Code)

	tl := svc.Tail(context.Background(), QueryRequest{RequestID: "u3"}, TailFollowLive)
	require.NotNil(t, tl.Err)
	require.Equal(t, ErrCodeUnsupported, tl.Err.Code)
}

// Search VIEW_STALE：owner switch 后旧 view_id 失效。
func TestSearch_ViewStaleAfterOwnerSwitch(t *testing.T) {
	c := newCatalogWithHot(t)
	p := NewPlanner(c, nil)
	first := p.Plan(PlanRequest{RequestID: "create"})

	_, err := c.BeginMigration(testKey(), catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	for _, st := range []catalog.MigrationState{
		catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify, catalog.StateAttachedStaging,
	} {
		_, err = c.Advance(testKey(), st)
		require.NoError(t, err)
	}
	_, err = c.SwitchOwner(testKey(), catalog.OwnerCold, "cold-g2", 2, &catalog.PublishedProjection{
		ManifestVersion:  "pv-2",
		CoverageComplete: true,
		ClosedVisibleSeq: map[string]uint64{testKey().String(): 50},
	})
	require.NoError(t, err)

	client := FuncRangeClient{
		SearchFn: func(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error) {
			return RangeResult{Items: []logtypes.Event{buildEvent("s", "g2", 1, 2, "2026-09-20T12:00:00Z", "x")}}, nil
		},
	}
	svc := NewService(p, client, "b")

	// 新请求（无 view）→ 使用 cold 权威
	fresh := svc.Search(context.Background(), QueryRequest{RequestID: "fresh", Budget: Budget{Limit: 10}})
	require.Nil(t, fresh.Err)
	require.Len(t, fresh.Items, 1)

	// 旧 view → VIEW_STALE
	stale := svc.Search(context.Background(), QueryRequest{
		RequestID: "stale",
		View:      &ViewRef{ViewID: first.View.ViewID, OrderVersion: SortVersion},
		Budget:    Budget{Limit: 10},
	})
	require.NotNil(t, stale.Err)
	require.Equal(t, ErrCodeViewStale, stale.Err.Code)
}

// Stats stub：消费 planner ranges；二次聚合 Avg=sum/count。
func TestStats_MergesLogicalEventSets(t *testing.T) {
	c := newCatalogWithHot(t)
	client := FuncRangeClient{
		StatsFn: func(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (StatsResult, error) {
			return StatsResult{
				Points: []StatsPoint{
					{Dimensions: map[string]string{"level": "INFO"}, Count: 3, Sum: 30, Min: 5, Max: 20},
					{Dimensions: map[string]string{"level": "ERROR"}, Count: 1, Sum: 7, Min: 7, Max: 7},
				},
				StatsQuality: StatsExact,
			}, nil
		},
	}
	svc := NewService(NewPlanner(c, nil), client, "b")
	resp := svc.Stats(context.Background(), QueryRequest{RequestID: "st", Budget: Budget{Limit: 50}}, nil)
	require.Nil(t, resp.Err)
	require.True(t, resp.Coverage.Complete)
	require.Len(t, resp.Points, 2)
	for _, p := range resp.Points {
		if p.Dimensions["level"] == "INFO" {
			require.EqualValues(t, 3, p.Count)
			require.InDelta(t, 10.0, p.Avg(), 1e-9)
		}
	}
}

// Tail VIEW_BOUNDED：使用 planner ranges + limit。
func TestTail_ViewBoundedRespectsLimit(t *testing.T) {
	c := newCatalogWithHot(t)
	client := FuncRangeClient{
		TailFn: func(ctx context.Context, rng AuthoritativeRange, q RangeQuery, mode TailMode) (RangeResult, error) {
			require.Equal(t, TailViewBounded, mode)
			return RangeResult{Items: []logtypes.Event{
				buildEvent("s", "g1", 1, 2, "2026-09-20T10:00:00Z", "t1"),
				buildEvent("s", "g1", 2, 3, "2026-09-20T10:01:00Z", "t2"),
				buildEvent("s", "g1", 3, 4, "2026-09-20T10:02:00Z", "t3"),
			}}, nil
		},
	}
	svc := NewService(NewPlanner(c, nil), client, "b")
	resp := svc.Tail(context.Background(), QueryRequest{RequestID: "tl", Budget: Budget{Limit: 2}}, TailViewBounded)
	require.Nil(t, resp.Err)
	require.Len(t, resp.Items, 2)
	require.False(t, resp.Exhausted)
	require.Equal(t, TailViewBounded, resp.Mode)
}

// Cursor 只携带 view_id + sort key + page_limit。
func TestCursor_RoundTrip(t *testing.T) {
	c := Cursor{
		ViewID:       "view-1",
		OrderVersion: SortVersion,
		SortKey:      SortKey{EventTimeUTC: "2026-09-20T10:00:00Z", LogSourceID: "s", SourceGeneration: "g1", RecordStart: 3, EventID: "e"},
		PageLimit:    200,
	}
	enc := EncodeCursor(c)
	require.NotEmpty(t, enc)
	dec, err := DecodeCursor(enc)
	require.NoError(t, err)
	require.Equal(t, c.ViewID, dec.ViewID)
	require.Equal(t, c.PageLimit, dec.PageLimit)
	require.Equal(t, c.SortKey.EventID, dec.SortKey.EventID)
}

// Search 分页：cursor 之后继续，且仍受 limit 约束。
func TestSearch_CursorPagination(t *testing.T) {
	c := newCatalogWithHot(t)
	client := FuncRangeClient{
		SearchFn: func(ctx context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error) {
			return RangeResult{Items: []logtypes.Event{
				buildEvent("s", "g1", 1, 2, "2026-09-20T10:00:00Z", "e1"),
				buildEvent("s", "g1", 2, 3, "2026-09-20T11:00:00Z", "e2"),
				buildEvent("s", "g1", 3, 4, "2026-09-20T12:00:00Z", "e3"),
			}}, nil
		},
	}
	svc := NewService(NewPlanner(c, nil), client, "b")
	page1 := svc.Search(context.Background(), QueryRequest{RequestID: "p1", Budget: Budget{Limit: 2}})
	require.Len(t, page1.Items, 2)
	require.Equal(t, "e3", page1.Items[0].Message, "DESC: newest first")
	require.NotEmpty(t, page1.NextCursor)

	page2 := svc.Search(context.Background(), QueryRequest{
		RequestID: "p2",
		View:      &ViewRef{ViewID: page1.View.ViewID, Cursor: page1.NextCursor, OrderVersion: SortVersion},
		Budget:    Budget{Limit: 2},
	})
	require.Nil(t, page2.Err)
	require.Len(t, page2.Items, 1)
	require.Equal(t, "e1", page2.Items[0].Message)
	require.True(t, page2.Exhausted)
}

// RecoveryRequired + failure 状态：coverage partial，Catalog owner 仍可查询（若 side queryable）。
func TestPlanner_RecoveryRequiredPartial(t *testing.T) {
	c := newCatalogWithHot(t)
	_, err := c.BeginMigration(testKey(), catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	for _, st := range []catalog.MigrationState{catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify} {
		_, err = c.Advance(testKey(), st)
		require.NoError(t, err)
	}
	_, err = c.Fail(testKey(), catalog.StateFailedRetryable, "snapshot io error")
	require.NoError(t, err)

	p := NewPlanner(c, nil)
	res := p.Plan(PlanRequest{RequestID: "rec"})
	// 未切换权威：查询侧仍在 hot；但 recovery 未完成 → partial。
	require.Len(t, res.Ranges, 1)
	require.Equal(t, "hot-g1", res.Ranges[0].DirID)
	require.False(t, res.Coverage.Complete)
	require.Contains(t, res.Coverage.PartialReasons, ReasonRecoveryRequired)
}

// TestPlannerViewStoreIsBounded 锁定视图注册表有界：超上限时淘汰最旧视图（真机 30 分钟压测暴露
// 的无界增长会令 Worker RSS 超 §6.6 的 1GiB）。
func TestPlannerViewStoreIsBounded(t *testing.T) {
	p := NewPlanner(nil, nil)
	base := time.Now()
	for i := 0; i < maxRetainedViews+50; i++ {
		id := "v" + strconv.Itoa(i)
		p.views[id] = &QueryView{ViewID: id, CreatedAt: base.Add(time.Duration(i) * time.Millisecond)}
	}
	p.mu.Lock()
	p.pruneViewsLocked()
	size := len(p.views)
	_, hasOldest := p.views["v0"]
	_, hasNewest := p.views["v"+strconv.Itoa(maxRetainedViews+49)]
	p.mu.Unlock()

	if size >= maxRetainedViews+50 {
		t.Fatalf("view store must shrink: got %d", size)
	}
	if size >= maxRetainedViews {
		t.Fatalf("view store must be below cap after prune: got %d", size)
	}
	if hasOldest {
		t.Fatal("oldest view should be evicted")
	}
	if !hasNewest {
		t.Fatal("newest view must be retained")
	}
}
