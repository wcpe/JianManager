package grpcmap

import (
	"strconv"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// OrderVersion 是 wire 侧冻结排序版本；始终等于 query.SortVersion。
const OrderVersion = query.SortVersion

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// ErrorCodeToProto 映射 query.ErrorCode → workerpb.LogErrorCode。
func ErrorCodeToProto(c query.ErrorCode) workerpb.LogErrorCode {
	if v, ok := workerpb.LogErrorCode_value[string(c)]; ok {
		return workerpb.LogErrorCode(v)
	}
	return workerpb.LogErrorCode_LOG_ERROR_UNSPECIFIED
}

// ErrorCodeFromProto 映射 workerpb.LogErrorCode → query.ErrorCode。
func ErrorCodeFromProto(c workerpb.LogErrorCode) query.ErrorCode {
	if name, ok := workerpb.LogErrorCode_name[int32(c)]; ok {
		return query.ErrorCode(name)
	}
	return query.ErrCodeUnspecified
}

// CoverageStateToProto 映射 coverage state。
func CoverageStateToProto(s query.CoverageState) workerpb.LogCoverageState {
	if v, ok := workerpb.LogCoverageState_value[string(s)]; ok {
		return workerpb.LogCoverageState(v)
	}
	return workerpb.LogCoverageState_LOG_COVERAGE_STATE_UNSPECIFIED
}

// EnumerationStateToProto 映射 enumeration state。
func EnumerationStateToProto(s query.EnumerationState) workerpb.LogEnumerationState {
	if v, ok := workerpb.LogEnumerationState_value[string(s)]; ok {
		return workerpb.LogEnumerationState(v)
	}
	return workerpb.LogEnumerationState_LOG_ENUMERATION_OPEN
}

// DuplicateQualityToProto 映射 duplicate quality。
func DuplicateQualityToProto(q query.DuplicateQuality) workerpb.LogDuplicateQuality {
	if v, ok := workerpb.LogDuplicateQuality_value[string(q)]; ok {
		return workerpb.LogDuplicateQuality(v)
	}
	return workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_UNSPECIFIED
}

// StatsQualityToProto 映射 stats quality。
func StatsQualityToProto(q query.StatsQuality) workerpb.LogStatsQuality {
	if v, ok := workerpb.LogStatsQuality_value[string(q)]; ok {
		return workerpb.LogStatsQuality(v)
	}
	return workerpb.LogStatsQuality_LOG_QUALITY_STATS_UNSPECIFIED
}

// TailModeToProto 映射 tail mode；空值视为 VIEW_BOUNDED。
func TailModeToProto(m query.TailMode) workerpb.LogTailMode {
	switch m {
	case query.TailFollowLive:
		return workerpb.LogTailMode_LOG_TAIL_FOLLOW_LIVE
	case query.TailViewBounded:
		return workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED
	case "":
		return workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED
	default:
		return workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED
	}
}

// TailModeFromProto 映射 proto tail mode → query.TailMode。
func TailModeFromProto(m workerpb.LogTailMode) query.TailMode {
	switch m {
	case workerpb.LogTailMode_LOG_TAIL_FOLLOW_LIVE:
		return query.TailFollowLive
	case workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED:
		return query.TailViewBounded
	default:
		return query.TailViewBounded
	}
}

// ---------------------------------------------------------------------------
// Error / Coverage / Quality / Event
// ---------------------------------------------------------------------------

// QueryErrorToProto 映射结构化查询错误；nil 返回 nil。
func QueryErrorToProto(e *query.QueryError) *workerpb.LogError {
	if e == nil {
		return nil
	}
	return &workerpb.LogError{
		Code:         ErrorCodeToProto(e.Code),
		Message:      e.Message,
		Retryable:    e.Retryable,
		RetryAfterMs: e.RetryAfter,
	}
}

// QueryErrorFromProto 映射 proto LogError → query.QueryError；nil 返回 nil。
func QueryErrorFromProto(e *workerpb.LogError) *query.QueryError {
	if e == nil {
		return nil
	}
	return &query.QueryError{
		Code:       ErrorCodeFromProto(e.GetCode()),
		Message:    e.GetMessage(),
		Retryable:  e.GetRetryable(),
		RetryAfter: e.GetRetryAfterMs(),
	}
}

// CoverageTargetToProto 映射单目标 coverage。
// closed_visible_seq / catalog_generation 在 proto 侧是 string。
func CoverageTargetToProto(t query.TargetCoverage) *workerpb.LogCoverageTarget {
	return &workerpb.LogCoverageTarget{
		TargetId:          t.TargetID,
		State:             CoverageStateToProto(t.State),
		Reasons:           append([]string(nil), t.Reasons...),
		ClosedVisibleSeq:  formatUint(t.ClosedVisibleSeq),
		CatalogGeneration: formatUint(t.CatalogGeneration),
	}
}

// CoverageToProto 映射 Coverage 摘要。
func CoverageToProto(c query.Coverage) *workerpb.LogCoverage {
	out := &workerpb.LogCoverage{
		Complete:         c.Complete,
		PartialReasons:   append([]string(nil), c.PartialReasons...),
		EnumerationState: EnumerationStateToProto(c.EnumerationState),
		Targets:          make([]*workerpb.LogCoverageTarget, 0, len(c.Targets)),
	}
	for _, t := range c.Targets {
		out.Targets = append(out.Targets, CoverageTargetToProto(t))
	}
	return out
}

// QualityToProto 映射独立质量维度。
func QualityToProto(q query.Quality) *workerpb.LogQuality {
	return &workerpb.LogQuality{
		DuplicateQuality: DuplicateQualityToProto(q.DuplicateQuality),
		StatsQuality:     StatsQualityToProto(q.StatsQuality),
	}
}

// EventToProto 映射规范事件 → workerpb.LogEvent。
func EventToProto(ev logtypes.Event) *workerpb.LogEvent {
	return &workerpb.LogEvent{
		EventId:              ev.EventID,
		LogSourceId:          ev.Source.LogSourceID,
		SourceGeneration:     ev.Source.SourceGeneration,
		RecordStart:          formatUint(ev.Record.Start),
		RecordEnd:            formatUint(ev.Record.End),
		EventTimeUtc:         ev.EventTimeUTC,
		IngestTimeUtc:        ev.IngestTimeUTC,
		Level:                ev.Level,
		Stream:               ev.Stream,
		Message:              ev.Message,
		CanonicalContentHash: ev.CanonicalHash,
	}
}

// EventFromProto 反向映射 LogEvent → logtypes.Event。
func EventFromProto(ev *workerpb.LogEvent) logtypes.Event {
	if ev == nil {
		return logtypes.Event{}
	}
	return logtypes.Event{
		EventID:       ev.GetEventId(),
		Source:        logtypes.SourceIdentity{LogSourceID: ev.GetLogSourceId(), SourceGeneration: ev.GetSourceGeneration()},
		Record:        logtypes.RecordRange{Start: parseUint(ev.GetRecordStart()), End: parseUint(ev.GetRecordEnd())},
		EventTimeUTC:  ev.GetEventTimeUtc(),
		IngestTimeUTC: ev.GetIngestTimeUtc(),
		Level:         ev.GetLevel(),
		Stream:        ev.GetStream(),
		Message:       ev.GetMessage(),
		CanonicalHash: ev.GetCanonicalContentHash(),
	}
}

// EventsToProto 批量映射事件。
func EventsToProto(items []logtypes.Event) []*workerpb.LogEvent {
	if len(items) == 0 {
		return nil
	}
	out := make([]*workerpb.LogEvent, 0, len(items))
	for _, ev := range items {
		out = append(out, EventToProto(ev))
	}
	return out
}

// StatsPointToProto 映射统计点；AVG 以 sum/count 二次聚合。
func StatsPointToProto(p query.StatsPoint) *workerpb.LogStatsPoint {
	dims := p.Dimensions
	if dims != nil {
		cp := make(map[string]string, len(dims))
		for k, v := range dims {
			cp[k] = v
		}
		dims = cp
	}
	return &workerpb.LogStatsPoint{
		Dimensions: dims,
		Count:      p.Count,
		Sum:        p.Sum,
		Min:        p.Min,
		Max:        p.Max,
		Avg:        p.Avg(),
	}
}

// StatsPointsToProto 批量映射统计点。
func StatsPointsToProto(points []query.StatsPoint) []*workerpb.LogStatsPoint {
	if len(points) == 0 {
		return nil
	}
	out := make([]*workerpb.LogStatsPoint, 0, len(points))
	for _, p := range points {
		out = append(out, StatsPointToProto(p))
	}
	return out
}

// ---------------------------------------------------------------------------
// View / Request（proto → query 反向映射）
// ---------------------------------------------------------------------------

// ViewRefToProto 映射 query.ViewRef → proto；空 OrderVersion 回填 SortVersion。
func ViewRefToProto(v *query.ViewRef) *workerpb.LogQueryViewRef {
	if v == nil {
		return nil
	}
	ov := v.OrderVersion
	if ov == "" {
		ov = OrderVersion
	}
	return &workerpb.LogQueryViewRef{
		ViewId:       v.ViewID,
		Cursor:       v.Cursor,
		OrderVersion: ov,
	}
}

// ViewRefFromProto 反向映射；空 OrderVersion 统一为 query.SortVersion。
func ViewRefFromProto(v *workerpb.LogQueryViewRef) *query.ViewRef {
	if v == nil {
		return nil
	}
	ov := v.GetOrderVersion()
	if ov == "" {
		ov = OrderVersion
	}
	return &query.ViewRef{
		ViewID:       v.GetViewId(),
		Cursor:       v.GetCursor(),
		OrderVersion: ov,
	}
}

// TimeRangeFromProto 反向映射时间范围。
func TimeRangeFromProto(tr *workerpb.LogTimeRange) query.TimeRange {
	if tr == nil {
		return query.TimeRange{}
	}
	return query.TimeRange{FromUTC: tr.GetFromUtc(), ToUTC: tr.GetToUtc()}
}

// BudgetFromProto 反向映射查询预算。
func BudgetFromProto(b *workerpb.LogQueryBudget) query.Budget {
	if b == nil {
		return query.Budget{}
	}
	return query.Budget{
		Limit:     b.GetLimit(),
		MaxBytes:  b.GetMaxBytes(),
		TimeoutMS: b.GetTimeoutMs(),
		MaxFanout: b.GetMaxFanout(),
	}
}

// QueryRequestFromBase 反向映射 LogQueryRequestBase → query.QueryRequest。
func QueryRequestFromBase(qb *workerpb.LogQueryRequestBase) query.QueryRequest {
	if qb == nil {
		return query.QueryRequest{}
	}
	req := query.QueryRequest{
		RequestID:       qb.GetRequestId(),
		ProtocolVersion: qb.GetProtocolVersion(),
		TimeRange:       TimeRangeFromProto(qb.GetTimeRange()),
		Budget:          BudgetFromProto(qb.GetBudget()),
		View:            ViewRefFromProto(qb.GetView()),
		Filter:          qb.GetFilter(),
		PermissionScope: qb.GetPermissionScope(),
		CancelToken:     qb.GetCancellationToken(),
	}
	if at := qb.GetAuthorizedTargets(); at != nil {
		req.AuthorizedTargets = append([]string(nil), at.GetTargetIds()...)
	}
	return req
}

// CapabilitiesRequestFromProto 反向映射能力协商请求。
func CapabilitiesRequestFromProto(req *workerpb.GetLogCapabilitiesRequest) query.CapabilitiesRequest {
	if req == nil {
		return query.CapabilitiesRequest{ProtocolVersion: query.DefaultProtocolVersion}
	}
	proto := req.GetProtocolVersion()
	if proto == "" {
		proto = query.DefaultProtocolVersion
	}
	return query.CapabilitiesRequest{
		ProtocolVersion: proto,
		RequestID:       req.GetRequestId(),
	}
}

// ---------------------------------------------------------------------------
// PlanResult / Response（query → proto）
// ---------------------------------------------------------------------------

// PlanResultToProto 把 planner 输出映射为 proto coverage/quality/view。
// ClosedVisibleSeq 从 planner view 回填到 coverage targets（0 值目标补全）。
func PlanResultToProto(res query.PlanResult) (cov *workerpb.LogCoverage, quality *workerpb.LogQuality, view *workerpb.LogQueryViewRef) {
	covCopy := res.Coverage
	ApplyViewToCoverage(&covCopy, res.View)
	return CoverageToProto(covCopy), QualityToProto(res.Quality), ViewRefToProto(viewRefOfPlan(res))
}

func viewRefOfPlan(res query.PlanResult) *query.ViewRef {
	if res.View == nil {
		return nil
	}
	ov := res.View.OrderVersion
	if ov == "" {
		ov = OrderVersion
	}
	return &query.ViewRef{ViewID: res.View.ViewID, OrderVersion: ov}
}

// ApplyViewToCoverage 把 planner 视图固定的 closed_visible_seq / generation
// 回填到 coverage targets（仅补零，不覆盖已有非零值）。
func ApplyViewToCoverage(cov *query.Coverage, view *query.QueryView) {
	if cov == nil || view == nil {
		return
	}
	for i := range cov.Targets {
		t := &cov.Targets[i]
		if t.ClosedVisibleSeq == 0 {
			if v, ok := view.ClosedVisibleSeq[t.TargetID]; ok {
				t.ClosedVisibleSeq = v
			}
		}
		if t.CatalogGeneration == 0 {
			if v, ok := view.Generations[t.TargetID]; ok {
				t.CatalogGeneration = v
			}
		}
	}
}

// CapabilitiesResponseToProto 映射能力协商结果。
// max_limit 为空时回填 query.DefaultMaxLimit，保证 wire 侧有明确上界。
func CapabilitiesResponseToProto(caps query.CapabilitiesResponse) *workerpb.GetLogCapabilitiesResponse {
	out := &workerpb.GetLogCapabilitiesResponse{
		Supported:       caps.Supported,
		ProtocolVersion: caps.ProtocolVersion,
		Capabilities:    append([]string(nil), caps.Capabilities...),
		MaxLimit:        caps.MaxLimit,
		MaxRequestBytes: caps.MaxRequestBytes,
		Cancellation:    caps.Cancellation,
		BuildId:         caps.BuildID,
	}
	if out.MaxLimit == 0 && caps.Supported {
		out.MaxLimit = query.DefaultMaxLimit
	}
	for i := range caps.UnsupportedReasons {
		e := caps.UnsupportedReasons[i]
		out.UnsupportedReasons = append(out.UnsupportedReasons, QueryErrorToProto(&e))
	}
	return out
}

// SearchResponseToProto 映射 Search 结果。
func SearchResponseToProto(resp query.SearchResponse) *workerpb.LogSearchResponse {
	cov := resp.Coverage
	// view 仅有 ID/order；coverage targets 的 CVS 已由 planner/service 填好。
	// 若 view 携带完整信息由 grpcsvc 再调用 ApplyViewToCoverage。
	return &workerpb.LogSearchResponse{
		RequestId:  resp.RequestID,
		View:       ViewRefToProto(resp.View),
		Coverage:   CoverageToProto(cov),
		Quality:    QualityToProto(resp.Quality),
		Items:      EventsToProto(resp.Items),
		NextCursor: resp.NextCursor,
		Exhausted:  resp.Exhausted,
		Error:      QueryErrorToProto(resp.Err),
	}
}

// StatsResponseToProto 映射 Stats 结果。
func StatsResponseToProto(resp query.StatsResponse) *workerpb.LogStatsResponse {
	return &workerpb.LogStatsResponse{
		RequestId: resp.RequestID,
		View:      ViewRefToProto(resp.View),
		Coverage:  CoverageToProto(resp.Coverage),
		Quality:   QualityToProto(resp.Quality),
		Points:    StatsPointsToProto(resp.Points),
		Error:     QueryErrorToProto(resp.Err),
	}
}

// TailResponseItems 把 Tail 结果映射为事件序列（stream 侧无独立 response envelope）。
func TailResponseItems(resp query.TailResponse) []*workerpb.LogEvent {
	return EventsToProto(resp.Items)
}

func formatUint(v uint64) string {
	return strconv.FormatUint(v, 10)
}

func parseUint(s string) uint64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
