package query

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

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

// Service 是 FR-478 查询面 foundation：消费 planner ranges + budget。
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

// Search 执行查询面 Search stub。
func (s *Service) Search(ctx context.Context, req QueryRequest) SearchResponse {
	resp := SearchResponse{RequestID: req.RequestID}
	plan := s.planFromRequest(req)
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
		items     []logtypes.Event
		usedBytes uint64
		fanout    uint32
		cancelled bool
		budgetHit bool
	)
	cursorKey := decodeCursorSort(req.View)

	for _, rng := range plan.Ranges {
		if err := ctx.Err(); err != nil {
			cancelled = true
			break
		}
		if fanoutLimit > 0 && fanout >= fanoutLimit {
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
			budgetHit = true
			break
		}
		fanout++

		q := RangeQuery{
			RequestID:        req.RequestID,
			TimeRange:        req.TimeRange,
			Filter:           req.Filter,
			Budget:           req.Budget,
			ClosedVisibleSeq: rng.ClosedVisibleSeq,
			CursorSortKey:    cursorKey,
		}
		rres, err := s.client.Search(ctx, rng, q)
		if err != nil {
			if errors.Is(err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, "range client search unimplemented")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				resp.Coverage = plan.Coverage
				return resp
			}
			if errors.Is(err, context.Canceled) {
				cancelled = true
				break
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			for i := range plan.Coverage.Targets {
				if plan.Coverage.Targets[i].TargetID == rng.TargetID {
					plan.Coverage.Targets[i].State = CoverageNotReady
					plan.Coverage.Targets[i].Reasons = appendUniqueStr(plan.Coverage.Targets[i].Reasons, ReasonRangeUnavailable)
				}
			}
			continue
		}
		for _, r := range rres.CoverageReasons {
			plan.Coverage.MarkIncomplete(r)
		}
		if rres.DuplicateQuality != "" {
			resp.Quality.DuplicateQuality = rres.DuplicateQuality
		}
		if rres.StatsQuality != "" {
			resp.Quality.StatsQuality = rres.StatsQuality
		}
		items = append(items, rres.Items...)
		usedBytes += rres.Bytes
		if req.Budget.MaxBytes > 0 && usedBytes > req.Budget.MaxBytes {
			budgetHit = true
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
			break
		}
	}

	SortEvents(items)

	// cursor 过滤：丢弃 sort key <= cursor 的事件（逻辑分页）。
	if cursorKey != nil {
		items = filterAfterCursor(items, *cursorKey)
	}

	var next string
	exhausted := true
	if uint32(len(items)) > limit {
		items = items[:limit]
		exhausted = false
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
		resp.Quality = plan.Quality
	}
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
	var (
		points    []StatsPoint
		usedBytes uint64
		fanout    uint32
	)

	for _, rng := range plan.Ranges {
		if err := ctx.Err(); err != nil {
			plan.Coverage.MarkIncomplete(ReasonCancelled)
			resp.Err = newErr(ErrCodeCancelled, "stats cancelled")
			resp.Coverage = plan.Coverage
			return resp
		}
		if fanoutLimit > 0 && fanout >= fanoutLimit {
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
			break
		}
		fanout++
		q := RangeQuery{
			RequestID:        req.RequestID,
			TimeRange:        req.TimeRange,
			Filter:           req.Filter,
			Budget:           req.Budget,
			ClosedVisibleSeq: rng.ClosedVisibleSeq,
			GroupBy:          append([]string(nil), req.GroupBy...),
			TimeBucket:       req.TimeBucket,
			MetricField:      req.MetricField,
		}
		sres, err := s.client.Stats(ctx, rng, q)
		if err != nil {
			if errors.Is(err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, "range client stats unimplemented")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				resp.Coverage = plan.Coverage
				return resp
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			continue
		}
		for _, r := range sres.CoverageReasons {
			plan.Coverage.MarkIncomplete(r)
		}
		if sres.StatsQuality != "" {
			resp.Quality.StatsQuality = sres.StatsQuality
		}
		points = mergeStatsPoints(points, sres.Points)
		usedBytes += sres.Bytes
		if req.Budget.MaxBytes > 0 && usedBytes > req.Budget.MaxBytes {
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
			resp.Quality.StatsQuality = StatsPartial
			break
		}
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
	var used uint64
	for _, rng := range plan.Ranges {
		if err := ctx.Err(); err != nil {
			resp.Err = newErr(ErrCodeCancelled, "fields cancelled")
			plan.Coverage.MarkIncomplete(ReasonCancelled)
			break
		}
		res, err := client.Fields(ctx, rng, RangeQuery{
			RequestID: req.RequestID, TimeRange: req.TimeRange, Filter: req.Filter,
			Budget: req.Budget, ClosedVisibleSeq: rng.ClosedVisibleSeq,
		})
		if err != nil {
			if errors.Is(err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, "fields unimplemented on this worker")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				break
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			continue
		}
		for _, reason := range res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		for _, field := range res.Fields {
			if streamFieldAllowed(field) {
				fieldSet[field] = struct{}{}
			}
		}
		used += res.Bytes
		if res.Truncated || (req.Budget.MaxBytes > 0 && used > req.Budget.MaxBytes) {
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		}
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
	var used uint64
	for _, rng := range plan.Ranges {
		if err := ctx.Err(); err != nil {
			resp.Err = newErr(ErrCodeCancelled, "facets cancelled")
			plan.Coverage.MarkIncomplete(ReasonCancelled)
			break
		}
		res, err := client.Facets(ctx, rng, RangeQuery{
			RequestID: req.RequestID, TimeRange: req.TimeRange, Filter: req.Filter,
			Budget: req.Budget, ClosedVisibleSeq: rng.ClosedVisibleSeq,
		}, controlled, limit)
		if err != nil {
			if errors.Is(err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, "facets unimplemented on this worker")
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				break
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			continue
		}
		for _, reason := range res.CoverageReasons {
			plan.Coverage.MarkIncomplete(reason)
		}
		for _, value := range res.Values {
			if _, ok := allowed[value.Dimension]; !ok {
				continue
			}
			counts[value.Dimension+"\x00"+value.Value] += value.Count
		}
		used += res.Bytes
		if res.Truncated || (req.Budget.MaxBytes > 0 && used > req.Budget.MaxBytes) {
			resp.Truncated = true
			plan.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		}
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
	cov.Complete = true
	cov.PartialReasons = nil
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
	for _, rng := range plan.Ranges {
		if err := ctx.Err(); err != nil {
			plan.Coverage.MarkIncomplete(ReasonCancelled)
			resp.Err = newErr(ErrCodeCancelled, "tail cancelled")
			resp.Coverage = plan.Coverage
			return resp
		}
		q := RangeQuery{
			RequestID:        req.RequestID,
			TimeRange:        req.TimeRange,
			Filter:           req.Filter,
			Budget:           req.Budget,
			ClosedVisibleSeq: rng.ClosedVisibleSeq,
		}
		rres, err := s.client.Tail(ctx, rng, q, mode)
		if err != nil {
			if errors.Is(err, ErrRangeClientUnimplemented) {
				resp.Err = newErr(ErrCodeUnsupported, fmt.Sprintf("tail mode %s unimplemented", mode))
				plan.Coverage.MarkIncomplete(ReasonUnsupported)
				resp.Coverage = plan.Coverage
				return resp
			}
			plan.Coverage.MarkIncomplete(ReasonRangeUnavailable)
			continue
		}
		items = append(items, rres.Items...)
	}
	// VIEW_BOUNDED 使用与 Search 相同的稳定排序（固定视图尾部）。
	if mode == TailViewBounded {
		SortEvents(items)
	}
	if uint32(len(items)) > limit {
		items = items[:limit]
		resp.Exhausted = false
	} else {
		resp.Exhausted = true
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
