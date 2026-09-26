package logcoord

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// LogProtocolVersion Worker Log RPC 协议版本（与 FR-473 契约对齐）。
const LogProtocolVersion = "fr433/v1"

// WorkerClientFactory 按 WorkerID 取得 workerpb 日志查询客户端。
// 生产实现经 CP 反向 gRPC 隧道（ClientPool）；测试注入 fake。
type WorkerClientFactory func(ctx context.Context, workerID string) (workerpb.WorkerServiceClient, error)

// TunnelDialer 实现 WorkerDialer：经工厂拿到 workerpb 客户端并适配为 WorkerClient。
//
// 生产由 CP ClientPool.Get(workerID) 适配工厂（见 assemble.go PoolClientFactory），经 ADR-081 反向隧道；
// 未配置工厂时 Dial 失败，覆盖侧记 dial_failed，不得静默假完整。
type TunnelDialer struct {
	factory WorkerClientFactory
}

// NewTunnelDialer 创建隧道 Dialer。factory 可为 nil（此时 Dial 恒失败）。
func NewTunnelDialer(factory WorkerClientFactory) *TunnelDialer {
	return &TunnelDialer{factory: factory}
}

// Dial 实现 WorkerDialer。
func (d *TunnelDialer) Dial(ctx context.Context, workerID string) (WorkerClient, error) {
	if d == nil || d.factory == nil {
		return nil, fmt.Errorf("logcoord: tunnel dialer not wired (worker=%s)", workerID)
	}
	cli, err := d.factory(ctx, workerID)
	if err != nil {
		return nil, err
	}
	if cli == nil {
		return nil, fmt.Errorf("logcoord: no worker client for %s", workerID)
	}
	return NewWorkerClientAdapter(cli), nil
}

// WorkerClientAdapter 将 workerpb.WorkerServiceClient 适配为 logcoord.WorkerClient。
type WorkerClientAdapter struct {
	client workerpb.WorkerServiceClient
}

// NewWorkerClientAdapter 创建适配器。
func NewWorkerClientAdapter(client workerpb.WorkerServiceClient) *WorkerClientAdapter {
	return &WorkerClientAdapter{client: client}
}

// Search 实现 WorkerClient。
func (a *WorkerClientAdapter) Search(ctx context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("logcoord: worker client adapter not wired")
	}
	resp, err := a.client.LogSearch(ctx, &workerpb.LogSearchRequest{
		Query: toProtoQueryBase(req),
	})
	return mapSearchResponse(resp, err)
}

func (a *WorkerClientAdapter) Tail(ctx context.Context, req WorkerSearchRequest, mode string) (*WorkerSearchResponse, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("logcoord: worker client adapter not wired")
	}
	tailMode := workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED
	if strings.EqualFold(mode, "FOLLOW_LIVE") {
		tailMode = workerpb.LogTailMode_LOG_TAIL_FOLLOW_LIVE
	}
	stream, err := a.client.LogTail(ctx, &workerpb.LogTailRequest{Query: toProtoQueryBase(req), Mode: tailMode})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return &WorkerSearchResponse{Unsupported: true, Error: err.Error()}, nil
		}
		return nil, err
	}
	out := &WorkerSearchResponse{ViewID: req.ViewID, Exhausted: !strings.EqualFold(mode, "FOLLOW_LIVE")}
	for _, target := range req.TargetIDs {
		out.Targets = append(out.Targets, WorkerTargetResult{TargetID: target, State: CoverageSuccess})
	}
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			if status.Code(recvErr) == codes.Unimplemented || status.Code(recvErr) == codes.FailedPrecondition {
				return &WorkerSearchResponse{ViewID: req.ViewID, Unsupported: true, Error: recvErr.Error()}, nil
			}
			return nil, recvErr
		}
		out.Items = append(out.Items, eventFromProto(event))
	}
	return out, nil
}

func (a *WorkerClientAdapter) Fields(ctx context.Context, req WorkerSearchRequest) (*WorkerFieldsResponse, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("logcoord: worker client adapter not wired")
	}
	resp, err := a.client.LogFields(ctx, &workerpb.LogFieldsRequest{Query: toProtoQueryBase(req)})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return &WorkerFieldsResponse{Unsupported: true, Error: err.Error()}, nil
		}
		return nil, err
	}
	if resp == nil {
		return &WorkerFieldsResponse{Error: "empty_worker_response"}, nil
	}
	out := &WorkerFieldsResponse{Fields: append([]string(nil), resp.GetFields()...)}
	if resp.GetView() != nil {
		out.ViewID = resp.GetView().GetViewId()
	}
	if resp.GetCoverage() != nil {
		out.Targets = mapCoverageTargets(resp.GetCoverage().GetTargets())
	}
	if e := resp.GetError(); e != nil {
		out.Error = e.GetMessage()
		out.Unsupported = e.GetCode() == workerpb.LogErrorCode_LOG_UNSUPPORTED
	}
	return out, nil
}

// OpenView creates the Worker-local Query View before federated reads begin.
func (a *WorkerClientAdapter) OpenView(ctx context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("logcoord: worker client adapter not wired")
	}
	resp, err := a.client.LogCreateView(ctx, &workerpb.LogCreateViewRequest{
		Query: toProtoQueryBase(req),
	})
	return mapCreateViewResponse(resp, err)
}

// Stats 实现 WorkerClient。
func (a *WorkerClientAdapter) Stats(ctx context.Context, req WorkerSearchRequest, groupBy []string, timeBucket string) (*WorkerStatsResponse, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("logcoord: worker client adapter not wired")
	}
	base := toProtoQueryBase(req)
	// metric_field 无独立 proto 字段时经 filter 旁路传递不安全；Stats 预聚合由 Worker 按契约字段处理。
	resp, err := a.client.LogStats(ctx, &workerpb.LogStatsRequest{
		Query:      base,
		GroupBy:    append([]string(nil), groupBy...),
		TimeBucket: timeBucket,
	})
	return mapStatsResponse(resp, err)
}

// Facets 实现 WorkerClient。
func (a *WorkerClientAdapter) Facets(ctx context.Context, req WorkerSearchRequest, dimensions []string, dimensionLimit uint32) (*WorkerFacetsResponse, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("logcoord: worker client adapter not wired")
	}
	resp, err := a.client.LogFacets(ctx, &workerpb.LogFacetsRequest{
		Query:          toProtoQueryBase(req),
		Dimensions:     append([]string(nil), dimensions...),
		DimensionLimit: dimensionLimit,
	})
	return mapFacetsResponse(resp, err)
}

func toProtoQueryBase(req WorkerSearchRequest) *workerpb.LogQueryRequestBase {
	order := req.OrderVersion
	if order == "" {
		order = OrderVersion
	}
	return &workerpb.LogQueryRequestBase{
		ProtocolVersion: LogProtocolVersion,
		TimeRange: &workerpb.LogTimeRange{
			FromUtc: req.FromUTC,
			ToUtc:   req.ToUTC,
		},
		AuthorizedTargets: &workerpb.LogAuthorizedTargets{
			TargetIds: append([]string(nil), req.TargetIDs...),
		},
		Budget: &workerpb.LogQueryBudget{
			Limit:     uint32(maxInt(req.Budget.Limit, 0)),
			MaxBytes:  req.Budget.MaxBytes,
			TimeoutMs: req.Budget.TimeoutMS,
			MaxFanout: req.Budget.MaxFanout,
		},
		View: &workerpb.LogQueryViewRef{
			ViewId:       req.ViewID,
			Cursor:       req.Cursor,
			OrderVersion: order,
		},
		Filter:            req.Filter,
		PermissionScope:   "",
		CancellationToken: "",
	}
}

func mapSearchResponse(resp *workerpb.LogSearchResponse, err error) (*WorkerSearchResponse, error) {
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return &WorkerSearchResponse{Unsupported: true, Error: err.Error()}, nil
		}
		return nil, err
	}
	if resp == nil {
		return &WorkerSearchResponse{Error: "empty_worker_response"}, nil
	}
	if e := resp.GetError(); e != nil {
		out := &WorkerSearchResponse{Error: e.GetMessage()}
		if e.GetCode() == workerpb.LogErrorCode_LOG_UNSUPPORTED {
			out.Unsupported = true
		}
		if v := resp.GetView(); v != nil {
			out.ViewID = v.GetViewId()
		}
		out.Targets = mapCoverageTargets(resp.GetCoverage().GetTargets())
		return out, nil
	}
	out := &WorkerSearchResponse{
		ViewID:     resp.GetView().GetViewId(),
		NextCursor: resp.GetNextCursor(),
		Exhausted:  resp.GetExhausted(),
		Targets:    mapCoverageTargets(resp.GetCoverage().GetTargets()),
	}
	// !exhausted + next_cursor 是正常分页，不是资源截断。资源截断由 coverage
	// 原因显式表达，不能把页大小误报为不完整结果。
	for _, target := range out.Targets {
		for _, reason := range target.Reasons {
			if reason == "BUDGET_EXCEEDED" || reason == "TRUNCATED" {
				out.Truncated = true
			}
		}
	}
	for _, e := range resp.GetItems() {
		out.Items = append(out.Items, eventFromProto(e))
	}
	return out, nil
}

func mapCreateViewResponse(resp *workerpb.LogCreateViewResponse, err error) (*WorkerSearchResponse, error) {
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return &WorkerSearchResponse{Unsupported: true, Error: err.Error()}, nil
		}
		return nil, err
	}
	if resp == nil {
		return &WorkerSearchResponse{Error: "empty_worker_response"}, nil
	}
	out := &WorkerSearchResponse{Targets: mapCoverageTargets(resp.GetCoverage().GetTargets())}
	if resp.GetView() != nil {
		out.ViewID = resp.GetView().GetViewId()
	}
	if e := resp.GetError(); e != nil {
		out.Error = e.GetMessage()
		out.Unsupported = e.GetCode() == workerpb.LogErrorCode_LOG_UNSUPPORTED
	}
	return out, nil
}

func mapStatsResponse(resp *workerpb.LogStatsResponse, err error) (*WorkerStatsResponse, error) {
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return &WorkerStatsResponse{Unsupported: true, Error: err.Error()}, nil
		}
		return nil, err
	}
	if resp == nil {
		return &WorkerStatsResponse{Error: "empty_worker_response"}, nil
	}
	if e := resp.GetError(); e != nil {
		out := &WorkerStatsResponse{Error: e.GetMessage()}
		if e.GetCode() == workerpb.LogErrorCode_LOG_UNSUPPORTED {
			out.Unsupported = true
		}
		out.Targets = mapCoverageTargets(resp.GetCoverage().GetTargets())
		return out, nil
	}
	out := &WorkerStatsResponse{
		ViewID:  resp.GetView().GetViewId(),
		Targets: mapCoverageTargets(resp.GetCoverage().GetTargets()),
	}
	for _, p := range resp.GetPoints() {
		agg := StatsAggregate{
			Count: p.GetCount(),
			Sum:   p.GetSum(),
			Min:   p.GetMin(),
			Max:   p.GetMax(),
		}
		// proto 同时带 avg；以 sum/count 为准，HasMinMax 由 min/max 是否出现近似。
		if p.GetCount() > 0 {
			agg.HasMinMax = true
		}
		out.Points = append(out.Points, WorkerStatsPoint{
			Dimensions:    p.GetDimensions(),
			TimeBucketUTC: timeBucketFromDimensions(p.GetDimensions()),
			Agg:           agg,
		})
	}
	return out, nil
}

func timeBucketFromDimensions(dims map[string]string) string {
	if dims == nil {
		return ""
	}
	if v, ok := dims["time_bucket_utc"]; ok {
		return v
	}
	if v, ok := dims["time_bucket"]; ok {
		return v
	}
	return ""
}

func mapFacetsResponse(resp *workerpb.LogFacetsResponse, err error) (*WorkerFacetsResponse, error) {
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return &WorkerFacetsResponse{Unsupported: true, Error: err.Error()}, nil
		}
		return nil, err
	}
	if resp == nil {
		return &WorkerFacetsResponse{Error: "empty_worker_response"}, nil
	}
	if e := resp.GetError(); e != nil {
		out := &WorkerFacetsResponse{Error: e.GetMessage()}
		if e.GetCode() == workerpb.LogErrorCode_LOG_UNSUPPORTED {
			out.Unsupported = true
		}
		out.Targets = mapCoverageTargets(resp.GetCoverage().GetTargets())
		return out, nil
	}
	out := &WorkerFacetsResponse{
		ViewID:  resp.GetView().GetViewId(),
		Targets: mapCoverageTargets(resp.GetCoverage().GetTargets()),
	}
	// proto LogFacetValue 扁平带 dimension；按维重组。
	order := make([]string, 0, 4)
	byDim := make(map[string]*WorkerFacetDimension)
	for _, v := range resp.GetValues() {
		dim := v.GetDimension()
		d, ok := byDim[dim]
		if !ok {
			d = &WorkerFacetDimension{Dimension: dim}
			byDim[dim] = d
			order = append(order, dim)
		}
		d.Values = append(d.Values, FacetValue{Value: v.GetValue(), Count: v.GetCount()})
	}
	if resp.GetTruncated() {
		for _, d := range byDim {
			d.Truncated = true
		}
	}
	for _, dim := range order {
		out.Dimensions = append(out.Dimensions, *byDim[dim])
	}
	return out, nil
}

func mapCoverageTargets(protoTargets []*workerpb.LogCoverageTarget) []WorkerTargetResult {
	if len(protoTargets) == 0 {
		return nil
	}
	out := make([]WorkerTargetResult, 0, len(protoTargets))
	for _, t := range protoTargets {
		if t == nil {
			continue
		}
		out = append(out, WorkerTargetResult{
			TargetID:          t.GetTargetId(),
			State:             CoverageStateFromProto(t.GetState()),
			Reasons:           append([]string(nil), t.GetReasons()...),
			ClosedVisibleSeq:  t.GetClosedVisibleSeq(),
			CatalogGeneration: t.GetCatalogGeneration(),
		})
	}
	return out
}

func eventFromProto(e *workerpb.LogEvent) Event {
	if e == nil {
		return Event{}
	}
	return Event{
		EventID:          e.GetEventId(),
		LogSourceID:      e.GetLogSourceId(),
		SourceGeneration: e.GetSourceGeneration(),
		RecordStart:      parseUint64Loose(e.GetRecordStart()),
		RecordEnd:        parseUint64Loose(e.GetRecordEnd()),
		EventTimeUTC:     e.GetEventTimeUtc(),
		IngestTimeUTC:    e.GetIngestTimeUtc(),
		Level:            e.GetLevel(),
		Stream:           e.GetStream(),
		Message:          e.GetMessage(),
		CanonicalHash:    e.GetCanonicalContentHash(),
	}
}

func parseUint64Loose(s string) uint64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// CoverageStateFromProto 映射 proto 覆盖状态 → logcoord 字符串枚举。
func CoverageStateFromProto(s workerpb.LogCoverageState) CoverageState {
	switch s {
	case workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS:
		return CoverageSuccess
	case workerpb.LogCoverageState_LOG_COVERAGE_OFFLINE:
		return CoverageOffline
	case workerpb.LogCoverageState_LOG_COVERAGE_NOT_READY:
		return CoverageNotReady
	case workerpb.LogCoverageState_LOG_COVERAGE_ARCHIVE_MISSING:
		return CoverageArchiveMissing
	case workerpb.LogCoverageState_LOG_COVERAGE_PARTIAL:
		return CoveragePartial
	case workerpb.LogCoverageState_LOG_COVERAGE_STALE:
		return CoverageStale
	default:
		return CoverageUnspecified
	}
}

// CoverageStateToProto 映射 logcoord 覆盖状态 → proto。
func CoverageStateToProto(s CoverageState) workerpb.LogCoverageState {
	switch strings.ToLower(string(s)) {
	case string(CoverageSuccess):
		return workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS
	case string(CoverageOffline):
		return workerpb.LogCoverageState_LOG_COVERAGE_OFFLINE
	case string(CoverageNotReady):
		return workerpb.LogCoverageState_LOG_COVERAGE_NOT_READY
	case string(CoverageArchiveMissing):
		return workerpb.LogCoverageState_LOG_COVERAGE_ARCHIVE_MISSING
	case string(CoveragePartial):
		return workerpb.LogCoverageState_LOG_COVERAGE_PARTIAL
	case string(CoverageStale):
		return workerpb.LogCoverageState_LOG_COVERAGE_STALE
	default:
		return workerpb.LogCoverageState_LOG_COVERAGE_STATE_UNSPECIFIED
	}
}

// ParseDuplicateQuality 大小写不敏感解析 duplicate quality。
func ParseDuplicateQuality(s string) DuplicateQuality {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "exact":
		return DupExact
	case "unresolved":
		return DupUnresolved
	case "conflict":
		return DupConflict
	case "unspecified", "":
		return DupUnspecified
	default:
		return DupUnspecified
	}
}

// ParseStatsQuality 大小写不敏感解析 stats quality。
func ParseStatsQuality(s string) StatsQuality {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "exact":
		return StatsQExact
	case "partial":
		return StatsQPartial
	case "unavailable":
		return StatsQUnavailable
	case "unspecified", "":
		return StatsQUnspecified
	default:
		return StatsQUnspecified
	}
}

// DuplicateQualityFromProto 映射 proto → logcoord。
func DuplicateQualityFromProto(q workerpb.LogDuplicateQuality) DuplicateQuality {
	switch q {
	case workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_EXACT:
		return DupExact
	case workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_UNRESOLVED:
		return DupUnresolved
	case workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_CONFLICT:
		return DupConflict
	default:
		return DupUnspecified
	}
}

// StatsQualityFromProto 映射 proto → logcoord。
func StatsQualityFromProto(q workerpb.LogStatsQuality) StatsQuality {
	switch q {
	case workerpb.LogStatsQuality_LOG_QUALITY_STATS_EXACT:
		return StatsQExact
	case workerpb.LogStatsQuality_LOG_QUALITY_STATS_PARTIAL:
		return StatsQPartial
	case workerpb.LogStatsQuality_LOG_QUALITY_STATS_UNAVAILABLE:
		return StatsQUnavailable
	default:
		return StatsQUnspecified
	}
}

// QualityBlocksExport 判定质量是否阻断导出成功附件。
// duplicate=unresolved/conflict 或 stats=partial/unavailable 均阻断；
// 枚举值大小写不敏感。
func QualityBlocksExport(q Quality) bool {
	dq := ParseDuplicateQuality(string(q.DuplicateQuality))
	sq := ParseStatsQuality(string(q.StatsQuality))
	return dq == DupUnresolved || dq == DupConflict ||
		sq == StatsQPartial || sq == StatsQUnavailable
}
