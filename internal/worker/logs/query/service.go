package query

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// QueryRequest 是 Search/Stats/Tail 共用请求基座（对应 LogQueryRequestBase）。
type QueryRequest struct {
	RequestID         string
	ProtocolVersion   string
	TimeRange         TimeRange
	AuthorizedTargets []string
	Budget            Budget
	View              *ViewRef
	Filter            string
	PermissionScope   string
	CancelToken       string
	RequireCold       bool
	RequireArchive    bool
	GroupBy           []string
	TimeBucket        string
	MetricField       string
	// AllowProjectionDrift 本次请求豁免「投影清单版本变化」判 VIEW_STALE。
	// 仅 CP 导出路径（自己创建视图、自己逐页读完）置位；见 PlanRequest.AllowProjectionDrift。
	AllowProjectionDrift bool
}

// SearchResponse 对应 LogSearchResponse。
type SearchResponse struct {
	RequestID  string           `json:"request_id"`
	View       *ViewRef         `json:"view,omitempty"`
	Coverage   Coverage         `json:"coverage"`
	Quality    Quality          `json:"quality"`
	Items      []logtypes.Event `json:"items,omitempty"`
	NextCursor string           `json:"next_cursor,omitempty"`
	Exhausted  bool             `json:"exhausted"`
	Err        *QueryError      `json:"error,omitempty"`
}

// OpenView creates the Worker-local fixed Query View without executing a range.
// CP uses the returned view ID as the per-Worker reference for later calls.
func (s *Service) OpenView(req QueryRequest) SearchResponse {
	resp := SearchResponse{RequestID: req.RequestID}
	plan := s.planFromRequest(req)
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	resp.Quality = plan.Quality
	if plan.View != nil {
		resp.View = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
	}
	resp.Err = plan.Err
	return resp
}

// StatsResponse 对应 LogStatsResponse。
type StatsResponse struct {
	RequestID string       `json:"request_id"`
	View      *ViewRef     `json:"view,omitempty"`
	Coverage  Coverage     `json:"coverage"`
	Quality   Quality      `json:"quality"`
	Points    []StatsPoint `json:"points,omitempty"`
	Err       *QueryError  `json:"error,omitempty"`
}

// TailResponse 是 Tail stub 结果（契约：FOLLOW_LIVE 与 VIEW_BOUNDED 不可混称）。
type TailResponse struct {
	RequestID string           `json:"request_id"`
	Mode      TailMode         `json:"mode"`
	View      *ViewRef         `json:"view,omitempty"`
	Coverage  Coverage         `json:"coverage"`
	Quality   Quality          `json:"quality"`
	Items     []logtypes.Event `json:"items,omitempty"`
	Exhausted bool             `json:"exhausted"`
	Err       *QueryError      `json:"error,omitempty"`
}

// CapabilitiesResponse 对应 GetLogCapabilitiesResponse。
// 老 Worker / 未实现路径：Supported=false + LOG_UNSUPPORTED，CP 必须标 LOG_UNSUPPORTED/not-ready。
type CapabilitiesResponse struct {
	Supported          bool         `json:"supported"`
	ProtocolVersion    string       `json:"protocol_version"`
	Capabilities       []string     `json:"capabilities,omitempty"`
	MaxLimit           uint32       `json:"max_limit,omitempty"`
	MaxRequestBytes    uint64       `json:"max_request_bytes,omitempty"`
	Cancellation       bool         `json:"cancellation"`
	BuildID            string       `json:"build_id,omitempty"`
	UnsupportedReasons []QueryError `json:"unsupported_reasons,omitempty"`
}

// FieldsResponse 对应字段枚举查询。
type FieldsResponse struct {
	RequestID string      `json:"request_id"`
	View      *ViewRef    `json:"view,omitempty"`
	Coverage  Coverage    `json:"coverage"`
	Fields    []string    `json:"fields,omitempty"`
	Err       *QueryError `json:"error,omitempty"`
}

// FacetsResponse 对应受控维度查询。
type FacetsResponse struct {
	RequestID string       `json:"request_id"`
	View      *ViewRef     `json:"view,omitempty"`
	Coverage  Coverage     `json:"coverage"`
	Values    []FacetValue `json:"values,omitempty"`
	Truncated bool         `json:"truncated"`
	Err       *QueryError  `json:"error,omitempty"`
}

// ArchiveTaskResponse 是归档/恢复任务的查询面投影。
type ArchiveTaskResponse struct {
	TaskID string
	State  string
	Err    *QueryError
}

// ArchiveStatusResponse 是归档对象可用性投影。
type ArchiveStatusResponse struct {
	RequestID    string
	Coverage     Coverage
	AvailableIDs []string
	MissingIDs   []string
	Err          *QueryError
}

// ArchiveBackend 由 logassemble 注入真实 Registry/Rehydrate 实现。
type ArchiveBackend interface {
	Rehydrate(ctx context.Context, req QueryRequest, objectIDs []string) ArchiveTaskResponse
	ArchiveStatus(ctx context.Context, req QueryRequest, objectIDs []string) ArchiveStatusResponse
}

// Service 是 FR-479 查询面 foundation：消费 planner ranges + budget。
type Service struct {
	planner *Planner
	client  RangeClient
	buildID string
	archive ArchiveBackend
	// maxRequestBytes 能力协商上界。
	maxRequestBytes uint64
}

// SetArchiveBackend 接入真实归档任务和对象状态查询。
func (s *Service) SetArchiveBackend(backend ArchiveBackend) { s.archive = backend }

// NewService 构造查询服务。client 为 nil 时使用 UnimplementedRangeClient。
func NewService(planner *Planner, client RangeClient, buildID string) *Service {
	if client == nil {
		client = UnimplementedRangeClient{}
	}
	return &Service{
		planner:         planner,
		client:          client,
		buildID:         buildID,
		maxRequestBytes: 1 << 20,
	}
}

// GetCapabilities 返回能力协商结果。
// foundation 支持 query_view|coverage|quality|tail_mode_split；真实 VL 检索取决于 client。
func (s *Service) GetCapabilities(req CapabilitiesRequest) CapabilitiesResponse {
	proto := req.ProtocolVersion
	if proto == "" {
		proto = DefaultProtocolVersion
	}
	// 旧 Worker 风格：协议不匹配时显式 unsupported，不静默降级。
	if proto != DefaultProtocolVersion {
		return CapabilitiesResponse{
			Supported:       false,
			ProtocolVersion: proto,
			UnsupportedReasons: []QueryError{{
				Code:    ErrCodeUnsupported,
				Message: fmt.Sprintf("unsupported protocol_version %q", proto),
			}},
		}
	}
	// client 为未实现 stub 时，Search/Stats 不得宣称完整支持。
	caps := []string{"query_view", "coverage", "quality", "sort_key_v1", "capabilities"}
	if _, unimpl := s.client.(UnimplementedRangeClient); !unimpl {
		caps = append(caps, "search", "stats", "tail_view_bounded")
		if _, ok := s.client.(FieldsRangeClient); ok {
			caps = append(caps, "fields")
		}
		if _, ok := s.client.(FacetsRangeClient); ok {
			caps = append(caps, "facets")
		}
		if _, ok := s.client.(FollowLiveRangeClient); ok {
			caps = append(caps, "tail_follow_live")
		}
	}
	return CapabilitiesResponse{
		Supported:       true,
		ProtocolVersion: DefaultProtocolVersion,
		Capabilities:    caps,
		MaxLimit:        DefaultMaxLimit,
		MaxRequestBytes: s.maxRequestBytes,
		Cancellation:    true,
		BuildID:         s.buildID,
	}
}

// CapabilitiesRequest 对应 GetLogCapabilitiesRequest。
type CapabilitiesRequest struct {
	ProtocolVersion string
	RequestID       string
}

// budgetTaskOutcome 只保留在途任务的结果；完成后立即归并并释放未用预留。
type budgetTaskOutcome[T any] struct {
	value    T
	bytes    uint64
	reserved uint64
}

// budgetStopKind 描述 runBudgetTasks 为什么没有把 count 个任务全部跑完（QRY-4）。
//
// 为什么必须区分：旧实现只回一个 bool `cut`，而 `apply` 返回 false 造成的提前停止会在
// 「active==0 && next<count && 父 ctx 未取消」时被记成 `cut`，调用方随即无条件
// `MarkIncomplete(ReasonBudgetExceeded)` ⇒ 典型响应变成「Err=UNSUPPORTED +
// PartialReasons 里带 BUDGET_EXCEEDED」——排障时把「能力未实现」读成「预算超限」。
// 现在覆盖原因只在**真正的预算路径**上落 BUDGET_EXCEEDED。
type budgetStopKind int

const (
	// budgetStopComplete 全部任务正常跑完（或被排队任务从未启动但额度未耗尽）。
	budgetStopComplete budgetStopKind = iota
	// budgetStopCut 额度耗尽 / 在途任务消费超过预留 ⇒ 真的是预算问题。
	budgetStopCut
	// budgetStopApply apply 返回 false（能力未实现、需要提前停止）⇒ 与预算无关。
	budgetStopApply
)

// runBudgetTasks 以最多八个真实在途任务调度，预算记账为已消费加在途预留。
// 零值入站预算只在请求级归一为默认总额，不能让每个 range 再各领一份默认额度。
//
// 返回值是停跑**原因**（见 budgetStopKind）：调用方据此决定覆盖原因码，不得把
// 「提前停止」误标成预算耗尽。
func runBudgetTasks[T any](ctx context.Context, budget Budget, count int, execute func(context.Context, int, Budget) (T, uint64), apply func(T) bool) budgetStopKind {
	if budget.MaxBytes == 0 {
		budget.MaxBytes = DefaultMaxBytes
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	const parallel = 8
	done := make(chan budgetTaskOutcome[T], parallel)
	available := budget.MaxBytes
	var used uint64
	next, active := 0, 0
	cut, stop := false, false
	earlyStop := false
	for next < count || active > 0 {
		for !stop && workCtx.Err() == nil && next < count && active < parallel {
			b := budget
			var reserved uint64
			if budget.MaxBytes > 0 {
				if available == 0 {
					break
				}
				slots := parallel - active
				if left := count - next; left < slots {
					slots = left
				}
				reserved = available / uint64(slots)
				if reserved == 0 {
					reserved = 1
				}
				available -= reserved
				b.MaxBytes = reserved
			}
			idx := next
			next++
			active++
			go func() {
				value, bytes := execute(workCtx, idx, b)
				done <- budgetTaskOutcome[T]{value: value, bytes: bytes, reserved: reserved}
			}()
		}
		if active == 0 {
			if next < count && ctx.Err() == nil {
				// 一个任务都发不出 ⇒ 额度已见底（apply 未要求停止时才归因于预算 ✓）。
				cut = true
			}
			break
		}
		o := <-done
		active--
		if budget.MaxBytes > 0 {
			charged := o.bytes
			if charged > o.reserved {
				charged = o.reserved
				cut = true
			}
			used += charged
			available += o.reserved - charged
			if used >= budget.MaxBytes {
				cut, stop = true, true
				cancel()
			}
		}
		if !apply(o.value) {
			stop = true
			earlyStop = true
			cancel()
		}
		// stop 后仍排空在途结果，避免遗留发送协程；不会再启动排队任务。
	}
	switch {
	case earlyStop:
		// apply 要求提前停止：原因由 apply 自己记进 coverage（UNSUPPORTED 等），
		// 调用方不得再补一条 BUDGET_EXCEEDED。
		return budgetStopApply
	case cut:
		return budgetStopCut
	default:
		return budgetStopComplete
	}
}

func budgetRangeCount(b Budget, coverage *Coverage, n int) int {
	if b.MaxFanout > 0 && uint32(n) > b.MaxFanout {
		coverage.MarkIncomplete(ReasonBudgetExceeded)
		return int(b.MaxFanout)
	}
	return n
}

// Search 执行查询面 Search stub。
func (s *Service) Search(ctx context.Context, req QueryRequest) SearchResponse {
	// 固定观测口径（慢查询定位用）：分解 plan / 任务执行 / 组装三段耗时。
	// 全程 Debug 级 —— 默认 Info 门下不产出任何日志，只有显式开 Debug 才会付出两次
	// time.Now() 的纳秒级开销。回答的问题固定为"端到端慢查询花在哪一段"，
	// 与 vlrange 的 `query-timing: vl-merged`（VL 侧往返）配套构成完整分解。
	queryStart := time.Now()
	resp := SearchResponse{RequestID: req.RequestID}
	plan := s.planFromRequest(req)
	planElapsedMs := time.Since(queryStart).Milliseconds()
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	resp.Quality = plan.Quality
	if plan.View != nil {
		resp.View = &ViewRef{
			ViewID:       plan.View.ViewID,
			OrderVersion: plan.View.OrderVersion,
		}
	}
	if plan.Err != nil {
		resp.Err = plan.Err
		if req.View != nil && req.View.Cursor != "" {
			// keep cursor echo for client correlation
			resp.View = &ViewRef{ViewID: req.View.ViewID, Cursor: req.View.Cursor, OrderVersion: SortVersion}
		}
		return resp
	}

	// 旧 Worker / 无实现：Unimplemented 能力路径。
	if len(plan.Ranges) == 0 {
		// 无 range 且 coverage 不完整：不得伪装空成功。
		if !plan.Coverage.Complete {
			resp.Err = newErr(ErrCodePartial, "no authoritative ranges; coverage partial")
			return resp
		}
		resp.Exhausted = true
		resp.Coverage.EnumerationState = EnumExhausted
		return resp
	}

	limit := req.Budget.EffectiveLimit()
	fanoutLimit := req.Budget.MaxFanout
	var (
		items      []logtypes.Event
		cancelled  bool
		budgetHit  bool
		safeBefore string
	)
	cursorKey := decodeCursorSort(req.View)

	// 任务有界并发；每个完成结果立即释放未用额度并归并，不能先发完整队列再事后裁字节。
	// 结果顺序由稳定复合键冻结，partial 质量与覆盖原因只累积、不因后完成的 exact 回升。
	//
	// 合并执行（2026-10-08）：可合并 range 聚为一次粗筛（MergedRangeClient，行级按各
	// range 权威谓词归因）；客户端未实现该能力、或组内存在无来源约束 range 时自动
	// 退回逐 range——语义不变，仅性能差异。
	//
	// 跨天合并（2026-10-08 修正，直测 10× 差距）：**不得按 UTC 天拆查询**——VL 在
	// "窗口内匹配行数 < limit"时必须扫完整个窗口，按天拆分把一次查询放大成每天一次全扫（实测 154 源 24h 跨天一次查 0.44-0.83s，分天后旧天单查即
	// 4.8-6.2s）。同 Owner 的 rng 合并为一次跨天查询，行归因按各 rng 谓词
	// （含各自天分区水位）在内存完成；不同 Owner 的批次必须全部参与全局 top-N。

	mergedClient, _ := s.client.(MergedRangeClient)
	tasks := groupSearchTasks(plan.Ranges, mergedClient != nil)
	// 按原始 range 计 fanout，合并不得把多日/多源组偷换成一个预算单位。
	allowed := make([]searchTask, 0, len(tasks))
	appliedRanges := 0
	for _, task := range tasks {
		if fanoutLimit > 0 && uint32(appliedRanges+len(task.rngs)) > fanoutLimit {
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
			budgetHit = true
			break
		}
		appliedRanges += len(task.rngs)
		allowed = append(allowed, task)
	}
	type searchRangeOutcome struct {
		res       RangeResult
		err       error
		targetIDs []string
	}
	exhausted := true
	stopKind := runBudgetTasks(ctx, req.Budget, len(allowed), func(ctx context.Context, idx int, budget Budget) (searchRangeOutcome, uint64) {
		task := allowed[idx]
		ids := make([]string, 0, len(task.rngs))
		for _, rng := range task.rngs {
			ids = append(ids, rng.TargetID)
		}
		q := RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange, Filter: req.Filter, Budget: budget, CursorSortKey: cursorKey}
		var res RangeResult
		var err error
		if task.merged {
			// 合并组水位由各 range 的归因谓词检查，不能借用首个 range 的水位。
			res, err = mergedClient.SearchMerged(ctx, task.rngs, q)
		} else {
			rng := task.rngs[0]
			q.ClosedVisibleSeq = rng.ClosedVisibleSeq
			res, err = s.client.Search(ctx, rng, q)
		}
		return searchRangeOutcome{res: res, err: err, targetIDs: ids}, res.Bytes
	}, func(o searchRangeOutcome) bool {
		if ctx.Err() != nil {
			cancelled = true
			exhausted = false
			return false
		}
		if o.err != nil {
			exhausted = false
			if errors.Is(o.err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, "range client search unimplemented")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				return false
			}
			if errors.Is(o.err, context.Canceled) {
				budgetHit = true
				plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
				return true
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			slog.Info("search range 不可用，标记 RANGE_UNAVAILABLE", "targets", o.targetIDs, "err", o.err)
			for _, tid := range o.targetIDs {
				for i := range plan.Coverage.Targets {
					if plan.Coverage.Targets[i].TargetID == tid {
						plan.Coverage.Targets[i].State = CoverageNotReady
						plan.Coverage.Targets[i].Reasons = appendUniqueStr(plan.Coverage.Targets[i].Reasons, ReasonRangeUnavailable)
					}
				}
			}
			return true
		}
		if !o.res.Exhausted {
			exhausted = false
		}
		if fence := o.res.SafeBeforeUTC; fence != "" && (safeBefore == "" || compareUTCDESC(fence, safeBefore) < 0) {
			safeBefore = fence
		}
		for _, reason := range o.res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
			if reason == ReasonBudgetExceeded {
				budgetHit = true
			}
		}
		if o.res.DuplicateQuality != "" && o.res.DuplicateQuality != DupExact {
			resp.Quality.DuplicateQuality = o.res.DuplicateQuality
		}
		if o.res.StatsQuality != "" && o.res.StatsQuality != StatsExact {
			resp.Quality.StatsQuality = o.res.StatsQuality
		}
		rangeItems := o.res.Items
		if cursorKey != nil {
			rangeItems = filterAfterCursor(rangeItems, *cursorKey)
		}
		items = append(items, rangeItems...)
		SortEvents(items)
		if safeBefore != "" {
			items = safeTimePrefix(items, safeBefore)
		}
		// 及时释放已归并结果：全局只需 top-N 加一条前瞻，不能囤积所有完成任务。
		if len(items) > int(limit)+1 {
			items = append([]logtypes.Event(nil), items[:int(limit)+1]...)
			exhausted = false
		}
		return true
	})
	if stopKind == budgetStopCut {
		budgetHit = true
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
	}
	slog.Debug("query-timing: tasks", "req", req.RequestID, "plan_ms", planElapsedMs,
		"tasks_ms", time.Since(queryStart).Milliseconds()-planElapsedMs, "tasks", len(tasks), "applied_ranges", appliedRanges)

	SortEvents(items)

	// cursor 过滤：丢弃 sort key <= cursor 的事件（逻辑分页）。
	if cursorKey != nil {
		items = filterAfterCursor(items, *cursorKey)
	}

	var next string
	// 任一 range 未耗尽、出现 range 错误或覆盖本身不完整，都不得宣称分页结束。
	exhausted = exhausted && plan.Coverage.Complete
	if uint32(len(items)) > limit {
		items = items[:limit]
		exhausted = false
	}
	if !exhausted && len(items) > 0 && !budgetHit && !cancelled {
		next = EncodeCursor(Cursor{
			ViewID:       viewIDOf(plan.View, req.View),
			OrderVersion: SortVersion,
			SortKey:      SortKeyOf(items[len(items)-1]),
			PageLimit:    limit,
		})
	}

	if cancelled {
		plan.Coverage.MarkIncomplete(ReasonCancelled)
		plan.Coverage.EnumerationState = EnumCancelled
		resp.Err = newErr(ErrCodeCancelled, "query cancelled")
	} else if budgetHit && len(items) == 0 && !plan.Coverage.Complete {
		resp.Err = newErr(ErrCodeBudgetExceeded, "budget exceeded before any items")
	}

	resp.Items = items
	resp.NextCursor = next
	resp.Exhausted = exhausted && plan.Coverage.EnumerationState != EnumStale
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	if exhausted && plan.Coverage.Complete {
		resp.Coverage.EnumerationState = EnumExhausted
	}
	if plan.Quality.DuplicateQuality != "" && resp.Quality.DuplicateQuality == DupExact {
		resp.Quality.DuplicateQuality = plan.Quality.DuplicateQuality
	}
	if !plan.Coverage.Complete {
		resp.Quality.StatsQuality = StatsPartial
	}
	// 固定观测口径：端到端总耗时（worker 视角）。
	slog.Debug("query-timing: total",
		"req", req.RequestID, "total_ms", time.Since(queryStart).Milliseconds(), "items", len(items))
	return resp
}

// Stats 执行查询面 Stats stub（输入是逻辑事件集合；AVG=sum/count）。
func (s *Service) Stats(ctx context.Context, req QueryRequest, groupBy []string) StatsResponse {
	resp := StatsResponse{RequestID: req.RequestID}
	plan := s.planFromRequest(req)
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	resp.Quality = plan.Quality
	resp.Quality.StatsQuality = StatsExact
	if plan.View != nil {
		resp.View = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
	}
	if plan.Err != nil {
		resp.Err = plan.Err
		return resp
	}

	if _, unimpl := s.client.(UnimplementedRangeClient); unimpl {
		resp.Err = newErr(ErrCodeUnsupported, "stats unimplemented on this worker")
		plan.Coverage.MarkIncomplete(ReasonUnsupported)
		resp.Coverage = plan.Coverage
		resp.Quality.StatsQuality = StatsUnavailable
		return resp
	}

	limit := req.Budget.EffectiveLimit()
	_ = limit // stats 合并点数上限由后续 FR 冻结；foundation 仍受 fanout/bytes 约束
	fanoutLimit := req.Budget.MaxFanout
	var points []StatsPoint

	type statsRangeOutcome struct {
		res StatsResult
		err error
	}
	launch := len(plan.Ranges)
	if fanoutLimit > 0 && uint32(launch) > fanoutLimit {
		launch = int(fanoutLimit)
		// 必须在发起前记录跳过范围，不能等截短 outcomes 中永远不会发生的下一轮。
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		resp.Quality.StatsQuality = StatsPartial
	}
	stopKind := runBudgetTasks(ctx, req.Budget, launch, func(ctx context.Context, idx int, budget Budget) (statsRangeOutcome, uint64) {
		rng := plan.Ranges[idx]
		q := RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange, Filter: req.Filter, Budget: budget,
			ClosedVisibleSeq: rng.ClosedVisibleSeq, GroupBy: append([]string(nil), req.GroupBy...),
			TimeBucket: req.TimeBucket, MetricField: req.MetricField}
		res, err := s.client.Stats(ctx, rng, q)
		return statsRangeOutcome{res: res, err: err}, res.Bytes
	}, func(o statsRangeOutcome) bool {
		if ctx.Err() != nil {
			plan.Coverage.MarkIncomplete(ReasonCancelled)
			resp.Err = newErr(ErrCodeCancelled, "stats cancelled")
			return false
		}
		if o.err != nil {
			if errors.Is(o.err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, "range client stats unimplemented")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				return false
			}
			if errors.Is(o.err, context.Canceled) {
				plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
			} else {
				plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			}
			return true
		}
		for _, reason := range o.res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		if o.res.StatsQuality != "" && o.res.StatsQuality != StatsExact {
			resp.Quality.StatsQuality = o.res.StatsQuality
		}
		points = mergeStatsPoints(points, o.res.Points)
		return true
	})
	if stopKind == budgetStopCut {
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
	}
	if !plan.Coverage.Complete {
		resp.Quality.StatsQuality = StatsPartial
	}
	resp.Points = points
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	return resp
}

// Fields 枚举同一固定 View 的字段。内部身份字段仍由 Worker schema 白名单控制，
// RangeClient 只负责补充真实数据中出现的用户字段。
func (s *Service) Fields(ctx context.Context, req QueryRequest) FieldsResponse {
	resp := FieldsResponse{RequestID: req.RequestID}
	plan := s.planFromRequest(req)
	resp.Coverage = plan.Coverage
	if plan.View != nil {
		resp.View = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
	}
	if plan.Err != nil {
		resp.Err = plan.Err
		return resp
	}
	fieldSet := map[string]struct{}{"level": {}, "stream": {}}
	client, ok := s.client.(FieldsRangeClient)
	if !ok {
		resp.Err = newErr(ErrCodeUnsupported, "fields unimplemented on this worker")
		resp.Coverage.MarkIncomplete(ReasonUnsupported)
		resp.Coverage = plan.Coverage
		return resp
	}
	// 并发取数，但按 ranges 原顺序施加副作用：
	// 错误、早停（cancelled/unsupported）与覆盖原因的顺序都与串行版本逐字一致，
	// 差别仅在于错误路径上多做了少量无用的 range 请求（不影响任何观测结果）。
	type fieldsRangeOutcome struct {
		res FieldsResult
		err error
	}
	launch := budgetRangeCount(req.Budget, &plan.Coverage, len(plan.Ranges))
	stopKind := runBudgetTasks(ctx, req.Budget, launch, func(ctx context.Context, idx int, budget Budget) (fieldsRangeOutcome, uint64) {
		rng := plan.Ranges[idx]
		res, err := client.Fields(ctx, rng, RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange,
			Filter: req.Filter, Budget: budget, ClosedVisibleSeq: rng.ClosedVisibleSeq})
		return fieldsRangeOutcome{res: res, err: err}, res.Bytes
	}, func(o fieldsRangeOutcome) bool {
		if err := ctx.Err(); err != nil {
			resp.Err = newErr(ErrCodeCancelled, "fields cancelled")
			plan.Coverage.MarkIncomplete(ReasonCancelled)
			return false
		}
		if o.err != nil {
			if errors.Is(o.err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, "fields unimplemented on this worker")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				return false
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			return true
		}
		for _, reason := range o.res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		for _, field := range o.res.Fields {
			if streamFieldAllowed(field) {
				fieldSet[field] = struct{}{}
			}
		}
		if o.res.Truncated {
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		}
		return true
	})
	if stopKind == budgetStopCut {
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
	}
	resp.Fields = make([]string, 0, len(fieldSet))
	for field := range fieldSet {
		resp.Fields = append(resp.Fields, field)
	}
	sort.Strings(resp.Fields)
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	return resp
}

// Facets 在 Worker 侧合并每个权威 range 的受控维度，返回截断标记而不是伪完整结果。
func (s *Service) Facets(ctx context.Context, req QueryRequest, dimensions []string, limit uint32) FacetsResponse {
	resp := FacetsResponse{RequestID: req.RequestID}
	plan := s.planFromRequest(req)
	resp.Coverage = plan.Coverage
	if plan.View != nil {
		resp.View = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
	}
	if plan.Err != nil {
		resp.Err = plan.Err
		return resp
	}
	client, ok := s.client.(FacetsRangeClient)
	if !ok {
		resp.Err = newErr(ErrCodeUnsupported, "facets unimplemented on this worker")
		resp.Coverage.MarkIncomplete(ReasonUnsupported)
		resp.Coverage = plan.Coverage
		return resp
	}
	if limit == 0 {
		limit = 100
	}
	allowed := make(map[string]struct{}, len(dimensions))
	for _, dim := range dimensions {
		if streamFieldAllowed(dim) {
			allowed[dim] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		resp.Err = newErr(ErrCodeUnsupported, "facets requires controlled dimensions")
		resp.Coverage.MarkIncomplete(ReasonUnsupported)
		resp.Coverage = plan.Coverage
		return resp
	}
	controlled := make([]string, 0, len(allowed))
	for _, dim := range dimensions {
		if _, ok := allowed[dim]; ok {
			controlled = append(controlled, dim)
		}
	}
	counts := make(map[string]uint64)
	type facetsRangeOutcome struct {
		res RangeFacetsResult
		err error
	}
	launch := budgetRangeCount(req.Budget, &plan.Coverage, len(plan.Ranges))
	stopKind := runBudgetTasks(ctx, req.Budget, launch, func(ctx context.Context, idx int, budget Budget) (facetsRangeOutcome, uint64) {
		rng := plan.Ranges[idx]
		res, err := client.Facets(ctx, rng, RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange,
			Filter: req.Filter, Budget: budget, ClosedVisibleSeq: rng.ClosedVisibleSeq}, controlled, limit)
		return facetsRangeOutcome{res: res, err: err}, res.Bytes
	}, func(o facetsRangeOutcome) bool {
		if ctx.Err() != nil {
			resp.Err = newErr(ErrCodeCancelled, "facets cancelled")
			plan.Coverage.MarkIncomplete(ReasonCancelled)
			return false
		}
		if o.err != nil {
			if errors.Is(o.err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, "facets unimplemented on this worker")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				return false
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			return true
		}
		for _, reason := range o.res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		for _, value := range o.res.Values {
			if _, ok := allowed[value.Dimension]; ok {
				counts[value.Dimension+"\x00"+value.Value] += value.Count
			}
		}
		if o.res.Truncated {
			resp.Truncated = true
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		}
		return true
	})
	if stopKind == budgetStopCut {
		resp.Truncated = true
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
	}
	for key, count := range counts {
		parts := strings.SplitN(key, "\x00", 2)
		resp.Values = append(resp.Values, FacetValue{Dimension: parts[0], Value: parts[1], Count: count})
	}
	sort.Slice(resp.Values, func(i, j int) bool {
		if resp.Values[i].Dimension != resp.Values[j].Dimension {
			return resp.Values[i].Dimension < resp.Values[j].Dimension
		}
		if resp.Values[i].Count != resp.Values[j].Count {
			return resp.Values[i].Count > resp.Values[j].Count
		}
		return resp.Values[i].Value < resp.Values[j].Value
	})
	// limit applies per dimension, preserving deterministic ordering.
	seen := make(map[string]uint32)
	filtered := resp.Values[:0]
	for _, value := range resp.Values {
		if seen[value.Dimension] >= limit {
			resp.Truncated = true
			continue
		}
		seen[value.Dimension]++
		filtered = append(filtered, value)
	}
	resp.Values = filtered
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	return resp
}

// Rehydrate 由 ArchiveBackend 执行受管恢复；未接入时显式 unsupported。
func (s *Service) Rehydrate(ctx context.Context, req QueryRequest, objectIDs []string) ArchiveTaskResponse {
	if s.archive == nil {
		return ArchiveTaskResponse{Err: newErr(ErrCodeUnsupported, "rehydrate unimplemented on this worker")}
	}
	return s.archive.Rehydrate(ctx, req, objectIDs)
}

// ArchiveStatus 查询真实 manifest/provider 状态。
func (s *Service) ArchiveStatus(ctx context.Context, req QueryRequest, objectIDs []string) ArchiveStatusResponse {
	if s.archive == nil {
		return ArchiveStatusResponse{RequestID: req.RequestID, Err: newErr(ErrCodeUnsupported, "archive status unimplemented on this worker")}
	}
	return s.archive.ArchiveStatus(ctx, req, objectIDs)
}

func streamFieldAllowed(field string) bool {
	switch field {
	case "level", "stream", "instance_id":
		return true
	default:
		return false
	}
}

// remapCoverageTargets projects physical Catalog partition IDs back to the CP
// authorization target that selected them (for example node:1/day -> node:1).
// The worker keeps physical IDs internally; the wire contract uses authorized
// target IDs so CP can merge coverage without reporting false target-missing.
func remapCoverageTargets(cov *Coverage, authorized []string) {
	if cov == nil || len(authorized) == 0 || len(cov.Targets) == 0 {
		return
	}
	byID := make(map[string]int, len(cov.Targets))
	out := make([]TargetCoverage, 0, len(cov.Targets))
	for _, target := range cov.Targets {
		id := target.TargetID
		for _, auth := range authorized {
			if id == auth || strings.HasPrefix(id, auth+"/") {
				id = auth
				break
			}
		}
		target.TargetID = id
		if idx, ok := byID[id]; ok {
			out[idx].Reasons = appendUniqueStrings(out[idx].Reasons, target.Reasons...)
			if coverageRank(target.State) > coverageRank(out[idx].State) {
				out[idx].State = target.State
			}
			if target.ClosedVisibleSeq > out[idx].ClosedVisibleSeq {
				out[idx].ClosedVisibleSeq = target.ClosedVisibleSeq
			}
			if target.CatalogGeneration > out[idx].CatalogGeneration {
				out[idx].CatalogGeneration = target.CatalogGeneration
			}
			continue
		}
		byID[id] = len(out)
		out = append(out, target)
	}
	cov.Targets = out
	// plan 级完整性必须保留：预算截断、fanout 上限、底层 RangeClient 的 CoverageReasons
	// 只调用 MarkIncomplete 记在 plan 级，不一定把某个目标降级为非 success。若此处无条件
	// 重置为 complete，这些原因会被整体抹掉，截断结果被上报为完整（M-5）。
	planComplete := cov.Complete
	planReasons := append([]string(nil), cov.PartialReasons...)

	cov.Complete = planComplete
	cov.PartialReasons = planReasons
	for _, target := range out {
		if target.State != CoverageSuccess {
			cov.Complete = false
			for _, reason := range target.Reasons {
				if reason != "" && !containsString(cov.PartialReasons, reason) {
					cov.PartialReasons = append(cov.PartialReasons, reason)
				}
			}
		}
	}
}

func coverageRank(state CoverageState) int {
	switch state {
	case CoverageSuccess:
		return 0
	case CoveragePartial:
		return 1
	case CoverageNotReady, CoverageOffline, CoverageArchiveMiss, CoverageStale:
		return 2
	default:
		return 3
	}
}

func appendUniqueStrings(dst []string, values ...string) []string {
	for _, value := range values {
		if value != "" && !containsString(dst, value) {
			dst = append(dst, value)
		}
	}
	return dst
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

// Tail separates a fixed-view read from live observation of published ranges.
func (s *Service) Tail(ctx context.Context, req QueryRequest, mode TailMode) TailResponse {
	resp := TailResponse{RequestID: req.RequestID, Mode: mode}
	if mode == "" {
		mode = TailViewBounded
		resp.Mode = mode
	}
	// 契约：FOLLOW_LIVE 与 VIEW_BOUNDED 不共享“完整历史”语义。
	if mode != TailFollowLive && mode != TailViewBounded {
		resp.Err = newErr(ErrCodeUnsupported, fmt.Sprintf("unknown tail mode %q", mode))
		return resp
	}
	if mode == TailFollowLive {
		return s.followLive(ctx, req)
	}

	plan := s.planFromRequest(req)
	resp.Coverage = plan.Coverage
	resp.Quality = plan.Quality
	if plan.View != nil {
		resp.View = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
	}
	if plan.Err != nil {
		resp.Err = plan.Err
		return resp
	}

	limit := req.Budget.EffectiveLimit()
	var items []logtypes.Event
	type tailRangeOutcome struct {
		res RangeResult
		err error
	}
	exhausted := true
	safeBefore := ""
	launch := budgetRangeCount(req.Budget, &plan.Coverage, len(plan.Ranges))
	stopKind := runBudgetTasks(ctx, req.Budget, launch, func(ctx context.Context, idx int, budget Budget) (tailRangeOutcome, uint64) {
		rng := plan.Ranges[idx]
		res, err := s.client.Tail(ctx, rng, RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange,
			Filter: req.Filter, Budget: budget, ClosedVisibleSeq: rng.ClosedVisibleSeq}, mode)
		return tailRangeOutcome{res: res, err: err}, res.Bytes
	}, func(o tailRangeOutcome) bool {
		if ctx.Err() != nil {
			plan.Coverage.MarkIncomplete(ReasonCancelled)
			resp.Err = newErr(ErrCodeCancelled, "tail cancelled")
			return false
		}
		if o.err != nil {
			if errors.Is(o.err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, fmt.Sprintf("tail mode %s unimplemented", mode))
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				return false
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			return true
		}
		for _, reason := range o.res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		exhausted = exhausted && o.res.Exhausted
		if fence := o.res.SafeBeforeUTC; fence != "" && (safeBefore == "" || compareUTCDESC(fence, safeBefore) < 0) {
			safeBefore = fence
		}
		items = append(items, o.res.Items...)
		SortEvents(items)
		if safeBefore != "" {
			items = safeTimePrefix(items, safeBefore)
		}
		if len(items) > int(limit)+1 {
			items = append([]logtypes.Event(nil), items[:int(limit)+1]...)
			exhausted = false
		}
		return true
	})
	if stopKind == budgetStopCut {
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
	}
	// VIEW_BOUNDED 使用与 Search 相同的稳定排序（固定视图尾部）。
	if mode == TailViewBounded {
		SortEvents(items)
	}
	if uint32(len(items)) > limit {
		items = items[:limit]
		resp.Exhausted = false
	} else {
		resp.Exhausted = exhausted && plan.Coverage.Complete
	}
	if !plan.Coverage.Complete {
		resp.Quality.StatsQuality = StatsPartial
	}
	resp.Items = items
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	return resp
}

func (s *Service) planFromRequest(req QueryRequest) PlanResult {
	return s.planner.Plan(PlanRequest{
		RequestID:      req.RequestID,
		TimeRange:      req.TimeRange,
		TargetIDs:      req.AuthorizedTargets,
		Budget:         req.Budget,
		ViewRef:        req.View,
		RequireCold:    req.RequireCold,
		RequireArchive: req.RequireArchive,
		// 豁免标记是请求级授权：由 CP 在导出路径显式置位，Worker 侧原样透传给 planner。
		AllowProjectionDrift: req.AllowProjectionDrift,
	})
}

func viewIDOf(v *QueryView, ref *ViewRef) string {
	if v != nil {
		return v.ViewID
	}
	if ref != nil {
		return ref.ViewID
	}
	return ""
}

func decodeCursorSort(ref *ViewRef) *SortKey {
	if ref == nil || ref.Cursor == "" {
		return nil
	}
	c, err := DecodeCursor(ref.Cursor)
	if err != nil {
		return nil
	}
	k := c.SortKey
	return &k
}

func filterAfterCursor(items []logtypes.Event, after SortKey) []logtypes.Event {
	out := make([]logtypes.Event, 0, len(items))
	for _, ev := range items {
		// 结果序列序：Compare(ev, cursor) > 0 表示 ev 排在 cursor 之后（更旧/次页）。
		// 排除 cursor 本身（==0）与 cursor 之前（已返回过的更前页）。
		if CompareSortKey(SortKeyOf(ev), after) > 0 {
			out = append(out, ev)
		}
	}
	return out
}

// mergeStatsPoints 按维度键合并逻辑事件统计；AVG 不落盘，由 Avg() 二次聚合。
func mergeStatsPoints(dst, src []StatsPoint) []StatsPoint {
	idx := make(map[string]int, len(dst))
	for i, p := range dst {
		idx[dimKey(p.Dimensions)] = i
	}
	for _, p := range src {
		k := dimKey(p.Dimensions)
		if i, ok := idx[k]; ok {
			dst[i].Count += p.Count
			dst[i].Sum += p.Sum
			if p.Min < dst[i].Min || dst[i].Count == p.Count {
				// 第一次合并时 Min/Max 以 src 校正
			}
			if p.Min < dst[i].Min {
				dst[i].Min = p.Min
			}
			if p.Max > dst[i].Max {
				dst[i].Max = p.Max
			}
			continue
		}
		dst = append(dst, p)
		idx[k] = len(dst) - 1
	}
	return dst
}

func dimKey(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	// 稳定拼接
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 简单插入排序避免引入额外依赖
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 && keys[j] < keys[j-1] {
			keys[j], keys[j-1] = keys[j-1], keys[j]
			j--
		}
	}
	out := ""
	for _, k := range keys {
		out += k + "\x00" + m[k] + "\x01"
	}
	return out
}
