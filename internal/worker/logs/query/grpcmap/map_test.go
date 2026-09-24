package grpcmap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func sampleEvent() logtypes.Event {
	src := logtypes.SourceIdentity{LogSourceID: "src", SourceGeneration: "g1", ParserVersion: "p1"}
	return logtypes.BuildEvent(src, logtypes.RecordRange{Start: 10, End: 20},
		"2026-09-20T10:00:00Z", "2026-09-20T10:00:01Z", "INFO", "stdout", "hello")
}

func TestOrderVersion_WireEqualsSortVersion(t *testing.T) {
	require.Equal(t, query.SortVersion, OrderVersion)

	// 空 order_version 在正/反向映射中都必须落到 SortVersion。
	out := ViewRefToProto(&query.ViewRef{ViewID: "v1"})
	require.Equal(t, query.SortVersion, out.GetOrderVersion())

	back := ViewRefFromProto(&workerpb.LogQueryViewRef{ViewId: "v1"})
	require.Equal(t, query.SortVersion, back.OrderVersion)

	// 显式值保留。
	explicit := ViewRefFromProto(&workerpb.LogQueryViewRef{ViewId: "v2", OrderVersion: "custom"})
	require.Equal(t, "custom", explicit.OrderVersion)
}

func TestErrorCode_RoundTrip(t *testing.T) {
	cases := []query.ErrorCode{
		query.ErrCodeUnspecified,
		query.ErrCodeViewStale,
		query.ErrCodePartial,
		query.ErrCodeUnauthorized,
		query.ErrCodeBudgetExceeded,
		query.ErrCodeNotReady,
		query.ErrCodeArchiveMissing,
		query.ErrCodeDuplicateUnresolved,
		query.ErrCodeRecoveryRequired,
		query.ErrCodeCancelled,
		query.ErrCodeUnsupported,
	}
	for _, c := range cases {
		p := ErrorCodeToProto(c)
		require.Equal(t, c, ErrorCodeFromProto(p), "code %s", c)
	}
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, ErrorCodeToProto(query.ErrCodeUnsupported))
	require.Equal(t, workerpb.LogErrorCode_LOG_ERROR_UNSPECIFIED, ErrorCodeToProto(query.ErrorCode("NOPE")))
}

func TestCoverageQuality_Event_ProtoMapping(t *testing.T) {
	cov := query.Coverage{
		Complete:         false,
		PartialReasons:   []string{query.ReasonColdMissing},
		EnumerationState: query.EnumOpen,
		Targets: []query.TargetCoverage{{
			TargetID:          "ns/game-1/2026-09-20",
			State:             query.CoveragePartial,
			Reasons:           []string{query.ReasonColdMissing},
			ClosedVisibleSeq:  42,
			CatalogGeneration: 7,
		}},
	}
	pcov := CoverageToProto(cov)
	require.False(t, pcov.GetComplete())
	require.Equal(t, workerpb.LogEnumerationState_LOG_ENUMERATION_OPEN, pcov.GetEnumerationState())
	require.Len(t, pcov.GetTargets(), 1)
	require.Equal(t, "42", pcov.GetTargets()[0].GetClosedVisibleSeq())
	require.Equal(t, "7", pcov.GetTargets()[0].GetCatalogGeneration())
	require.Equal(t, workerpb.LogCoverageState_LOG_COVERAGE_PARTIAL, pcov.GetTargets()[0].GetState())

	q := QualityToProto(query.Quality{DuplicateQuality: query.DupConflict, StatsQuality: query.StatsPartial})
	require.Equal(t, workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_CONFLICT, q.GetDuplicateQuality())
	require.Equal(t, workerpb.LogStatsQuality_LOG_QUALITY_STATS_PARTIAL, q.GetStatsQuality())

	ev := sampleEvent()
	pev := EventToProto(ev)
	require.Equal(t, ev.EventID, pev.GetEventId())
	require.Equal(t, "10", pev.GetRecordStart())
	require.Equal(t, "20", pev.GetRecordEnd())
	require.Equal(t, ev.EventTimeUTC, pev.GetEventTimeUtc())
	back := EventFromProto(pev)
	require.Equal(t, ev.EventID, back.EventID)
	require.EqualValues(t, 10, back.Record.Start)
	require.Equal(t, "hello", back.Message)
}

func TestQueryRequest_ReverseMapping(t *testing.T) {
	pb := &workerpb.LogSearchRequest{
		Query: &workerpb.LogQueryRequestBase{
			RequestId:         "r1",
			ProtocolVersion:   query.DefaultProtocolVersion,
			TimeRange:         &workerpb.LogTimeRange{FromUtc: "2026-09-20T00:00:00Z", ToUtc: "2026-09-21T00:00:00Z"},
			AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{"ns/game-1"}},
			Budget:            &workerpb.LogQueryBudget{Limit: 50, MaxBytes: 1024, TimeoutMs: 3000, MaxFanout: 2},
			View:              &workerpb.LogQueryViewRef{ViewId: "view-1", Cursor: "c", OrderVersion: query.SortVersion},
			Filter:            "level:ERROR",
			PermissionScope:   "ns:game-1",
			CancellationToken: "tok",
		},
	}
	req := QueryRequestFromBase(pb.GetQuery())
	require.Equal(t, "r1", req.RequestID)
	require.Equal(t, query.DefaultProtocolVersion, req.ProtocolVersion)
	require.Equal(t, "2026-09-20T00:00:00Z", req.TimeRange.FromUTC)
	require.Equal(t, []string{"ns/game-1"}, req.AuthorizedTargets)
	require.EqualValues(t, 50, req.Budget.Limit)
	require.EqualValues(t, 1024, req.Budget.MaxBytes)
	require.Equal(t, "view-1", req.View.ViewID)
	require.Equal(t, query.SortVersion, req.View.OrderVersion)
	require.Equal(t, "level:ERROR", req.Filter)
	require.Equal(t, "tok", req.CancelToken)
}

func TestPlanResult_ApplyViewClosedVisibleSeq(t *testing.T) {
	view := &query.QueryView{
		ViewID:           "view-9",
		OrderVersion:     query.SortVersion,
		ClosedVisibleSeq: map[string]uint64{"ns/game-1/2026-09-20": 99},
		Generations:      map[string]uint64{"ns/game-1/2026-09-20": 3},
	}
	// coverage target 上 CVS/generation 为 0，必须从 view 回填。
	res := query.PlanResult{
		Coverage: query.Coverage{
			Complete:         true,
			EnumerationState: query.EnumOpen,
			Targets: []query.TargetCoverage{{
				TargetID: "ns/game-1/2026-09-20",
				State:    query.CoverageSuccess,
			}},
		},
		Quality: query.Quality{DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact},
		View:    view,
	}
	cov, quality, vref := PlanResultToProto(res)
	require.NotNil(t, cov)
	require.Equal(t, "99", cov.GetTargets()[0].GetClosedVisibleSeq())
	require.Equal(t, "3", cov.GetTargets()[0].GetCatalogGeneration())
	require.Equal(t, query.SortVersion, vref.GetOrderVersion())
	require.Equal(t, workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_EXACT, quality.GetDuplicateQuality())

	// 已有非零值不覆盖。
	cov2 := query.Coverage{Targets: []query.TargetCoverage{{
		TargetID:          "ns/game-1/2026-09-20",
		ClosedVisibleSeq:  5,
		CatalogGeneration: 1,
	}}}
	ApplyViewToCoverage(&cov2, view)
	require.EqualValues(t, 5, cov2.Targets[0].ClosedVisibleSeq)
	require.EqualValues(t, 1, cov2.Targets[0].CatalogGeneration)
}

func TestCapabilitiesResponse_Mapping(t *testing.T) {
	caps := query.CapabilitiesResponse{
		Supported:       true,
		ProtocolVersion: query.DefaultProtocolVersion,
		Capabilities:    []string{"query_view", "coverage", "search"},
		MaxLimit:        query.DefaultMaxLimit,
		MaxRequestBytes: 1 << 20,
		Cancellation:    true,
		BuildID:         "b1",
	}
	out := CapabilitiesResponseToProto(caps)
	require.True(t, out.GetSupported())
	require.Equal(t, query.DefaultProtocolVersion, out.GetProtocolVersion())
	require.Equal(t, []string{"query_view", "coverage", "search"}, out.GetCapabilities())
	require.EqualValues(t, query.DefaultMaxLimit, out.GetMaxLimit())

	// supported=true 且 max_limit 缺省 → 回填 DefaultMaxLimit
	zero := CapabilitiesResponseToProto(query.CapabilitiesResponse{Supported: true, ProtocolVersion: query.DefaultProtocolVersion})
	require.EqualValues(t, query.DefaultMaxLimit, zero.GetMaxLimit())

	// unsupported 原因
	bad := CapabilitiesResponseToProto(query.CapabilitiesResponse{
		Supported:       false,
		ProtocolVersion: "log-query/0",
		UnsupportedReasons: []query.QueryError{{
			Code:    query.ErrCodeUnsupported,
			Message: "unsupported protocol_version",
		}},
	})
	require.False(t, bad.GetSupported())
	require.Len(t, bad.GetUnsupportedReasons(), 1)
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, bad.GetUnsupportedReasons()[0].GetCode())
}

func TestSearchStatsResponse_Mapping(t *testing.T) {
	ev := sampleEvent()
	sresp := query.SearchResponse{
		RequestID: "s1",
		View:      &query.ViewRef{ViewID: "v1", OrderVersion: query.SortVersion},
		Coverage: query.Coverage{
			Complete: false,
			Targets: []query.TargetCoverage{{
				TargetID:         "ns/game-1/2026-09-20",
				State:            query.CoveragePartial,
				ClosedVisibleSeq: 11,
			}},
		},
		Quality:    query.Quality{DuplicateQuality: query.DupExact, StatsQuality: query.StatsPartial},
		Items:      []logtypes.Event{ev},
		NextCursor: "nc",
		Exhausted:  false,
		Err:        &query.QueryError{Code: query.ErrCodeUnsupported, Message: "range client search unimplemented"},
	}
	ps := SearchResponseToProto(sresp)
	require.Equal(t, "s1", ps.GetRequestId())
	require.Equal(t, query.SortVersion, ps.GetView().GetOrderVersion())
	require.Equal(t, "11", ps.GetCoverage().GetTargets()[0].GetClosedVisibleSeq())
	require.Len(t, ps.GetItems(), 1)
	require.Equal(t, "nc", ps.GetNextCursor())
	require.Equal(t, workerpb.LogErrorCode_LOG_UNSUPPORTED, ps.GetError().GetCode())
	require.False(t, ps.GetExhausted())

	st := StatsResponseToProto(query.StatsResponse{
		RequestID: "st1",
		Coverage:  query.Coverage{Complete: true},
		Points: []query.StatsPoint{{
			Dimensions: map[string]string{"level": "INFO"},
			Count:      4,
			Sum:        40,
			Min:        5,
			Max:        20,
		}},
	})
	require.Len(t, st.GetPoints(), 1)
	require.InDelta(t, 10.0, st.GetPoints()[0].GetAvg(), 1e-9)

	items := TailResponseItems(query.TailResponse{Items: []logtypes.Event{ev}})
	require.Len(t, items, 1)
	require.Equal(t, ev.EventID, items[0].GetEventId())
}

func TestTailMode_ProtoMapping(t *testing.T) {
	require.Equal(t, workerpb.LogTailMode_LOG_TAIL_FOLLOW_LIVE, TailModeToProto(query.TailFollowLive))
	require.Equal(t, workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED, TailModeToProto(query.TailViewBounded))
	require.Equal(t, workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED, TailModeToProto(""))
	require.Equal(t, query.TailFollowLive, TailModeFromProto(workerpb.LogTailMode_LOG_TAIL_FOLLOW_LIVE))
	require.Equal(t, query.TailViewBounded, TailModeFromProto(workerpb.LogTailMode_LOG_TAIL_VIEW_BOUNDED))
}
