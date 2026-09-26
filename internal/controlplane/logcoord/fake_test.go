package logcoord

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// fakeTargetResolver 静态授权目标注册表。
type fakeTargetResolver struct {
	targets []TargetInfo
	err     error
}

func (f *fakeTargetResolver) Resolve(_ context.Context, _ []string) ([]TargetInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]TargetInfo(nil), f.targets...), nil
}

// fakeWorker 单 Worker 的可编程日志查询面。
type fakeWorker struct {
	workerID string

	mu sync.Mutex

	searchItems       []Event
	searchTarget      []WorkerTargetResult
	searchTrunc       bool
	searchUnsup       bool
	searchErr         string
	searchDelay       time.Duration
	searchCalls       int
	lastSearchView    string
	lastOpenViewView  string
	lastSearchTargets []string
	lastOpenTargets   []string

	statsPoints  []WorkerStatsPoint
	statsTargets []WorkerTargetResult
	statsTrunc   bool
	statsCalls   int

	facets        []WorkerFacetDimension
	facetTargets  []WorkerTargetResult
	facetCalls    int
	openViewCalls int

	// openViewFail 模拟本地 Query View 建立失败（未启用日志查询服务、混版
	// Unimplemented、绑定缺失等）。置为非空时 OpenView 返回其文本作为错误，
	// 使 bindWorkerViews 写入 WorkerViewErrors，从而构造 worker_view_failed 场景。
	openViewFail string
}

func (w *fakeWorker) OpenView(_ context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.openViewCalls++
	w.lastOpenViewView = req.ViewID
	w.lastOpenTargets = append([]string(nil), req.TargetIDs...)
	if w.openViewFail != "" {
		return &WorkerSearchResponse{Error: w.openViewFail}, nil
	}
	return &WorkerSearchResponse{ViewID: "worker-view-" + w.workerID, Targets: append([]WorkerTargetResult(nil), w.searchTarget...)}, nil
}

func (w *fakeWorker) Search(_ context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	if w.searchDelay > 0 {
		time.Sleep(w.searchDelay)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.searchCalls++
	w.lastSearchView = req.ViewID
	w.lastSearchTargets = append([]string(nil), req.TargetIDs...)
	resp := &WorkerSearchResponse{
		ViewID:      req.ViewID,
		Items:       append([]Event(nil), w.searchItems...),
		Targets:     append([]WorkerTargetResult(nil), w.searchTarget...),
		Exhausted:   !w.searchTrunc,
		Truncated:   w.searchTrunc,
		Unsupported: w.searchUnsup,
		Error:       w.searchErr,
	}
	return resp, nil
}

func (w *fakeWorker) Tail(ctx context.Context, req WorkerSearchRequest, _ string) (*WorkerSearchResponse, error) {
	return w.Search(ctx, req)
}

func (w *fakeWorker) Fields(_ context.Context, req WorkerSearchRequest) (*WorkerFieldsResponse, error) {
	return &WorkerFieldsResponse{ViewID: req.ViewID, Fields: []string{"level", "stream"},
		Targets: append([]WorkerTargetResult(nil), w.searchTarget...)}, nil
}

func (w *fakeWorker) Stats(_ context.Context, req WorkerSearchRequest, _ []string, _ string) (*WorkerStatsResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statsCalls++
	return &WorkerStatsResponse{
		ViewID:    req.ViewID,
		Points:    append([]WorkerStatsPoint(nil), w.statsPoints...),
		Targets:   append([]WorkerTargetResult(nil), w.statsTargets...),
		Truncated: w.statsTrunc,
	}, nil
}

func (w *fakeWorker) Facets(_ context.Context, req WorkerSearchRequest, _ []string, _ uint32) (*WorkerFacetsResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.facetCalls++
	return &WorkerFacetsResponse{
		ViewID:     req.ViewID,
		Dimensions: append([]WorkerFacetDimension(nil), w.facets...),
		Targets:    append([]WorkerTargetResult(nil), w.facetTargets...),
	}, nil
}

// fakeDialer workerID → client。
type fakeDialer struct {
	mu      sync.Mutex
	clients map[string]WorkerClient
	fail    map[string]error
	calls   map[string]int
}

func newFakeDialer() *fakeDialer {
	return &fakeDialer{
		clients: make(map[string]WorkerClient),
		fail:    make(map[string]error),
		calls:   make(map[string]int),
	}
}

func (d *fakeDialer) Dial(_ context.Context, workerID string) (WorkerClient, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls[workerID]++
	if err, ok := d.fail[workerID]; ok && err != nil {
		return nil, err
	}
	c, ok := d.clients[workerID]
	if !ok {
		return nil, fmt.Errorf("unknown worker %s", workerID)
	}
	return c, nil
}

func (d *fakeDialer) callCount(workerID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[workerID]
}

// successTarget 覆盖成功的 Worker 目标结果。
func successTarget(id, workerID, cvs, gen string) WorkerTargetResult {
	return WorkerTargetResult{
		TargetID:          id,
		State:             CoverageSuccess,
		ClosedVisibleSeq:  cvs,
		CatalogGeneration: gen,
	}
}

func mkEvent(id, source, gen string, start uint64, eventTime, level, msg string) Event {
	return Event{
		EventID:          id,
		LogSourceID:      source,
		SourceGeneration: gen,
		RecordStart:      start,
		RecordEnd:        start + uint64(len(msg)),
		EventTimeUTC:     eventTime,
		Level:            level,
		Message:          msg,
	}
}
