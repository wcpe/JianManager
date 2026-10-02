package grpcsvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
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

// 「已放弃位置」标记必须**单独**回填在 ingest_position_gaps 上，且不得污染 coverage 语义。
//
// 为什么这条断言是核心：用户要的是「下游查询对已放弃区间显式返回位置缺失标记，不得静默跳过」。
// 两件事必须同时成立——
//
//	① 有放弃记录时，响应里**带出**该区间（否则等于没有实现，下游仍无从得知）；
//	② 它**不得**改写 coverage（coverage 回答"分区能不能查"，放弃回答"分区内哪些位置永久没了"；
//	   把后者塞进前者会让下游把"永久丢失"误读成"暂时查不到"而一直重试）。
//
// 转红方式（实测）：把 LogSearch 里的 s.fillIngestPositionGaps(out, ...) 调用去掉，①立即红；
// 把回填改成写进 out.Coverage，②立即红。
func TestLogSearch_IngestPositionGapsAnnotatedSeparately(t *testing.T) {
	fq := &fakeQuery{search: query.SearchResponse{
		RequestID: "r-gap",
		View:      &query.ViewRef{ViewID: "view-gap", OrderVersion: query.SortVersion},
		Coverage: query.Coverage{
			Complete: true,
			Targets: []query.TargetCoverage{{
				TargetID: "ns:game/2026-09-20",
				State:    query.CoverageSuccess,
			}},
		},
		Quality: query.Quality{DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact},
	}}
	svc := New(fq, &fakeViews{})

	// 未接线提供者：不得编造标记（也不得报错）。
	resp, err := svc.LogSearch(context.Background(), searchReq("r-gap"))
	require.NoError(t, err)
	require.Empty(t, resp.GetIngestPositionGaps(), "未接线时不得编造放弃标记")

	// 接线提供者：必须带出区间，且 coverage 逐字不变。
	var gotTargets []string
	var called bool
	svc.SetIngestAbandonmentProvider(IngestAbandonmentFunc(func(targetIDs []string) []IngestPositionGap {
		called = true
		gotTargets = append([]string(nil), targetIDs...)
		return []IngestPositionGap{{
			StorageNamespace: "ns:game", From: 100, To: 150,
			ReasonCode: "PERMANENTLY_LOST", Operator: "ops@example", AtUTC: "2026-10-02T08:00:00Z",
		}}
	}))
	resp2, err := svc.LogSearch(context.Background(), searchReq("r-gap"))
	require.NoError(t, err)
	require.True(t, called, "提供者必须被调用")
	_ = gotTargets // 目标集来自请求授权范围（空 = 本 Worker 全部），本用例不绑定其内容。

	require.Len(t, resp2.GetIngestPositionGaps(), 1, "有放弃记录时必须显式带出该区间（不得静默跳过）")
	g := resp2.GetIngestPositionGaps()[0]
	require.Equal(t, "ns:game", g.GetStorageNamespace())
	require.EqualValues(t, 100, g.GetFrom())
	require.EqualValues(t, 150, g.GetTo())
	require.Equal(t, "PERMANENTLY_LOST", g.GetReasonCode())
	require.Equal(t, "ops@example", g.GetOperator())
	require.Equal(t, "2026-10-02T08:00:00Z", g.GetAtUtc())

	// ② coverage 必须不受影响（语义分开）。
	require.True(t, resp2.GetCoverage().GetComplete(), "放弃标记不得改写 coverage 的完整度")
	require.Len(t, resp2.GetCoverage().GetTargets(), 1)
	require.Equal(t, "ns:game/2026-09-20", resp2.GetCoverage().GetTargets()[0].GetTargetId())
	require.Equal(t, workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS,
		resp2.GetCoverage().GetTargets()[0].GetState(),
		"放弃标记不得改写分区的可查询性状态")
	require.Empty(t, resp2.GetCoverage().GetPartialReasons(),
		"放弃标记不得写进 logcoord 的 partial_reasons（两者语义必须分开标注）")
}

// 手动归档导入入口：必须要求操作人（无痕触发 = "谁灌了数据"留空白），且把范围原样下传。
//
// 转红方式（实测）：把 LogImportArchives 里的 operatorFromContext 空值判定去掉，①立即红。
func TestLogImportArchives_RequiresOperatorAndPassesScope(t *testing.T) {
	svc := New(&fakeQuery{}, &fakeViews{})

	// 未接线：必须明确失败，不得空成功（否则运维会以为"扫过了、没东西"）。
	resp, err := svc.LogImportArchives(context.Background(), &workerpb.LogImportArchivesRequest{})
	require.NoError(t, err)
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_FAILED, resp.GetState())

	fake := &fakeArchiveImporter{}
	svc.SetIngestArchiveImporter(fake)

	// ① 无操作人：拒绝，且**不得**调用导入器。
	resp, err = svc.LogImportArchives(context.Background(), &workerpb.LogImportArchivesRequest{
		StorageNamespace: "ns:game",
	})
	require.NoError(t, err)
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_FAILED, resp.GetState())
	require.Contains(t, resp.GetError().GetMessage(), OperatorMetadataKey)
	require.Zero(t, fake.calls, "无操作人时不得执行导入（不可无痕）")

	// ② 带操作人：执行，范围原样下传。
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(OperatorMetadataKey, "ops@example"))
	resp, err = svc.LogImportArchives(ctx, &workerpb.LogImportArchivesRequest{
		StorageNamespace: " ns:game ",
	})
	require.NoError(t, err)
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_SUCCEEDED, resp.GetState())
	require.Equal(t, 1, fake.calls)
	require.Equal(t, "ns:game", fake.namespace, "命名空间应 trim 后下传")
	require.Equal(t, "ops@example", fake.operator)
}

type fakeArchiveImporter struct {
	calls     int
	namespace string
	operator  string
}

func (f *fakeArchiveImporter) ImportArchivesNow(ns, operator string) (int, int, error) {
	f.calls++
	f.namespace, f.operator = ns, operator
	return 1, 1, nil
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
	require.False(t, after.GetInstances()[1].GetQueryReady(), "empty COLD namespace cannot claim query completeness")
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

func TestLogCutoverReadinessUsesCapabilitiesAndLedgerProvider(t *testing.T) {
	fq := &fakeQuery{caps: query.CapabilitiesResponse{Supported: true, ProtocolVersion: query.DefaultProtocolVersion,
		Capabilities: []string{"query_view", "search", "stats"}}}
	svc := New(fq, nil)
	cutoff := time.Date(2026, 9, 23, 4, 0, 0, 0, time.UTC)
	svc.SetCutoverReadiness(CutoverReadinessFunc(func() (bool, time.Time, []string) { return true, cutoff, nil }))
	resp, err := svc.LogCutoverReadiness(context.Background(), &workerpb.LogCutoverReadinessRequest{ProtocolVersion: query.DefaultProtocolVersion})
	require.NoError(t, err)
	require.Nil(t, resp.GetError())
	require.True(t, resp.GetCapabilityConfirmed())
	require.True(t, resp.GetLedgerReady())
	require.Equal(t, cutoff.Format(time.RFC3339Nano), resp.GetCutoffTimeUtc())

	fq.caps.Capabilities = []string{"query_view", "search"}
	resp, err = svc.LogCutoverReadiness(context.Background(), &workerpb.LogCutoverReadinessRequest{ProtocolVersion: query.DefaultProtocolVersion})
	require.NoError(t, err)
	require.False(t, resp.GetCapabilityConfirmed())
	require.Contains(t, resp.GetReasons(), "required_log_capabilities_missing")
}

// ---- LogTail ----

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
	require.NoError(t, c.Put(catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")))

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
	require.NoError(t, c.Put(catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")))
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
