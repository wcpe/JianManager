package vlrange

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 审查反例进入正式回归，使用真实 Planner 与归因谓词。
func review05ScopedCatalog(t *testing.T) (*catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion: "m1", CoverageComplete: true, QueryLocationDirID: "hot-1", QueryGeneration: 1,
		ClosedVisibleSeq: map[string]uint64{key.String(): 1000, "default": 1000, "slow/g1": 100, "fast/g1": 1000},
		SourceProjections: []catalog.SourceProjection{
			{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "slow", SourceGeneration: "g1"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 100},
			{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "fast", SourceGeneration: "g1"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 1000},
		},
	}
	require.NoError(t, cat.Put(rec))
	return cat, key
}
func review05ScopedEvent(end uint64) logtypes.Event {
	return logtypes.Event{EventID: "slow-event", EventTimeUTC: "2026-09-22T12:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "slow", SourceGeneration: "g1"}, Record: logtypes.RecordRange{Start: end - 1, End: end}, Fields: map[string]string{projectionGenerationField: "p1"}}
}

func TestReview05ProjectionDriftChangesPerSourceSnapshot(t *testing.T) {
	for _, change := range []string{"advance", "remove", "zero", "add", "rebuild"} {
		t.Run(change, func(t *testing.T) {
			cat, key := review05ScopedCatalog(t)
			planner := query.NewPlanner(cat, nil)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			first := planner.Plan(query.PlanRequest{TargetIDs: []string{key.StorageNamespace}})
			require.Nil(t, first.Err)
			require.Len(t, first.Ranges, 1)
			rec, _ := cat.Get(key)
			rec.PublishedProjection.ManifestVersion = "m2"
			var ev logtypes.Event
			wantMatch := false
			switch change {
			case "advance":
				ev = review05ScopedEvent(150)
				require.False(t, rangeMatchesEvent(first.Ranges[0], ev))
				rec.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 200
				rec.PublishedProjection.ClosedVisibleSeq["slow/g1"] = 200
			case "remove":
				ev = review05ScopedEvent(90)
				require.True(t, rangeMatchesEvent(first.Ranges[0], ev))
				rec.PublishedProjection.SourceProjections = rec.PublishedProjection.SourceProjections[1:]
				delete(rec.PublishedProjection.ClosedVisibleSeq, "slow/g1")
				wantMatch = true
			case "zero":
				ev = review05ScopedEvent(90)
				require.True(t, rangeMatchesEvent(first.Ranges[0], ev))
				rec.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 0
				rec.PublishedProjection.ClosedVisibleSeq["slow/g1"] = 0
				wantMatch = true
			case "add":
				rec.PublishedProjection.SourceProjections = append(rec.PublishedProjection.SourceProjections,
					catalog.SourceProjection{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "new-source", SourceGeneration: "g1"},
						ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 100})
			case "rebuild":
				rec.PublishedProjection.SourceProjections[0].ProjectionGenerations = []string{"replacement"}
			}
			require.NoError(t, cat.Put(rec))
			next := planner.Plan(query.PlanRequest{TargetIDs: []string{key.StorageNamespace}, ViewRef: &query.ViewRef{ViewID: first.View.ViewID}, AllowProjectionDrift: true})
			t.Logf("变化=%s；旧partition水位=%d，reuse错误=%v complete=%v", change, first.Ranges[0].ClosedVisibleSeq, next.Err, next.Coverage.Complete)
			if change != "advance" {
				require.NotNil(t, next.Err, "破坏原集合必须明确失效")
				require.Equal(t, query.ErrCodeViewStale, next.Err.Code)
				return
			}
			require.Nil(t, next.Err, "正常追加必须继续使用旧集合，不能令活源导出恒失败")
			require.Len(t, next.Ranges, 1)
			gotMatch := rangeMatchesEvent(next.Ranges[0], ev)
			t.Logf("旧view归因发生变化：record_end=%d，复用匹配=%v，冻结集合应匹配=%v", ev.Record.End, gotMatch, wantMatch)
			require.Equal(t, wantMatch, gotMatch, "同一view要么保持逐源集合、要么明确VIEW_STALE，不能静默改变集合")
		})
	}
}

// 零封闭前缀（cvs=0）的语义：**不加上界**，不是「无限读取」。
//
// # 这条用例原来锁的是相反的方向（2026-11-05 订正）
//
// 原文断言 `require.False(t, rangeMatchesEvent(rng, ev), "零封闭前缀不能代表无限读取")`。
// 但同一个 rng 喂给 `selectorQuery` 得到的是
//
//	(log_source_id:="slow" OR log_source_id:"slow/"*)
//
// ——**根本没有 record_end 谓词**，VL 会把行返回给客户端。也就是说旧断言要求
// 「LogsQL 放行、内存归因丢弃」：粗筛有行、结果为空。那正是本次事故的同源形态，
// 而且它在合并路径上表现为「整页静默为空 + Exhausted=true + quality=exact」。
//
// # 为什么 cvs=0 不是「无限读取」
//
// 来源约束仍然生效：命名空间前缀 + 该 range 的 UTC 天窗口（本用例里是
// inst:slow / 2026-09-22）。cvs 只是「已封水位的记录前缀」，0 = 没有封窗信息，
// 并不意味着可以越过任何边界——边界是分区本身。
//
// 这也是 8324d505（零水位不再当零上界）已定下的口径，单 range 路径与 selector
// 两侧都是它；本条把合并路径也钉到同一口径上。
func TestReview05LegacyZeroClosedPrefixIsUnlimited(t *testing.T) {
	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "slow", UTCDay: "2026-09-22"}, Owner: catalog.OwnerHot, ClosedVisibleSeq: 0, Projection: &catalog.PublishedProjection{ProjectionGeneration: "p1"}}
	ev := review05ScopedEvent(10)
	sel := selectorQuery(rng, query.RangeQuery{ClosedVisibleSeq: 0})
	matched := rangeMatchesEvent(rng, ev)
	t.Logf("legacy ClosedVisibleSeq=0；selector=%s，内存归因匹配=%v", sel, matched)

	// ① 零水位不得渲染成 `record_end:<=0`（否则恒 0 行）。
	require.NotContains(t, sel, "record_end:<=", "零水位是「无封窗信息」，不是「上界为 0」")
	// ② 来源约束仍在：只读该命名空间，不是整个实例。
	require.Contains(t, sel, `log_source_id:="slow"`)
	// ③ **两条路径必须一致**：selector 放行的行，内存归因也必须放行。
	//    这是本次故障的根因形式（一处改了另一处没改 → 粗筛有行、结果全丢）。
	require.Equal(t, true, matched,
		"selectorQuery 不加上界 ⇒ 内存归因也不得上界，两侧必须等价（否则合并路径静默丢整页）")
}

// 反向守卫：cvs>0 是真实封窗语义，两侧都不能放宽。
func TestReview05NonZeroClosedPrefixStillBounds(t *testing.T) {
	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "slow", UTCDay: "2026-09-22"}, Owner: catalog.OwnerHot, ClosedVisibleSeq: 100, Projection: &catalog.PublishedProjection{ProjectionGeneration: "p1"}}
	require.Contains(t, selectorQuery(rng, query.RangeQuery{ClosedVisibleSeq: 100}), "record_end:<=100")
	require.False(t, rangeMatchesEvent(rng, review05ScopedEvent(101)), "越过水位的行不得放行")
	require.True(t, rangeMatchesEvent(rng, review05ScopedEvent(100)), "恰好等于水位的行必须可见")
}
