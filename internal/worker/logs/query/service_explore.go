package query

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

const (
	ExploreMaxFields             = 256
	ExploreMaxFieldLength        = 128
	ExploreMaxFacetLimit  uint32 = 20
	ExploreMaxBytes       uint64 = 1 << 20
	ExploreMaxTimeoutMS   uint32 = 5000
	ExploreMaxFanout      uint32 = 16
)

var exploreFieldSyntax = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// Document discovery is an explicit, bounded operation. This policy is separate
// from streamFieldAllowed and never changes stream-label identity.
func explorableField(field string) bool {
	if len(field) == 0 || len(field) > ExploreMaxFieldLength || !exploreFieldSyntax.MatchString(field) {
		return false
	}
	switch field {
	case "_time", "_msg", "_stream", "_stream_id", "message", "event_id",
		"canonical_content_hash", "source_generation", "record_start", "record_end",
		"event_time", "event_time_utc", "ingest_time", "ingest_time_utc",
		"projection_generation", "storage_namespace", "closed_visible_seq", "catalog_generation":
		return false
	}
	return true
}

func explorationBudget(b Budget) Budget {
	if b.MaxBytes == 0 || b.MaxBytes > ExploreMaxBytes {
		b.MaxBytes = ExploreMaxBytes
	}
	if b.TimeoutMS == 0 || b.TimeoutMS > ExploreMaxTimeoutMS {
		b.TimeoutMS = ExploreMaxTimeoutMS
	}
	if b.MaxFanout == 0 || b.MaxFanout > ExploreMaxFanout {
		b.MaxFanout = ExploreMaxFanout
	}
	return b
}

func finiteExplorationWindow(r TimeRange) bool {
	from, e1 := time.Parse(time.RFC3339Nano, r.FromUTC)
	to, e2 := time.Parse(time.RFC3339Nano, r.ToUTC)
	return e1 == nil && e2 == nil && !from.IsZero() && !to.IsZero() && from.Before(to)
}

type metadataTransportBudgetKey struct{}

// Add a child cap while charging any existing parent ledger too. A metadata
// preflight must not get a fresh byte/fanout allowance before the value query.
func explorationReadContext(ctx context.Context, b Budget) context.Context {
	parent, _ := ctx.Value(physicalBudgetKey{}).(*physicalReadBudget)
	ledger := &physicalReadBudget{remaining: b.MaxBytes, fanout: b.MaxFanout, parent: parent}
	budgetCtx := context.WithValue(ctx, physicalBudgetKey{}, ledger)
	transportCtx := context.WithValue(budgetCtx, metadataTransportBudgetKey{}, true)
	return vlsup.WithMetadataReadBudget(transportCtx, func() (uint64, func(uint64), error) {
		if err := budgetCtx.Err(); err != nil {
			return 0, func(uint64) {}, err
		}
		return ledger.reserve(b.MaxBytes)
	})
}

type explorationResult struct {
	view      *ViewRef
	coverage  Coverage
	fields    []string
	values    []FacetValue
	truncated bool
	err       *QueryError
}

func (s *Service) exploreFields(ctx context.Context, req QueryRequest) FieldsResponse {
	r := s.exploreDocumentFields(ctx, req, "", 0)
	return FieldsResponse{ExploreApplied: true, RequestID: req.RequestID, View: r.view, Coverage: r.coverage, Fields: r.fields, Err: r.err}
}

func (s *Service) exploreFacets(ctx context.Context, req QueryRequest, dimensions []string, limit uint32) FacetsResponse {
	if len(dimensions) != 1 || limit == 0 || limit > ExploreMaxFacetLimit {
		return FacetsResponse{ExploreApplied: true, RequestID: req.RequestID, Coverage: Coverage{PartialReasons: []string{ReasonUnsupported}, EnumerationState: EnumOpen},
			Err: newErr(ErrCodeUnsupported, "field exploration requires exactly one dimension and dimensionLimit in [1,20]")}
	}
	if !explorableField(dimensions[0]) {
		return FacetsResponse{ExploreApplied: true, RequestID: req.RequestID, Coverage: Coverage{PartialReasons: []string{ReasonUnsupported}, EnumerationState: EnumOpen},
			Err: newErr(ErrCodeUnsupported, "invalid or reserved exploration field")}
	}
	r := s.exploreDocumentFields(ctx, req, dimensions[0], limit)
	return FacetsResponse{ExploreApplied: true, RequestID: req.RequestID, View: r.view, Coverage: r.coverage, Values: r.values, Truncated: r.truncated, Err: r.err}
}

// Both operations use one fixed plan, absolute deadline and physical ledger.
// Facets only reads values from ranges whose scoped field_names proved presence.
func (s *Service) exploreDocumentFields(ctx context.Context, req QueryRequest, dimension string, limit uint32) (resp explorationResult) {
	req.Budget = explorationBudget(req.Budget)
	ctx, cancel := WithQueryDeadline(ctx, req.Budget)
	defer cancel()
	ctx = explorationReadContext(ctx, req.Budget)
	ctx = vlsup.WithFieldNamesValidation(ctx, vlsup.FieldNamesValidation{Allowed: explorableField,
		MaxFields: ExploreMaxFields, OnlyField: dimension, Truncated: resp.budgetCut})
	if len(req.AuthorizedTargets) == 0 {
		resp.err = newErr(ErrCodeUnauthorized, "field exploration requires explicit authorized targets")
		resp.coverage = Coverage{PartialReasons: []string{ReasonUnauthorizedTarget}, EnumerationState: EnumOpen}
		return resp
	}
	if (req.View == nil || req.View.ViewID == "") && !finiteExplorationWindow(req.TimeRange) {
		resp.err = newErr(ErrCodeUnsupported, "field exploration requires a finite absolute from/to time range")
		resp.coverage = Coverage{PartialReasons: []string{ReasonUnsupported}, EnumerationState: EnumOpen}
		return resp
	}
	plan := s.planForRead(ctx, req)
	resp.coverage = plan.Coverage
	if plan.View != nil {
		resp.view = &ViewRef{ViewID: plan.View.ViewID, OrderVersion: plan.View.OrderVersion}
	}
	if plan.Err != nil {
		resp.err = plan.Err
		return resp
	}
	finish, readErr := s.beginRead(ctx, req, &plan)
	if readErr != nil {
		resp.err = readErr
		markReadError(&resp.coverage, readErr)
		return resp
	}
	defer func() {
		finish()
		if resp.err != nil && (req.View == nil || req.View.ViewID == "") {
			logLeaseCleanupError(s.planner.ReleaseView(viewIDOf(plan.View, req.View)))
		}
	}()
	defer func() {
		if ctx.Err() != nil {
			resp.readFailure(ctx.Err())
		} else {
			plan.Coverage = resp.coverage
			s.revalidateRead(ctx, &plan, &resp.err)
			resp.coverage = plan.Coverage
		}
		remapCoverageTargets(&resp.coverage, req.AuthorizedTargets)
	}()
	if !finiteExplorationWindow(plan.View.TimeRange) {
		resp.err = newErr(ErrCodeUnsupported, "field exploration requires a finite frozen View time range")
		resp.coverage.MarkIncomplete(ReasonUnsupported)
		return resp
	}
	if req.Filter != plan.View.Filter ||
		((req.TimeRange.FromUTC != "" || req.TimeRange.ToUTC != "") && req.TimeRange != plan.View.TimeRange) {
		resp.err = newErr(ErrCodeViewStale, "field exploration View time range or filter mismatch")
		markReadError(&resp.coverage, resp.err)
		return resp
	}
	// Reused Views may omit from/to, but must still use the frozen finite window.
	req.TimeRange = plan.View.TimeRange
	fieldsClient, ok := s.client.(FieldsRangeClient)
	if !ok {
		resp.readFailure(ErrRangeClientUnimplemented)
		return resp
	}
	facetsClient, facetsOK := s.client.(FacetsRangeClient)
	if dimension != "" && !facetsOK {
		resp.readFailure(ErrRangeClientUnimplemented)
		return resp
	}
	fieldSet := make(map[string]struct{})
	counts := make(map[string]uint64)
	found := false
	remaining := req.Budget.MaxBytes
	launch := budgetRangeCount(req.Budget, &resp.coverage, len(plan.Ranges))
	for _, rng := range plan.Ranges[:launch] {
		if remaining == 0 {
			resp.budgetCut()
			break
		}
		readCtx, unpin, pinErr := s.pinRange(ctx, &plan)
		if pinErr != nil {
			resp.readFailure(pinErr)
			break
		}
		q := RangeQuery{RequestID: req.RequestID, TimeRange: req.TimeRange, Filter: req.Filter,
			Budget: req.Budget, ClosedVisibleSeq: rng.ClosedVisibleSeq}
		q.Budget.MaxBytes = remaining
		fields, err := fieldsClient.Fields(readCtx, rng, q)
		remaining = explorationRemaining(remaining, fields.Bytes)
		if err != nil {
			unpin()
			if resp.readFailure(err) {
				break
			}
			continue
		}
		for _, reason := range fields.CoverageReasons {
			resp.coverage.MarkIncomplete(reason)
		}
		if fields.Truncated {
			resp.budgetCut()
		}
		present := false
		sort.Strings(fields.Fields)
		for _, field := range fields.Fields {
			if !explorableField(field) {
				continue
			}
			if field == dimension {
				present = true
				found = true
			}
			if dimension == "" {
				if _, seen := fieldSet[field]; !seen && len(fieldSet) >= ExploreMaxFields {
					resp.budgetCut()
					continue
				}
				fieldSet[field] = struct{}{}
			}
		}
		if dimension == "" || !present {
			unpin()
			continue
		}
		if remaining == 0 {
			unpin()
			resp.budgetCut()
			break
		}
		q.Budget.MaxBytes = remaining
		facets, err := facetsClient.Facets(readCtx, rng, q, []string{dimension}, limit)
		unpin()
		remaining = explorationRemaining(remaining, facets.Bytes)
		for _, reason := range facets.CoverageReasons {
			resp.coverage.MarkIncomplete(reason)
		}
		if facets.Truncated {
			resp.budgetCut()
		}
		if err != nil {
			if resp.readFailure(err) {
				break
			}
			continue
		}
		for index, value := range facets.Values {
			if uint32(index) >= limit {
				resp.budgetCut()
				break
			}
			if value.Dimension == dimension {
				counts[value.Value] += value.Count
			}
		}
	}
	if launch < len(plan.Ranges) {
		resp.budgetCut()
	}
	for field := range fieldSet {
		resp.fields = append(resp.fields, field)
	}
	sort.Strings(resp.fields)
	if dimension != "" && !found && resp.err == nil {
		if resp.coverage.Complete {
			resp.err = newErr(ErrCodeUnsupported, fmt.Sprintf("field %q is not present in this View/filter/time range", dimension))
			resp.coverage.MarkIncomplete(ReasonUnsupported)
		} else {
			resp.err = newErr(ErrCodePartial, "field presence could not be verified within the exploration budget")
		}
	}
	for value, count := range counts {
		resp.values = append(resp.values, FacetValue{Dimension: dimension, Value: value, Count: count})
	}
	sort.Slice(resp.values, func(i, j int) bool {
		if resp.values[i].Count != resp.values[j].Count {
			return resp.values[i].Count > resp.values[j].Count
		}
		return resp.values[i].Value < resp.values[j].Value
	})
	if limit > 0 && uint32(len(resp.values)) > limit {
		resp.values = resp.values[:limit]
		resp.budgetCut()
	}
	return resp
}

func explorationRemaining(remaining, used uint64) uint64 {
	if used >= remaining {
		return 0
	}
	return remaining - used
}

func (r *explorationResult) budgetCut() {
	r.truncated = true
	r.coverage.MarkIncomplete(ReasonBudgetExceeded)
}

// Return true for errors that stop further physical reads. A failed metadata
// response never becomes a successful empty field or facet result.
func (r *explorationResult) readFailure(err error) bool {
	switch {
	case errors.Is(err, catalog.ErrQueryViewStale):
		r.err = newErr(ErrCodeViewStale, err.Error())
		markReadError(&r.coverage, r.err)
	case errors.Is(err, ErrRangeQueryBudget), errors.Is(err, vlsup.ErrMetadataReadBudget), errors.Is(err, context.DeadlineExceeded):
		r.err = newErr(ErrCodeBudgetExceeded, err.Error())
		r.budgetCut()
	case errors.Is(err, context.Canceled):
		r.err = newErr(ErrCodeCancelled, err.Error())
		r.coverage.MarkIncomplete(ReasonCancelled)
		r.coverage.EnumerationState = EnumCancelled
	case errors.Is(err, ErrRangeClientUnimplemented):
		r.err = newErr(ErrCodeUnsupported, "document-field exploration unimplemented on this worker")
		r.coverage.MarkIncomplete(ReasonUnsupported)
	default:
		r.err = newErr(ErrCodePartial, err.Error())
		r.coverage.MarkIncomplete(ReasonRangeUnavailable)
		return false
	}
	return true
}
