package query

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// Stats 执行查询面 Stats stub（输入是逻辑事件集合；AVG=sum/count）。
func (s *Service) Stats(ctx context.Context, req QueryRequest, groupBy []string) StatsResponse {
	resp := StatsResponse{RequestID: req.RequestID}
	ctx, cancel := WithQueryDeadline(ctx, req.Budget)
	defer cancel()
	ctx = WithPhysicalReadBudget(ctx, req.Budget)
	plan := s.planForRead(ctx, req)
	resp.Coverage = plan.Coverage
	remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
	resp.Quality = plan.Quality
	// 此处禁止再把 StatsQuality 无条件重置为 StatsExact（历史遗留）：planner 会因
	// projection 不完整或 ConflictCount>0 判定 StatsPartial，而该冲突路径不追加任何
	// coverage 原因，下方的 !plan.Coverage.Complete 兜底不会触发，重置会让分区被错报成
	// stats_quality=exact。plan 已种入 StatsExact 作为基线，按 rank 取 worst 的结论
	// 就是保留 plan 值（exact 是最不差者），故直接沿用即可。
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

	if _, unimpl := s.client.(UnimplementedRangeClient); unimpl {
		resp.Err = newErr(ErrCodeUnsupported, "stats unimplemented on this worker")
		plan.Coverage.MarkIncomplete(ReasonUnsupported)
		resp.Coverage = plan.Coverage
		resp.Quality.StatsQuality = StatsUnavailable
		return resp
	}

	limit := req.Budget.EffectiveLimit()
	_ = limit // stats 合并点数上限由后续规格冻结；foundation 仍受 fanout/bytes 约束
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
		// 同上用 worst 合并：这里是「请求前截短」，plan 判定可能是 exact 或 partial，
		// 直接置 partial 在今天等价，但一旦 plan 未来能给出更差等级就会被冲掉。
		resp.Quality.StatsQuality = worstStatsQuality(resp.Quality.StatsQuality, StatsPartial)
	}
	stopKind := runBudgetTasks(ctx, req.Budget, launch, func(ctx context.Context, idx int, budget Budget) (statsRangeOutcome, uint64) {
		readCtx, unpin, pinErr := s.pinRange(ctx, &plan)
		if pinErr != nil {
			return statsRangeOutcome{err: pinErr}, 0
		}
		defer unpin()
		ctx = readCtx
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
		// 空值 = 该 range 未上报，跳过（避免把未上报按 unspecified 计入而拖累 exact）；
		// 非空按 worst 合并，防止多个 range 的非 exact 等级互相覆盖后随完成顺序漂移。
		if o.res.StatsQuality != "" {
			resp.Quality.StatsQuality = worstStatsQuality(resp.Quality.StatsQuality, o.res.StatsQuality)
		}
		points = mergeStatsPoints(points, o.res.Points)
		return true
	})
	if stopKind == budgetStopCut {
		plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
	}
	s.revalidateRead(ctx, &plan, &resp.Err)
	if !plan.Coverage.Complete {
		// 按 worst 合并：range 回调可能已把 StatsUnavailable 累积进来，
		// 直接置 partial 会把它降级。
		resp.Quality.StatsQuality = worstStatsQuality(resp.Quality.StatsQuality, StatsPartial)
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
	fieldSet := map[string]struct{}{"level": {}, "stream": {}}
	client, ok := s.client.(FieldsRangeClient)
	if !ok {
		resp.Err = newErr(ErrCodeUnsupported, "fields unimplemented on this worker")
		plan.Coverage.MarkIncomplete(ReasonUnsupported)
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
		readCtx, unpin, pinErr := s.pinRange(ctx, &plan)
		if pinErr != nil {
			return fieldsRangeOutcome{err: pinErr}, 0
		}
		defer unpin()
		ctx = readCtx
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
	s.revalidateRead(ctx, &plan, &resp.Err)
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
	client, ok := s.client.(FacetsRangeClient)
	if !ok {
		resp.Err = newErr(ErrCodeUnsupported, "facets unimplemented on this worker")
		plan.Coverage.MarkIncomplete(ReasonUnsupported)
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
		plan.Coverage.MarkIncomplete(ReasonUnsupported)
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
		readCtx, unpin, pinErr := s.pinRange(ctx, &plan)
		if pinErr != nil {
			return facetsRangeOutcome{err: pinErr}, 0
		}
		defer unpin()
		ctx = readCtx
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
	s.revalidateRead(ctx, &plan, &resp.Err)
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
