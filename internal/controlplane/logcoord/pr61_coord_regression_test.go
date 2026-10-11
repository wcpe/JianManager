package logcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// pr61Worker 通过真实适配映射响应，观测协调器下发额度。
type pr61Worker struct {
	*fakeWorker
	searchPB *workerpb.LogSearchResponse
	statsPB  *workerpb.LogStatsResponse
	muBudget sync.Mutex
	budgets  []uint64
	requests []WorkerSearchRequest
}

func (w *pr61Worker) Search(ctx context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	w.muBudget.Lock()
	w.budgets = append(w.budgets, req.Budget.MaxBytes)
	w.requests = append(w.requests, req)
	w.muBudget.Unlock()
	resp, err := mapSearchResponse(w.searchPB, nil)
	if resp != nil {
		resp.ViewID = req.ViewID
	}
	return resp, err
}
func (w *pr61Worker) Stats(ctx context.Context, req WorkerSearchRequest, dims []string, bucket string) (*WorkerStatsResponse, error) {
	w.muBudget.Lock()
	w.budgets = append(w.budgets, req.Budget.MaxBytes)
	w.requests = append(w.requests, req)
	w.muBudget.Unlock()
	resp, err := mapStatsResponse(w.statsPB, nil)
	if resp != nil {
		resp.ViewID = req.ViewID
	}
	return resp, err
}
func pr61Coverage(partial bool) *workerpb.LogCoverage {
	return &workerpb.LogCoverage{Complete: !partial, PartialReasons: func() []string {
		if partial {
			return []string{"BUDGET_EXCEEDED"}
		}
		return nil
	}(), EnumerationState: workerpb.LogEnumerationState_LOG_ENUMERATION_OPEN, Targets: []*workerpb.LogCoverageTarget{{TargetId: "inst:1", State: workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS}}}
}
func pr61Query() Query {
	return Query{AuthorizedTargetIDs: []string{"inst:1"}, PrincipalKey: "user:1", PermissionScope: "scope:1", FromUTC: "2026-10-08T00:00:00Z", ToUTC: "2026-10-09T00:00:00Z", Budget: QueryBudget{Limit: 10, MaxBytes: 10000}}
}
func pr61Coord(workers map[string]*pr61Worker) *Coordinator {
	d := newFakeDialer()
	r := &fakeTargetResolver{}
	for id, w := range workers {
		d.clients[id] = w
		r.targets = append(r.targets, TargetInfo{ID: "inst:1@" + id, WorkerTargetID: "inst:1", AuthorizationID: "inst:1", WorkerID: id, Readiness: ReadyOnline})
	}
	return New(r, d)
}
func TestPR61SearchTopLevelCutOpen(t *testing.T) {
	w := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w1"}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(true), Quality: &workerpb.LogQuality{DuplicateQuality: workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_EXACT, StatsQuality: workerpb.LogStatsQuality_LOG_QUALITY_STATS_PARTIAL}, Exhausted: true, Items: []*workerpb.LogEvent{{EventId: "one", EventTimeUtc: "2026-10-08T12:00:00Z"}}}}
	for _, fast := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "fast"}[fast], func(t *testing.T) {
			c := pr61Coord(map[string]*pr61Worker{"w1": w})
			q := pr61Query()
			q.Fast = fast
			for i := 0; i < 2; i++ {
				resp, err := c.Search(context.Background(), q)
				require.NoError(t, err)
				require.False(t, resp.Coverage.Complete)
				require.True(t, resp.BudgetCut)
				require.Equal(t, StatsQPartial, resp.Quality.StatsQuality)
				require.False(t, resp.Exhausted)
				require.Empty(t, resp.NextCursor, "预算扫描缺少全局安全边界，不得跳过另一Worker未知组")
			}
		})
	}
}
func TestPR61StatsTopLevelPartial(t *testing.T) {
	w := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w1"}, statsPB: &workerpb.LogStatsResponse{Coverage: pr61Coverage(true)}}
	resp, err := pr61Coord(map[string]*pr61Worker{"w1": w}).Stats(context.Background(), StatsQuery{Query: pr61Query()})
	require.NoError(t, err)
	require.False(t, resp.Coverage.Complete)
	require.Contains(t, resp.Coverage.PartialReasons, "BUDGET_EXCEEDED")
	require.Equal(t, StatsQPartial, resp.Quality.StatsQuality)
}
func TestPR61GapProtoNormalFastCache(t *testing.T) {
	gap := &workerpb.IngestPositionGap{StorageNamespace: "inst:1/stdout", From: 9007199254740993, To: ^uint64(0), ReasonCode: "PERMANENTLY_LOST", Operator: "ops", AtUtc: "2026-10-08T12:00:00Z"}
	for _, fast := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "fast"}[fast], func(t *testing.T) {
			workers := map[string]*pr61Worker{}
			for _, id := range []string{"w1", "w2"} {
				workers[id] = &pr61Worker{fakeWorker: &fakeWorker{workerID: id}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Exhausted: true, IngestPositionGaps: []*workerpb.IngestPositionGap{gap}}}
			}
			c := pr61Coord(workers)
			q := pr61Query()
			q.Fast = fast
			for i := 0; i < 2; i++ {
				resp, err := c.Search(context.Background(), q)
				require.NoError(t, err)
				raw, err := json.Marshal(resp)
				require.NoError(t, err)
				var wire map[string]any
				require.NoError(t, json.Unmarshal(raw, &wire))
				gaps, ok := wire["ingest_position_gaps"].([]any)
				require.True(t, ok, "缺失独立永久位置字段: %s", raw)
				require.Len(t, gaps, 2)
				ids := map[string]bool{}
				for _, v := range gaps {
					g := v.(map[string]any)
					ids[g["worker_id"].(string)] = true
					require.Equal(t, "9007199254740993", g["from"])
					require.Equal(t, "18446744073709551615", g["to"])
					require.Equal(t, "ops", g["operator"])
				}
				require.Len(t, ids, 2)
				require.True(t, resp.Coverage.Complete)
				require.Equal(t, DupExact, resp.Quality.DuplicateQuality)
			}
			for _, w := range workers {
				require.Len(t, w.budgets, 1, "第二次必须命中缓存")
			}
		})
	}
}
func TestPR61BudgetReservation(t *testing.T) {
	for _, amount := range []uint64{1, 19} {
		workers := map[string]*pr61Worker{}
		for _, id := range []string{"w1", "w2", "w3"} {
			workers[id] = &pr61Worker{fakeWorker: &fakeWorker{workerID: id}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Exhausted: true}}
		}
		q := pr61Query()
		q.Fast = true
		q.Budget.MaxBytes = amount
		_, err := pr61Coord(workers).Search(context.Background(), q)
		require.NoError(t, err)
		var sum uint64
		for _, w := range workers {
			for _, n := range w.budgets {
				require.Positive(t, n, "零额度不发RPC")
				sum += n
			}
		}
		require.LessOrEqual(t, sum, amount)
	}
}

// TestPR61CacheAuthorizationIsolation（CP-1 改后重写）固定缓存键去掉身份段后的**新**不变量：
//
//	① 同一授权集合（targets digest）下，不同主体/权限版本**应当**命中同一条目（否则预热器
//	   写死的占位身份永远命中不了，预热零收益却仍全平台扇出）；
//	② 跨授权集合**绝不**命中（不同 targets ⇒ 不同 digest）——这是真正的安全边界；
//	③ 命中不得外泄创建者的 View 身份：返回的 View 必须是**当前调用方**的（见
//	   rebindCachedSearchResponse）。
//
// 历史形态（本用例改写前）断言「不同主体不得互命中」，那是把身份放进键时的过度保守设计；
// 身份入键会使预热条目永不命中，与「限制宽窗查询对整机的抢占」的初衷相反（PR review CP-1）。
func TestPR61CacheAuthorizationIsolation(t *testing.T) {
	w := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w1"}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Exhausted: true}}
	c := pr61Coord(map[string]*pr61Worker{"w1": w})
	q := pr61Query()
	q.Fast = true
	first, err := c.Search(context.Background(), q)
	require.NoError(t, err)

	// ① 同集合换主体：必须命中缓存（只扇出过一次），且 View 身份归属当前调用方。
	q.PrincipalKey = "user:2"
	q.PermissionScope = "scope:2"
	second, err := c.Search(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, w.budgets, 1, "同一授权集合下不同主体必须命中同一缓存条目（预热可共享的前提）")
	require.Equal(t, "user:2", second.View.PrincipalKey, "命中不得外泄创建者的 View 身份")
	require.Equal(t, "scope:2", second.View.PermissionScope, "命中不得外泄创建者的权限范围")

	// ③ 返回的行集合与首次一致（同集合 ⇒ 同结果）。
	require.Equal(t, len(first.Items), len(second.Items))

	// ② 跨授权集合：绝不命中（必须再扇出一次）。
	q.AuthorizedTargetIDs = []string{"inst:other"}
	different, err := c.Search(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, w.budgets, 2, "不同授权集合必须各查一次（digest 不同 ⇒ 不得命中）")
	require.Equal(t, "user:2", different.View.PrincipalKey)
}

func TestPR61DedupeCannotOverwriteConflictQuality(t *testing.T) {
	for _, sameID := range []bool{true, false} {
		t.Run(fmt.Sprint(sameID), func(t *testing.T) {
			workers := map[string]*pr61Worker{}
			for _, id := range []string{"w1", "w2"} {
				eventID := id
				if sameID {
					eventID = "same"
				}
				workers[id] = &pr61Worker{fakeWorker: &fakeWorker{workerID: id}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Exhausted: true, Quality: &workerpb.LogQuality{DuplicateQuality: workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_CONFLICT}, Items: []*workerpb.LogEvent{{EventId: eventID, CanonicalContentHash: id, EventTimeUtc: "2026-10-08T12:00:00Z"}}}}
			}
			q := pr61Query()
			q.Fast = true
			resp, err := pr61Coord(workers).Search(context.Background(), q)
			require.NoError(t, err)
			require.Equal(t, DupConflict, resp.Quality.DuplicateQuality)
			require.Len(t, resp.Items, 2, "同事件不同内容必须保留，不得重写成未决质量")
		})
	}
}

func TestPR61ConflictQualityCannotBeOverwrittenByPartialTarget(t *testing.T) {
	w1 := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w1"}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Quality: &workerpb.LogQuality{DuplicateQuality: workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_CONFLICT}, Exhausted: true}}
	w2 := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w2"}, searchPB: &workerpb.LogSearchResponse{Coverage: &workerpb.LogCoverage{Complete: false, Targets: []*workerpb.LogCoverageTarget{{TargetId: "inst:1", State: workerpb.LogCoverageState_LOG_COVERAGE_PARTIAL}}}, Exhausted: true}}
	q := pr61Query()
	q.Fast = true
	resp, err := pr61Coord(map[string]*pr61Worker{"w1": w1, "w2": w2}).Search(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, DupConflict, resp.Quality.DuplicateQuality)
	require.False(t, resp.Coverage.Complete)
}

func TestPR61GapSourceGenerationNotMerged(t *testing.T) {
	gaps := []*workerpb.IngestPositionGap{
		{StorageNamespace: "inst:1/shared", From: 1, To: 3, ReasonCode: "PERMANENTLY_LOST", LogSourceId: "inst:1/file-a", SourceGeneration: "g1"},
		{StorageNamespace: "inst:1/shared", From: 1, To: 3, ReasonCode: "PERMANENTLY_LOST", LogSourceId: "inst:1/file-a", SourceGeneration: "g2"},
		{StorageNamespace: "inst:1/shared", From: 1, To: 3, ReasonCode: "PERMANENTLY_LOST", LogSourceId: "inst:1/file-b", SourceGeneration: "g1"},
		{StorageNamespace: "inst:1/shared", From: 1, To: 3, ReasonCode: "PERMANENTLY_LOST"},
	}
	w := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w1"}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Exhausted: true, IngestPositionGaps: gaps}}
	c := pr61Coord(map[string]*pr61Worker{"w1": w})
	q := pr61Query()
	q.Fast = true
	for i := 0; i < 2; i++ {
		resp, err := c.Search(context.Background(), q)
		require.NoError(t, err)
		require.Len(t, resp.IngestPositionGaps, 4)
		keys := map[string]bool{}
		for _, g := range resp.IngestPositionGaps {
			keys[g.WorkerID+"/"+g.LogSourceID+"/"+g.SourceGeneration] = true
		}
		require.Len(t, keys, 4)
		require.True(t, resp.Coverage.Complete)
		require.False(t, resp.BudgetCut)
		require.Equal(t, DupExact, resp.Quality.DuplicateQuality)
	}
	legacy, err := mapSearchResponse(&workerpb.LogSearchResponse{}, nil)
	require.NoError(t, err)
	require.Nil(t, legacy.IngestPositionGaps)
	raw, err := json.Marshal(positionGapsFromProto(gaps)[3])
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.NotContains(t, wire, "log_source_id")
	require.NotContains(t, wire, "source_generation")
}

func TestPR61ViewReuseHonorsCurrentCostBudget(t *testing.T) {
	for _, stats := range []bool{false, true} {
		for _, fast := range []bool{false, true} {
			t.Run(fmt.Sprintf("stats=%t/fast=%t", stats, fast), func(t *testing.T) {
				workers := map[string]*pr61Worker{}
				for _, id := range []string{"w1", "w2"} {
					workers[id] = &pr61Worker{fakeWorker: &fakeWorker{workerID: id}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Exhausted: true}, statsPB: &workerpb.LogStatsResponse{Coverage: pr61Coverage(false)}}
				}
				c := pr61Coord(workers)
				q := pr61Query()
				q.Fast = fast
				view, err := c.CreateView(context.Background(), q)
				require.NoError(t, err)
				q.ViewID = view.ViewID
				q.BypassCache = true
				q.Budget.MaxBytes = 1
				q.Budget.MaxFanout = 1
				q.Budget.TimeoutMS = 7
				var cov Coverage
				if stats {
					resp, err := c.Stats(context.Background(), StatsQuery{Query: q})
					require.NoError(t, err)
					cov = resp.Coverage
				} else {
					resp, err := c.Search(context.Background(), q)
					require.NoError(t, err)
					cov = resp.Coverage
				}
				var sum uint64
				var calls int
				for _, w := range workers {
					for _, req := range w.requests {
						calls++
						sum += req.Budget.MaxBytes
						require.Equal(t, uint32(1), req.Budget.MaxFanout)
						require.Equal(t, uint32(7), req.Budget.TimeoutMS)
					}
				}
				require.Equal(t, 1, calls, "复用View也必须遵守本次fanout")
				require.Equal(t, uint64(1), sum, "View不能把旧10000预算覆盖本次1字节")
				require.False(t, cov.Complete)
			})
		}
	}
}

func TestPR61StatsBudgetAndFanoutReservation(t *testing.T) {
	for _, fanout := range []uint32{0, 1, 2} {
		t.Run(fmt.Sprint(fanout), func(t *testing.T) {
			workers := map[string]*pr61Worker{}
			for _, id := range []string{"w1", "w2", "w3"} {
				workers[id] = &pr61Worker{fakeWorker: &fakeWorker{workerID: id}, statsPB: &workerpb.LogStatsResponse{Coverage: pr61Coverage(false)}}
			}
			q := pr61Query()
			q.Budget.MaxBytes = 2
			q.Budget.MaxFanout = fanout
			resp, err := pr61Coord(workers).Stats(context.Background(), StatsQuery{Query: q})
			require.NoError(t, err)
			require.False(t, resp.Coverage.Complete)
			require.True(t, resp.BudgetCut)
			require.Equal(t, StatsQPartial, resp.Quality.StatsQuality)
			var sum uint64
			var calls int
			for _, w := range workers {
				for _, n := range w.budgets {
					require.Positive(t, n)
					sum += n
					calls++
				}
				require.LessOrEqual(t, w.openViewCalls, 1)
			}
			require.LessOrEqual(t, sum, uint64(2))
			if fanout == 1 {
				require.Equal(t, 1, calls)
			} else {
				require.Equal(t, 2, calls)
			}
		})
	}
}
func TestPR61ReserveDefaultAndUint64(t *testing.T) {
	order := []string{"w1", "w2", "w3"}
	for _, n := range []uint64{0, 1, 19, ^uint64(0)} {
		budget := QueryBudget{MaxBytes: n}
		assigned := reserveWorkerBudgets(order, budget)
		var sum uint64
		for _, v := range assigned {
			require.Positive(t, v.MaxBytes)
			sum += v.MaxBytes
		}
		if n == 0 {
			require.Equal(t, uint64(32<<20), sum)
		} else {
			require.Equal(t, n, sum)
		}
	}
}

// pr61BlockedWorker 用取消屏障验证未开始的路由不再发起请求。
type pr61BlockedWorker struct {
	*fakeWorker
	started, stopped *atomic.Int32
}

func (w *pr61BlockedWorker) Search(ctx context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	w.started.Add(1)
	defer w.stopped.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestPR61BudgetTimeoutCancelsInflight(t *testing.T) {
	var started, stopped atomic.Int32
	d := newFakeDialer()
	r := &fakeTargetResolver{}
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("worker-%02d", i)
		r.targets = append(r.targets, TargetInfo{ID: "inst:1@" + id, WorkerTargetID: "inst:1", WorkerID: id, Readiness: ReadyOnline})
		d.clients[id] = &pr61BlockedWorker{fakeWorker: &fakeWorker{workerID: id}, started: &started, stopped: &stopped}
	}
	q := pr61Query()
	q.Fast = true
	q.Budget.TimeoutMS = 50
	_, err := New(r, d).Search(context.Background(), q)
	require.Error(t, err)
	require.Positive(t, started.Load())
	require.LessOrEqual(t, started.Load(), int32(8), "并发请求必须有界；超时后的排队任务不能开始")
	require.Eventually(t, func() bool { return stopped.Load() == started.Load() }, time.Second, time.Millisecond, "所有在途读取必须收到取消")
}
func TestPR61MultiWorkerCutCannotCrossUnknownGroup(t *testing.T) {
	for _, safeRows := range []bool{true, false} {
		for _, cut := range []bool{true, false} {
			t.Run(fmt.Sprintf("safe=%t/cut=%t", safeRows, cut), func(t *testing.T) {
				w1 := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w1"}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(cut), Exhausted: !cut}}
				if safeRows {
					w1.searchPB.Items = []*workerpb.LogEvent{{EventId: "safe-13", EventTimeUtc: "2026-10-08T13:00:00Z"}}
				}
				w2 := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w2"}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Exhausted: true, Items: []*workerpb.LogEvent{{EventId: "older-11", EventTimeUtc: "2026-10-08T11:00:00Z"}}}}
				q := pr61Query()
				q.Fast = true
				resp, err := pr61Coord(map[string]*pr61Worker{"w1": w1, "w2": w2}).Search(context.Background(), q)
				require.NoError(t, err)
				if cut {
					require.Empty(t, resp.Items, "另一Worker的11点行不能越过缺少可信wire边界的12点组")
					require.Empty(t, resp.NextCursor)
					require.False(t, resp.Exhausted)
					require.True(t, resp.BudgetCut)
				} else {
					require.NotEmpty(t, resp.Items)
					require.False(t, resp.BudgetCut)
				}
			})
		}
	}
}

// pr61WindowWorker 在复合游标过滤前按真实闭开窗口读取。
type pr61WindowWorker struct {
	cursorWorker
	calls int
}

func (w *pr61WindowWorker) Search(ctx context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	w.calls++
	copyWorker := w.cursorWorker
	copyWorker.items = nil
	from, to := MustParseRFC3339(req.FromUTC), MustParseRFC3339(req.ToUTC)
	for _, e := range w.items {
		ts := e.SortKey().EventTimeUTC
		if !ts.Before(from) && ts.Before(to) {
			copyWorker.items = append(copyWorker.items, e)
		}
	}
	return copyWorker.Search(ctx, req)
}
func TestPR61MinuteCacheExpandedWindowDoesNotSkipNewRows(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(fmt.Sprint(fast), func(t *testing.T) {
			w := &pr61WindowWorker{cursorWorker: cursorWorker{id: "w1", target: "inst:1", items: []Event{
				mkEvent("new-15", "s", "g", 15, "2026-10-08T12:00:15Z", "INFO", "new"),
				mkEvent("old-09", "s", "g", 9, "2026-10-08T12:00:09Z", "INFO", "old"),
				mkEvent("old-08", "s", "g", 8, "2026-10-08T12:00:08Z", "INFO", "old"),
				mkEvent("old-07", "s", "g", 7, "2026-10-08T12:00:07Z", "INFO", "old"),
			}}}
			d := newFakeDialer()
			d.clients["w1"] = w
			c := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}, d)
			q := pr61Query()
			q.Fast = fast
			q.Budget.Limit = 2
			q.FromUTC = "2026-10-08T11:00:00Z"
			q.ToUTC = "2026-10-08T12:00:10Z"
			a, err := c.Search(context.Background(), q)
			require.NoError(t, err)
			require.Equal(t, "old-09", a.Items[0].EventID)
			_, err = c.Search(context.Background(), q)
			require.NoError(t, err)
			require.Equal(t, 1, w.calls, "精确相同窗口应保持热缓存命中")
			q.ToUTC = "2026-10-08T12:00:20Z"
			b, err := c.Search(context.Background(), q)
			require.NoError(t, err)
			require.Equal(t, "new-15", b.Items[0].EventID, "同分钟扩大窗口不能用旧窗口08游标越过新增15")
			q.ViewID = b.View.ViewID
			q.Cursor = b.NextCursor
			second, err := c.Search(context.Background(), q)
			require.NoError(t, err)
			ids := map[string]bool{}
			for _, page := range []*SearchResponse{b, second} {
				for _, e := range page.Items {
					ids[e.EventID] = true
				}
			}
			require.Len(t, ids, 4)
			require.Contains(t, ids, "new-15")
		})
	}
}

func TestPR61NormalPaginationKeepsCursor(t *testing.T) {
	w := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w1"}, searchPB: &workerpb.LogSearchResponse{Coverage: pr61Coverage(false), Exhausted: false, NextCursor: "normal-next", Items: []*workerpb.LogEvent{{EventId: "one", EventTimeUtc: "2026-10-08T12:00:00Z"}}}}
	q := pr61Query()
	q.Fast = true
	resp, err := pr61Coord(map[string]*pr61Worker{"w1": w}).Search(context.Background(), q)
	require.NoError(t, err)
	require.False(t, resp.BudgetCut)
	require.NotEmpty(t, resp.NextCursor)
	require.False(t, resp.Exhausted)
}
