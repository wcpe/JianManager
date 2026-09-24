package grpcsvc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
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

type CutoverReadinessProvider interface {
	Readiness() (ledgerReady bool, cutoff time.Time, reasons []string)
}

type IngestGapResolver interface{ ResolveCoveredGaps() error }

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

	query       QueryService
	views       ViewLookup
	enabled     bool
	runtime     RuntimeController
	installer   RuntimeInstaller
	catalog     *catalog.Catalog
	migrator    PartitionMigrator
	cutover     CutoverReadinessProvider
	gapResolver IngestGapResolver
}

// New 构造已启用的日志 RPC 服务层。
// q 为 nil 时服务视为未就绪（老 Worker 协商路径：supported=false）。
// views 可为 nil（不做 view 回填）。
func New(q QueryService, views ViewLookup) *Service {
	return &Service{query: q, views: views, enabled: q != nil}
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

func (s *Service) SetRuntimeCatalog(cat *catalog.Catalog)                { s.catalog = cat }
func (s *Service) SetPartitionMigrator(migrator PartitionMigrator)       { s.migrator = migrator }
func (s *Service) SetCutoverReadiness(provider CutoverReadinessProvider) { s.cutover = provider }
func (s *Service) SetIngestGapResolver(resolver IngestGapResolver)       { s.gapResolver = resolver }

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

// LogCreateView creates the Worker-local view used by subsequent federated calls.
func (s *Service) LogCreateView(ctx context.Context, req *workerpb.LogCreateViewRequest) (*workerpb.LogCreateViewResponse, error) {
	_ = ctx
	if !s.plannerReady() {
		return &workerpb.LogCreateViewResponse{
			RequestId: requestIDOfQuery(req.GetQuery()),
			Error:     unsupportedErr("log query service disabled on this worker"),
		}, nil
	}
	qreq := grpcmap.QueryRequestFromBase(req.GetQuery())
	resp := s.query.OpenView(qreq)
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
	for _, ns := range []vlsup.Namespace{vlsup.NamespaceHot, vlsup.NamespaceCold, vlsup.NamespaceRehydrate} {
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

func ownerUsesNamespace(owner catalog.Owner, ns vlsup.Namespace) bool {
	switch owner {
	case catalog.OwnerHot:
		return ns == vlsup.NamespaceHot
	case catalog.OwnerCold:
		return ns == vlsup.NamespaceCold
	case catalog.OwnerArchive:
		return ns == vlsup.NamespaceRehydrate
	default:
		return false
	}
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
	if ns != vlsup.NamespaceHot && ns != vlsup.NamespaceCold && ns != vlsup.NamespaceRehydrate {
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

func (s *Service) LogCutoverReadiness(_ context.Context, req *workerpb.LogCutoverReadinessRequest) (*workerpb.LogCutoverReadinessResponse, error) {
	resp := &workerpb.LogCutoverReadinessResponse{RequestId: req.GetRequestId()}
	if s.cutover == nil || s.query == nil {
		resp.Error = unsupportedErr("log cutover readiness is not configured")
		return resp, nil
	}
	caps := s.query.GetCapabilities(query.CapabilitiesRequest{ProtocolVersion: req.GetProtocolVersion(), RequestID: req.GetRequestId()})
	required := map[string]bool{"query_view": false, "search": false, "stats": false}
	for _, capability := range caps.Capabilities {
		if _, ok := required[capability]; ok {
			required[capability] = true
		}
	}
	confirmed := caps.Supported
	for _, present := range required {
		confirmed = confirmed && present
	}
	ready, cutoff, reasons := s.cutover.Readiness()
	resp.CapabilityConfirmed, resp.LedgerReady = confirmed, ready
	resp.Reasons = append(resp.Reasons, reasons...)
	if !confirmed {
		resp.Reasons = append(resp.Reasons, "required_log_capabilities_missing")
	}
	if !cutoff.IsZero() {
		resp.CutoffTimeUtc = cutoff.UTC().Format(time.RFC3339Nano)
	}
	return resp, nil
}

func (s *Service) LogResolveIngestGaps(_ context.Context, _ *workerpb.LogResolveIngestGapsRequest) (*workerpb.LogTaskResponse, error) {
	if s.gapResolver == nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: unsupportedErr("ingest gap resolution is not configured")}, nil
	}
	if err := s.gapResolver.ResolveCoveredGaps(); err != nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_NOT_READY, Message: err.Error()}}, nil
	}
	return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
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

func taskStateFromString(state string) workerpb.LogTaskState {
	switch strings.ToUpper(strings.TrimSpace(state)) {
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
