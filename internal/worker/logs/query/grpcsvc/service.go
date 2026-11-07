package grpcsvc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcmap"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// QueryService 是 grpcsvc 消费的查询面抽象。
// *query.Service 满足本接口；单测可注入 fake。
type QueryService interface {
	GetCapabilities(req query.CapabilitiesRequest) query.CapabilitiesResponse
	OpenView(req query.QueryRequest) query.SearchResponse
	Search(ctx context.Context, req query.QueryRequest) query.SearchResponse
	Stats(ctx context.Context, req query.QueryRequest, groupBy []string) query.StatsResponse
	Tail(ctx context.Context, req query.QueryRequest, mode query.TailMode) query.TailResponse
}

// ViewLookup 用于从 planner 视图回填 closed_visible_seq。
// *query.Planner 满足本接口。
type ViewLookup interface {
	GetView(viewID string) (*query.QueryView, bool)
}

// FieldsQuerier 是可选扩展：实现后 LogFields 走真实路径，否则 stub 返回 LOG_UNSUPPORTED。
type FieldsQuerier interface {
	Fields(ctx context.Context, req query.QueryRequest) query.FieldsResponse
}

// FacetsQuerier 是可选扩展：实现后 LogFacets 走真实路径，否则 stub 返回 LOG_UNSUPPORTED。
type FacetsQuerier interface {
	Facets(ctx context.Context, req query.QueryRequest, dimensions []string, dimensionLimit uint32) query.FacetsResponse
}

// StreamsQuerier 是可选扩展：实现后三个 LogStream* RPC 走真实路径，
// 否则 stub 返回 LOG_UNSUPPORTED（不返回空成功——空成功会被前端当成「没有流」）。
type StreamsQuerier interface {
	Streams(ctx context.Context, req query.QueryRequest) query.StreamsResponse
	StreamFieldNames(ctx context.Context, req query.QueryRequest) query.StreamFieldNamesResponse
	StreamFieldValues(ctx context.Context, req query.QueryRequest, field string) query.StreamFieldValuesResponse
}

// ArchiveQuerier 是可选的真实归档/恢复能力。
type ArchiveQuerier interface {
	Rehydrate(ctx context.Context, req query.QueryRequest, objectIDs []string) query.ArchiveTaskResponse
	ArchiveStatus(ctx context.Context, req query.QueryRequest, objectIDs []string) query.ArchiveStatusResponse
}

// RuntimeController 是受管 VL supervisor 的最小控制面。
type RuntimeController interface {
	Status(ns vlsup.Namespace) (vlsup.InstanceStatus, error)
	Start(ctx context.Context, ns vlsup.Namespace) error
	Stop(ctx context.Context, ns vlsup.Namespace) error
}

type RuntimeInstaller interface {
	Install(ctx context.Context, packageURL, packageSHA string) error
}

type PartitionMigrator interface {
	Migrate(ctx context.Context, storageNamespace, utcDay, targetTier string) error
}

// OperatorMetadataKey 是承载「操作人认证主体」的 incoming gRPC metadata 键。//
// 为什么用 metadata 而不是给 proto 加字段：本路径的操作人来自**调用方的认证上下文**，
// 而不是请求体（请求体可被任意伪造，PUT 一个字段就把责任推给别人）。用 metadata 让
// 「谁在调用」由传输层携带，且**无需 proto 变更与代码生成**，CP 侧只要在发起该 RPC 时
// 带上这个头即可生效；未携带时本层**拒绝**执行（见 LogResolveIngestGaps）。
//
// 契约点（CP 侧需配合）：发起 LogResolveIngestGaps 且带 storage_namespace 时，
// 必须设置本头为可审计的操作人标识（如控制台登录名 / API 密钥主体）。
const OperatorMetadataKey = "x-jm-operator"

// operatorFromContext 取出调用方认证主体；取不到返回空串（由调用方负责拒绝）。
func operatorFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, v := range md.Get(OperatorMetadataKey) {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// ReasonMetadataKey 是承载「本次操作的显式理由」的 incoming gRPC metadata 键。
//
// 与 OperatorMetadataKey 同源同口径（都走传输层，不进请求体）：理由要进审计凭据，
// 而请求体可被任意伪造 —— 让"为什么可以这么做"与"谁做的"一样由传输层给出，凭据才可信。
// 该键在缺口放弃路径上早已在用（ResolveCoveredGapsForSource 读同一个字面量），
// 此处只是把它收敛成常量名，不改任何 wire 行为。
const ReasonMetadataKey = "x-jm-reason"

// reasonFromContext 取出调用方给出的操作理由；取不到返回空串（由调用方负责拒绝）。
func reasonFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, v := range md.Get(ReasonMetadataKey) {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

type CutoverReadinessFunc func() (bool, time.Time, []string)

func (f CutoverReadinessFunc) Readiness() (bool, time.Time, []string) { return f() }

type PartitionMigrateFunc func(context.Context, string, string, string) error

func (f PartitionMigrateFunc) Migrate(ctx context.Context, storageNamespace, utcDay, targetTier string) error {
	return f(ctx, storageNamespace, utcDay, targetTier)
}

type RuntimeInstallFunc func(context.Context, string, string) error

func (f RuntimeInstallFunc) Install(ctx context.Context, packageURL, packageSHA string) error {
	return f(ctx, packageURL, packageSHA)
}

// Service 实现 workerpb.WorkerServiceServer 的日志查询 RPC 子集。
// 嵌入 UnimplementedWorkerServiceServer 以保持前向兼容（Rehydrate 等后续 FR）。
type Service struct {
	workerpb.UnimplementedWorkerServiceServer

	query     QueryService
	views     ViewLookup
	enabled   bool
	runtime   RuntimeController
	installer RuntimeInstaller
	catalog   *catalog.Catalog
	migrator  PartitionMigrator
}

// New 构造已启用的日志 RPC 服务层。
// q 为 nil 时服务视为未就绪（老 Worker 协商路径：supported=false）。
// views 可为 nil（不做 view 回填）。
func New(q QueryService, views ViewLookup) *Service {
	return &Service{query: q, views: views, enabled: q != nil}
}

// SetResourceBudget 将 Worker 共享资源预算交给真实查询面。
func (s *Service) SetResourceBudget(budget query.ResourceBudget) {
	if target, ok := s.query.(interface{ SetResourceBudget(query.ResourceBudget) }); ok {
		target.SetResourceBudget(budget)
	}
}

// Close 在 Worker 停机时释放固定视图，但不提前解除在途读取 pin。
func (s *Service) Close() error {
	if target, ok := s.query.(interface{ Close() error }); ok {
		return target.Close()
	}
	return nil
}

// NewDisabled 构造显式禁用的服务（老 Worker / 未启用日志平台）。
func NewDisabled() *Service {
	return &Service{enabled: false}
}

// SetEnabled 运行时开关日志查询面。
func (s *Service) SetEnabled(v bool) { s.enabled = v }

// SetRuntimeController 连接 Worker 本地 supervisor；CP 通过 RPC 使用，不直连 VL。
func (s *Service) SetRuntimeController(controller RuntimeController) { s.runtime = controller }

func (s *Service) SetRuntimeInstaller(installer RuntimeInstaller) { s.installer = installer }

func (s *Service) SetRuntimeCatalog(cat *catalog.Catalog)          { s.catalog = cat }
func (s *Service) SetPartitionMigrator(migrator PartitionMigrator) { s.migrator = migrator }

// LogClearIdentityConflicts 人工确认并清空身份冲突记录。
//
// # 为何现在是固定 unsupported（2026-11-06）
//
// 冲突记录的唯一写入方是旧采集链路的账本（`ingest.Manager`），它已在阶段三删除。
// 新采集器（tailer/logtail）按文件 fingerprint + offset 采集，不维护「同区间多哈希」
// 的概念，**不产生也不累积身份冲突**。既然不再有冲突记录，清空动作就没有作用对象。
//
// 保留 RPC 本身（而非直接删 proto）的理由：CP 与 Worker 可独立滚动发版。
// 混版期间老 CP 仍会发起该 RPC，本层必须回一个**明确的 unsupported**，
// 而不能因为方法不存在而让调用方吃 UNIMPLEMENTED 或 panic。
// 返回 unsupported 而不是空成功：后者会让运维以为「标记已清」，从此不再排查。
func (s *Service) LogClearIdentityConflicts(_ context.Context, req *workerpb.LogClearIdentityConflictsRequest) (*workerpb.LogClearIdentityConflictsResponse, error) {
	return &workerpb.LogClearIdentityConflictsResponse{
		RequestId: req.GetRequestId(),
		Error:     unsupportedErr(ingestRetiredReason("ingest identity conflict clearing")),
	}, nil
}

// LogImportArchives 手动触发归档（.gz）导入扫描。
//
// # 为何现在是固定 unsupported（2026-11-06）
//
// 手动导入服务的是旧采集链路的「归档回灌」：把躺在源目录里、尚未被 ArchiveImporter
// 导入的历史 .gz 走一遍与自动路径相同的管道。新采集器不再回灌历史文件——
// 它要么按 `log_tailer.include_gzip` 直接读 gz（在线径内），要么不读。
// 没有「待导入队列」这个概念，手动触发也就没有意义。
//
// 保留 RPC 的理由同 LogClearIdentityConflicts（混版兼容，明确 unsupported）。
func (s *Service) LogImportArchives(_ context.Context, req *workerpb.LogImportArchivesRequest) (*workerpb.LogTaskResponse, error) {
	return &workerpb.LogTaskResponse{
		State: workerpb.LogTaskState_LOG_TASK_FAILED,
		Error: unsupportedErr(ingestRetiredReason("ingest archive import")),
	}, nil
}

// Enabled 报告服务是否启用。
func (s *Service) Enabled() bool { return s.enabled && s.query != nil }

// plannerReady 报告查询面是否就绪（planner + service 均可用）。
func (s *Service) plannerReady() bool { return s.Enabled() }

func (s *Service) disabledCaps(protoVersion string) *workerpb.GetLogCapabilitiesResponse {
	if protoVersion == "" {
		protoVersion = query.DefaultProtocolVersion
	}
	return &workerpb.GetLogCapabilitiesResponse{
		Supported:       false,
		ProtocolVersion: protoVersion,
		UnsupportedReasons: []*workerpb.LogError{{
			Code:    workerpb.LogErrorCode_LOG_UNSUPPORTED,
			Message: "log query service disabled on this worker",
		}},
	}
}

// GetLogCapabilities 能力协商。
// planner/service 就绪 → supported=true + capabilities + max_limit；
// 服务禁用或 q==nil（老 Worker 协商）→ supported=false + LOG_UNSUPPORTED。
func (s *Service) GetLogCapabilities(_ context.Context, req *workerpb.GetLogCapabilitiesRequest) (*workerpb.GetLogCapabilitiesResponse, error) {
	in := grpcmap.CapabilitiesRequestFromProto(req)
	if !s.plannerReady() {
		return s.disabledCaps(in.ProtocolVersion), nil
	}
	caps := s.query.GetCapabilities(in)
	return grpcmap.CapabilitiesResponseToProto(caps), nil
}

// authorizeScope 在 Worker 边界强制授权作用域，作为 CP 之外的纵深防御。
//
// 规则与 CP 的 `logcoord.ValidateQuery` 一致：**无 View 时授权目标不得为空**。
// 理由：planner 把「空 TargetIDs」解释为「本 Worker 全部 Catalog 分区」（见 planner
// 的 PlanRequest 注释），故空授权 + 无 View 等于一次全量读取原语——任何能直接调用
// Worker query 服务的组件（被攻陷的 CP、未来的直连入口、其它内部调用方）都能借此
// 越过目标收敛读到该 Worker 上全部源的日志（M-9）。
//
// 携带 View 时放行：那样读取范围由 View 创建时的目标决定，不再取决于本次请求的授权
// 列表，故不构成「空即全量」；CP 侧另有当前授权对 View 目标的覆盖校验。
//
// 返回非 nil 表示应拒绝本次请求。
func authorizeScope(targets []string, viewID string) error {
	if len(targets) == 0 && strings.TrimSpace(viewID) == "" {
		return fmt.Errorf("%s: authorized target set is empty", query.ErrCodeUnauthorized)
	}
	return nil
}

// unauthorizedSearch 构造授权拒绝响应（复用既有 error 字段，不返回空成功）。
func unauthorizedSearch(req *workerpb.LogSearchRequest, err error) *workerpb.LogSearchResponse {
	return &workerpb.LogSearchResponse{
		RequestId: requestIDOfQuery(req.GetQuery()),
		View:      grpcmap.ViewRefToProto(grpcmap.ViewRefFromProto(req.GetQuery().GetView())),
		Error:     &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_UNAUTHORIZED, Message: err.Error()},
	}
}

// LogCreateView 创建后续联邦请求复用的 Worker 固定视图。
func (s *Service) LogCreateView(ctx context.Context, req *workerpb.LogCreateViewRequest) (*workerpb.LogCreateViewResponse, error) {
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if !s.plannerReady() {
		return &workerpb.LogCreateViewResponse{
			RequestId: requestIDOfQuery(req.GetQuery()),
			Error:     unsupportedErr("log query service disabled on this worker"),
		}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	// 建 View 即确定后续读取范围，故授权目标不得为空（否则该 View 覆盖全量分区）。
	if err := authorizeScope(qreq.AuthorizedTargets, ""); err != nil {
		return &workerpb.LogCreateViewResponse{
			RequestId: qreq.RequestID,
			Error:     &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_UNAUTHORIZED, Message: err.Error()},
		}, nil
	}
	var resp query.SearchResponse
	if contextual, ok := s.query.(interface {
		OpenViewContext(context.Context, query.QueryRequest) query.SearchResponse
	}); ok {
		resp = contextual.OpenViewContext(ctx, qreq)
	} else {
		resp = s.query.OpenView(qreq)
	}
	if ctx.Err() != nil {
		if target, ok := s.query.(interface{ ReleaseView(string) error }); ok && resp.View != nil {
			if err := target.ReleaseView(resp.View.ViewID); err != nil {
				slog.Warn("取消建View后的租约清理失败", "err", err)
			}
		}
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	s.fillClosedVisibleSeq(&resp.Coverage, viewIDOf(resp.View, qreq.View))
	return &workerpb.LogCreateViewResponse{
		RequestId: resp.RequestID,
		View:      grpcmap.ViewRefToProto(resp.View),
		Coverage:  grpcmap.CoverageToProto(resp.Coverage),
		Quality:   grpcmap.QualityToProto(resp.Quality),
		Error:     grpcmap.QueryErrorToProto(resp.Err),
	}, nil
}

// LogSearch 检索固定 Query View 下的规范事件。
func (s *Service) LogSearch(ctx context.Context, req *workerpb.LogSearchRequest) (*workerpb.LogSearchResponse, error) {
	if !s.plannerReady() {
		return s.disabledSearch(req), nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	if err := authorizeScope(qreq.AuthorizedTargets, viewIDOf(qreq.View, qreq.View)); err != nil {
		return unauthorizedSearch(req, err), nil
	}
	resp := s.query.Search(ctx, qreq)
	s.fillClosedVisibleSeq(&resp.Coverage, viewIDOf(resp.View, qreq.View))
	out := grpcmap.SearchResponseToProto(resp)
	// 二次回填：确保 proto 侧 targets 携带 planner view 的 closed_visible_seq。
	s.fillProtoClosedVisibleSeq(out.GetCoverage(), out.GetView().GetViewId())
	return out, nil
}

// LogStats 聚合逻辑事件集合。
func (s *Service) LogStats(ctx context.Context, req *workerpb.LogStatsRequest) (*workerpb.LogStatsResponse, error) {
	if !s.plannerReady() {
		return s.disabledStats(req), nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	if err := authorizeScope(qreq.AuthorizedTargets, viewIDOf(qreq.View, qreq.View)); err != nil {
		return &workerpb.LogStatsResponse{
			RequestId: qreq.RequestID,
			View:      grpcmap.ViewRefToProto(grpcmap.ViewRefFromProto(req.GetQuery().GetView())),
			Error:     &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_UNAUTHORIZED, Message: err.Error()},
		}, nil
	}
	qreq.GroupBy = append([]string(nil), req.GetGroupBy()...)
	qreq.TimeBucket = req.GetTimeBucket()
	resp := s.query.Stats(ctx, qreq, req.GetGroupBy())
	s.fillClosedVisibleSeq(&resp.Coverage, viewIDOf(resp.View, qreq.View))
	out := grpcmap.StatsResponseToProto(resp)
	s.fillProtoClosedVisibleSeq(out.GetCoverage(), out.GetView().GetViewId())
	return out, nil
}

// LogFields 字段枚举 stub：未接入 FieldsQuerier 时返回 LOG_UNSUPPORTED，不返回空成功。
func (s *Service) LogFields(ctx context.Context, req *workerpb.LogFieldsRequest) (*workerpb.LogFieldsResponse, error) {
	if !s.plannerReady() {
		return &workerpb.LogFieldsResponse{
			RequestId: requestIDOfQuery(req.GetQuery()),
			Error:     unsupportedErr("log fields disabled on this worker"),
		}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	fq, ok := s.query.(FieldsQuerier)
	if !ok {
		return &workerpb.LogFieldsResponse{
			RequestId: qreq.RequestID,
			Error:     unsupportedErr("log fields unimplemented on this worker"),
		}, nil
	}
	res := fq.Fields(ctx, qreq)
	s.fillClosedVisibleSeq(&res.Coverage, viewIDOf(res.View, qreq.View))
	out := &workerpb.LogFieldsResponse{
		RequestId: res.RequestID,
		View:      grpcmap.ViewRefToProto(res.View),
		Coverage:  grpcmap.CoverageToProto(res.Coverage),
		Fields:    append([]string(nil), res.Fields...),
		Error:     grpcmap.QueryErrorToProto(res.Err),
	}
	s.fillProtoClosedVisibleSeq(out.GetCoverage(), out.GetView().GetViewId())
	return out, nil
}

// LogFacets 维度统计 stub：未接入 FacetsQuerier 时返回 LOG_UNSUPPORTED。
func (s *Service) LogFacets(ctx context.Context, req *workerpb.LogFacetsRequest) (*workerpb.LogFacetsResponse, error) {
	if !s.plannerReady() {
		return &workerpb.LogFacetsResponse{
			RequestId: requestIDOfQuery(req.GetQuery()),
			Error:     unsupportedErr("log facets disabled on this worker"),
		}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	fq, ok := s.query.(FacetsQuerier)
	if !ok {
		return &workerpb.LogFacetsResponse{
			RequestId: qreq.RequestID,
			Error:     unsupportedErr("log facets unimplemented on this worker"),
		}, nil
	}
	res := fq.Facets(ctx, qreq, req.GetDimensions(), req.GetDimensionLimit())
	s.fillClosedVisibleSeq(&res.Coverage, viewIDOf(res.View, qreq.View))
	values := make([]*workerpb.LogFacetValue, 0, len(res.Values))
	for _, v := range res.Values {
		values = append(values, &workerpb.LogFacetValue{Dimension: v.Dimension, Value: v.Value, Count: v.Count})
	}
	out := &workerpb.LogFacetsResponse{
		RequestId: res.RequestID,
		View:      grpcmap.ViewRefToProto(res.View),
		Coverage:  grpcmap.CoverageToProto(res.Coverage),
		Values:    values,
		Truncated: res.Truncated,
		Error:     grpcmap.QueryErrorToProto(res.Err),
	}
	s.fillProtoClosedVisibleSeq(out.GetCoverage(), out.GetView().GetViewId())
	return out, nil
}

// LogStreams 枚举查询窗内的 stream。未接入 StreamsQuerier 时返回 LOG_UNSUPPORTED。
//
// 语义边界（与 Fields 区分）：
//   - LogFields  = 「窗内出现过哪些**文档字段名**」（/field_names）
//   - LogStreams = 「窗内有哪些**流**、各多少条」（/streams）
//
// 两者都能回答「有哪些流字段」，但只有后者能给出**条数**，
// 而条数正是前端「按日志量排序看哪个源在刷屏」所需。
func (s *Service) LogStreams(ctx context.Context, req *workerpb.LogStreamsRequest) (*workerpb.LogStreamsResponse, error) {
	if !s.plannerReady() {
		return &workerpb.LogStreamsResponse{
			RequestId: requestIDOfQuery(req.GetQuery()),
			Error:     unsupportedErr("log streams disabled on this worker"),
		}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	sq, ok := s.query.(StreamsQuerier)
	if !ok {
		return &workerpb.LogStreamsResponse{
			RequestId: qreq.RequestID,
			Error:     unsupportedErr("log streams unimplemented on this worker"),
		}, nil
	}
	res := sq.Streams(ctx, qreq)
	s.fillClosedVisibleSeq(&res.Coverage, viewIDOf(res.View, qreq.View))
	streams := make([]*workerpb.LogStream, 0, len(res.Streams))
	for _, sv := range res.Streams {
		streams = append(streams, &workerpb.LogStream{
			Raw:         sv.Raw,
			LogSourceId: sv.LogSourceID,
			Stream:      sv.Stream,
			Hits:        sv.Hits,
		})
	}
	out := &workerpb.LogStreamsResponse{
		RequestId: res.RequestID,
		View:      grpcmap.ViewRefToProto(res.View),
		Coverage:  grpcmap.CoverageToProto(res.Coverage),
		Streams:   streams,
		Complete:  res.Complete,
		Error:     grpcmap.QueryErrorToProto(res.Err),
	}
	s.fillProtoClosedVisibleSeq(out.GetCoverage(), out.GetView().GetViewId())
	return out, nil
}

// LogStreamFieldNames 列出 stream 的标签维度名。
func (s *Service) LogStreamFieldNames(ctx context.Context, req *workerpb.LogStreamFieldNamesRequest) (*workerpb.LogStreamFieldNamesResponse, error) {
	if !s.plannerReady() {
		return &workerpb.LogStreamFieldNamesResponse{
			RequestId: requestIDOfQuery(req.GetQuery()),
			Error:     unsupportedErr("log stream field names disabled on this worker"),
		}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	sq, ok := s.query.(StreamsQuerier)
	if !ok {
		return &workerpb.LogStreamFieldNamesResponse{
			RequestId: qreq.RequestID,
			Error:     unsupportedErr("log stream field names unimplemented on this worker"),
		}, nil
	}
	res := sq.StreamFieldNames(ctx, qreq)
	s.fillClosedVisibleSeq(&res.Coverage, viewIDOf(res.View, qreq.View))
	out := &workerpb.LogStreamFieldNamesResponse{
		RequestId: res.RequestID,
		View:      grpcmap.ViewRefToProto(res.View),
		Coverage:  grpcmap.CoverageToProto(res.Coverage),
		Fields:    append([]string(nil), res.Fields...),
		Error:     grpcmap.QueryErrorToProto(res.Err),
	}
	s.fillProtoClosedVisibleSeq(out.GetCoverage(), out.GetView().GetViewId())
	return out, nil
}

// LogStreamFieldValues 按单个 stream 标签维度取取值分布。
func (s *Service) LogStreamFieldValues(ctx context.Context, req *workerpb.LogStreamFieldValuesRequest) (*workerpb.LogStreamFieldValuesResponse, error) {
	if !s.plannerReady() {
		return &workerpb.LogStreamFieldValuesResponse{
			RequestId: requestIDOfQuery(req.GetQuery()),
			Error:     unsupportedErr("log stream field values disabled on this worker"),
		}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	sq, ok := s.query.(StreamsQuerier)
	if !ok {
		return &workerpb.LogStreamFieldValuesResponse{
			RequestId: qreq.RequestID,
			Error:     unsupportedErr("log stream field values unimplemented on this worker"),
		}, nil
	}
	res := sq.StreamFieldValues(ctx, qreq, req.GetField())
	s.fillClosedVisibleSeq(&res.Coverage, viewIDOf(res.View, qreq.View))
	values := make([]*workerpb.LogStreamFieldValue, 0, len(res.Values))
	for _, v := range res.Values {
		values = append(values, &workerpb.LogStreamFieldValue{Value: v.Value, Hits: v.Count})
	}
	out := &workerpb.LogStreamFieldValuesResponse{
		RequestId: res.RequestID,
		View:      grpcmap.ViewRefToProto(res.View),
		Coverage:  grpcmap.CoverageToProto(res.Coverage),
		Values:    values,
		Complete:  res.Complete,
		Error:     grpcmap.QueryErrorToProto(res.Err),
	}
	s.fillProtoClosedVisibleSeq(out.GetCoverage(), out.GetView().GetViewId())
	return out, nil
}

// LogRehydrate 创建或复用受管 Rehydrate 任务。
func (s *Service) LogRehydrate(ctx context.Context, req *workerpb.LogRehydrateRequest) (*workerpb.LogTaskResponse, error) {
	if !s.plannerReady() {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED, Error: unsupportedErr("rehydrate disabled on this worker")}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	backend, ok := s.query.(ArchiveQuerier)
	if !ok {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED, Error: unsupportedErr("rehydrate unimplemented on this worker")}, nil
	}
	res := backend.Rehydrate(ctx, qreq, req.GetArchiveObjectIds())
	return &workerpb.LogTaskResponse{TaskId: res.TaskID, State: taskStateFromString(res.State), Error: grpcmap.QueryErrorToProto(res.Err)}, nil
}

// LogArchiveStatus 查询归档对象是否存在且校验通过。
func (s *Service) LogArchiveStatus(ctx context.Context, req *workerpb.LogArchiveStatusRequest) (*workerpb.LogArchiveStatusResponse, error) {
	if !s.plannerReady() {
		return &workerpb.LogArchiveStatusResponse{RequestId: requestIDOfQuery(req.GetQuery()), Error: unsupportedErr("archive status disabled on this worker")}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	backend, ok := s.query.(ArchiveQuerier)
	if !ok {
		return &workerpb.LogArchiveStatusResponse{RequestId: qreq.RequestID, Error: unsupportedErr("archive status unimplemented on this worker")}, nil
	}
	res := backend.ArchiveStatus(ctx, qreq, req.GetArchiveObjectIds())
	return &workerpb.LogArchiveStatusResponse{
		RequestId:                 res.RequestID,
		Coverage:                  grpcmap.CoverageToProto(res.Coverage),
		AvailableArchiveObjectIds: append([]string(nil), res.AvailableIDs...),
		MissingArchiveObjectIds:   append([]string(nil), res.MissingIDs...),
		Error:                     grpcmap.QueryErrorToProto(res.Err),
	}, nil
}

func (s *Service) LogRuntimeStatus(ctx context.Context, req *workerpb.LogRuntimeStatusRequest) (*workerpb.LogRuntimeStatusResponse, error) {
	resp := &workerpb.LogRuntimeStatusResponse{RequestId: req.GetRequestId()}
	if s.runtime == nil {
		resp.Error = unsupportedErr("managed VictoriaLogs supervisor is not configured")
		return resp, nil
	}
	for _, ns := range []vlsup.Namespace{vlsup.NamespaceHot} {
		if probe, ok := s.runtime.(interface {
			Health(context.Context, vlsup.Namespace) error
		}); ok {
			healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_ = probe.Health(healthCtx, ns)
			cancel()
		}
		status, err := s.runtime.Status(ns)
		if err != nil {
			resp.Error = &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: err.Error(), Retryable: true}
			continue
		}
		status.PartitionRecoveryComplete, status.QueryReady = runtimeCatalogReadiness(s.catalog, ns)
		status.QueryReady = status.HealthOK && status.QueryReady
		resp.Instances = append(resp.Instances, &workerpb.LogRuntimeInstance{
			Namespace: string(status.Namespace), State: string(status.State), Port: int32(status.Port),
			ListenAddr: status.ListenAddr, StorageDataPath: status.StorageDataPath, Pid: int32(status.PID),
			AssetTag: status.AssetTag, AssetBuildId: status.AssetBuildID, AssetSha256: status.AssetSHA256,
			LastError: status.LastError, HealthOk: status.HealthOK,
			PartitionRecoveryComplete: status.PartitionRecoveryComplete, QueryReady: status.QueryReady,
		})
	}
	resp.Supported = true
	return resp, nil
}

func runtimeCatalogReadiness(cat *catalog.Catalog, ns vlsup.Namespace) (recovered, queryReady bool) {
	if cat == nil {
		return false, false
	}
	seen, complete, published := false, true, true
	for _, key := range cat.Keys() {
		rec, ok := cat.Get(key)
		if !ok || !ownerUsesNamespace(rec.Owner, ns) {
			continue
		}
		seen = true
		ref, ok := cat.OwnerForQuery(key)
		if !ok || !ref.OK || rec.RecoveryRequired {
			complete = false
		}
		if ref.Projection == nil || !ref.Projection.CoverageComplete ||
			ref.Projection.QueryLocationDirID != ref.DirID || ref.Projection.QueryGeneration != ref.Generation {
			published = false
		}
	}
	return seen && complete, seen && complete && published
}

// ownerUsesNamespace 报告某归属的数据在不在该受管实例里。
//
// 2026-10-29：只剩余一个 VL，冷热双层已废除 ⇒ 所有归属的数据都在这唯一的实例里。
// 保留这个函数而不是直接 return true，是因为 catalog 里仍可能有历史记录的 Owner=cold/archive
// （今天之前搬过去的），那些记录同样是这一个 VL 的（它们的物理分区现在都在同一 data_root 下）。
func ownerUsesNamespace(owner catalog.Owner, ns vlsup.Namespace) bool {
	if ns != vlsup.NamespaceHot {
		return false
	}
	return owner.Valid()
}

func (s *Service) LogRuntimeControl(ctx context.Context, req *workerpb.LogRuntimeControlRequest) (*workerpb.LogTaskResponse, error) {
	action := strings.ToLower(strings.TrimSpace(req.GetAction()))
	if action == "install" {
		if s.installer == nil {
			return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED, Error: unsupportedErr("managed asset installation is not configured")}, nil
		}
		if req.GetPackageUrl() == "" || req.GetPackageSha256() == "" {
			return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED, Error: &workerpb.LogError{
				Code: workerpb.LogErrorCode_LOG_ERROR_UNSPECIFIED, Message: "approved package URL and SHA-256 required"}}, nil
		}
		if err := s.installer.Install(ctx, req.GetPackageUrl(), req.GetPackageSha256()); err != nil {
			return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED, Error: &workerpb.LogError{
				Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: err.Error(), Retryable: true}}, nil
		}
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
	}
	if s.runtime == nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED, Error: unsupportedErr("managed VictoriaLogs supervisor is not configured")}, nil
	}
	ns := vlsup.Namespace(strings.ToLower(strings.TrimSpace(req.GetNamespace())))
	if !vlsup.ValidNamespace(ns) {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED, Error: &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_ERROR_UNSPECIFIED, Message: "unknown VictoriaLogs namespace"}}, nil
	}
	var err error
	switch action {
	case "start":
		err = s.runtime.Start(ctx, ns)
	case "stop":
		err = s.runtime.Stop(ctx, ns)
	case "restart":
		_ = s.runtime.Stop(ctx, ns)
		err = s.runtime.Start(ctx, ns)
	default:
		err = fmt.Errorf("unknown VictoriaLogs runtime action %q", req.GetAction())
	}
	if err != nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED, Error: &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: err.Error(), Retryable: true}}, nil
	}
	return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
}

func (s *Service) LogMigratePartition(ctx context.Context, req *workerpb.LogMigratePartitionRequest) (*workerpb.LogTaskResponse, error) {
	if s.migrator == nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: unsupportedErr("log partition migration is not configured")}, nil
	}
	namespace, day, target := strings.TrimSpace(req.GetStorageNamespace()), strings.TrimSpace(req.GetUtcDay()), strings.ToLower(strings.TrimSpace(req.GetTargetTier()))
	if namespace == "" || len(day) != 10 || target != "cold" {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_ERROR_UNSPECIFIED, Message: "storage_namespace, YYYY-MM-DD utc_day and target_tier=cold are required"}}, nil
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_ERROR_UNSPECIFIED, Message: "utc_day must be YYYY-MM-DD"}}, nil
	}
	if err := s.migrator.Migrate(ctx, namespace, day, target); err != nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: err.Error(), Retryable: true}}, nil
	}
	return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
}

// LogCutoverReadiness 报告日志平台切换就绪度。
//
// # 为何现在是固定 unsupported（2026-11-06）
//
// 它回答的是旧链路的「账本能否接管写入」：ledgerReady 来自账本的 CutoverReadiness，
// 即「已发布投影是否覆盖了需接管的位置区间」。账本已在阶段三删除，新采集器用
// registry（fingerprint → offset）续读，不存在「接管判定」这个环节。
//
// 保留 RPC 的理由同 LogClearIdentityConflicts（混版兼容，明确 unsupported）。
func (s *Service) LogCutoverReadiness(_ context.Context, req *workerpb.LogCutoverReadinessRequest) (*workerpb.LogCutoverReadinessResponse, error) {
	return &workerpb.LogCutoverReadinessResponse{
		RequestId: req.GetRequestId(),
		Error:     unsupportedErr(ingestRetiredReason("log cutover readiness")),
	}, nil
}

// LogResolveIngestGaps 核销/放弃采集缺口。
//
// # 为何现在是固定 unsupported（2026-11-06）
//
// 「缺口」是旧账本的概念：它记录源位置上哪段没被投递（含被放弃的永久丢失区间），
// 并由人工裁定去核销。新采集器**主动不追踪完整性**（用户裁定："日志不重要，投递失败
// 就失败了，做好提示消息就行"），因此没有缺口账本、没有放弃裁定、也没有可核销的对象。
//
// 保留 RPC 的理由同上。注意：本方法**不再**校验操作人/理由——那两个校验的存在意义
// 是「永久放弃不可无痕」，而放弃动作本身已不存在，留着校验只会给运维一个
// 「填了理由就能成功」的错觉。
func (s *Service) LogResolveIngestGaps(_ context.Context, req *workerpb.LogResolveIngestGapsRequest) (*workerpb.LogTaskResponse, error) {
	return &workerpb.LogTaskResponse{
		State: workerpb.LogTaskState_LOG_TASK_FAILED,
		Error: unsupportedErr(ingestRetiredReason("ingest gap resolution")),
	}, nil
}

// LogTail 尾部查询（server-streaming LogEvent）。
// stream 协议没有独立 response envelope：unsupported/partial 以 gRPC status 返回，
// 不得静默结束空流伪装成功。成功路径逐条推送 LogEvent。
func (s *Service) LogTail(req *workerpb.LogTailRequest, stream workerpb.WorkerService_LogTailServer) error {
	ctx := stream.Context()
	if !s.plannerReady() {
		return status.Error(codes.FailedPrecondition, string(query.ErrCodeUnsupported)+": log query service disabled on this worker")
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	mode := grpcmap.TailModeFromProto(req.GetMode())
	resp := s.query.Tail(ctx, qreq, mode)
	if resp.Err != nil {
		// LOG_UNSUPPORTED / VIEW_STALE 等必须显式失败，禁止空流成功。
		return status.Error(statusCodeFor(resp.Err.Code), fmt.Sprintf("%s: %s", resp.Err.Code, resp.Err.Message))
	}
	if !resp.Coverage.Complete {
		return status.Errorf(codes.Unavailable, "%s: tail coverage incomplete: %s",
			query.ErrCodeNotReady, strings.Join(resp.Coverage.PartialReasons, ","))
	}
	for _, ev := range grpcmap.TailResponseItems(resp) {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) disabledSearch(req *workerpb.LogSearchRequest) *workerpb.LogSearchResponse {
	q := req.GetQuery()
	return &workerpb.LogSearchResponse{
		RequestId: requestIDOfQuery(q),
		View:      grpcmap.ViewRefToProto(grpcmap.ViewRefFromProto(q.GetView())),
		Error:     unsupportedErr("log query service disabled on this worker"),
		Coverage:  &workerpb.LogCoverage{Complete: false},
	}
}

func (s *Service) disabledStats(req *workerpb.LogStatsRequest) *workerpb.LogStatsResponse {
	q := req.GetQuery()
	return &workerpb.LogStatsResponse{
		RequestId: requestIDOfQuery(q),
		View:      grpcmap.ViewRefToProto(grpcmap.ViewRefFromProto(q.GetView())),
		Error:     unsupportedErr("log query service disabled on this worker"),
		Coverage:  &workerpb.LogCoverage{Complete: false},
	}
}

func unsupportedErr(msg string) *workerpb.LogError {
	return &workerpb.LogError{
		Code:    workerpb.LogErrorCode_LOG_UNSUPPORTED,
		Message: msg,
	}
}

// ingestRetiredReason 构造「该能力已随旧采集链路退役」的统一错误文案。
//
// 与普通 unsupported 区分开的理由：运维看到 `... is not configured` 会去查配置
// （以为是漏配了），而实际是**代码已经没了**——两种处置完全不同。
// 文案里显式点名退役与替代路径，能把排查引向正确方向。
func ingestRetiredReason(capability string) string {
	return capability + " 已随旧采集链路（ingest/ledger）退役：新采集器（tailer/logtail）不维护缺口账本、" +
		"不产生身份冲突、不回灌历史归档。详见 docs/ARCHITECTURE.md 的采集链路说明。"
}

// taskStateFromString 把归档任务状态字符串映射为 proto 枚举。
//
// archive.TaskState 的取值自带 LOG_TASK_ 前缀（与 proto 枚举同名，见
// archive/types.go），而历史调用方与测试也可能传裸值（QUEUED/SUCCEEDED…），
// 因此这里**先剥前缀再匹配**，两种形态都认。
// 此前只匹配裸值，带前缀的取值全部落入 default：成功回灌被报成 FAILED 且
// error 为空，调用方无法用 state 判定成功。
// 选在映射层归一化而不是改 archive 常量：TaskState 会随 RehydrateJob 的
// `json:"state"` 出现在持久化快照里，改常量等于改那份表示；proto 枚举是外部契约，
// 更不能动。无法识别的取值仍 fail-closed 归到 FAILED。
func taskStateFromString(state string) workerpb.LogTaskState {
	// 只剥一层：`LOG_TASK_` 已是两种形态的唯一差异，多剥会把异常输入误判成合法值。
	norm := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(state)), "LOG_TASK_")
	switch norm {
	case "QUEUED":
		return workerpb.LogTaskState_LOG_TASK_QUEUED
	case "RUNNING":
		return workerpb.LogTaskState_LOG_TASK_RUNNING
	case "SUCCEEDED", "SUCCESS":
		return workerpb.LogTaskState_LOG_TASK_SUCCEEDED
	case "CANCELLED", "CANCELED":
		return workerpb.LogTaskState_LOG_TASK_CANCELLED
	case "STALE":
		return workerpb.LogTaskState_LOG_TASK_STALE
	default:
		return workerpb.LogTaskState_LOG_TASK_FAILED
	}
}

func statusCodeFor(c query.ErrorCode) codes.Code {
	switch c {
	case query.ErrCodeUnsupported:
		return codes.FailedPrecondition
	case query.ErrCodeViewStale:
		return codes.Aborted
	case query.ErrCodeUnauthorized:
		return codes.PermissionDenied
	case query.ErrCodeCancelled:
		return codes.Canceled
	case query.ErrCodeBudgetExceeded:
		return codes.ResourceExhausted
	case query.ErrCodeNotReady:
		return codes.Unavailable
	default:
		return codes.Internal
	}
}

func requestIDOfQuery(q *workerpb.LogQueryRequestBase) string {
	if q == nil {
		return ""
	}
	return q.GetRequestId()
}

func viewIDOf(respView, reqView *query.ViewRef) string {
	if respView != nil && respView.ViewID != "" {
		return respView.ViewID
	}
	if reqView != nil {
		return reqView.ViewID
	}
	return ""
}

// fillClosedVisibleSeq 从 planner view 回填 coverage targets 的 closed_visible_seq / generation。
func (s *Service) fillClosedVisibleSeq(cov *query.Coverage, viewID string) {
	if s.views == nil || cov == nil || viewID == "" {
		return
	}
	view, ok := s.views.GetView(viewID)
	if !ok || view == nil {
		return
	}
	grpcmap.ApplyViewToCoverage(cov, view)
}

// fillProtoClosedVisibleSeq 在 proto coverage 上二次回填（targets 仍为 "0" 时）。
func (s *Service) fillProtoClosedVisibleSeq(cov *workerpb.LogCoverage, viewID string) {
	if s.views == nil || cov == nil || viewID == "" {
		return
	}
	view, ok := s.views.GetView(viewID)
	if !ok || view == nil {
		return
	}
	for _, t := range cov.GetTargets() {
		if t == nil {
			continue
		}
		if t.GetClosedVisibleSeq() == "" || t.GetClosedVisibleSeq() == "0" {
			if v, ok := view.ClosedVisibleSeq[t.GetTargetId()]; ok && v != 0 {
				t.ClosedVisibleSeq = fmt.Sprintf("%d", v)
			}
		}
		if t.GetCatalogGeneration() == "" || t.GetCatalogGeneration() == "0" {
			if v, ok := view.Generations[t.GetTargetId()]; ok && v != 0 {
				t.CatalogGeneration = fmt.Sprintf("%d", v)
			}
		}
	}
}
