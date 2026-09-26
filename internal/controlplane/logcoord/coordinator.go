package logcoord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
)

var (
	errNoResolver = errors.New("logcoord: TargetResolver is nil")
	errNoDialer   = errors.New("logcoord: WorkerDialer is nil")
	errNoView     = errors.New("logcoord: view not found")
	// ErrViewAuthz 复用 view 时授权主体不一致或当前授权集合不再覆盖 view 目标。
	ErrViewAuthz = errors.New("logcoord: view authorization mismatch")
	// ErrViewReuseMismatch 复用 view 时固定查询条件不一致（时间/过滤/onlineOnly/scope）。
	ErrViewReuseMismatch = errors.New("logcoord: view reuse query mismatch")
)

// Coordinator CP 跨 Worker 日志查询协调器（纯 service）。
type Coordinator struct {
	resolver TargetResolver
	dialer   WorkerDialer

	mu    sync.Mutex
	views map[string]View
}

// maxRetainedViews CP 视图注册表上限。与 Worker 侧同理：每个「不带 view_id 的首次查询」都新建视图，
// 持续负载下无界增长会泄漏内存（真机 30 分钟压测暴露）。插入前按创建时间淘汰最旧视图，被淘汰视图的
// 后续 Cursor 请求显式返回 view-not-found/VIEW_STALE。
const maxRetainedViews = 4096

// pruneViewsLocked 在插入前按创建时间淘汰超量视图。调用方须持有 c.mu。
func (c *Coordinator) pruneViewsLocked() {
	for len(c.views) >= maxRetainedViews {
		oldestID := ""
		var oldest time.Time
		for id, v := range c.views {
			if oldestID == "" || v.CreatedAt.Before(oldest) {
				oldestID, oldest = id, v.CreatedAt
			}
		}
		if oldestID == "" {
			break
		}
		delete(c.views, oldestID)
	}
}

// New 创建协调器。
func New(resolver TargetResolver, dialer WorkerDialer) *Coordinator {
	return &Coordinator{
		resolver: resolver,
		dialer:   dialer,
		views:    make(map[string]View),
	}
}

// CreateView 解析授权目标并创建跨 Worker Query View。
//
// online_only 仅在显式 true 时收缩扇出集合；被排除目标仍写入 view 元数据与后续 coverage。
func (c *Coordinator) CreateView(ctx context.Context, q Query) (*View, error) {
	if err := ValidateQuery(q); err != nil {
		return nil, err
	}
	resolved, err := ResolveTargets(ctx, c.resolver, q.AuthorizedTargetIDs, q.OnlineOnly)
	if err != nil {
		return nil, err
	}
	view := View{
		ViewID:                   NewViewID(),
		OrderVersion:             OrderVersion,
		FromUTC:                  q.FromUTC,
		ToUTC:                    q.ToUTC,
		TargetIDs:                resolved.TargetIDs(),
		AuthorizedIDs:            append([]string(nil), q.AuthorizedTargetIDs...),
		PrincipalKey:             q.PrincipalKey,
		OnlineOnly:               q.OnlineOnly,
		Budget:                   q.Budget,
		Filter:                   q.Filter,
		PermissionScope:          q.PermissionScope,
		WorkerViewIDs:            make(map[string]string),
		WorkerViewErrors:         make(map[string]string),
		WorkerCatalogGenerations: make(map[string]string),
		TargetAuthorizationIDs:   make(map[string]string),
		CreatedAt:                time.Now().UTC(),
	}
	for _, target := range resolved.All {
		if target.AuthorizationID != "" {
			view.TargetAuthorizationIDs[target.ID] = target.AuthorizationID
		}
	}
	c.bindWorkerViews(ctx, &view, resolved)
	c.mu.Lock()
	c.pruneViewsLocked()
	c.views[view.ViewID] = view
	c.mu.Unlock()
	return &view, nil
}

// bindWorkerViews creates one immutable local view per Worker. The CP view ID
// is never sent to Worker Planner as if it were a Worker-local view ID.
func (c *Coordinator) bindWorkerViews(ctx context.Context, view *View, resolved *ResolvedTargets) {
	if c == nil || c.dialer == nil || view == nil || resolved == nil {
		return
	}
	order, byWorker := resolved.ByWorker()
	for _, workerID := range order {
		targets := byWorker[workerID]
		targetIDs := make([]string, 0, len(targets))
		for _, t := range targets {
			targetIDs = append(targetIDs, workerTargetID(t))
		}
		client, err := c.dialer.Dial(ctx, workerID)
		if err != nil {
			view.WorkerViewErrors[workerID] = err.Error()
			continue
		}
		resp, err := client.OpenView(ctx, WorkerSearchRequest{
			TargetIDs:    targetIDs,
			FromUTC:      view.FromUTC,
			ToUTC:        view.ToUTC,
			Filter:       view.Filter,
			Budget:       view.Budget,
			OrderVersion: view.OrderVersion,
		})
		if err != nil {
			view.WorkerViewErrors[workerID] = err.Error()
			continue
		}
		if resp == nil || resp.Unsupported || resp.Error != "" || resp.ViewID == "" {
			if resp != nil && resp.Error != "" {
				view.WorkerViewErrors[workerID] = resp.Error
			} else {
				view.WorkerViewErrors[workerID] = "worker view creation failed"
			}
			continue
		}
		view.WorkerViewIDs[workerID] = resp.ViewID
	}
}

func workerViewID(view View, workerID string) (string, string, bool) {
	if id := view.WorkerViewIDs[workerID]; id != "" {
		return id, "", true
	}
	if reason := view.WorkerViewErrors[workerID]; reason != "" {
		return "", reason, false
	}
	return "", "worker view is not bound", false
}

func workerErrorCoverage(target TargetInfo, workerID string, results []WorkerTargetResult, message string) TargetCoverage {
	for _, result := range results {
		if result.TargetID != workerTargetID(target) {
			continue
		}
		state := result.State
		if state == "" || state == CoverageSuccess {
			state = CoveragePartial
		}
		return TargetCoverage{
			TargetID: target.ID, WorkerID: workerID, State: state,
			Reasons:          append(append([]string(nil), result.Reasons...), "worker_error", message),
			ClosedVisibleSeq: result.ClosedVisibleSeq, CatalogGeneration: result.CatalogGeneration,
			HistoricalHolder: target.HistoricalHolder,
		}
	}
	coverage := CoverageFromReadiness(target, []string{"worker_error", message})
	if coverage.State == CoverageSuccess {
		coverage.State = CoveragePartial
	}
	return coverage
}

func workerTargetID(target TargetInfo) string {
	if target.WorkerTargetID != "" {
		return target.WorkerTargetID
	}
	return target.ID
}

func ensureWorkerViewBinding(view View, workerID string, targets []TargetInfo, respViewID string, results []WorkerTargetResult) error {
	wantView, _, ok := workerViewID(view, workerID)
	if !ok || respViewID != wantView {
		return fmt.Errorf("%w: worker %s returned view %q want %q", ErrViewReuseMismatch, workerID, respViewID, wantView)
	}
	got := make(map[string]WorkerTargetResult, len(results))
	for _, result := range results {
		got[result.TargetID] = result
	}
	for _, target := range targets {
		wantGeneration := view.WorkerCatalogGenerations[target.ID]
		if wantGeneration == "" {
			continue
		}
		result, ok := got[workerTargetID(target)]
		if !ok || result.CatalogGeneration != wantGeneration {
			return fmt.Errorf("%w: worker %s target %s generation changed", ErrViewReuseMismatch, workerID, target.ID)
		}
	}
	return nil
}

// GetView 读取已创建视图。
func (c *Coordinator) GetView(viewID string) (View, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.views[viewID]
	return v, ok
}

// resolveForQuery 复用 view 或新建；返回 view、本次解析结果。
//
// 安全（F-001）：复用 viewId 时必须——
//  1. PrincipalKey 与创建主体一致（调用方由 CP 鉴权层注入）；
//  2. 当前 AuthorizedTargetIDs 仍覆盖 view.TargetIDs 与 AuthorizedIDs 的交集目标。
//
// 任一不满足返回 ErrViewAuthz / ErrViewReuseMismatch，禁止用创建者集合越权查询。
func (c *Coordinator) resolveForQuery(ctx context.Context, q Query) (View, *ResolvedTargets, error) {
	if q.ViewID != "" {
		v, ok := c.GetView(q.ViewID)
		if !ok {
			return View{}, nil, fmt.Errorf("%w: %s", errNoView, q.ViewID)
		}
		if err := authorizeViewReuse(v, q); err != nil {
			return View{}, nil, err
		}
		// 复用同一逻辑目标集合；不因当前 readiness 变化而重新选择集合。
		// 仍需 TargetInfo 以便 fanout/coverage：按 view.TargetIDs 再解析身份映射，
		// 但查询目标集合锁定为 view.TargetIDs。
		infos, err := c.resolver.Resolve(ctx, v.AuthorizedIDs)
		if err != nil {
			return View{}, nil, err
		}
		byID := make(map[string]TargetInfo, len(infos))
		for _, t := range infos {
			byID[t.ID] = t
		}
		rt := &ResolvedTargets{All: infos, OnlineOnly: v.OnlineOnly}
		for _, id := range v.TargetIDs {
			if t, ok := byID[id]; ok {
				rt.Queried = append(rt.Queried, t)
			} else {
				// view 中的目标已消失：保留占位，coverage 记 not_ready。
				rt.Queried = append(rt.Queried, TargetInfo{ID: id, Readiness: ReadyNotReady})
			}
		}
		// 排除项（online_only）从 All-Queried 推导。
		inView := make(map[string]bool, len(rt.Queried))
		for _, t := range rt.Queried {
			inView[t.ID] = true
		}
		for _, t := range infos {
			if !inView[t.ID] {
				rt.Excluded = append(rt.Excluded, t)
			}
		}
		return v, rt, nil
	}
	view, err := c.CreateView(ctx, q)
	if err != nil {
		return View{}, nil, err
	}
	resolved, err := ResolveTargets(ctx, c.resolver, q.AuthorizedTargetIDs, q.OnlineOnly)
	if err != nil {
		return View{}, nil, err
	}
	return *view, resolved, nil
}

// authorizeViewReuse 校验当前调用方可复用既有 Query View（F-001/F-006）。
//
// 复用必须同时满足：
//  1. 同一授权主体（PrincipalKey）；
//  2. 固定查询维度与创建时一致（时间范围、filter、onlineOnly、permission scope）；
//  3. 当前授权集合仍覆盖 view.TargetIDs。
//
// 不一致返回 ErrViewReuseMismatch / ErrViewAuthz，禁止静默忽略新条件或越权复用。
func authorizeViewReuse(v View, q Query) error {
	if v.PrincipalKey == "" || q.PrincipalKey == "" || v.PrincipalKey != q.PrincipalKey {
		return fmt.Errorf("%w: principal mismatch", ErrViewAuthz)
	}
	// 固定查询条件指纹（F-006）。
	if v.FromUTC != q.FromUTC || v.ToUTC != q.ToUTC {
		return fmt.Errorf("%w: time range %s..%s != view %s..%s",
			ErrViewReuseMismatch, q.FromUTC, q.ToUTC, v.FromUTC, v.ToUTC)
	}
	if normalizeFilter(v.Filter) != normalizeFilter(q.Filter) {
		return fmt.Errorf("%w: filter mismatch", ErrViewReuseMismatch)
	}
	if v.OnlineOnly != q.OnlineOnly {
		return fmt.Errorf("%w: onlineOnly mismatch", ErrViewReuseMismatch)
	}
	if v.PermissionScope != q.PermissionScope {
		return fmt.Errorf("%w: permission scope mismatch", ErrViewReuseMismatch)
	}
	if v.OrderVersion != "" && q.OrderVersion != "" && v.OrderVersion != q.OrderVersion {
		return fmt.Errorf("%w: order version mismatch", ErrViewReuseMismatch)
	}

	allowed := make(map[string]bool, len(q.AuthorizedTargetIDs))
	for _, id := range q.AuthorizedTargetIDs {
		allowed[id] = true
	}
	if allowed["*"] {
		return nil
	}
	for _, id := range v.TargetIDs {
		authID := id
		if mapped := v.TargetAuthorizationIDs[id]; mapped != "" {
			authID = mapped
		}
		if !allowed[authID] {
			return fmt.Errorf("%w: target %s not in current authorization", ErrViewAuthz, id)
		}
	}
	return nil
}

func normalizeFilter(s string) string {
	return strings.TrimSpace(s)
}

// fanoutSearch 扇出各 Worker Search，返回每路事件、覆盖汇总输入、全局 cut 标记。
func (c *Coordinator) fanoutSearch(ctx context.Context, view View, resolved *ResolvedTargets, q Query, tailMode string) (
	streams []EventStream,
	covByTarget map[string]TargetCoverage,
	quality Quality,
	workerCut bool,
	workerOpen bool,
	err error,
) {
	if c.dialer == nil {
		return nil, nil, quality, false, false, errNoDialer
	}
	covByTarget = make(map[string]TargetCoverage)
	quality.DuplicateQuality = DupExact
	order, byWorker := resolved.ByWorker()

	// max_fanout 预算：超出的 worker 不扇出，coverage 记 partial。
	fanoutBudget := 0
	if q.Budget.MaxFanout > 0 {
		fanoutBudget = int(q.Budget.MaxFanout)
	}

	budget := view.Budget
	if budget.Limit == 0 {
		budget = q.Budget
	}

	type fanoutResult struct {
		streams  []EventStream
		coverage map[string]TargetCoverage
		cut      bool
		open     bool
		err      error
	}
	results := make(chan fanoutResult, len(order))
	launched := 0
	for i, workerID := range order {
		targets := byWorker[workerID]
		if fanoutBudget > 0 && i >= fanoutBudget {
			for _, t := range targets {
				covByTarget[t.ID] = CoverageFromReadiness(t, []string{"fanout_budget_exceeded"})
			}
			continue
		}
		launched++
		go func(workerID string, targets []TargetInfo) {
			workerStreams, workerCoverage, cut, open, callErr := c.fanoutOne(ctx, view, q, budget, workerID, targets, tailMode)
			results <- fanoutResult{streams: workerStreams, coverage: workerCoverage, cut: cut, open: open, err: callErr}
		}(workerID, append([]TargetInfo(nil), targets...))
	}
	for i := 0; i < launched; i++ {
		result := <-results
		if result.err != nil {
			return nil, nil, quality, false, false, result.err
		}
		streams = append(streams, result.streams...)
		for targetID, coverage := range result.coverage {
			covByTarget[targetID] = coverage
		}
		workerCut = workerCut || result.cut
		workerOpen = workerOpen || result.open
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].WorkerID < streams[j].WorkerID })

	// duplicate quality：任一 partial → unresolved
	for _, tc := range covByTarget {
		if tc.State == CoveragePartial || tc.State == CoverageStale {
			quality.DuplicateQuality = DupUnresolved
			break
		}
	}
	return streams, covByTarget, quality, workerCut, workerOpen, nil
}

func (c *Coordinator) fanoutOne(ctx context.Context, view View, q Query, budget QueryBudget, workerID string, targets []TargetInfo, tailMode string) (
	streams []EventStream, coverage map[string]TargetCoverage, cut bool, open bool, err error,
) {
	coverage = make(map[string]TargetCoverage, len(targets))
	targetIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		targetIDs = append(targetIDs, workerTargetID(target))
	}
	client, dialErr := c.dialer.Dial(ctx, workerID)
	if dialErr != nil {
		for _, target := range targets {
			tc := CoverageFromReadiness(target, []string{"dial_failed", dialErr.Error()})
			if target.Readiness == ReadyOnline {
				tc.State = CoverageOffline
			}
			coverage[target.ID] = tc
		}
		return
	}
	workerView, viewErr, viewOK := workerViewID(view, workerID)
	if !viewOK {
		for _, target := range targets {
			// 显式补 NotReady：不依赖 CoverageFromReadiness 的原因白名单单点生效，
			// 与 dial_failed/search_failed 的既有写法一致（B-1 双保险）。
			tc := CoverageFromReadiness(target, []string{"worker_view_failed", viewErr})
			if tc.State == CoverageSuccess {
				tc.State = CoverageNotReady
			}
			coverage[target.ID] = tc
		}
		return
	}
	req := WorkerSearchRequest{TargetIDs: targetIDs, FromUTC: view.FromUTC, ToUTC: view.ToUTC,
		Filter: view.Filter, Budget: budget, ViewID: workerView,
		Cursor: workerCursor(q.Cursor, workerView, budget.Limit), OrderVersion: view.OrderVersion}
	var resp *WorkerSearchResponse
	if tailMode == "" {
		resp, err = client.Search(ctx, req)
	} else {
		resp, err = client.Tail(ctx, req, tailMode)
	}
	if err != nil {
		callErr := err
		err = nil
		for _, target := range targets {
			tc := CoverageFromReadiness(target, []string{"search_failed", callErr.Error()})
			if target.Readiness == ReadyOnline {
				tc.State = CoveragePartial
			}
			coverage[target.ID] = tc
		}
		return
	}
	if resp == nil || resp.Unsupported || resp.Error != "" {
		for _, target := range targets {
			switch {
			case resp == nil:
				coverage[target.ID] = CoverageFromReadiness(target, []string{"empty_worker_response"})
			case resp.Unsupported:
				coverage[target.ID] = CoverageFromReadiness(target, []string{"log_rpc_unsupported"})
			default:
				coverage[target.ID] = workerErrorCoverage(target, workerID, resp.Targets, resp.Error)
			}
		}
		return
	}
	if err = ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); err != nil {
		return
	}
	cut, open = resp.Truncated, !resp.Exhausted
	if len(resp.Items) > 0 {
		items := append([]Event(nil), resp.Items...)
		SortEventsInPlace(items)
		primary := ""
		if len(targets) == 1 {
			primary = targets[0].ID
		}
		streams = append(streams, EventStream{WorkerID: workerID, TargetID: primary, Items: items})
	}
	got := make(map[string]WorkerTargetResult, len(resp.Targets))
	for _, target := range resp.Targets {
		got[target.TargetID] = target
	}
	for _, target := range targets {
		result, ok := got[workerTargetID(target)]
		if !ok {
			tc := CoverageFromReadiness(target, []string{"target_missing_in_worker_response"})
			if tc.State == CoverageSuccess {
				tc.State = CoverageNotReady
			}
			coverage[target.ID] = tc
			continue
		}
		tc := TargetCoverage{TargetID: target.ID, State: result.State,
			Reasons:           append([]string(nil), result.Reasons...),
			ClosedVisibleSeq:  firstNonEmpty(result.ClosedVisibleSeq, target.ClosedVisibleSeq),
			CatalogGeneration: firstNonEmpty(result.CatalogGeneration, target.CatalogGeneration),
			WorkerID:          workerID, HistoricalHolder: target.HistoricalHolder}
		if tc.State == CoverageSuccess && resp.Truncated {
			tc.State = CoveragePartial
			tc.Reasons = append(tc.Reasons, "worker_truncated")
		}
		if tc.State == CoverageUnspecified {
			tc.State = CoverageNotReady
			tc.Reasons = append(tc.Reasons, "unspecified_worker_state")
		}
		coverage[target.ID] = tc
	}
	return
}

func workerCursor(raw, workerViewID string, pageLimit int) string {
	if raw == "" {
		return ""
	}
	cursor, err := DecodePageCursor(raw)
	if err != nil {
		return ""
	}
	if pageLimit <= 0 {
		pageLimit = cursor.PageLimit
	}
	return workerquery.EncodeCursor(workerquery.Cursor{
		ViewID: workerViewID, OrderVersion: workerquery.SortVersion, PageLimit: uint32(pageLimit),
		SortKey: workerquery.SortKey{
			EventTimeUTC: cursor.SortKey.EventTimeUTC.UTC().Format(time.RFC3339Nano),
			LogSourceID:  cursor.SortKey.LogSourceID, SourceGeneration: cursor.SortKey.SourceGeneration,
			RecordStart: cursor.SortKey.RecordStart, RecordEnd: cursor.SortKey.RecordEnd, EventID: cursor.SortKey.EventID,
		},
	})
}

// Search 跨 Worker K-way merge。
func (c *Coordinator) Search(ctx context.Context, q Query) (*SearchResponse, error) {
	var pageCursor PageCursor
	if q.Cursor != "" {
		var cursorErr error
		pageCursor, cursorErr = DecodePageCursor(q.Cursor)
		if cursorErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrViewReuseMismatch, cursorErr)
		}
		if q.ViewID != "" && q.ViewID != pageCursor.ViewID {
			return nil, fmt.Errorf("%w: cursor view mismatch", ErrViewReuseMismatch)
		}
		q.ViewID = pageCursor.ViewID
		if q.OrderVersion != "" && q.OrderVersion != pageCursor.OrderVersion {
			return nil, fmt.Errorf("%w: cursor order mismatch", ErrViewReuseMismatch)
		}
		q.OrderVersion = pageCursor.OrderVersion
		if q.Budget.Limit != 0 && q.Budget.Limit != pageCursor.PageLimit {
			return nil, fmt.Errorf("%w: cursor page limit mismatch", ErrViewReuseMismatch)
		}
		q.Budget.Limit = pageCursor.PageLimit
	}
	view, resolved, err := c.resolveForQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	streams, covBy, quality, workerCut, workerOpen, err := c.fanoutSearch(ctx, view, resolved, q, "")
	if err != nil {
		return nil, err
	}

	capRows := view.Budget.Limit
	if capRows == 0 {
		capRows = q.Budget.Limit
	}
	if capRows <= 0 {
		capRows = DefaultPageLimit
	}
	maxBytes := view.Budget.MaxBytes
	if maxBytes == 0 {
		maxBytes = q.Budget.MaxBytes
	}
	rows, pageMore, byteCut := MergeStreams(streams, capRows, maxBytes)
	hasMore := pageMore || workerOpen

	enum := EnumExhausted
	if hasMore || byteCut || workerCut {
		enum = EnumOpen
	}
	for _, s := range streams {
		// 若某路仍有未消费事件，枚举未结束——MergeStreams 已在 budgetCut 反映。
		_ = s
	}

	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, enum)
	if byteCut {
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, "BUDGET_EXCEEDED")
		cov.Complete = false
	}
	// FR-480：Search 完成后把 coverage 上的 closed_visible_seq 向量固化到 View，
	// Export 与后续 view 复用必须看到同一向量。
	c.persistClosedVisibleSeq(&view, cov)

	resp := &SearchResponse{
		View:      view,
		Coverage:  cov,
		Quality:   quality,
		Items:     rows,
		Exhausted: !hasMore && !byteCut && !workerCut && cov.Complete,
		BudgetCut: byteCut || workerCut,
	}
	if (hasMore || byteCut) && len(rows) > 0 {
		resp.NextCursor = EncodePageCursor(PageCursor{
			ViewID: view.ViewID, OrderVersion: view.OrderVersion,
			SortKey: rows[len(rows)-1].SortKey(), PageLimit: capRows,
		})
	}
	return resp, nil
}

// Tail fans out the explicit Tail mode. FOLLOW_LIVE is a bounded dynamic
// window and always keeps enumeration OPEN; it never inherits fixed-history
// completeness from Search pagination.
func (c *Coordinator) Tail(ctx context.Context, q Query, mode string) (*SearchResponse, error) {
	if !strings.EqualFold(mode, "FOLLOW_LIVE") {
		return c.Search(ctx, q)
	}
	view, resolved, err := c.resolveForQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	streams, covBy, quality, workerCut, _, err := c.fanoutSearch(ctx, view, resolved, q, "FOLLOW_LIVE")
	if err != nil {
		return nil, err
	}
	limit := view.Budget.Limit
	if limit <= 0 {
		limit = q.Budget.Limit
	}
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	rows, _, byteCut := MergeStreams(streams, limit, view.Budget.MaxBytes)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumOpen)
	if byteCut || workerCut {
		cov.Complete = false
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, "TRUNCATED")
	}
	c.persistClosedVisibleSeq(&view, cov)
	return &SearchResponse{View: view, Coverage: cov, Quality: quality, Items: rows,
		Exhausted: false, BudgetCut: byteCut || workerCut}, nil
}

func (c *Coordinator) Fields(ctx context.Context, q Query) (*FieldsResponse, error) {
	if c.dialer == nil {
		return nil, errNoDialer
	}
	view, resolved, err := c.resolveForQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	fieldSet := map[string]bool{}
	covBy := make(map[string]TargetCoverage)
	order, byWorker := resolved.ByWorker()
	for _, workerID := range order {
		targets := byWorker[workerID]
		targetIDs := make([]string, 0, len(targets))
		for _, target := range targets {
			targetIDs = append(targetIDs, workerTargetID(target))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		workerView, _, viewOK := workerViewID(view, workerID)
		if dialErr != nil || !viewOK {
			for _, target := range targets {
				covBy[target.ID] = CoverageFromReadiness(target, []string{"fields_failed"})
			}
			continue
		}
		resp, callErr := client.Fields(ctx, WorkerSearchRequest{TargetIDs: targetIDs, FromUTC: view.FromUTC,
			ToUTC: view.ToUTC, Filter: view.Filter, Budget: view.Budget, ViewID: workerView, OrderVersion: view.OrderVersion})
		if callErr != nil || resp == nil || resp.Unsupported || resp.Error != "" {
			for _, target := range targets {
				covBy[target.ID] = CoverageFromReadiness(target, []string{"fields_failed"})
			}
			continue
		}
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		for _, field := range resp.Fields {
			fieldSet[field] = true
		}
		got := make(map[string]WorkerTargetResult)
		for _, target := range resp.Targets {
			got[target.TargetID] = target
		}
		for _, target := range targets {
			result, ok := got[workerTargetID(target)]
			if !ok {
				covBy[target.ID] = CoverageFromReadiness(target, []string{"target_missing_in_worker_response"})
				continue
			}
			covBy[target.ID] = TargetCoverage{TargetID: target.ID, State: result.State,
				Reasons: result.Reasons, ClosedVisibleSeq: result.ClosedVisibleSeq,
				CatalogGeneration: result.CatalogGeneration, WorkerID: workerID,
				HistoricalHolder: target.HistoricalHolder}
		}
	}
	fields := make([]string, 0, len(fieldSet))
	for field := range fieldSet {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	c.persistClosedVisibleSeq(&view, cov)
	return &FieldsResponse{View: view, Coverage: cov, Fields: fields}, nil
}

// persistClosedVisibleSeq 从 coverage targets 提取 closed_visible_seq 向量写回 view（含存储视图）。
func (c *Coordinator) persistClosedVisibleSeq(view *View, cov Coverage) {
	if view == nil {
		return
	}
	cvs := make(map[string]string)
	generations := make(map[string]string)
	for _, tc := range cov.Targets {
		if tc.ClosedVisibleSeq != "" {
			cvs[tc.TargetID] = tc.ClosedVisibleSeq
		}
		if tc.CatalogGeneration != "" {
			generations[tc.TargetID] = tc.CatalogGeneration
		}
	}
	if len(cvs) == 0 && len(generations) == 0 {
		return
	}
	view.ClosedVisibleSeq = cvs
	view.WorkerCatalogGenerations = generations
	viewID := view.ViewID
	if viewID == "" {
		return
	}
	c.mu.Lock()
	if stored, ok := c.views[viewID]; ok {
		stored.ClosedVisibleSeq = cvs
		stored.WorkerCatalogGenerations = generations
		c.views[viewID] = stored
	}
	c.mu.Unlock()
}

// Stats 二次聚合：可合并 count/sum/min/max；avg = sum/count（加权）。
func (c *Coordinator) Stats(ctx context.Context, q StatsQuery) (*StatsResponse, error) {
	view, resolved, err := c.resolveForQuery(ctx, q.Query)
	if err != nil {
		return nil, err
	}
	if c.dialer == nil {
		return nil, errNoDialer
	}

	covBy := make(map[string]TargetCoverage)
	quality := Quality{DuplicateQuality: DupExact, StatsQuality: StatsQExact}
	order, byWorker := resolved.ByWorker()

	var merged []StatsPoint
	anyTrunc := false

	for _, workerID := range order {
		targets := byWorker[workerID]
		targetIDs := make([]string, 0, len(targets))
		for _, t := range targets {
			targetIDs = append(targetIDs, workerTargetID(t))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		if dialErr != nil {
			for _, t := range targets {
				tc := CoverageFromReadiness(t, []string{"dial_failed", dialErr.Error()})
				if t.Readiness == ReadyOnline {
					tc.State = CoverageOffline
				}
				covBy[t.ID] = tc
			}
			quality.StatsQuality = StatsQPartial
			continue
		}
		workerView, viewErr, viewOK := workerViewID(view, workerID)
		if !viewOK {
			for _, t := range targets {
				tc := CoverageFromReadiness(t, []string{"worker_view_failed", viewErr})
				if tc.State == CoverageSuccess {
					tc.State = CoverageNotReady
				}
				covBy[t.ID] = tc
			}
			quality.StatsQuality = StatsQPartial
			continue
		}
		resp, callErr := client.Stats(ctx, WorkerSearchRequest{
			TargetIDs:    targetIDs,
			FromUTC:      view.FromUTC,
			ToUTC:        view.ToUTC,
			Filter:       view.Filter,
			Budget:       view.Budget,
			ViewID:       workerView,
			OrderVersion: view.OrderVersion,
			MetricField:  q.MetricField,
		}, q.GroupBy, q.TimeBucket)
		if callErr != nil || resp == nil {
			// 稳定字面量必须保留：它是 CoverageFromReadiness 白名单与 normalize 的识别锚点。
			// 若用 callErr.Error() 覆盖它，原因既不入白名单、也无法归一，目标会停留 success
			// 并使 complete 误判为 true（与 B-1 同源的假 complete）。错误文本改为附加原因。
			reasons := []string{"stats_failed"}
			if callErr != nil {
				reasons = append(reasons, callErr.Error())
			}
			for _, t := range targets {
				tc := CoverageFromReadiness(t, reasons)
				if tc.State == CoverageSuccess {
					tc.State = CoveragePartial
				}
				covBy[t.ID] = tc
			}
			quality.StatsQuality = StatsQPartial
			continue
		}
		if resp.Unsupported {
			for _, t := range targets {
				covBy[t.ID] = CoverageFromReadiness(t, []string{"log_rpc_unsupported"})
			}
			quality.StatsQuality = StatsQUnavailable
			continue
		}
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		if resp.Error != "" {
			for _, t := range targets {
				covBy[t.ID] = workerErrorCoverage(t, workerID, resp.Targets, resp.Error)
			}
			quality.StatsQuality = StatsQPartial
			continue
		}
		if resp.Truncated {
			anyTrunc = true
		}
		got := make(map[string]WorkerTargetResult)
		for _, tr := range resp.Targets {
			got[tr.TargetID] = tr
		}
		for _, t := range targets {
			tr, ok := got[workerTargetID(t)]
			if !ok {
				tc := CoverageFromReadiness(t, []string{"target_missing_in_worker_response"})
				if tc.State == CoverageSuccess {
					tc.State = CoverageNotReady
				}
				covBy[t.ID] = tc
				continue
			}
			tc := TargetCoverage{
				TargetID:          t.ID,
				State:             tr.State,
				Reasons:           append([]string(nil), tr.Reasons...),
				ClosedVisibleSeq:  firstNonEmpty(tr.ClosedVisibleSeq, t.ClosedVisibleSeq),
				CatalogGeneration: firstNonEmpty(tr.CatalogGeneration, t.CatalogGeneration),
				WorkerID:          workerID,
				HistoricalHolder:  t.HistoricalHolder,
			}
			if tr.State == CoverageSuccess && resp.Truncated {
				tc.State = CoveragePartial
				tc.Reasons = append(tc.Reasons, "stats_truncated")
			}
			covBy[t.ID] = tc
		}
		merged = append(merged, WorkerPointsToStats(resp.Points)...)
	}

	points := MergeStatsPoints(merged)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	if !cov.Complete && quality.StatsQuality == StatsQExact {
		quality.StatsQuality = StatsQPartial
	}
	if anyTrunc && quality.StatsQuality == StatsQExact {
		quality.StatsQuality = StatsQPartial
	}
	c.persistClosedVisibleSeq(&view, cov)

	return &StatsResponse{
		View:       view,
		Coverage:   cov,
		Quality:    quality,
		Points:     points,
		Metric:     q.MetricField,
		TimeBucket: q.TimeBucket,
	}, nil
}

// Facets 受控维度精确合并；高基数截断语义传染。
func (c *Coordinator) Facets(ctx context.Context, q FacetsQuery) (*FacetsResponse, error) {
	view, resolved, err := c.resolveForQuery(ctx, q.Query)
	if err != nil {
		return nil, err
	}
	if c.dialer == nil {
		return nil, errNoDialer
	}

	covBy := make(map[string]TargetCoverage)
	quality := Quality{DuplicateQuality: DupExact, StatsQuality: StatsQExact}
	order, byWorker := resolved.ByWorker()

	var perWorker []WorkerFacetsResponse

	for _, workerID := range order {
		targets := byWorker[workerID]
		targetIDs := make([]string, 0, len(targets))
		for _, t := range targets {
			targetIDs = append(targetIDs, workerTargetID(t))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		if dialErr != nil {
			for _, t := range targets {
				covBy[t.ID] = CoverageFromReadiness(t, []string{"dial_failed", dialErr.Error()})
			}
			continue
		}
		workerView, viewErr, viewOK := workerViewID(view, workerID)
		if !viewOK {
			for _, t := range targets {
				tc := CoverageFromReadiness(t, []string{"worker_view_failed", viewErr})
				if tc.State == CoverageSuccess {
					tc.State = CoverageNotReady
				}
				covBy[t.ID] = tc
			}
			continue
		}
		resp, callErr := client.Facets(ctx, WorkerSearchRequest{
			TargetIDs:    targetIDs,
			FromUTC:      view.FromUTC,
			ToUTC:        view.ToUTC,
			Filter:       view.Filter,
			Budget:       view.Budget,
			ViewID:       workerView,
			OrderVersion: view.OrderVersion,
		}, q.Dimensions, q.DimensionLimit)
		if callErr != nil || resp == nil {
			// 同 Stats：稳定字面量是白名单识别锚点，错误文本只作附加原因，不得覆盖它。
			// Facets 无独立质量标记，若此处失败被误判为 success，用户会静默失去整个
			// Worker 的 facet 面板且无从察觉，故必须显式降为 partial。
			reasons := []string{"facets_failed"}
			if callErr != nil {
				reasons = append(reasons, callErr.Error())
			}
			for _, t := range targets {
				tc := CoverageFromReadiness(t, reasons)
				if tc.State == CoverageSuccess {
					tc.State = CoveragePartial
				}
				covBy[t.ID] = tc
			}
			continue
		}
		if resp.Unsupported {
			for _, t := range targets {
				covBy[t.ID] = CoverageFromReadiness(t, []string{"log_rpc_unsupported"})
			}
			continue
		}
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		if resp.Error != "" {
			for _, t := range targets {
				covBy[t.ID] = workerErrorCoverage(t, workerID, resp.Targets, resp.Error)
			}
			continue
		}
		got := make(map[string]WorkerTargetResult)
		for _, tr := range resp.Targets {
			got[tr.TargetID] = tr
		}
		for _, t := range targets {
			tr, ok := got[workerTargetID(t)]
			if !ok {
				tc := CoverageFromReadiness(t, []string{"target_missing_in_worker_response"})
				if tc.State == CoverageSuccess {
					tc.State = CoverageNotReady
				}
				covBy[t.ID] = tc
				continue
			}
			covBy[t.ID] = TargetCoverage{
				TargetID:          t.ID,
				State:             tr.State,
				Reasons:           append([]string(nil), tr.Reasons...),
				ClosedVisibleSeq:  firstNonEmpty(tr.ClosedVisibleSeq, t.ClosedVisibleSeq),
				CatalogGeneration: firstNonEmpty(tr.CatalogGeneration, t.CatalogGeneration),
				WorkerID:          workerID,
				HistoricalHolder:  t.HistoricalHolder,
			}
		}
		perWorker = append(perWorker, *resp)
	}

	dims := MergeFacets(perWorker, q.Dimensions, q.DimensionLimit)
	truncated := FacetsTruncated(dims)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	if truncated {
		cov.PartialReasons = append(cov.PartialReasons, "facet_truncated")
		// Facet 截断不等于目标覆盖失败，但禁止把截断结果当成完整列表交付成功导出。
	}
	c.persistClosedVisibleSeq(&view, cov)

	return &FacetsResponse{
		View:       view,
		Coverage:   cov,
		Quality:    quality,
		Dimensions: dims,
		Truncated:  truncated,
	}, nil
}

// Export 复用同一 view/budget/coverage 的有界物化。
//
// 任一 partial / cut / 超预算 → ExportIncomplete=true 且 Artifact=nil。
// 源 Worker 后续离线不改变已完成成功产物的语义（完整通过后 Artifact 不再依赖 Worker）。
func (c *Coordinator) Export(ctx context.Context, q Query) (*ExportResult, error) {
	const defaultExportRows = 10_000
	const defaultExportBytes = 64 << 20
	totalRows := q.Budget.Limit
	if totalRows <= 0 {
		totalRows = defaultExportRows
	}
	totalBytes := q.Budget.MaxBytes
	if totalBytes == 0 {
		totalBytes = defaultExportBytes
	}
	pageLimit := DefaultPageLimit
	if totalRows < pageLimit {
		pageLimit = totalRows
	}

	// 强制同 view：若调用方未带 ViewID，先建 view 再 Search，保证导出与列表同快照语义。
	searchQ := q
	if searchQ.ViewID == "" {
		viewQuery := q
		viewQuery.Budget.Limit = pageLimit
		viewQuery.Budget.MaxBytes = 0
		view, err := c.CreateView(ctx, viewQuery)
		if err != nil {
			return nil, err
		}
		searchQ.ViewID = view.ViewID
	}
	searchQ.Budget.Limit = pageLimit
	searchQ.Budget.MaxBytes = 0
	var items []Event
	var last *SearchResponse
	for {
		resp, err := c.Search(ctx, searchQ)
		if err != nil {
			return nil, err
		}
		last = resp
		items = append(items, resp.Items...)
		var used uint64
		for _, event := range items {
			used += event.ApproxBytes()
		}
		if len(items) > totalRows || used > totalBytes {
			exportView := resp.View
			exportView.Budget.Limit = totalRows
			exportView.Budget.MaxBytes = totalBytes
			return BuildExport(exportView, resp.Coverage, resp.Quality, items, true, true), nil
		}
		if resp.BudgetCut || !resp.Coverage.Complete {
			exportView := resp.View
			exportView.Budget.Limit = totalRows
			exportView.Budget.MaxBytes = totalBytes
			return BuildExport(exportView, resp.Coverage, resp.Quality, items, resp.BudgetCut, true), nil
		}
		if resp.Exhausted {
			break
		}
		if resp.NextCursor == "" || len(items) >= totalRows {
			exportView := resp.View
			exportView.Budget.Limit = totalRows
			exportView.Budget.MaxBytes = totalBytes
			return BuildExport(exportView, resp.Coverage, resp.Quality, items, true, true), nil
		}
		searchQ.Cursor = resp.NextCursor
		searchQ.ViewID = resp.View.ViewID
		if cursor, decodeErr := DecodePageCursor(resp.NextCursor); decodeErr == nil {
			searchQ.Budget.Limit = cursor.PageLimit
		}
	}
	if last == nil {
		return nil, fmt.Errorf("logcoord: export produced no search response")
	}
	exportView := last.View
	exportView.Budget.Limit = totalRows
	exportView.Budget.MaxBytes = totalBytes
	return BuildExport(exportView, last.Coverage, last.Quality, items, false, coverageHasCut(last.Coverage)), nil
}

func coverageHasCut(cov Coverage) bool {
	for _, tc := range cov.Targets {
		for _, r := range tc.Reasons {
			if r == "worker_truncated" || r == "stats_truncated" || r == "fanout_budget_exceeded" {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
