package query

import (
	"context"
	"errors"
	"time"
)

// followLive 对变化中的已发布集合做有界观测，每轮独立短租约，不登记无限固定视图。
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
		maxBytes = DefaultMaxBytes
	}
	var used uint64
	first := true
	for {
		if ctx.Err() != nil {
			resp.Err = newErr(ErrCodeCancelled, "live tail cancelled")
			resp.Coverage.MarkIncomplete(ReasonCancelled)
			return resp
		}
		if err := s.foregroundAdmission(); err != nil {
			resp.Err = err
			markReadError(&resp.Coverage, err)
			return resp
		}
		cycleReq := req
		if !first {
			cycleReq.View = nil
		}
		plan := s.planner.Plan(PlanRequest{Transient: true, RequestID: req.RequestID, TargetIDs: req.AuthorizedTargets,
			TimeRange: TimeRange{FromUTC: req.TimeRange.FromUTC}, Budget: req.Budget, ViewRef: cycleReq.View,
			RequireCold: req.RequireCold, RequireArchive: req.RequireArchive})
		finished := func() bool {
			resp.Coverage, resp.Quality = plan.Coverage, plan.Quality
			resp.Coverage.EnumerationState = EnumOpen
			remapCoverageTargets(&resp.Coverage, req.AuthorizedTargets)
			if first && plan.View != nil {
				resp.View = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
			}
			first = false
			if plan.Err != nil {
				resp.Err = plan.Err
				return true
			}
			finishRead, readErr := s.beginRead(workCtx, cycleReq, &plan)
			if readErr != nil {
				resp.Err = readErr
				markReadError(&resp.Coverage, readErr)
				return true
			}
			// 本轮所有真实调用结束才归还 pin，下一轮不得累积旧 view 租约。
			defer finishRead()
			if !resp.Coverage.Complete {
				return true
			}
			for i, rng := range plan.Ranges {
				if req.Budget.MaxFanout > 0 && uint32(i) >= req.Budget.MaxFanout {
					resp.Coverage.MarkIncomplete(ReasonBudgetExceeded)
					return true
				}
				budget := req.Budget
				budget.MaxBytes = maxBytes - used
				readCtx, unpin, pinErr := s.pinRange(workCtx, &plan)
				if pinErr != nil {
					resp.Err = newErr(ErrCodeViewStale, pinErr.Error())
					markReadError(&resp.Coverage, resp.Err)
					return true
				}
				result, err := liveClient.FollowLive(readCtx, rng, RangeQuery{RequestID: req.RequestID,
					TimeRange: TimeRange{FromUTC: req.TimeRange.FromUTC}, Filter: req.Filter, Budget: budget, ClosedVisibleSeq: rng.ClosedVisibleSeq})
				unpin()
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
					return true
				}
				used += result.Bytes
				for _, reason := range result.CoverageReasons {
					resp.Coverage.MarkIncomplete(reason)
				}
				if used >= maxBytes {
					resp.Coverage.MarkIncomplete(ReasonBudgetExceeded)
					return true
				}
				for _, event := range result.Items {
					if hash, ok := seen[event.EventID]; ok {
						if hash != event.CanonicalHash {
							resp.Quality.DuplicateQuality = DupConflict
							resp.Coverage.MarkIncomplete(ReasonProjectionIncomplete)
							return true
						}
						continue
					}
					seen[event.EventID] = event.CanonicalHash
					resp.Items = append(resp.Items, event)
					if len(resp.Items) >= limit {
						return true
					}
				}
				if !resp.Coverage.Complete {
					return true
				}
			}
			return false
		}()
		if finished {
			return resp
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
