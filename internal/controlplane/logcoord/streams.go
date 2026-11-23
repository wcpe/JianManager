package logcoord

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// StreamLabelDimension 判定某字段是不是 stream 的标签维度。
//
// # ⚠️ 与 ControlledFacetDimensions **不是**同一份清单
//
// 这两个判据看着都是「哪些字段可用」，但问的是不同问题：
//   - ControlledFacetDimensions：「哪些**文档字段**能做分面聚合」（level/parser_version 等，10 个）；
//   - 本函数：「它是不是**流的标签**」（实测只有 log_source_id 与 stream）。
//
// 混用的后果是本项目已经踩过一次的同源缺陷：把 level 放行给 stream_field_values
// 会拿到**空结果而不报错**，前端看到「这个维度可选但永远没数据」。
//
// 与 Worker 侧 service_streams.go 的 streamLabelDimensionAllowed 逐字一致。
func StreamLabelDimension(dimension string) bool {
	switch dimension {
	case "log_source_id", "stream":
		return true
	default:
		return false
	}
}

// MergeStreamItems 合并多 Worker 的 stream 枚举结果。
//
// # 为何不叫 MergeStreams
//
// `merge.go` 里已经有一个 `MergeStreams`，它做的是**事件流的有序归并**
// （K-way merge，参数是 []EventStream）。两者名同、义全不同：那个拼的是日志行，
// 这个拼的是「流清单 + 条数」。同名会让调用方抓错函数而不报错（类型不同拒绝，
// 但读代码的人会误会），所以这里显式叫 Items。
//
// 规则：
//   - 同 (log_source_id, stream) 的 Hits **相加**——同一条流可能跨 Worker 存在
//     （历史数据持有者），也可能被同一 Worker 的多个 range 拆开报；
//   - 任一来源 Complete=false ⇒ 合并结果 Complete=false。不把不完整拼成完整；
//   - 排序与 Worker 侧一致（Hits 降序，同数按源然后流名升序），
//     保证单 Worker 与多 Worker 场景下前端拿到的顺序语义相同。
func MergeStreamItems(perWorker []WorkerStreamsResponse) ([]StreamItem, bool) {
	type acc struct {
		item StreamItem
	}
	byKey := make(map[string]*acc)
	complete := true
	for _, resp := range perWorker {
		if !resp.Complete {
			complete = false
		}
		for _, s := range resp.Streams {
			key := s.LogSourceID + "\x00" + s.Stream
			a, ok := byKey[key]
			if !ok {
				a = &acc{item: StreamItem{LogSourceID: s.LogSourceID, Stream: s.Stream, Raw: s.Raw}}
				byKey[key] = a
			}
			a.item.Hits += s.Hits
		}
	}
	out := make([]StreamItem, 0, len(byKey))
	for _, a := range byKey {
		out = append(out, a.item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		if out[i].LogSourceID != out[j].LogSourceID {
			return out[i].LogSourceID < out[j].LogSourceID
		}
		return out[i].Stream < out[j].Stream
	})
	return out, complete
}

// MergeStreamFieldValues 合并多 Worker 的同维度取值分布。
//
// 按 value 相加；任一来源 Complete=false ⇒ 结果 Complete=false。
func MergeStreamFieldValues(perWorker []WorkerStreamFieldValuesResponse) ([]FacetValue, bool) {
	counts := make(map[string]uint64)
	complete := true
	for _, resp := range perWorker {
		if !resp.Complete {
			complete = false
		}
		for _, v := range resp.Values {
			counts[v.Value] += v.Count
		}
	}
	out := make([]FacetValue, 0, len(counts))
	for value, count := range counts {
		out = append(out, FacetValue{Value: value, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out, complete
}

// Streams 枚举窗内的流并按 (源, 流) 合并。
//
// # 与 Facets 的关系
//
// 两者都能回答「有哪些源」：Facets(dimensions=["log_source_id"]) 按文档字段聚合，
// Streams 按 VL 的 stream 索引聚合。差别是 Streams 一次请求就能同时拿到
// 「源 + 流名 + 条数」，而 Facets 每个维度一次、且流名要单独再查一次。
//
// # Complete 的口径
//
// 取三条交集的**保守方向**：
//   - 任一 Worker 报 Complete=false ⇒ false（条数不可信）；
//   - 覆盖不完整（有 partial target）⇒ false（没查到的源的条数当然不可信）；
//   - quality 非 exact ⇒ false（Worker 自报质量不确定）。
//
// 三条都是「宁可说不可信」：把 false 说成 true 的后果是前端把截断归零当成
// 「这些源没数据」，而反过来只会多一个提示。
func (c *Coordinator) Streams(ctx context.Context, q StreamsQuery) (*StreamsResponse, error) {
	if q.Budget.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(q.Budget.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	view, resolved, err := c.resolveForQuery(ctx, q.Query)
	if err != nil {
		return nil, err
	}
	if c.dialer == nil {
		return nil, errNoDialer
	}

	covBy := make(map[string]TargetCoverage)
	quality := Quality{DuplicateQuality: DupExact, StatsQuality: StatsQExact}
	workerCoverage := Coverage{Complete: true, EnumerationState: EnumExhausted}
	order, byWorker := resolved.ByWorker()

	var perWorker []WorkerStreamsResponse
	reserved := reserveWorkerBudgets(order, q.Budget)
	for _, workerID := range order {
		targets := byWorker[workerID]
		local, allowed := reserved[workerID]
		if !allowed || ctx.Err() != nil {
			markTargetsPartial(covBy, targets, "BUDGET_EXCEEDED")
			continue
		}
		workerView, viewErr, viewOK := workerViewID(view, workerID)
		if !viewOK {
			markTargetsPartial(covBy, targets, "worker_view_failed", viewErr)
			continue
		}
		targetIDs := make([]string, 0, len(targets))
		for _, target := range targets {
			targetIDs = append(targetIDs, workerTargetID(target))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		if dialErr != nil {
			markTargetsPartial(covBy, targets, "streams_failed", dialErr.Error())
			continue
		}
		resp, callErr := client.Streams(ctx, WorkerSearchRequest{TargetIDs: targetIDs, FromUTC: view.FromUTC,
			ToUTC: view.ToUTC, Filter: view.Filter, Budget: local, ViewID: workerView, OrderVersion: view.OrderVersion})
		if callErr != nil || resp == nil || resp.Unsupported || resp.Error != "" {
			// **不能只写一句 streams_failed**：覆盖里的 reason 是运维排查的唯一线索，
			// 而「哪个环节失败」恰好是最关键的部分（拨号/传输/不支持/Worker 内部错）。
			// 原实现把 callErr 与 resp.Error 全丢了，2026-11-05 真机验证时就因此
			// 只能看到「streams_failed」，完全无法定位。
			reason := "streams_failed"
			switch {
			case callErr != nil:
				reason = "streams_failed"
			case resp == nil:
				reason = "streams_failed_empty_response"
			case resp.Unsupported:
				reason = "streams_unsupported"
			case resp.Error != "":
				reason = "streams_failed"
			}
			detail := ""
			if callErr != nil {
				detail = callErr.Error()
			} else if resp != nil {
				detail = resp.Error
			}
			markTargetsPartial(covBy, targets, reason, detail)
			continue
		}
		mergeWorkerCoverage(&workerCoverage, resp.Coverage, false)
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		got := make(map[string]WorkerTargetResult)
		for _, tr := range resp.Targets {
			got[tr.TargetID] = tr
		}
		for _, target := range targets {
			tr, ok := got[workerTargetID(target)]
			if !ok {
				covBy[target.ID] = absentTargetCoverage(target, resp.Targets)
				continue
			}
			covBy[target.ID] = TargetCoverage{
				TargetID:          target.ID,
				State:             tr.State,
				Reasons:           append([]string(nil), tr.Reasons...),
				ClosedVisibleSeq:  tr.ClosedVisibleSeq,
				CatalogGeneration: tr.CatalogGeneration,
				WorkerID:          workerID,
				HistoricalHolder:  target.HistoricalHolder,
			}
		}
		perWorker = append(perWorker, *resp)
	}

	streams, complete := MergeStreamItems(perWorker)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	mergeWorkerCoverage(&cov, &workerCoverage, false)
	if !cov.Complete || cov.Pending || len(cov.PartialReasons) > 0 {
		// 没查到的源，其条数当然不可信。
		complete = false
	}
	if quality.StatsQuality != StatsQExact {
		complete = false
	}
	if err := c.persistClosedVisibleSeq(&view, cov); err != nil {
		return nil, err
	}

	return &StreamsResponse{
		View:     view,
		Coverage: cov,
		Quality:  quality,
		Streams:  streams,
		Complete: complete,
	}, nil
}

// StreamFieldNames 合并多 Worker 的 stream 标签维度名（取并集）。
//
// 取并集是安全方向：多给一个维度只会让前端多一个可选项，
// 少给会让用户筛不到。实测恒为 [log_source_id, stream]。
func (c *Coordinator) StreamFieldNames(ctx context.Context, q StreamsQuery) (*StreamFieldNamesResponse, error) {
	if q.Budget.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(q.Budget.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	view, resolved, err := c.resolveForQuery(ctx, q.Query)
	if err != nil {
		return nil, err
	}
	if c.dialer == nil {
		return nil, errNoDialer
	}

	covBy := make(map[string]TargetCoverage)
	workerCoverage := Coverage{Complete: true, EnumerationState: EnumExhausted}
	order, byWorker := resolved.ByWorker()
	fieldSet := make(map[string]struct{}, 4)
	reserved := reserveWorkerBudgets(order, q.Budget)
	for _, workerID := range order {
		targets := byWorker[workerID]
		local, allowed := reserved[workerID]
		if !allowed || ctx.Err() != nil {
			markTargetsPartial(covBy, targets, "BUDGET_EXCEEDED")
			continue
		}
		workerView, viewErr, viewOK := workerViewID(view, workerID)
		if !viewOK {
			markTargetsPartial(covBy, targets, "worker_view_failed", viewErr)
			continue
		}
		targetIDs := make([]string, 0, len(targets))
		for _, target := range targets {
			targetIDs = append(targetIDs, workerTargetID(target))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		if dialErr != nil {
			markTargetsPartial(covBy, targets, "stream_field_names_failed", dialErr.Error())
			continue
		}
		resp, callErr := client.StreamFieldNames(ctx, WorkerSearchRequest{TargetIDs: targetIDs, FromUTC: view.FromUTC,
			ToUTC: view.ToUTC, Filter: view.Filter, Budget: local, ViewID: workerView, OrderVersion: view.OrderVersion})
		if callErr != nil || resp == nil || resp.Unsupported || resp.Error != "" {
			markTargetsPartial(covBy, targets, "stream_field_names_failed")
			continue
		}
		mergeWorkerCoverage(&workerCoverage, resp.Coverage, false)
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		for _, field := range resp.Fields {
			if field != "" {
				fieldSet[field] = struct{}{}
			}
		}
		for _, target := range targets {
			got := make(map[string]WorkerTargetResult)
			for _, tr := range resp.Targets {
				got[tr.TargetID] = tr
			}
			tr, ok := got[workerTargetID(target)]
			if !ok {
				covBy[target.ID] = absentTargetCoverage(target, resp.Targets)
				continue
			}
			covBy[target.ID] = TargetCoverage{TargetID: target.ID, State: tr.State,
				Reasons: append([]string(nil), tr.Reasons...), ClosedVisibleSeq: tr.ClosedVisibleSeq,
				CatalogGeneration: tr.CatalogGeneration, WorkerID: workerID,
				HistoricalHolder: target.HistoricalHolder}
		}
	}

	fields := make([]string, 0, len(fieldSet))
	for field := range fieldSet {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	mergeWorkerCoverage(&cov, &workerCoverage, false)
	if err := c.persistClosedVisibleSeq(&view, cov); err != nil {
		return nil, err
	}
	return &StreamFieldNamesResponse{View: view, Coverage: cov, Fields: fields}, nil
}

// StreamFieldValues 合并多 Worker 的 stream 标签维度取值分布。
//
// 非法维度直接拒绝（与 Worker 侧同源判据）：
// 放行它会拿到空结果而**不报错**，前端看到「可选但永远没数据」。
func (c *Coordinator) StreamFieldValues(ctx context.Context, q StreamFieldValuesQuery) (*StreamFieldValuesResponse, error) {
	if !StreamLabelDimension(q.Field) {
		return nil, fmt.Errorf("logcoord: stream field %q 不是 stream 标签维度（只支持 log_source_id / stream）", q.Field)
	}
	if q.Budget.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(q.Budget.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	view, resolved, err := c.resolveForQuery(ctx, q.Query)
	if err != nil {
		return nil, err
	}
	if c.dialer == nil {
		return nil, errNoDialer
	}

	covBy := make(map[string]TargetCoverage)
	workerCoverage := Coverage{Complete: true, EnumerationState: EnumExhausted}
	order, byWorker := resolved.ByWorker()
	var perWorker []WorkerStreamFieldValuesResponse
	reserved := reserveWorkerBudgets(order, q.Budget)
	for _, workerID := range order {
		targets := byWorker[workerID]
		local, allowed := reserved[workerID]
		if !allowed || ctx.Err() != nil {
			markTargetsPartial(covBy, targets, "BUDGET_EXCEEDED")
			continue
		}
		workerView, viewErr, viewOK := workerViewID(view, workerID)
		if !viewOK {
			markTargetsPartial(covBy, targets, "worker_view_failed", viewErr)
			continue
		}
		targetIDs := make([]string, 0, len(targets))
		for _, target := range targets {
			targetIDs = append(targetIDs, workerTargetID(target))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		if dialErr != nil {
			markTargetsPartial(covBy, targets, "stream_field_values_failed", dialErr.Error())
			continue
		}
		resp, callErr := client.StreamFieldValues(ctx, WorkerSearchRequest{TargetIDs: targetIDs, FromUTC: view.FromUTC,
			ToUTC: view.ToUTC, Filter: view.Filter, Budget: local, ViewID: workerView, OrderVersion: view.OrderVersion}, q.Field)
		if callErr != nil || resp == nil || resp.Unsupported || resp.Error != "" {
			markTargetsPartial(covBy, targets, "stream_field_values_failed")
			continue
		}
		mergeWorkerCoverage(&workerCoverage, resp.Coverage, false)
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		got := make(map[string]WorkerTargetResult)
		for _, tr := range resp.Targets {
			got[tr.TargetID] = tr
		}
		for _, target := range targets {
			tr, ok := got[workerTargetID(target)]
			if !ok {
				covBy[target.ID] = absentTargetCoverage(target, resp.Targets)
				continue
			}
			covBy[target.ID] = TargetCoverage{TargetID: target.ID, State: tr.State,
				Reasons: append([]string(nil), tr.Reasons...), ClosedVisibleSeq: tr.ClosedVisibleSeq,
				CatalogGeneration: tr.CatalogGeneration, WorkerID: workerID,
				HistoricalHolder: target.HistoricalHolder}
		}
		perWorker = append(perWorker, *resp)
	}

	values, complete := MergeStreamFieldValues(perWorker)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	mergeWorkerCoverage(&cov, &workerCoverage, false)
	if !cov.Complete || cov.Pending || len(cov.PartialReasons) > 0 {
		complete = false
	}
	if err := c.persistClosedVisibleSeq(&view, cov); err != nil {
		return nil, err
	}
	return &StreamFieldValuesResponse{
		View:     view,
		Coverage: cov,
		Field:    q.Field,
		Values:   values,
		Complete: complete,
	}, nil
}
