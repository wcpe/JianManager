package grpcsvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"github.com/wcpe/JianManager/internal/worker/logutil"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ---- fakes（无网络、无真实 planner 依赖）----

type fakeQuery struct {
	caps        query.CapabilitiesResponse
	search      query.SearchResponse
	stats       query.StatsResponse
	tail        query.TailResponse
	searchCalls int
	statsCalls  int
	tailCalls   int
	lastReq     query.QueryRequest
	lastGroupBy []string
	lastMode    query.TailMode
}

func (f *fakeQuery) GetCapabilities(query.CapabilitiesRequest) query.CapabilitiesResponse {
	return f.caps
}

func (f *fakeQuery) OpenView(req query.QueryRequest) query.SearchResponse {
	f.lastReq = req
	return f.search
}

func (f *fakeQuery) Search(_ context.Context, req query.QueryRequest) query.SearchResponse {
	f.searchCalls++
	f.lastReq = req
	return f.search
}

func (f *fakeQuery) Stats(_ context.Context, req query.QueryRequest, groupBy []string) query.StatsResponse {
	f.statsCalls++
	f.lastReq = req
	f.lastGroupBy = groupBy
	return f.stats
}

func (f *fakeQuery) Tail(_ context.Context, req query.QueryRequest, mode query.TailMode) query.TailResponse {
	f.tailCalls++
	f.lastReq = req
	f.lastMode = mode
	return f.tail
}

type fakeContextQuery struct {
	fakeQuery
	received context.Context
}

func (f *fakeContextQuery) OpenViewContext(ctx context.Context, req query.QueryRequest) query.SearchResponse {
	f.received = ctx
	return f.OpenView(req)
}

func TestLogCreateViewPreservesCallerContextWhenSupported(t *testing.T) {
	queryService := &fakeContextQuery{fakeQuery: fakeQuery{caps: query.CapabilitiesResponse{Supported: true}}}
	svc := New(queryService, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := svc.LogCreateView(ctx, &workerpb.LogCreateViewRequest{Query: &workerpb.LogQueryRequestBase{
		RequestId: "context-view", AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{"instance"}},
	}})
	require.NoError(t, err)
	require.Same(t, ctx, queryService.received, "真实Worker建view入口必须保留调用deadline/cancel，不能重新改用Background")
}

type fakeViews struct {
	views map[string]*query.QueryView
}

func (v *fakeViews) GetView(id string) (*query.QueryView, bool) {
	if v == nil || v.views == nil {
		return nil, false
	}
	view, ok := v.views[id]
	return view, ok
}

type fakeTailStream struct {
	grpc.ServerStream
	ctx   context.Context
	items []*workerpb.LogEvent
}

func (s *fakeTailStream) Context() context.Context { return s.ctx }

func (s *fakeTailStream) Send(ev *workerpb.LogEvent) error {
	s.items = append(s.items, ev)
	return nil
}

func sampleEV(msg string) logtypes.Event {
	src := logtypes.SourceIdentity{LogSourceID: "src", SourceGeneration: "g1", ParserVersion: "p1"}
	return logtypes.BuildEvent(src, logtypes.RecordRange{Start: 1, End: 2},
		"2026-09-20T10:00:00Z", "2026-09-20T10:00:00Z", "INFO", "stdout", msg)
}

func searchReq(id string) *workerpb.LogSearchRequest {
	return &workerpb.LogSearchRequest{
		Query: &workerpb.LogQueryRequestBase{
			RequestId:       id,
			ProtocolVersion: query.DefaultProtocolVersion,
			Budget:          &workerpb.LogQueryBudget{Limit: 10},
			View:            &workerpb.LogQueryViewRef{ViewId: "view-1"},
		},
	}
}

// ---- GetLogCapabilities ----

func TestGetLogCapabilities_SupportedWhenPlannerReady(t *testing.T) {
	fq := &fakeQuery{caps: query.CapabilitiesResponse{
		Supported:       true,
		ProtocolVersion: query.DefaultProtocolVersion,
		Capabilities:    []string{"query_view", "coverage", "quality", "search", "stats"},
		MaxLimit:        query.DefaultMaxLimit,
		Cancellation:    true,
		BuildID:         "build-1",
	}}
	svc := New(fq, &fakeViews{})

	resp, err := svc.GetLogCapabilities(context.Background(), &workerpb.GetLogCapabilitiesRequest{
		ProtocolVersion: query.DefaultProtocolVersion,
	})
	require.NoError(t, err)
	require.True(t, resp.GetSupported())
	require.Equal(t, query.DefaultProtocolVersion, resp.GetProtocolVersion())
	require.Contains(t, resp.GetCapabilities(), "query_view")
	require.Contains(t, resp.GetCapabilities(), "search")
	require.EqualValues(t, query.DefaultMaxLimit, resp.GetMaxLimit())
	require.Empty(t, resp.GetUnsupportedReasons())
}

func TestGetLogCapabilities_DisabledWhenServiceOff(t *testing.T) {
	// 老 Worker 协商：服务禁用 → supported=false + LOG_UNSUPPORTED
	svc := NewDisabled()
	resp, err := svc.GetLogCapabilities(context.Background(), &workerpb.GetLogCapabilitiesRequest{
		ProtocolVersion: query.DefaultProtocolVersion,
	})
	require.NoError(t, err)
	require.False(t, resp.GetSupported())
	require.Len(t, resp.GetUnsupportedReasons(), 1)
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetUnsupportedReasons()[0].GetCode())
	require.Empty(t, resp.GetCapabilities())
}

func TestGetLogCapabilities_NilQueryService(t *testing.T) {
	svc := New(nil, nil)
	resp, err := svc.GetLogCapabilities(context.Background(), &workerpb.GetLogCapabilitiesRequest{})
	require.NoError(t, err)
	require.False(t, resp.GetSupported())
	require.Equal(t, query.DefaultProtocolVersion, resp.GetProtocolVersion())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetUnsupportedReasons()[0].GetCode())
}

func TestGetLogCapabilities_ToggleEnabled(t *testing.T) {
	fq := &fakeQuery{caps: query.CapabilitiesResponse{
		Supported: true, ProtocolVersion: query.DefaultProtocolVersion, MaxLimit: query.DefaultMaxLimit,
	}}
	svc := New(fq, nil)
	on, err := svc.GetLogCapabilities(context.Background(), &workerpb.GetLogCapabilitiesRequest{
		ProtocolVersion: query.DefaultProtocolVersion,
	})
	require.NoError(t, err)
	require.True(t, on.GetSupported())

	svc.SetEnabled(false)
	off, err := svc.GetLogCapabilities(context.Background(), &workerpb.GetLogCapabilitiesRequest{
		ProtocolVersion: query.DefaultProtocolVersion,
	})
	require.NoError(t, err)
	require.False(t, off.GetSupported())
}

// ---- LogSearch / Stats ----

func TestLogSearch_MapsResponseAndRequest(t *testing.T) {
	viewID := "view-1"
	fq := &fakeQuery{search: query.SearchResponse{
		RequestID: "r1",
		View:      &query.ViewRef{ViewID: viewID, OrderVersion: query.SortVersion},
		Coverage: query.Coverage{
			Complete: true,
			Targets: []query.TargetCoverage{{
				TargetID:          "ns/game-1/2026-09-20",
				State:             query.CoverageSuccess,
				ClosedVisibleSeq:  42,
				CatalogGeneration: 3,
			}},
		},
		Quality:    query.Quality{DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact},
		Items:      []logtypes.Event{sampleEV("hello")},
		Exhausted:  true,
		NextCursor: "",
	}}
	svc := New(fq, &fakeViews{})

	resp, err := svc.LogSearch(context.Background(), searchReq("r1"))
	require.NoError(t, err)
	require.Equal(t, "r1", resp.GetRequestId())
	require.Equal(t, viewID, resp.GetView().GetViewId())
	require.Equal(t, query.SortVersion, resp.GetView().GetOrderVersion())
	require.Nil(t, resp.GetError(), "successful search must not carry error")
	require.True(t, resp.GetExhausted())
	require.Len(t, resp.GetItems(), 1)
	require.Equal(t, "hello", resp.GetItems()[0].GetMessage())
	require.Equal(t, "42", resp.GetCoverage().GetTargets()[0].GetClosedVisibleSeq())
	require.Equal(t, "3", resp.GetCoverage().GetTargets()[0].GetCatalogGeneration())

	// request 反向映射进了 query 服务
	require.Equal(t, 1, fq.searchCalls)
	require.Equal(t, "r1", fq.lastReq.RequestID)
	require.EqualValues(t, 10, fq.lastReq.Budget.Limit)
	require.Equal(t, "view-1", fq.lastReq.View.ViewID)
	require.Equal(t, query.SortVersion, fq.lastReq.View.OrderVersion)
}

func TestIngestPositionGapSourceIdentityContract(t *testing.T) {
	fields := (&workerpb.IngestPositionGap{}).ProtoReflect().Descriptor().Fields()
	require.NotNil(t, fields.ByNumber(7), "协议必须保留真实日志源身份")
	require.Equal(t, "log_source_id", string(fields.ByNumber(7).Name()))
	require.NotNil(t, fields.ByNumber(8), "协议必须保留真实源代次")
	require.Equal(t, "source_generation", string(fields.ByNumber(8).Name()))
}

func TestLogSearch_UnimplementedRangeClient_LOGUnsupported(t *testing.T) {
	// 缺失 RangeClient：query 面返回 LOG_UNSUPPORTED，grpcsvc 必须原样透传，不得空成功。
	fq := &fakeQuery{search: query.SearchResponse{
		RequestID: "u1",
		View:      &query.ViewRef{ViewID: "view-1", OrderVersion: query.SortVersion},
		Coverage: query.Coverage{
			Complete:         false,
			PartialReasons:   []string{query.ReasonUnsupported},
			EnumerationState: query.EnumOpen,
			Targets: []query.TargetCoverage{{
				TargetID: "ns/game-1/2026-09-20",
				State:    query.CoverageNotReady,
				Reasons:  []string{query.ReasonUnsupported},
			}},
		},
		Err: &query.QueryError{
			Code:    query.ErrCodeUnsupported,
			Message: "range client search unimplemented",
		},
	}}
	svc := New(fq, nil)

	resp, err := svc.LogSearch(context.Background(), searchReq("u1"))
	require.NoError(t, err, "protocol error stays in response envelope, not transport")
	require.NotNil(t, resp.GetError())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetError().GetCode())
	require.False(t, resp.GetCoverage().GetComplete(), "unsupported must not look like complete empty success")
	require.False(t, resp.GetExhausted() && len(resp.GetItems()) == 0 && resp.GetError() == nil)
	require.Contains(t, resp.GetCoverage().GetPartialReasons(), query.ReasonUnsupported)
}

func TestLogSearch_DisabledService_LOGUnsupported(t *testing.T) {
	svc := NewDisabled()
	resp, err := svc.LogSearch(context.Background(), searchReq("d1"))
	require.NoError(t, err)
	require.NotNil(t, resp.GetError())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetError().GetCode())
	require.Equal(t, "d1", resp.GetRequestId())
	require.False(t, resp.GetCoverage().GetComplete())
}

// M-9 回归：Worker 边界必须拒绝「空授权 + 无 View」的请求。
//
// planner 把空 TargetIDs 解释为「本 Worker 全部 Catalog 分区」，故该组合等于一次全量
// 读取原语：任何能直接调用 Worker query 服务的组件都能借此越过目标收敛。CP 正常路径
// 会先 fail-closed（logcoord.ValidateQuery），但 Worker 自身不应依赖调用方守规矩。
func TestLogQuery_EmptyAuthorizationWithoutViewIsRejected(t *testing.T) {
	fq := &fakeQuery{}
	svc := New(fq, nil)

	t.Run("search", func(t *testing.T) {
		resp, err := svc.LogSearch(context.Background(), &workerpb.LogSearchRequest{
			Query: &workerpb.LogQueryRequestBase{RequestId: "noauth-1", ProtocolVersion: query.DefaultProtocolVersion},
		})
		require.NoError(t, err)
		require.NotNil(t, resp.GetError(), "空授权且无 View 必须拒绝，不得全量读取")
		require.Equal(t, workerpb.LogErrorCode_LOG_UNAUTHORIZED, resp.GetError().GetCode())
		require.False(t, resp.GetCoverage().GetComplete())
	})

	t.Run("stats", func(t *testing.T) {
		resp, err := svc.LogStats(context.Background(), &workerpb.LogStatsRequest{
			Query: &workerpb.LogQueryRequestBase{RequestId: "noauth-2", ProtocolVersion: query.DefaultProtocolVersion},
		})
		require.NoError(t, err)
		require.NotNil(t, resp.GetError())
		require.Equal(t, workerpb.LogErrorCode_LOG_UNAUTHORIZED, resp.GetError().GetCode())
	})

	t.Run("create_view", func(t *testing.T) {
		resp, err := svc.LogCreateView(context.Background(), &workerpb.LogCreateViewRequest{
			Query: &workerpb.LogQueryRequestBase{RequestId: "noauth-3", ProtocolVersion: query.DefaultProtocolVersion},
		})
		require.NoError(t, err)
		require.NotNil(t, resp.GetError(), "建 View 即确定读取范围，空授权不得放行")
		require.Equal(t, workerpb.LogErrorCode_LOG_UNAUTHORIZED, resp.GetError().GetCode())
	})
}

func TestLogStats_MapsPointsAndClosedVisibleSeq(t *testing.T) {
	viewID := "view-stats"
	views := &fakeViews{views: map[string]*query.QueryView{
		viewID: {
			ViewID:           viewID,
			OrderVersion:     query.SortVersion,
			ClosedVisibleSeq: map[string]uint64{"ns/game-1/2026-09-20": 77},
			Generations:      map[string]uint64{"ns/game-1/2026-09-20": 5},
		},
	}}
	// coverage target CVS=0 → 必须从 planner view 回填
	fq := &fakeQuery{stats: query.StatsResponse{
		RequestID: "st1",
		View:      &query.ViewRef{ViewID: viewID, OrderVersion: query.SortVersion},
		Coverage: query.Coverage{
			Complete: true,
			Targets: []query.TargetCoverage{{
				TargetID: "ns/game-1/2026-09-20",
				State:    query.CoverageSuccess,
			}},
		},
		Quality: query.Quality{StatsQuality: query.StatsExact},
		Points: []query.StatsPoint{{
			Dimensions: map[string]string{"level": "ERROR"},
			Count:      2,
			Sum:        20,
			Min:        8,
			Max:        12,
		}},
	}}
	svc := New(fq, views)

	resp, err := svc.LogStats(context.Background(), &workerpb.LogStatsRequest{
		Query: &workerpb.LogQueryRequestBase{
			RequestId: "st1", ProtocolVersion: query.DefaultProtocolVersion,
			// 显式授权目标：Worker 边界已要求非空（无 View 时空授权=全量读取原语，M-9）。
			AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{"ns/game-1/2026-09-20"}},
		},
		GroupBy: []string{"level"},
	})
	require.NoError(t, err)
	require.Nil(t, resp.GetError())
	require.Len(t, resp.GetPoints(), 1)
	require.InDelta(t, 10.0, resp.GetPoints()[0].GetAvg(), 1e-9)
	require.Equal(t, []string{"level"}, fq.lastGroupBy)
	// ClosedVisibleSeq 从 planner view 回填
	require.Equal(t, "77", resp.GetCoverage().GetTargets()[0].GetClosedVisibleSeq())
	require.Equal(t, "5", resp.GetCoverage().GetTargets()[0].GetCatalogGeneration())
}

func TestLogStats_Unimplemented_LOGUnsupported(t *testing.T) {
	fq := &fakeQuery{stats: query.StatsResponse{
		RequestID: "u2",
		Coverage: query.Coverage{
			Complete:       false,
			PartialReasons: []string{query.ReasonUnsupported},
		},
		Quality: query.Quality{StatsQuality: query.StatsUnavailable},
		Err: &query.QueryError{
			Code:    query.ErrCodeUnsupported,
			Message: "stats unimplemented on this worker",
		},
	}}
	svc := New(fq, nil)
	resp, err := svc.LogStats(context.Background(), &workerpb.LogStatsRequest{
		Query: &workerpb.LogQueryRequestBase{
			RequestId: "u2",
			// 显式授权目标：本用例测的是 Unimplemented 路径，需先通过边界授权校验。
			AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{"ns/game-1/2026-09-20"}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetError())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetError().GetCode())
	require.Equal(t, workerpb.LogStatsQuality_LOG_QUALITY_STATS_UNAVAILABLE, resp.GetQuality().GetStatsQuality())
	require.False(t, resp.GetCoverage().GetComplete())
}

// ---- Fields / Facets stubs ----

func TestLogFields_StubReturnsLOGUnsupported(t *testing.T) {
	fq := &fakeQuery{caps: query.CapabilitiesResponse{Supported: true, ProtocolVersion: query.DefaultProtocolVersion}}
	svc := New(fq, nil)
	resp, err := svc.LogFields(context.Background(), &workerpb.LogFieldsRequest{
		Query: &workerpb.LogQueryRequestBase{RequestId: "f1"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetError())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetError().GetCode())
	require.Empty(t, resp.GetFields(), "must not return empty field list as success")
}

func TestLogFacets_StubReturnsLOGUnsupported(t *testing.T) {
	fq := &fakeQuery{}
	svc := New(fq, nil)
	resp, err := svc.LogFacets(context.Background(), &workerpb.LogFacetsRequest{
		Query:      &workerpb.LogQueryRequestBase{RequestId: "fa1"},
		Dimensions: []string{"level"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetError())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetError().GetCode())
	require.Empty(t, resp.GetValues())
}

func TestLogFields_DisabledService(t *testing.T) {
	svc := NewDisabled()
	resp, err := svc.LogFields(context.Background(), &workerpb.LogFieldsRequest{
		Query: &workerpb.LogQueryRequestBase{RequestId: "f2"},
	})
	require.NoError(t, err)
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetError().GetCode())
}

func TestLogRuntimeInstallRequiresAssetAndInvokesManagedInstaller(t *testing.T) {
	svc := NewDisabled()
	called := false
	svc.SetRuntimeInstaller(RuntimeInstallFunc(func(_ context.Context, packageURL, packageSHA string) error {
		called = true
		require.Equal(t, "https://cp.example/log-vl-assets/package?token=redacted", packageURL)
		require.Equal(t, "approved-hash", packageSHA)
		return nil
	}))
	missing, err := svc.LogRuntimeControl(context.Background(), &workerpb.LogRuntimeControlRequest{Action: "install"})
	require.NoError(t, err)
	require.NotNil(t, missing.GetError())
	require.False(t, called)

	installed, err := svc.LogRuntimeControl(context.Background(), &workerpb.LogRuntimeControlRequest{
		Action: "install", PackageUrl: "https://cp.example/log-vl-assets/package?token=redacted", PackageSha256: "approved-hash",
	})
	require.NoError(t, err)
	require.Nil(t, installed.GetError())
	require.True(t, called)
}

type runtimeStatusProbe struct{ healthCalls int }

func (p *runtimeStatusProbe) Status(ns vlsup.Namespace) (vlsup.InstanceStatus, error) {
	state := vlsup.StateStopped
	if ns == vlsup.NamespaceHot {
		state = vlsup.StateRunning
	}
	return vlsup.InstanceStatus{Namespace: ns, State: state, HealthOK: ns == vlsup.NamespaceHot && p.healthCalls > 0}, nil
}

func (p *runtimeStatusProbe) Health(context.Context, vlsup.Namespace) error {
	p.healthCalls++
	return nil
}
func (*runtimeStatusProbe) Start(context.Context, vlsup.Namespace) error { return nil }
func (*runtimeStatusProbe) Stop(context.Context, vlsup.Namespace) error  { return nil }

func TestLogRuntimeStatusSeparatesHealthFromCatalogReadiness(t *testing.T) {
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22"}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	require.NoError(t, cat.Put(rec))
	probe := &runtimeStatusProbe{}
	svc := NewDisabled()
	svc.SetRuntimeController(probe)
	svc.SetRuntimeCatalog(cat)
	initial, err := svc.LogRuntimeStatus(context.Background(), &workerpb.LogRuntimeStatusRequest{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, probe.healthCalls, 1)
	// 2026-10-29 起冷热双层已废除：vlsup 只保留唯一的 NamespaceHot（见 vlsup/cmdline.go），
	// LogRuntimeStatus 也只遍历那一个。所以必须显式钉住个数——否则一旦实现又长出第二个实例，
	// “哪些归属算在哪”这个前提就静默变化了。
	require.Len(t, initial.GetInstances(), 1, "只剩唯一受管 VL 实例（冷热双层已废除）")
	require.True(t, initial.GetInstances()[0].GetHealthOk())
	require.True(t, initial.GetInstances()[0].GetPartitionRecoveryComplete())
	require.False(t, initial.GetInstances()[0].GetQueryReady(), "health without a published projection is not query readiness")

	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion: "manifest-1", CoverageComplete: true, QueryLocationDirID: "hot-g1", QueryGeneration: 1,
	}
	require.NoError(t, cat.Put(rec))
	after, err := svc.LogRuntimeStatus(context.Background(), &workerpb.LogRuntimeStatusRequest{})
	require.NoError(t, err)
	require.True(t, after.GetInstances()[0].GetQueryReady())

	// 第二条断言的语义在冷热废除后仍然存在，只是从“另一个实例”换成了“同一实例内的清单边界”：
	// 空 catalog（无任何记录）不得自称已恢复/查询就绪——`seen` 为假时两侧都必须为 false，
	// 否则“什么都没有”会被当成“全部已就绪”返回给调用方。
	//
	// 为什么不用“另一个 namespace 的记录”来挖：冷热双层废除后所有**已知**归属（hot/cold/archive）
	// 的物理数据都在唯一那个 VL 里（见 ownerUsesNamespace 的注释），而未知归属会被 catalog.Put 直接拒绝。
	// 所以边界在 catalog 清单这一维：有记录且已发布 ⇒ 就绪；无记录 ⇒ 不得自称就绪。
	recovered, queryReady := runtimeCatalogReadiness(catalog.New(nil), vlsup.NamespaceHot)
	require.False(t, recovered, "空 catalog 不得自称已恢复（seen=false）")
	require.False(t, queryReady, "空 catalog 不得自称查询完整")
	require.False(t, ownerUsesNamespace(catalog.Owner(""), vlsup.NamespaceHot),
		"空归属没有任何实例负责 ⇒ 不得计入")
}

func TestLogMigratePartitionValidatesAndDelegates(t *testing.T) {
	svc := NewDisabled()
	called := false
	svc.SetPartitionMigrator(PartitionMigrateFunc(func(_ context.Context, namespace, day, target string) error {
		called = true
		require.Equal(t, "node:1", namespace)
		require.Equal(t, "2026-09-23", day)
		require.Equal(t, "cold", target)
		return nil
	}))
	bad, err := svc.LogMigratePartition(context.Background(), &workerpb.LogMigratePartitionRequest{StorageNamespace: "node:1", UtcDay: "bad", TargetTier: "cold"})
	require.NoError(t, err)
	require.NotNil(t, bad.GetError())
	require.False(t, called)
	ok, err := svc.LogMigratePartition(context.Background(), &workerpb.LogMigratePartitionRequest{StorageNamespace: "node:1", UtcDay: "2026-09-23", TargetTier: "cold"})
	require.NoError(t, err)
	require.Nil(t, ok.GetError())
	require.True(t, called)
}

func TestLogTailPartialCoverageCannotBecomeSuccessfulStream(t *testing.T) {
	for _, items := range [][]logtypes.Event{nil, {sampleEV("partial")}} {
		fq := &fakeQuery{tail: query.TailResponse{Items: items,
			Coverage: query.Coverage{Complete: false, PartialReasons: []string{query.ReasonArchiveNotRestored}}}}
		stream := &fakeTailStream{ctx: context.Background()}
		err := New(fq, nil).LogTail(&workerpb.LogTailRequest{Mode: workerpb.LogTailMode_LOG_TAIL_FOLLOW_LIVE}, stream)
		require.Error(t, err)
		require.Contains(t, err.Error(), query.ReasonArchiveNotRestored)
		require.Empty(t, stream.items)
	}
}

func TestLogTail_StreamsEventsOnSuccess(t *testing.T) {
	fq := &fakeQuery{tail: query.TailResponse{
		RequestID: "tl1",
		Coverage:  query.Coverage{Complete: true},
		Mode:      query.TailViewBounded,
		Items:     []logtypes.Event{sampleEV("t1"), sampleEV("t2")},
		Exhausted: true,
	}}
	svc := New(fq, nil)
	stream := &fakeTailStream{ctx: context.Background()}

	err := svc.LogTail(&workerpb.LogTailRequest{
		Query: &workerpb.LogQueryRequestBase{RequestId: "tl1", ProtocolVersion: query.DefaultProtocolVersion},
		Mode:  workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED,
	}, stream)
	require.NoError(t, err)
	require.Len(t, stream.items, 2)
	require.Equal(t, "t1", stream.items[0].GetMessage())
	require.Equal(t, query.TailViewBounded, fq.lastMode)
}

func TestLogTail_UnimplementedReturnsStatusNotEmptySuccess(t *testing.T) {
	fq := &fakeQuery{tail: query.TailResponse{
		RequestID: "u3",
		Coverage: query.Coverage{
			Complete:       false,
			PartialReasons: []string{query.ReasonUnsupported},
		},
		Err: &query.QueryError{
			Code:    query.ErrCodeUnsupported,
			Message: "FOLLOW_LIVE unimplemented",
		},
	}}
	svc := New(fq, nil)
	stream := &fakeTailStream{ctx: context.Background()}

	err := svc.LogTail(&workerpb.LogTailRequest{
		Query: &workerpb.LogQueryRequestBase{RequestId: "u3"},
		Mode:  workerpb.LogTailMode_LOG_TAIL_FOLLOW_LIVE,
	}, stream)
	require.Error(t, err, "unsupported tail must not stream empty success")
	require.Contains(t, err.Error(), string(query.ErrCodeUnsupported))
	require.Empty(t, stream.items)
}

func TestLogTail_DisabledService(t *testing.T) {
	svc := NewDisabled()
	stream := &fakeTailStream{ctx: context.Background()}
	err := svc.LogTail(&workerpb.LogTailRequest{
		Query: &workerpb.LogQueryRequestBase{RequestId: "d3"},
		Mode:  workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED,
	}, stream)
	require.Error(t, err)
	require.Contains(t, err.Error(), string(query.ErrCodeUnsupported))
}

// ---- 集成：真实 query.Service + planner，无网络 ----

func TestLogSearch_RealQueryService_ClosedVisibleSeqFromPlannerView(t *testing.T) {
	key := catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}
	c := catalog.New(nil)
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:    "pv-1",
		CoverageComplete:   true,
		ClosedVisibleSeq:   map[string]uint64{key.String(): 88},
		QueryLocationDirID: "hot-g1",
		QueryGeneration:    1,
	}
	require.NoError(t, c.Put(rec))

	client := query.FuncRangeClient{
		SearchFn: func(_ context.Context, _ query.AuthoritativeRange, _ query.RangeQuery) (query.RangeResult, error) {
			return query.RangeResult{
				Items: []logtypes.Event{sampleEV("real")},
			}, nil
		},
	}
	// Unimplemented client 路径另测；这里用 fake client 验证 ClosedVisibleSeq 回填。
	planner := query.NewPlanner(c, nil)
	qs := query.NewService(planner, client, "build-x")
	svc := New(qs, planner)

	caps, err := svc.GetLogCapabilities(context.Background(), &workerpb.GetLogCapabilitiesRequest{
		ProtocolVersion: query.DefaultProtocolVersion,
	})
	require.NoError(t, err)
	require.True(t, caps.GetSupported(), "planner ready → supported=true")
	require.Contains(t, caps.GetCapabilities(), "search")
	require.EqualValues(t, query.DefaultMaxLimit, caps.GetMaxLimit())

	resp, err := svc.LogSearch(context.Background(), &workerpb.LogSearchRequest{
		Query: &workerpb.LogQueryRequestBase{
			RequestId:       "real-1",
			ProtocolVersion: query.DefaultProtocolVersion,
			Budget:          &workerpb.LogQueryBudget{Limit: 10},
			// 显式授权目标：Worker 边界已要求非空（无 View 时空授权=全量读取原语，M-9）。
			AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{key.String()}},
		},
	})
	require.NoError(t, err)
	require.Nil(t, resp.GetError())
	require.Len(t, resp.GetItems(), 1)
	require.True(t, resp.GetCoverage().GetComplete())
	require.NotEmpty(t, resp.GetCoverage().GetTargets())
	require.Equal(t, key.String(), resp.GetCoverage().GetTargets()[0].GetTargetId())
	require.Equal(t, "88", resp.GetCoverage().GetTargets()[0].GetClosedVisibleSeq())
	require.Equal(t, "1", resp.GetCoverage().GetTargets()[0].GetCatalogGeneration())
	require.Equal(t, query.SortVersion, resp.GetView().GetOrderVersion())
}

func TestLogSearch_RealQueryService_UnimplementedClient(t *testing.T) {
	key := catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}
	c := catalog.New(nil)
	record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	record.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "fixture-v1", CoverageComplete: true,
		QueryLocationDirID: "hot-g1", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 100}}
	require.NoError(t, c.Put(record))

	planner := query.NewPlanner(c, nil)
	qs := query.NewService(planner, nil, "old-worker") // nil → UnimplementedRangeClient
	svc := New(qs, planner)

	resp, err := svc.LogSearch(context.Background(), &workerpb.LogSearchRequest{
		Query: &workerpb.LogQueryRequestBase{
			RequestId:       "old-1",
			ProtocolVersion: query.DefaultProtocolVersion,
			Budget:          &workerpb.LogQueryBudget{Limit: 10},
			// 显式授权目标：本用例测的是 Unimplemented 路径，需先通过边界授权校验。
			AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{key.String()}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetError())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, resp.GetError().GetCode())
	require.False(t, resp.GetCoverage().GetComplete())
	require.Contains(t, resp.GetCoverage().GetPartialReasons(), query.ReasonUnsupported)
}

func TestLogSearch_RealQueryService_ProtocolMismatchCaps(t *testing.T) {
	key := catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}
	c := catalog.New(nil)
	record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	record.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "fixture-v1", CoverageComplete: true,
		QueryLocationDirID: "hot-g1", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 100}}
	require.NoError(t, c.Put(record))
	planner := query.NewPlanner(c, nil)
	qs := query.NewService(planner, nil, "b")
	svc := New(qs, planner)

	// 协议不匹配：query 面 supported=false → grpcsvc 透传
	caps, err := svc.GetLogCapabilities(context.Background(), &workerpb.GetLogCapabilitiesRequest{
		ProtocolVersion: "log-query/0",
	})
	require.NoError(t, err)
	require.False(t, caps.GetSupported())
	require.NotEmpty(t, caps.GetUnsupportedReasons())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, caps.GetUnsupportedReasons()[0].GetCode())
}

// 编译期断言：Service 满足 workerpb.WorkerServiceServer 子集（嵌入 Unimplemented）。
var _ workerpb.WorkerServiceServer = (*Service)(nil)

// 归档任务状态映射：logutil.TaskState 的取值自带 LOG_TASK_ 前缀，RPC 边界必须
// 剥掉前缀后再匹配。只认裸值时带前缀的取值会全部落入 default → 成功回灌被报成
// FAILED 且 error 为空，调用方无法用 state 判定成功。
//
// 转红方式（实测）：删掉 taskStateFromString 里的 strings.TrimPrefix 归一化，
// 全部带前缀用例立刻红；恢复后转绿。
func TestTaskStateFromString_AllTaskStates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want workerpb.LogTaskState
	}{
		// 全部带前缀形态（生产路径的真实取值）。
		// 2026-11-10：archive 包随冷热分层删除，改写字面量——协议值本身未变。
		{"prefixed/QUEUED", "LOG_TASK_QUEUED", workerpb.LogTaskState_LOG_TASK_QUEUED},
		{"prefixed/RUNNING", "LOG_TASK_RUNNING", workerpb.LogTaskState_LOG_TASK_RUNNING},
		{"prefixed/SUCCEEDED", "LOG_TASK_SUCCEEDED", workerpb.LogTaskState_LOG_TASK_SUCCEEDED},
		{"prefixed/FAILED", "LOG_TASK_FAILED", workerpb.LogTaskState_LOG_TASK_FAILED},
		{"prefixed/CANCELLED", "LOG_TASK_CANCELLED", workerpb.LogTaskState_LOG_TASK_CANCELLED},
		// STALE 覆盖超时/租约过期，必须落到专门枚举，不能被吞成 FAILED。
		{"prefixed/STALE", "LOG_TASK_STALE", workerpb.LogTaskState_LOG_TASK_STALE},

		// 裸值形态（历史调用方/测试仍在传），剥前缀后语义必须一致。
		{"bare/QUEUED", "QUEUED", workerpb.LogTaskState_LOG_TASK_QUEUED},
		{"bare/RUNNING", "RUNNING", workerpb.LogTaskState_LOG_TASK_RUNNING},
		{"bare/SUCCEEDED", "SUCCEEDED", workerpb.LogTaskState_LOG_TASK_SUCCEEDED},
		{"bare/SUCCESS", "SUCCESS", workerpb.LogTaskState_LOG_TASK_SUCCEEDED},
		{"bare/FAILED", "FAILED", workerpb.LogTaskState_LOG_TASK_FAILED},
		{"bare/CANCELLED", "CANCELLED", workerpb.LogTaskState_LOG_TASK_CANCELLED},
		{"bare/CANCELED", "CANCELED", workerpb.LogTaskState_LOG_TASK_CANCELLED},
		{"bare/STALE", "STALE", workerpb.LogTaskState_LOG_TASK_STALE},

		// 大小写与首尾空白容错。
		{"normalize/lower-prefixed", " log_task_succeeded ", workerpb.LogTaskState_LOG_TASK_SUCCEEDED},
		{"normalize/lower-bare", "stale", workerpb.LogTaskState_LOG_TASK_STALE},

		// fail-closed：无法识别（含空值、重复前缀）仍归 FAILED，不得凭空报成功。
		{"unknown/empty", "", workerpb.LogTaskState_LOG_TASK_FAILED},
		{"unknown/garbage", "NOPE", workerpb.LogTaskState_LOG_TASK_FAILED},
		{"unknown/double-prefix", "LOG_TASK_LOG_TASK_SUCCEEDED", workerpb.LogTaskState_LOG_TASK_FAILED},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, taskStateFromString(tc.in))
		})
	}

	// 反向不变式：每个带前缀状态映射出来的枚举名必须与它自身**同名**。
	// 这条断言让「新增状态但忘记映射」必然转红，而不是静默落 FAILED。
	for _, state := range []string{
		"LOG_TASK_QUEUED", "LOG_TASK_RUNNING", "LOG_TASK_SUCCEEDED",
		"LOG_TASK_FAILED", "LOG_TASK_CANCELLED", "LOG_TASK_STALE",
	} {
		got := taskStateFromString(state)
		require.Equal(t, state, got.String(), "状态 %q 应映射到同名 proto 枚举", state)
	}

	// 重点回归：成功不得被报成失败（缺陷原貌）。
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_SUCCEEDED, taskStateFromString("LOG_TASK_SUCCEEDED"))
	require.NotEqual(t, workerpb.LogTaskState_LOG_TASK_FAILED, taskStateFromString("LOG_TASK_STALE"),
		"STALE 有专门枚举，不能被吞成 FAILED")
}

// fakeArchiveQuerier 在 fakeQuery 之上补出 ArchiveQuerier 扩展。
type fakeArchiveQuerier struct {
	fakeQuery
	taskID string
	state  string
	err    *query.QueryError
}

func (f *fakeArchiveQuerier) Rehydrate(context.Context, query.QueryRequest, []string) query.ArchiveTaskResponse {
	return query.ArchiveTaskResponse{TaskID: f.taskID, State: f.state, Err: f.err}
}

func (f *fakeArchiveQuerier) ArchiveStatus(context.Context, query.QueryRequest, []string) query.ArchiveStatusResponse {
	return query.ArchiveStatusResponse{}
}

// LogRehydrate 端到端回归：archive 侧回带前缀的成功状态时，RPC 响应必须是
// SUCCEEDED 且 error 为空——否则调用方拿到的 {"state":3} 看起来像失败。
func TestLogRehydrate_PrefixedStateSurvivesRPCMapping(t *testing.T) {
	cases := []struct {
		name  string
		state logutil.TaskState
		want  workerpb.LogTaskState
	}{
		{"succeeded", logutil.TaskSucceeded, workerpb.LogTaskState_LOG_TASK_SUCCEEDED},
		{"running", logutil.TaskRunning, workerpb.LogTaskState_LOG_TASK_RUNNING},
		{"queued", logutil.TaskQueued, workerpb.LogTaskState_LOG_TASK_QUEUED},
		{"stale", logutil.TaskStale, workerpb.LogTaskState_LOG_TASK_STALE},
		{"cancelled", logutil.TaskCancelled, workerpb.LogTaskState_LOG_TASK_CANCELLED},
		{"failed", logutil.TaskFailed, workerpb.LogTaskState_LOG_TASK_FAILED},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(&fakeArchiveQuerier{
				taskID: "task-1",
				state:  string(tc.state),
			}, &fakeViews{})

			resp, err := svc.LogRehydrate(context.Background(), &workerpb.LogRehydrateRequest{
				ArchiveObjectIds: []string{"obj-1"},
			})
			require.NoError(t, err)
			require.Equal(t, "task-1", resp.GetTaskId())
			require.Equal(t, tc.want, resp.GetState())
			require.Nil(t, resp.GetError())
		})
	}
}
