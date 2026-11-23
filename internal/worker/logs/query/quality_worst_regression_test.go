package query

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件守护「质量等级必须按 rank 取 worst 合并」的 Worker 侧路径。
//
// 共同缺陷模式：调用点绕过 worst* 合并函数，改用无条件赋值 ⇒ 更差的等级被更好的
// 覆盖，或结果随 key 排序顺序漂移。质量等级在 UI 上承载补救指引（conflict 有人工
// 修复入口，unresolved 没有），降级不只是数字错，而是入口丢失。

const qualityTestDay = "2026-09-10"

// putQualityPartition 写入一个带指定 PublishedProjection 的 HOT 分区。
func putQualityPartition(t *testing.T, c *catalog.Catalog, ns string, project func(key catalog.PartitionKey) *catalog.PublishedProjection) catalog.PartitionKey {
	t.Helper()
	key := catalog.PartitionKey{StorageNamespace: ns, UTCDay: qualityTestDay}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	rec.PublishedProjection = project(key)
	require.NoError(t, c.Put(rec))
	return key
}

// conflictProjection：coverage 完整但存在冲突 ⇒ DupConflict。
func conflictProjection(key catalog.PartitionKey) *catalog.PublishedProjection {
	return &catalog.PublishedProjection{
		ManifestVersion:    "pv-conflict",
		CoverageComplete:   true,
		ConflictCount:      1,
		QueryLocationDirID: "hot-g1",
		QueryGeneration:    1,
		ClosedVisibleSeq:   map[string]uint64{key.String(): 10},
	}
}

// incompleteProjection：projection 未覆盖完整 ⇒ DupUnresolved。
func incompleteProjection(key catalog.PartitionKey) *catalog.PublishedProjection {
	return &catalog.PublishedProjection{
		ManifestVersion:    "pv-incomplete",
		CoverageComplete:   false,
		QueryLocationDirID: "hot-g1",
		QueryGeneration:    1,
		ClosedVisibleSeq:   map[string]uint64{key.String(): 10},
	}
}

// D1-a：planner 跨 target 的质量合并必须与 namespace 命名顺序无关。
//
// 修复前 selectKeys 按 key.String() 升序给出 keys，循环内是无条件赋值：
//   - conflict 分区排在前面 ⇒ 被后处理的 unresolved 覆盖，最终上报 unresolved；
//   - 交换命名后同一份数据又上报 conflict。
//
// 两个方向都必须得到 conflict（更差者胜）。
func TestPlanner_DuplicateQualityIndependentOfTargetOrder(t *testing.T) {
	for _, tc := range []struct {
		name         string
		conflictNS   string
		unresolvedNS string
	}{
		{name: "conflict 分区排序在前", conflictNS: "a-ns", unresolvedNS: "b-ns"},
		{name: "conflict 分区排序在后", conflictNS: "b-ns", unresolvedNS: "a-ns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := catalog.New(nil)
			conflictKey := putQualityPartition(t, c, tc.conflictNS, conflictProjection)
			unresolvedKey := putQualityPartition(t, c, tc.unresolvedNS, incompleteProjection)

			// 先确认排序方向符合用例意图，否则「两个方向」的对照会失去意义。
			if tc.conflictNS < tc.unresolvedNS {
				require.Less(t, conflictKey.String(), unresolvedKey.String(),
					"本用例要求 conflict 分区排在 unresolved 之前")
			} else {
				require.Greater(t, conflictKey.String(), unresolvedKey.String(),
					"本用例要求 conflict 分区排在 unresolved 之后")
			}

			res := NewPlanner(c, nil).Plan(PlanRequest{RequestID: "quality-order"})
			require.Nil(t, res.Err)
			require.Equal(t, DupConflict, res.Quality.DuplicateQuality,
				"conflict 比 unresolved 更差，跨 target 合并后必须保留")
			require.Equal(t, StatsPartial, res.Quality.StatsQuality)
		})
	}
}

// D1-b：range 侧只能报 unresolved 时，planner 判定的 conflict 不得被丢弃。
//
// 修复前守卫是「resp 仍为 exact 才采纳 plan」：range 回调先把 resp 置为 unresolved，
// 守卫即为假，plan 的 conflict 被整体丢弃 ⇒ 用户失去唯一的人工修复入口。
func TestSearchKeepsPlanConflictWhenRangeReportsUnresolved(t *testing.T) {
	c := catalog.New(nil)
	putQualityPartition(t, c, "ns/game-1", conflictProjection)

	// 前置条件：planner 必须判定 conflict 且给出 range，否则本用例不成立。
	plan := NewPlanner(c, nil).Plan(PlanRequest{RequestID: "precheck"})
	require.Equal(t, DupConflict, plan.Quality.DuplicateQuality)
	require.Len(t, plan.Ranges, 1)

	client := FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
		return RangeResult{
			Items:            []logtypes.Event{buildEvent("ns/game-1", "g1", 1, 2, qualityTestDay+"T01:00:00Z", "dup")},
			DuplicateQuality: DupUnresolved, // 同 id 异哈希：range 侧只能报 unresolved。
			Exhausted:        true,
			Bytes:            1,
		}, nil
	}}
	resp := NewService(NewPlanner(c, nil), client, "test").Search(context.Background(), QueryRequest{RequestID: "d1b"})
	require.Nil(t, resp.Err)
	require.Equal(t, DupConflict, resp.Quality.DuplicateQuality,
		"planner 的 conflict 比 range 的 unresolved 更差，必须整体保留")
}

// D3：ConflictCount>0 但 coverage 仍 complete 的分区，stats_quality 不得是 exact。
//
// planner 会因冲突判 StatsPartial，而该路径**不追加任何 coverage 原因**，
// 故 service 末尾「!plan.Coverage.Complete 才降 partial」的兜底不会触发；
// 修复前 Stats 入口处的无条件 `= StatsExact` 会把握手结果错报成精确统计。
func TestStatsReportsPartialForConflictPartition(t *testing.T) {
	c := catalog.New(nil)
	putQualityPartition(t, c, "ns/game-1", conflictProjection)

	plan := NewPlanner(c, nil).Plan(PlanRequest{RequestID: "precheck"})
	require.Equal(t, StatsPartial, plan.Quality.StatsQuality)
	require.True(t, plan.Coverage.Complete,
		"前置条件：冲突不追加 coverage 原因，故 coverage 仍是 complete（这正是错报的成因）")

	client := FuncRangeClient{StatsFn: func(context.Context, AuthoritativeRange, RangeQuery) (StatsResult, error) {
		return StatsResult{}, nil
	}}
	resp := NewService(NewPlanner(c, nil), client, "test").Stats(context.Background(), QueryRequest{RequestID: "d3"}, []string{"level"})
	require.Nil(t, resp.Err)
	require.NotEqual(t, StatsExact, resp.Quality.StatsQuality,
		"partition 存在未解决冲突时不得宣称精确统计")
	require.Equal(t, StatsPartial, resp.Quality.StatsQuality)
}

// putCleanPartition 写入一个 projection 完整、无冲突的 HOT 分区（质量基线 exact）。
func putCleanPartition(t *testing.T, c *catalog.Catalog, ns string) catalog.PartitionKey {
	t.Helper()
	return putQualityPartition(t, c, ns, func(key catalog.PartitionKey) *catalog.PublishedProjection {
		return &catalog.PublishedProjection{
			ManifestVersion:    "pv-clean",
			CoverageComplete:   true,
			QueryLocationDirID: "hot-g1",
			QueryGeneration:    1,
			ClosedVisibleSeq:   map[string]uint64{key.String(): 10},
		}
	})
}

// serialBudget 让 runBudgetTasks 一次只跑一个任务：MaxBytes=1 时首个任务即占满额度，
// 后续启动被 available==0 挡住；各 range 上报 Bytes=0 使 used 不增长、不会触发 stop，
// 于是任务严格按 plan.Ranges 顺序串行执行 → apply 顺序确定，用例可稳定复现顺序缺陷。
func serialBudget() Budget { return Budget{Limit: 100, MaxBytes: 1} }

// 跨 range 的重复质量必须按 worst 累积：两个方向（conflict 先/后于 unresolved）都必须
// 得到 conflict。
//
// 修复前 range 回调是 `if q != "" && q != DupExact { resp.Quality.DuplicateQuality = q }`：
// 它只挡住了「回升到 exact」，没挡住两个非 exact 值互相覆盖——conflict 先完成、unresolved
// 后完成时 conflict 被冲成 unresolved，结果取决于任务完成顺序。
func TestSearchAcrossRangesKeepsWorstDuplicateQuality(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstQuality  DuplicateQuality
		secondQuality DuplicateQuality
		firstNs       string
		secondNs      string
	}{
		{name: "conflict 先、unresolved 后", firstNs: "a-ns", secondNs: "b-ns",
			firstQuality: DupConflict, secondQuality: DupUnresolved},
		{name: "unresolved 先、conflict 后", firstNs: "a-ns", secondNs: "b-ns",
			firstQuality: DupUnresolved, secondQuality: DupConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := catalog.New(nil)
			firstKey := putCleanPartition(t, c, tc.firstNs)
			secondKey := putCleanPartition(t, c, tc.secondNs)
			// 前置条件：两分区都产出 range，且顺序按 key.String() 升序（即 first → second）。
			require.Less(t, firstKey.String(), secondKey.String())

			client := FuncRangeClient{SearchFn: func(_ context.Context, rng AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
				q := tc.firstQuality
				if rng.TargetID == secondKey.String() {
					q = tc.secondQuality
				}
				return RangeResult{DuplicateQuality: q, Exhausted: true, Bytes: 0}, nil
			}}
			resp := NewService(NewPlanner(c, nil), client, "test").Search(context.Background(), QueryRequest{
				RequestID: "across-ranges",
				Budget:    serialBudget(),
			})
			require.Nil(t, resp.Err)
			require.Equal(t, DupConflict, resp.Quality.DuplicateQuality,
				"conflict 比 unresolved 差，跨 range 累积后必须保留，且与完成顺序无关")
		})
	}
}

// 跨 range 的统计质量同样必须按 worst 累积：unavailable 与 partial 两个方向都得到
// unavailable（修复前非 exact 值互相覆盖，结果随完成顺序漂移）。
func TestSearchAcrossRangesKeepsWorstStatsQuality(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstQuality  StatsQuality
		secondQuality StatsQuality
	}{
		{name: "unavailable 先、partial 后", firstQuality: StatsUnavailable, secondQuality: StatsPartial},
		{name: "partial 先、unavailable 后", firstQuality: StatsPartial, secondQuality: StatsUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := catalog.New(nil)
			firstKey := putCleanPartition(t, c, "a-ns")
			secondKey := putCleanPartition(t, c, "b-ns")

			client := FuncRangeClient{SearchFn: func(_ context.Context, rng AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
				q := tc.firstQuality
				if rng.TargetID == secondKey.String() {
					q = tc.secondQuality
				}
				return RangeResult{StatsQuality: q, Exhausted: true, Bytes: 0}, nil
			}}
			resp := NewService(NewPlanner(c, nil), client, "test").Search(context.Background(), QueryRequest{
				RequestID: "across-ranges-stats",
				Budget:    serialBudget(),
			})
			require.Nil(t, resp.Err)
			_ = firstKey
			require.Equal(t, StatsUnavailable, resp.Quality.StatsQuality,
				"unavailable 比 partial 差，跨 range 累积后必须保留")
		})
	}
}

// range 上报的 unavailable 不得被「coverage 不完整 ⇒ partial」兜底降级。
//
// 修复前 Search 尾部是 `if !plan.Coverage.Complete { resp.Quality.StatsQuality = StatsPartial }`：
// 覆盖不完整与「完全拿不到统计」是两件事，直接赋值把后者降级成前者。
func TestSearchKeepsRangeUnavailableAgainstCoveragePartial(t *testing.T) {
	// incompleteProjection 让 plan 判 partial 且 Coverage.Complete=false（进入兜底分支）。
	c := catalog.New(nil)
	putQualityPartition(t, c, "ns/game-1", incompleteProjection)
	plan := NewPlanner(c, nil).Plan(PlanRequest{RequestID: "precheck"})
	require.False(t, plan.Coverage.Complete, "前置条件：coverage 必须不完整以触发兜底分支")
	require.Equal(t, StatsPartial, plan.Quality.StatsQuality, "前置条件：plan 判 partial")

	client := FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
		return RangeResult{StatsQuality: StatsUnavailable, Exhausted: true, Bytes: 1}, nil
	}}
	resp := NewService(NewPlanner(c, nil), client, "test").Search(context.Background(), QueryRequest{RequestID: "keep-unavailable"})
	require.Nil(t, resp.Err)
	require.Equal(t, StatsUnavailable, resp.Quality.StatsQuality,
		"range 报 unavailable 时不得被 coverage 兜底的 partial 降级")
}

// Stats 路径同上：range 回调累积的 unavailable 不得被尾部兜底降级。
func TestStatsKeepsRangeUnavailableAgainstCoveragePartial(t *testing.T) {
	c := catalog.New(nil)
	putQualityPartition(t, c, "ns/game-1", incompleteProjection)
	plan := NewPlanner(c, nil).Plan(PlanRequest{RequestID: "precheck"})
	require.False(t, plan.Coverage.Complete, "前置条件：触发 Stats 尾部兜底")
	require.Equal(t, StatsPartial, plan.Quality.StatsQuality, "前置条件：plan 判 partial")

	client := FuncRangeClient{StatsFn: func(context.Context, AuthoritativeRange, RangeQuery) (StatsResult, error) {
		return StatsResult{StatsQuality: StatsUnavailable, Bytes: 1}, nil
	}}
	resp := NewService(NewPlanner(c, nil), client, "test").Stats(context.Background(),
		QueryRequest{RequestID: "stats-keep-unavailable"}, []string{"level"})
	require.Nil(t, resp.Err)
	require.Equal(t, StatsUnavailable, resp.Quality.StatsQuality,
		"range 报 unavailable 时不得被 coverage 兜底的 partial 降级")
}

// Stats 跨 range：unavailable 与 partial 两个方向都得到 unavailable（覆盖 range 回调的
// 累积与尾部兜底两处）。
func TestStatsAcrossRangesKeepsWorstStatsQuality(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstQuality  StatsQuality
		secondQuality StatsQuality
	}{
		{name: "unavailable 先、partial 后", firstQuality: StatsUnavailable, secondQuality: StatsPartial},
		{name: "partial 先、unavailable 后", firstQuality: StatsPartial, secondQuality: StatsUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := catalog.New(nil)
			putCleanPartition(t, c, "a-ns")
			secondKey := putCleanPartition(t, c, "b-ns")

			client := FuncRangeClient{StatsFn: func(_ context.Context, rng AuthoritativeRange, _ RangeQuery) (StatsResult, error) {
				q := tc.firstQuality
				if rng.TargetID == secondKey.String() {
					q = tc.secondQuality
				}
				return StatsResult{StatsQuality: q, Bytes: 0}, nil
			}}
			resp := NewService(NewPlanner(c, nil), client, "test").Stats(context.Background(),
				QueryRequest{RequestID: "stats-across-ranges", Budget: serialBudget()}, []string{"level"})
			require.Nil(t, resp.Err)
			require.Equal(t, StatsUnavailable, resp.Quality.StatsQuality,
				"unavailable 比 partial 差，跨 range 累积后必须保留")
		})
	}
}

// worstDuplicateQuality / worstStatsQuality 的代数性质与 rank 顺序（与 CP 侧逐值一致）。
func TestWorstQualityAlgebra(t *testing.T) {
	dups := []DuplicateQuality{DupExact, DupUnspecified, DupUnresolved, DupConflict}
	for _, a := range dups {
		require.Equal(t, a, worstDuplicateQuality(a, a), "幂等：worst(%s,%s)", a, a)
		for _, b := range dups {
			require.Equal(t, worstDuplicateQuality(a, b), worstDuplicateQuality(b, a),
				"交换律：worst(%s,%s)", a, b)
			for _, d := range dups {
				require.Equal(t, worstDuplicateQuality(worstDuplicateQuality(a, b), d),
					worstDuplicateQuality(a, worstDuplicateQuality(b, d)),
					"结合律：worst(%s,%s,%s)", a, b, d)
			}
		}
	}
	require.Equal(t, DupConflict, worstDuplicateQuality(DupConflict, DupUnresolved), "conflict 比 unresolved 差")
	require.Equal(t, DupUnresolved, worstDuplicateQuality(DupUnresolved, DupUnspecified), "unresolved 比 unspecified 差")
	require.Equal(t, DupUnspecified, worstDuplicateQuality(DupUnspecified, DupExact),
		"unspecified 必须比 exact 差，否则未上报来源会冒充精确")
	require.Equal(t, DupUnspecified, worstDuplicateQuality("", DupExact),
		"零值空串归一为 unspecified，且它比 exact 差——故调用方必须先种入非零基线")

	stats := []StatsQuality{StatsExact, StatsUnspecified, StatsPartial, StatsUnavailable}
	for _, a := range stats {
		require.Equal(t, a, worstStatsQuality(a, a), "幂等：worst(%s,%s)", a, a)
		for _, b := range stats {
			require.Equal(t, worstStatsQuality(a, b), worstStatsQuality(b, a),
				"交换律：worst(%s,%s)", a, b)
		}
	}
	require.Equal(t, StatsUnavailable, worstStatsQuality(StatsUnavailable, StatsPartial), "unavailable 比 partial 差")
	require.Equal(t, StatsPartial, worstStatsQuality(StatsPartial, StatsUnspecified), "partial 比 unspecified 差")
	require.Equal(t, StatsUnspecified, worstStatsQuality(StatsUnspecified, StatsExact),
		"unspecified 必须比 exact 差")
}
