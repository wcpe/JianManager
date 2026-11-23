package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

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
		liveCtx, cancel := WithQueryDeadline(ctx, req.Budget)
		defer cancel()
		liveCtx = WithPhysicalReadBudget(liveCtx, req.Budget)
		return s.followLive(liveCtx, req)
	}

	ctx, cancel := WithQueryDeadline(ctx, req.Budget)
	defer cancel()
	ctx = WithPhysicalReadBudget(ctx, req.Budget)
	plan := s.planForRead(ctx, req)
	resp.Coverage = plan.Coverage
	resp.Quality = plan.Quality
	if plan.View != nil {
		resp.View = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
	}
	if plan.Err != nil {
		resp.Err = plan.Err
		return resp
	}
	finishRead, readErr := s.beginRead(ctx, req, &plan)
	if readErr != nil {
		resp.Err = readErr
		markReadError(&resp.Coverage, readErr)
		return resp
	}
	defer func() {
		finishRead()
		if resp.Err != nil && (req.View == nil || req.View.ViewID == "") {
			logLeaseCleanupError(s.planner.ReleaseView(viewIDOf(plan.View, req.View)))
		}
	}()

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
		readCtx, unpin, pinErr := s.pinRange(ctx, &plan)
		if pinErr != nil {
			return tailRangeOutcome{err: pinErr}, 0
		}
		defer unpin()
		ctx = readCtx
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
			if errors.Is(o.err, catalog.ErrQueryViewStale) {
				resp.Err = newErr(ErrCodeViewStale, o.err.Error())
				markReadError(&plan.Coverage, resp.Err)
				return false
			}
			if errors.Is(o.err, ErrRangeQueryBudget) {
				resp.Err = newErr(ErrCodeBudgetExceeded, o.err.Error())
				plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
				return false
			}
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
	s.revalidateRead(ctx, &plan, &resp.Err)
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
		// 与 Search/Stats 同口径用 worst 合并。本函数内 resp.Quality 只来自 plan
		// （exact 或 partial），当前不存在可达的 unavailable 降级路径，故这里是
		// 刻意统一（防止未来 RangeClient 开始上报 unavailable 时又被降级）。
		resp.Quality.StatsQuality = worstStatsQuality(resp.Quality.StatsQuality, StatsPartial)
	}
	resp.Items = items
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	return resp
}
