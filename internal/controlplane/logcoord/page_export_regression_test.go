package logcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pageExportWorker 只把同一固定视图的分页与统计协议组合起来，不引入网络或客户端行回传。
type pageExportWorker struct {
	cursorWorker
	statsQuality Quality
	statsPartial bool
	reads        []uint64
}

func (w *pageExportWorker) Search(ctx context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	w.reads = append(w.reads, req.Budget.MaxBytes)
	return w.cursorWorker.Search(ctx, req)
}

func (w *pageExportWorker) Stats(_ context.Context, req WorkerSearchRequest, _ []string, _ string) (*WorkerStatsResponse, error) {
	w.reads = append(w.reads, req.Budget.MaxBytes)
	coverage := &Coverage{Complete: !w.statsPartial, EnumerationState: EnumExhausted}
	if w.statsPartial {
		coverage.PartialReasons = []string{"RANGE_UNAVAILABLE"}
	}
	return &WorkerStatsResponse{ViewID: req.ViewID, Targets: []WorkerTargetResult{{TargetID: w.target, State: CoverageSuccess, CatalogGeneration: "1"}}, Coverage: coverage, Quality: w.statsQuality}, nil
}

func pageExportFixture(t *testing.T) (*Coordinator, *pageExportWorker, Query, *SearchResponse) {
	t.Helper()
	worker := &pageExportWorker{cursorWorker: cursorWorker{id: "w1", target: "inst:1", items: []Event{
		mkEvent("first", "s", "g", 20, "2026-09-22T12:00:00Z", "INFO", "首行"),
		mkEvent("second", "s", "g", 10, "2026-09-22T11:00:00Z", "INFO", "次行"),
		mkEvent("third", "s", "g", 0, "2026-09-22T10:00:00Z", "INFO", "末行"),
	}}, statsQuality: exactQuality}
	dialer := newFakeDialer()
	dialer.clients["w1"] = worker
	coord := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}, dialer)
	query := review05Query()
	query.Fast = true
	query.Budget.Limit = 1
	first, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	query.ViewID = first.View.ViewID
	return coord, worker, query, first
}

func TestSixModulePageExportNeedsExistingMatchingView(t *testing.T) {
	coord, _, query, _ := pageExportFixture(t)
	withoutView := query
	withoutView.ViewID = ""
	_, err := coord.ExportPage(context.Background(), withoutView)
	require.ErrorIs(t, err, ErrViewReuseMismatch)
	query.PermissionScope = "other"
	_, err = coord.ExportPage(context.Background(), query)
	require.ErrorIs(t, err, ErrViewReuseMismatch)
}

func TestSixModulePageExportReadsActualPageAndBypassesCachedQuality(t *testing.T) {
	coord, worker, query, first := pageExportFixture(t)
	query.Cursor = first.NextCursor
	exported, err := coord.ExportPage(context.Background(), query)
	require.NoError(t, err)
	require.False(t, exported.ExportIncomplete)
	require.False(t, exported.Coverage.Pending)
	var row Event
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(exported.Artifact))), &row))
	require.Equal(t, "second", row.EventID)
	require.Len(t, exported.Items, 1)

	worker.statsPartial = true
	blocked, err := coord.ExportPage(context.Background(), query)
	require.NoError(t, err)
	require.True(t, blocked.ExportIncomplete)
	require.Nil(t, blocked.Artifact, "先前完整页的缓存不能绕过最新完整性闸")
}

func TestSixModulePageExportSharesRequestReadBudget(t *testing.T) {
	coord, worker, query, _ := pageExportFixture(t)
	worker.reads = nil
	query.Budget.MaxBytes = 4096
	_, err := coord.ExportPage(context.Background(), query)
	require.NoError(t, err)
	var reserved uint64
	for _, amount := range worker.reads {
		require.Positive(t, amount)
		reserved += amount
	}
	require.Len(t, worker.reads, 2)
	require.LessOrEqual(t, reserved, query.Budget.MaxBytes, "页面读取与新鲜统计合计不能复制整请求预算")
}

func TestSixModulePendingCannotBecomeSuccessfulArtifact(t *testing.T) {
	result := BuildExport(View{}, Coverage{Complete: true, Pending: true}, exactQuality, nil, false, false)
	require.True(t, result.ExportIncomplete)
	require.Nil(t, result.Artifact)
}
