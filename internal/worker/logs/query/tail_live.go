package query

import (
	"context"
	"errors"
	"time"
)

// followLive is a bounded observation of the changing published Catalog.
// Each cycle still reads only verified generations and closed prefixes.
func (s *Service) followLive(ctx context.Context, req QueryRequest) TailResponse {
	resp := TailResponse{RequestID: req.RequestID, Mode: TailFollowLive}
	liveClient, ok := s.client.(FollowLiveRangeClient)
	if !ok {
		resp.Err = newErr(ErrCodeUnsupported, "FOLLOW_LIVE unimplemented")
		resp.Coverage.MarkIncomplete(ReasonUnsupported)
		return resp
	}
	window := time.Second
	if req.Budget.TimeoutMS > 0 {
		window = time.Duration(req.Budget.TimeoutMS) * time.Millisecond
	}
	if window > 30*time.Second {
		window = 30 * time.Second
	}
	workCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	seen := make(map[string]string)
	limit := int(req.Budget.EffectiveLimit())
	maxBytes := req.Budget.MaxBytes
	if maxBytes == 0 {
		maxBytes = 32 << 20
	}
	var used uint64
	first := true
	for {
		if ctx.Err() != nil {
			resp.Err = newErr(ErrCodeCancelled, "live tail cancelled")
			resp.Coverage.MarkIncomplete(ReasonCancelled)
			return resp
		}
		view := req.View
		if !first {
			view = nil
		}
		plan := s.planner.Plan(PlanRequest{Transient: true, RequestID: req.RequestID, TargetIDs: req.AuthorizedTargets,
			TimeRange: TimeRange{FromUTC: req.TimeRange.FromUTC}, Budget: req.Budget, ViewRef: view,
			RequireCold: req.RequireCold, RequireArchive: req.RequireArchive})
		resp.Coverage, resp.Quality = plan.Coverage, plan.Quality
		resp.Coverage.EnumerationState = EnumOpen
		remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
		if first && plan.View != nil {
			resp.View = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
		}
		first = false
		if plan.Err != nil {
			resp.Err = plan.Err
			return resp
		}
		if !resp.Coverage.Complete {
			return resp
		}
		for i, rng := range plan.Ranges {
			if req.Budget.MaxFanout > 0 && uint32(i) >= req.Budget.MaxFanout {
				resp.Coverage.MarkIncomplete(ReasonBudgetExceeded)
				return resp
			}
			budget := req.Budget
			budget.MaxBytes = maxBytes - used
			result, err := liveClient.FollowLive(workCtx, rng, RangeQuery{RequestID: req.RequestID,
				TimeRange: TimeRange{FromUTC: req.TimeRange.FromUTC}, Filter: req.Filter, Budget: budget, ClosedVisibleSeq: rng.ClosedVisibleSeq})
			if err != nil {
				if ctx.Err() != nil {
					resp.Err = newErr(ErrCodeCancelled, "live tail cancelled")
					resp.Coverage.MarkIncomplete(ReasonCancelled)
				} else {
					if errors.Is(err, ErrRangeClientUnimplemented) {
						resp.Err = newErr(ErrCodeUnsupported, "live search unimplemented")
					}
					resp.Coverage.MarkIncomplete(ReasonRangeUnavailable)
				}
				return resp
			}
			used += result.Bytes
			for _, reason := range result.CoverageReasons {
				resp.Coverage.MarkIncomplete(reason)
			}
			if used >= maxBytes {
				resp.Coverage.MarkIncomplete(ReasonBudgetExceeded)
				return resp
			}
			for _, event := range result.Items {
				if hash, ok := seen[event.EventID]; ok {
					if hash != event.CanonicalHash {
						resp.Quality.DuplicateQuality = DupConflict
						resp.Coverage.MarkIncomplete(ReasonProjectionIncomplete)
						return resp
					}
					continue
				}
				seen[event.EventID] = event.CanonicalHash
				resp.Items = append(resp.Items, event)
				if len(resp.Items) >= limit {
					return resp
				}
			}
			if !resp.Coverage.Complete {
				return resp
			}
		}
		select {
		case <-ctx.Done():
			resp.Err = newErr(ErrCodeCancelled, "live tail cancelled")
			resp.Coverage.MarkIncomplete(ReasonCancelled)
			return resp
		case <-workCtx.Done():
			if ctx.Err() != nil {
				resp.Err = newErr(ErrCodeCancelled, "live tail cancelled")
				resp.Coverage.MarkIncomplete(ReasonCancelled)
			}
			return resp
		case <-time.After(200 * time.Millisecond):
		}
	}
}
