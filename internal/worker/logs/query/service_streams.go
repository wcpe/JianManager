package query

import (
	"context"
	"errors"
	"sort"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// streamLabelDimensionAllowed 判定某字段是不是 stream 的标签维度。
//
// # 为什么它不能复用 streamFieldAllowed
//
// 两者问的不是同一件事：
//   - streamFieldAllowed 关心「能不能做文档字段聚合」（level/parser_version 等，7 个）；
//   - 本函数关心「它是不是 stream 的标签」（实测只有 log_source_id 与 stream）。
//
// 混用的后果（这正是本项目已经踩过一次的同源缺陷）：把 level 放行给
// stream_field_values 会得到一个**空结果而不报错**，前端看到的是
// 「这个维度可选但永远没数据」。
//
// 与 vlsup.StreamFieldNames 的返回值同源：那里是 VL 给的权威清单，
// 这里是「没拿到清单时」的保守兜底。两者都只能收窄不能放宽。
func streamLabelDimensionAllowed(field string) bool {
	switch field {
	case "log_source_id", "stream":
		return true
	default:
		return false
	}
}

// Streams 枚举每个权威 range 的 stream，并按 (源, 流) 合并条数。
//
// # 合并语义
//
// 同一 (log_source_id, stream) 可能出现在多个 range（跨 UTC 天分区），
// 条数必须**相加**。这与其 Facets 的按值合并同构。
//
// # Complete 的传播方向
//
// 任一 range 报 Complete=false ⇒ 整体 Complete=false（不能把不完整拼成完整）。
// 反之，只有全部 range 都确定时才是确定——这是本接口唯一允许的放宽方向。
func (s *Service) Streams(ctx context.Context, req QueryRequest) StreamsResponse {
	resp := StreamsResponse{RequestID: req.RequestID}
	ctx, cancel := WithQueryDeadline(ctx, req.Budget)
	defer cancel()
	ctx = WithPhysicalReadBudget(ctx, req.Budget)
	plan := s.planForRead(ctx, req)
	resp.Coverage = plan.Coverage
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
	client, ok := s.client.(StreamsRangeClient)
	if !ok {
		resp.Err = newErr(ErrCodeUnsupported, "streams unimplemented on this worker")
		plan.Coverage.MarkIncomplete(ReasonUnsupported)
		resp.Coverage = plan.Coverage
		return resp
	}

	type streamsOutcome struct {
		res RangeStreamsResult
		err error
	}
	// 按 (源, 流) 聚合；raw 取首次见到的那个（同一二元组的 raw 必然相同）。
	counts := make(map[string]*StreamValue)
	allComplete := true
	launch := budgetRangeCount(req.Budget, &plan.Coverage, len(plan.Ranges))
	stopKind := runBudgetTasks(ctx, req.Budget, launch, func(ctx context.Context, idx int, budget Budget) (streamsOutcome, uint64) {
		readCtx, unpin, pinErr := s.pinRange(ctx, &plan)
		if pinErr != nil {
			return streamsOutcome{err: pinErr}, 0
		}
		defer unpin()
		ctx = readCtx
		rng := plan.Ranges[idx]
		res, err := client.Streams(ctx, rng, RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange,
			Filter: req.Filter, Budget: budget, ClosedVisibleSeq: rng.ClosedVisibleSeq})
		return streamsOutcome{res: res, err: err}, res.Bytes
	}, func(o streamsOutcome) bool {
		if ctx.Err() != nil {
			resp.Err = newErr(ErrCodeCancelled, "streams cancelled")
			plan.Coverage.MarkIncomplete(ReasonCancelled)
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
				resp.Err = newErr(ErrCodeUnsupported, "streams unimplemented on this worker")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				return false
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			return true
		}
		for _, reason := range o.res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		if !o.res.Complete {
			// 任一 range 条数不可信 ⇒ 整体不可信。不把不完整拼成完整。
			allComplete = false
		}
		for _, sv := range o.res.Streams {
			key := sv.LogSourceID + "\x00" + sv.Stream
			if existing, hit := counts[key]; hit {
				existing.Hits += sv.Hits
				continue
			}
			copy := sv
			counts[key] = &copy
		}
		return true
	})
	if stopKind == budgetStopCut {
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		allComplete = false
	}
	s.revalidateRead(ctx, &plan, &resp.Err)

	resp.Streams = make([]StreamValue, 0, len(counts))
	for _, sv := range counts {
		resp.Streams = append(resp.Streams, *sv)
	}
	// 条数降序、同数按源然后流名升序：前端直接渲染即有意义的顺序。
	sort.Slice(resp.Streams, func(i, j int) bool {
		if resp.Streams[i].Hits != resp.Streams[j].Hits {
			return resp.Streams[i].Hits > resp.Streams[j].Hits
		}
		if resp.Streams[i].LogSourceID != resp.Streams[j].LogSourceID {
			return resp.Streams[i].LogSourceID < resp.Streams[j].LogSourceID
		}
		return resp.Streams[i].Stream < resp.Streams[j].Stream
	})
	// 覆盖不完整时条数一定不可信，即使用户拿到的 Complete 看上去是 true。
	if !plan.Coverage.Complete || len(plan.Coverage.PartialReasons) > 0 {
		allComplete = false
	}
	resp.Complete = allComplete
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	return resp
}

// StreamFieldNames 合并每个 range 的 stream 标签维度名。
//
// 实测恒为 [`log_source_id`, `stream`]，但**不写死**：以 VL 为准。
// 取并集是安全方向（多给一个维度只会让前端多一个可选项，而少给会让用户筛不到）。
func (s *Service) StreamFieldNames(ctx context.Context, req QueryRequest) StreamFieldNamesResponse {
	resp := StreamFieldNamesResponse{RequestID: req.RequestID}
	ctx, cancel := WithQueryDeadline(ctx, req.Budget)
	defer cancel()
	ctx = WithPhysicalReadBudget(ctx, req.Budget)
	plan := s.planForRead(ctx, req)
	resp.Coverage = plan.Coverage
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
	client, ok := s.client.(StreamsRangeClient)
	if !ok {
		resp.Err = newErr(ErrCodeUnsupported, "stream field names unimplemented on this worker")
		plan.Coverage.MarkIncomplete(ReasonUnsupported)
		resp.Coverage = plan.Coverage
		return resp
	}

	type namesOutcome struct {
		res StreamFieldNamesResult
		err error
	}
	fieldSet := make(map[string]struct{}, 4)
	launch := budgetRangeCount(req.Budget, &plan.Coverage, len(plan.Ranges))
	stopKind := runBudgetTasks(ctx, req.Budget, launch, func(ctx context.Context, idx int, budget Budget) (namesOutcome, uint64) {
		readCtx, unpin, pinErr := s.pinRange(ctx, &plan)
		if pinErr != nil {
			return namesOutcome{err: pinErr}, 0
		}
		defer unpin()
		ctx = readCtx
		rng := plan.Ranges[idx]
		res, err := client.StreamFieldNames(ctx, rng, RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange,
			Filter: req.Filter, Budget: budget, ClosedVisibleSeq: rng.ClosedVisibleSeq})
		return namesOutcome{res: res, err: err}, res.Bytes
	}, func(o namesOutcome) bool {
		if ctx.Err() != nil {
			resp.Err = newErr(ErrCodeCancelled, "stream field names cancelled")
			plan.Coverage.MarkIncomplete(ReasonCancelled)
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
				resp.Err = newErr(ErrCodeUnsupported, "stream field names unimplemented on this worker")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				return false
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			return true
		}
		for _, reason := range o.res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		for _, f := range o.res.Fields {
			if f != "" {
				fieldSet[f] = struct{}{}
			}
		}
		return true
	})
	if stopKind == budgetStopCut {
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
	}
	s.revalidateRead(ctx, &plan, &resp.Err)

	resp.Fields = make([]string, 0, len(fieldSet))
	for f := range fieldSet {
		resp.Fields = append(resp.Fields, f)
	}
	sort.Strings(resp.Fields)
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	return resp
}

// StreamFieldValues 按单个 stream 标签维度合并取值分布。
//
// 非法维度（不在 streamLabelDimensionAllowed）直接拒绝：
// 放行它去查 VL 会得到**空结果而不报错**，用户看到的是「这个维度可选但永远没数据」。
func (s *Service) StreamFieldValues(ctx context.Context, req QueryRequest, field string) StreamFieldValuesResponse {
	resp := StreamFieldValuesResponse{RequestID: req.RequestID, Field: field}
	if !streamLabelDimensionAllowed(field) {
		// 用 UNSUPPORTED 而不是自造一个码：proto 的 LogErrorCode 里没有
		// INVALID_REQUEST，而前端只需要区分「这个维度不支持」与「服务坏了」。
		resp.Err = newErr(ErrCodeUnsupported, "stream field values 只支持 stream 标签维度（log_source_id / stream）")
		return resp
	}
	ctx, cancel := WithQueryDeadline(ctx, req.Budget)
	defer cancel()
	ctx = WithPhysicalReadBudget(ctx, req.Budget)
	plan := s.planForRead(ctx, req)
	resp.Coverage = plan.Coverage
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
	client, ok := s.client.(StreamsRangeClient)
	if !ok {
		resp.Err = newErr(ErrCodeUnsupported, "stream field values unimplemented on this worker")
		plan.Coverage.MarkIncomplete(ReasonUnsupported)
		resp.Coverage = plan.Coverage
		return resp
	}

	type valuesOutcome struct {
		res RangeStreamFieldValuesResult
		err error
	}
	counts := make(map[string]uint64)
	allComplete := true
	launch := budgetRangeCount(req.Budget, &plan.Coverage, len(plan.Ranges))
	stopKind := runBudgetTasks(ctx, req.Budget, launch, func(ctx context.Context, idx int, budget Budget) (valuesOutcome, uint64) {
		readCtx, unpin, pinErr := s.pinRange(ctx, &plan)
		if pinErr != nil {
			return valuesOutcome{err: pinErr}, 0
		}
		defer unpin()
		ctx = readCtx
		rng := plan.Ranges[idx]
		res, err := client.StreamFieldValues(ctx, rng, RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange,
			Filter: req.Filter, Budget: budget, ClosedVisibleSeq: rng.ClosedVisibleSeq}, field)
		return valuesOutcome{res: res, err: err}, res.Bytes
	}, func(o valuesOutcome) bool {
		if ctx.Err() != nil {
			resp.Err = newErr(ErrCodeCancelled, "stream field values cancelled")
			plan.Coverage.MarkIncomplete(ReasonCancelled)
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
				resp.Err = newErr(ErrCodeUnsupported, "stream field values unimplemented on this worker")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				return false
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			return true
		}
		for _, reason := range o.res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		if !o.res.Complete {
			allComplete = false
		}
		for _, v := range o.res.Values {
			counts[v.Value] += v.Count
		}
		return true
	})
	if stopKind == budgetStopCut {
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		allComplete = false
	}
	s.revalidateRead(ctx, &plan, &resp.Err)

	resp.Values = make([]FacetValue, 0, len(counts))
	for value, count := range counts {
		resp.Values = append(resp.Values, FacetValue{Dimension: field, Value: value, Count: count})
	}
	sort.Slice(resp.Values, func(i, j int) bool {
		if resp.Values[i].Count != resp.Values[j].Count {
			return resp.Values[i].Count > resp.Values[j].Count
		}
		return resp.Values[i].Value < resp.Values[j].Value
	})
	if !plan.Coverage.Complete || len(plan.Coverage.PartialReasons) > 0 {
		allComplete = false
	}
	resp.Complete = allComplete
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	return resp
}
