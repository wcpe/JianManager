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

type IngestGapResolver interface {
	ResolveCoveredGaps() error
	// ResolveCoveredGapsForSource 是显式人工确认（**放弃裁定**）路径：只解指名源的缺口。
	// operator 是做出裁定的认证主体，为空一律拒绝（放弃不可无痕）。
	ResolveCoveredGapsForSource(storageNamespace, operator string) error
}

// IngestPositionGap 是「已放弃位置」查询面 DTO（与 logcoord 的 coverage 语义无关）。
type IngestPositionGap struct {
	StorageNamespace string
	From             uint64
	To               uint64
	ReasonCode       string
	Operator         string
	AtUTC            string
}

// IngestAbandonmentProvider 提供「已被人工裁定永久丢失」的源位置区间。
//
// 与 logcoord 的分区可查询性**分开**：本接口只回答「分区内部哪些位置永久缺失」，
// 不参与、也不修改 coverage 的状态判定。缺了它，下游查询会把"永久丢失"当成"本来就没有"，
// 即静默跳过。
type IngestAbandonmentProvider interface {
	AbandonedRanges(targetIDs []string) []IngestPositionGap
}

// IngestAbandonmentFunc 把普通函数适配成 IngestAbandonmentProvider
// （与既有 CutoverReadinessFunc 同一模式：让 apps/worker 做跨包适配，服务层不反向依赖 ingest）。
type IngestAbandonmentFunc func(targetIDs []string) []IngestPositionGap

// AbandonedRanges 实现 IngestAbandonmentProvider。
func (f IngestAbandonmentFunc) AbandonedRanges(targetIDs []string) []IngestPositionGap {
	if f == nil {
		return nil
	}
	return f(targetIDs)
}

// IngestArchiveImporter 是「手动触发归档导入」的提供者（由 ingest.Manager 满足）。
type IngestArchiveImporter interface {
	// ImportArchivesNow 扫描并导入待导入归档，返回 (扫描数, 导入数)。operator 为空必须拒绝。
	ImportArchivesNow(storageNamespace, operator string) (int, int, error)
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
	// abandonments 提供「已放弃位置」标记（可空：未接线时不返回该标记，而不是编造空成功）。
	abandonments IngestAbandonmentProvider
	// archiveImporter 提供手动归档导入入口（可空 = 未接线，返回明确不支持）。
	archiveImporter IngestArchiveImporter
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
func (s *Service) SetIngestAbandonmentProvider(p IngestAbandonmentProvider) {
	s.abandonments = p
}

// SetIngestArchiveImporter 注入手动归档导入入口。
func (s *Service) SetIngestArchiveImporter(i IngestArchiveImporter) { s.archiveImporter = i }

// LogImportArchives 手动触发归档导入扫描（操作人必需，审计留痕）。
func (s *Service) LogImportArchives(ctx context.Context, req *workerpb.LogImportArchivesRequest) (*workerpb.LogTaskResponse, error) {
	if s.archiveImporter == nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: unsupportedErr("ingest archive import is not configured")}, nil
	}
	// 与放弃裁定同一口径：状态改变类动作必须带操作人，取不到即拒绝（不可无痕）。
	operator := operatorFromContext(ctx)
	if operator == "" {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: unsupportedErr(fmt.Sprintf(
				"手动导入归档需要操作人认证主体：请在 incoming gRPC metadata 设置 %q", OperatorMetadataKey))}, nil
	}
	scanned, imported, err := s.archiveImporter.ImportArchivesNow(strings.TrimSpace(req.GetStorageNamespace()), operator)
	if err != nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_ERROR_UNSPECIFIED, Message: err.Error()}}, nil
	}
	slog.Info("已执行手动归档导入", "operator", operator,
		"storageNamespace", req.GetStorageNamespace(), "scanned", scanned, "imported", imported)
	return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_SUCCEEDED}, nil
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
	// 建 View 即确定后续读取范围，故授权目标不得为空（否则该 View 覆盖全量分区）。
	if err := authorizeScope(qreq.AuthorizedTargets, ""); err != nil {
		return &workerpb.LogCreateViewResponse{
			RequestId: qreq.RequestID,
			Error:     &workerpb.LogError{Code: workerpb.LogErrorCode_LOG_UNAUTHORIZED, Message: err.Error()},
		}, nil
	}
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
	if err := authorizeScope(qreq.AuthorizedTargets, viewIDOf(qreq.View, qreq.View)); err != nil {
		return unauthorizedSearch(req, err), nil
	}
	resp := s.query.Search(ctx, qreq)
	s.fillClosedVisibleSeq(&resp.Coverage, viewIDOf(resp.View, qreq.View))
	out := grpcmap.SearchResponseToProto(resp)
	// 二次回填：确保 proto 侧 targets 携带 planner view 的 closed_visible_seq。
	s.fillProtoClosedVisibleSeq(out.GetCoverage(), out.GetView().GetViewId())
	s.fillIngestPositionGaps(out, qreq.AuthorizedTargets)
	return out, nil
}

// fillIngestPositionGaps 回填「已放弃位置」标记：本响应涉及的命名空间里，哪些源位置区间
// 已被人工裁定为永久丢失。
//
// 为什么必须单独回填而不是并进 coverage：两者语义不同（见 proto 注释），把"永久丢失"塞进
// coverage 的枚举或在 partial_reasons 里混写，会让下游误以为那是"暂时查不到"，
// 从而按重试处理一个永远不会恢复的区间——那正是要消灭的静默跳过。
// 目标集取请求的授权目标（形如 storage_namespace 或 storage_namespace/YYYY-MM-DD）；
// **空 = 本 Worker 全部**（与 planner「空 = 全部 Catalog 分区」同一口径），由提供者展开为全源。
func (s *Service) fillIngestPositionGaps(out *workerpb.LogSearchResponse, targetIDs []string) {
	// 注意：**不因 targetIDs 为空而提前返回**。空在 planner 语义下是「本 Worker 全部」
	// （见 query.Planner 的 TargetIDs 说明），若此处提前返回，一次不带授权目标的查询就会
	// 拿到"没有永久丢失"的空结果——那正是要消灭的静默跳过（标记看似实现了、实际永远为空）。
	if s.abandonments == nil || out == nil {
		return
	}
	gaps := s.abandonments.AbandonedRanges(targetIDs)
	if len(gaps) == 0 {
		return
	}
	out.IngestPositionGaps = make([]*workerpb.IngestPositionGap, 0, len(gaps))
	for _, g := range gaps {
		out.IngestPositionGaps = append(out.IngestPositionGaps, &workerpb.IngestPositionGap{
			StorageNamespace: g.StorageNamespace,
			From:             g.From,
			To:               g.To,
			ReasonCode:       g.ReasonCode,
			Operator:         g.Operator,
			AtUtc:            g.AtUTC,
		})
	}
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

func (s *Service) LogResolveIngestGaps(ctx context.Context, req *workerpb.LogResolveIngestGapsRequest) (*workerpb.LogTaskResponse, error) {
	if s.gapResolver == nil {
		return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
			Error: unsupportedErr("ingest gap resolution is not configured")}, nil
	}
	var err error
	if ns := strings.TrimSpace(req.GetStorageNamespace()); ns != "" {
		// 显式人工确认（**放弃裁定**）：只解指名源（含自动路径拒绝的 Raw 写失败缺口，理由见 Manager 注释）。
		// 操作人必须来自认证上下文：取不到就拒绝——放弃是唯一允许回收链跨过永久空洞的动作，
		// 无痕放弃等于给静默丢日志开后门。
		operator := operatorFromContext(ctx)
		if operator == "" {
			return &workerpb.LogTaskResponse{State: workerpb.LogTaskState_LOG_TASK_FAILED,
				Error: unsupportedErr(fmt.Sprintf(
					"放弃缺口需要操作人认证主体：请在 incoming gRPC metadata 设置 %q", OperatorMetadataKey))}, nil
		}
		err = s.gapResolver.ResolveCoveredGapsForSource(ns, operator)
	} else {
		// 不传命名空间：与既有行为逐字一致（整节点自动解）。
		err = s.gapResolver.ResolveCoveredGaps()
	}
	if err != nil {
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
