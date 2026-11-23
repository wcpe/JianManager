package logcoord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 未知 Worker 的排序范围不能被另一条成功流的最后一行越过。
func TestSixModuleUnknownWorkerBoundaryCannotAdvanceCursor(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(map[bool]string{false: "普通", true: "首屏"}[fast], func(t *testing.T) {
			resolver := &fakeTargetResolver{targets: []TargetInfo{
				{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline},
				{ID: "inst:2", WorkerID: "w2", Readiness: ReadyOnline},
			}}
			dialer := newFakeDialer()
			dialer.clients["w1"] = &fakeWorker{workerID: "w1", searchTarget: []WorkerTargetResult{successTarget("inst:1", "w1", "20", "1")}, searchItems: []Event{
				mkEvent("old", "s", "g", 10, "2026-09-22T10:00:00Z", "INFO", "旧行"),
				mkEvent("older", "s", "g", 0, "2026-09-22T09:00:00Z", "INFO", "更旧行"),
			}}
			dialer.clients["w2"] = &fakeWorker{workerID: "w2", searchErr: "RANGE_UNAVAILABLE"}
			q := review05Query()
			q.AuthorizedTargetIDs = []string{"inst:1", "inst:2"}
			q.Fast = fast
			resp, err := New(resolver, dialer).Search(context.Background(), q)
			require.NoError(t, err)
			require.False(t, resp.Coverage.Complete)
			require.False(t, resp.Exhausted)
			require.Empty(t, resp.NextCursor, "未知范围恢复后只能重试同页，不得跳过其行")
			require.Contains(t, resp.Coverage.PartialReasons, "PAGE_RETRY_REQUIRED")
		})
	}
}

// Worker 本身的范围失败也不能被 CP 再次合成为全局游标。
func TestSixModuleWorkerTopLevelPartialCannotAdvanceCursor(t *testing.T) {
	coverage := pr61Coverage(false)
	coverage.Complete = false
	coverage.PartialReasons = []string{"RANGE_UNAVAILABLE"}
	w := &pr61Worker{fakeWorker: &fakeWorker{workerID: "w1"}, searchPB: &workerpb.LogSearchResponse{
		Coverage: coverage, Quality: pr61ExactQuality(), Exhausted: false,
		Items: []*workerpb.LogEvent{{EventId: "old", EventTimeUtc: "2026-10-08T10:00:00Z"}},
	}}
	q := pr61Query()
	q.Fast = true
	out, err := pr61Coord(map[string]*pr61Worker{"w1": w}).Search(context.Background(), q)
	require.NoError(t, err)
	require.NotEmpty(t, out.Items)
	require.Empty(t, out.NextCursor)
	require.False(t, out.Exhausted)
	require.Contains(t, out.Coverage.PartialReasons, "PAGE_RETRY_REQUIRED")
}

// 兼容留存的空绑定视图只能返回未就绪，不能借复用补采最新集合。
func TestSixModuleUnboundStoredViewReturnsNotReadyWithoutRecapture(t *testing.T) {
	worker := &fakeWorker{workerID: "w1", searchTarget: []WorkerTargetResult{successTarget("inst:1", "w1", "20", "1")}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = worker
	coord := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}, dialer)
	query := review05Query()
	view, _, err := coord.resolveForQueryFast(context.Background(), query)
	require.NoError(t, err)
	coord.mu.Lock()
	coord.views[view.ViewID] = cloneView(view)
	coord.mu.Unlock()
	query.ViewID, query.Fast = view.ViewID, true
	out, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	require.False(t, out.Coverage.Complete)
	require.Equal(t, CoverageNotReady, out.Coverage.Targets[0].State)
	require.Empty(t, out.NextCursor)
	require.Zero(t, worker.openViewCalls)
	require.Zero(t, worker.searchCalls)
	require.Zero(t, dialer.callCount("w1"))
}

// 同一真实 Worker 引用的目标向量首次固定后只能比较，不能被新返回值覆盖。
func TestSixModuleClosedVisibleVectorCannotBeOverwritten(t *testing.T) {
	w := &fakeWorker{workerID: "w1", searchTarget: []WorkerTargetResult{successTarget("inst:1", "w1", "20", "1")}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w
	coord := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}, dialer)
	q := review05Query()
	first, err := coord.Search(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, "20", first.View.ClosedVisibleSeq["inst:1"])
	w.searchTarget = []WorkerTargetResult{successTarget("inst:1", "w1", "30", "1")}
	q.ViewID, q.BypassCache = first.View.ViewID, true
	_, err = coord.Search(context.Background(), q)
	require.ErrorIs(t, err, ErrViewReuseMismatch)
	stored, ok := coord.GetView(q.ViewID)
	require.True(t, ok)
	require.Equal(t, "20", stored.ClosedVisibleSeq["inst:1"])
}
