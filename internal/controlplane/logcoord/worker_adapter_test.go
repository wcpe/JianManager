package logcoord

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakePBClient 可编程 workerpb 客户端（仅覆盖 Log* RPC）。
type fakePBClient struct {
	workerpb.WorkerServiceClient
	searchResp *workerpb.LogSearchResponse
	searchErr  error
	statsResp  *workerpb.LogStatsResponse
	statsErr   error
	facetsResp *workerpb.LogFacetsResponse
	facetsErr  error
	lastSearch *workerpb.LogSearchRequest
}

func (f *fakePBClient) LogSearch(_ context.Context, in *workerpb.LogSearchRequest, _ ...grpc.CallOption) (*workerpb.LogSearchResponse, error) {
	f.lastSearch = in
	return f.searchResp, f.searchErr
}

func (f *fakePBClient) LogStats(_ context.Context, _ *workerpb.LogStatsRequest, _ ...grpc.CallOption) (*workerpb.LogStatsResponse, error) {
	return f.statsResp, f.statsErr
}

func (f *fakePBClient) LogFacets(_ context.Context, _ *workerpb.LogFacetsRequest, _ ...grpc.CallOption) (*workerpb.LogFacetsResponse, error) {
	return f.facetsResp, f.facetsErr
}

func TestLogCoord_ParseDuplicateQuality_CaseInsensitive(t *testing.T) {
	assert.Equal(t, DupExact, ParseDuplicateQuality("exact"))
	assert.Equal(t, DupExact, ParseDuplicateQuality("EXACT"))
	assert.Equal(t, DupExact, ParseDuplicateQuality("Exact"))
	assert.Equal(t, DupUnresolved, ParseDuplicateQuality("unresolved"))
	assert.Equal(t, DupUnresolved, ParseDuplicateQuality("UNRESOLVED"))
	assert.Equal(t, DupConflict, ParseDuplicateQuality("Conflict"))
	assert.Equal(t, DupUnspecified, ParseDuplicateQuality(""))
	assert.Equal(t, DupUnspecified, ParseDuplicateQuality("nope"))
}

func TestParseStatsQuality_CaseInsensitive(t *testing.T) {
	assert.Equal(t, StatsQExact, ParseStatsQuality("Exact"))
	assert.Equal(t, StatsQPartial, ParseStatsQuality("PARTIAL"))
	assert.Equal(t, StatsQUnavailable, ParseStatsQuality("unavailable"))
}

func TestLogCoord_QualityBlocksExport_Unresolved(t *testing.T) {
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupUnresolved, StatsQuality: StatsQExact}))
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: "UNRESOLVED", StatsQuality: StatsQExact}))
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupExact, StatsQuality: StatsQPartial}))
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupExact, StatsQuality: "Unavailable"}))
	assert.False(t, QualityBlocksExport(Quality{DuplicateQuality: DupExact, StatsQuality: StatsQExact}))
	assert.False(t, QualityBlocksExport(Quality{DuplicateQuality: "exact", StatsQuality: "exact"}))
}

// M-3 fail-closed：未指定/未识别的质量必须阻断导出，而不是被当成良好放行。
//
// 缺陷形态：原实现只列举 unresolved/conflict/partial/unavailable 四个坏值，
// 于是 unspecified 落在集合之外 ⇒ 放行。而 Worker 未上报 LogQuality（proto 为 nil）
// 或上报本版本不认识的未来枚举时，解析结果正是 unspecified——未识别质量因此以
// 「良好」状态通过导出闸门。
//
// 转红方式（实测）：把 QualityBlocksExport 改回「只列举已知坏值」的写法，
// 本用例前五条断言立即红。
func TestLogCoord_QualityBlocksExport_UnspecifiedBlocks(t *testing.T) {
	// Worker 未上报 LogQuality（proto 字段缺省 / nil）→ Quality 零值。
	assert.True(t, QualityBlocksExport(qualityFromProto(nil)),
		"Worker 未上报质量必须阻断：不能把「未知」当成「良好」")
	assert.True(t, QualityBlocksExport(Quality{}), "零值 Quality 解析为 unspecified，必须阻断")

	// 显式 unspecified 与「只有一个维度未知」同样阻断。
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupUnspecified, StatsQuality: StatsQExact}))
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupExact, StatsQuality: StatsQUnspecified}))

	// 未来枚举值（本版本不认识）解析为 unspecified：天然落在阻断侧。
	assert.True(t, QualityBlocksExport(Quality{
		DuplicateQuality: "duplicate_state_from_future_worker",
		StatsQuality:     "stats_state_from_future_worker",
	}))
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupExact, StatsQuality: "stats_state_from_future_worker"}))

	// 防过度收紧：两个维度都明确 exact 时仍必须放行（这是唯一放行口径）。
	assert.False(t, QualityBlocksExport(exactQuality), "exact/exact 必须继续放行")
	assert.False(t, QualityBlocksExport(Quality{DuplicateQuality: "EXACT", StatsQuality: "Exact"}),
		"大小写不敏感的 exact 仍应放行")
}

func TestCoverageStateFromProto(t *testing.T) {
	assert.Equal(t, CoverageSuccess, CoverageStateFromProto(workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS))
	assert.Equal(t, CoveragePartial, CoverageStateFromProto(workerpb.LogCoverageState_LOG_COVERAGE_PARTIAL))
	assert.Equal(t, CoverageOffline, CoverageStateFromProto(workerpb.LogCoverageState_LOG_COVERAGE_OFFLINE))
	assert.Equal(t, CoverageUnspecified, CoverageStateFromProto(workerpb.LogCoverageState_LOG_COVERAGE_STATE_UNSPECIFIED))
}

func TestLogCoord_WorkerClientAdapter_SearchMapsProto(t *testing.T) {
	pb := &fakePBClient{
		searchResp: &workerpb.LogSearchResponse{
			RequestId: "r1",
			View: &workerpb.LogQueryViewRef{
				ViewId:       "cv_1",
				OrderVersion: OrderVersion,
			},
			Coverage: &workerpb.LogCoverage{
				Complete:         false,
				PartialReasons:   []string{"BUDGET_EXCEEDED"},
				EnumerationState: workerpb.LogEnumerationState_LOG_ENUMERATION_OPEN,
				Targets: []*workerpb.LogCoverageTarget{
					{
						TargetId:          "inst:1",
						State:             workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS,
						ClosedVisibleSeq:  "src/g:100",
						CatalogGeneration: "7",
					},
					{
						TargetId: "inst:2",
						State:    workerpb.LogCoverageState_LOG_COVERAGE_OFFLINE,
						Reasons:  []string{"dial_failed"},
					},
				},
			},
			Items: []*workerpb.LogEvent{
				{
					EventId:          "e1",
					LogSourceId:      "src",
					SourceGeneration: "g",
					RecordStart:      "10",
					RecordEnd:        "20",
					EventTimeUtc:     "2026-09-20T12:00:00Z",
					Level:            "INFO",
					Message:          "hello",
				},
			},
			Exhausted: true,
		},
	}
	adapter := NewWorkerClientAdapter(pb)
	resp, err := adapter.Search(context.Background(), WorkerSearchRequest{
		TargetIDs:    []string{"inst:1", "inst:2"},
		FromUTC:      "2026-09-20T00:00:00Z",
		ToUTC:        "2026-09-21T00:00:00Z",
		ViewID:       "cv_1",
		OrderVersion: OrderVersion,
		Budget:       QueryBudget{Limit: 50},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "cv_1", resp.ViewID)
	assert.True(t, resp.Exhausted)
	require.NotNil(t, resp.Coverage)
	assert.False(t, resp.Coverage.Complete)
	assert.Contains(t, resp.Coverage.PartialReasons, "BUDGET_EXCEEDED")
	assert.Equal(t, EnumOpen, resp.Coverage.EnumerationState)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "e1", resp.Items[0].EventID)
	assert.Equal(t, uint64(10), resp.Items[0].RecordStart)
	require.Len(t, resp.Targets, 2)
	assert.Equal(t, CoverageSuccess, resp.Targets[0].State)
	assert.Equal(t, "src/g:100", resp.Targets[0].ClosedVisibleSeq)
	assert.Equal(t, CoverageOffline, resp.Targets[1].State)

	// 请求侧映射
	require.NotNil(t, pb.lastSearch)
	require.NotNil(t, pb.lastSearch.GetQuery())
	assert.Equal(t, []string{"inst:1", "inst:2"}, pb.lastSearch.GetQuery().GetAuthorizedTargets().GetTargetIds())
	assert.Equal(t, OrderVersion, pb.lastSearch.GetQuery().GetView().GetOrderVersion())
	assert.Equal(t, uint32(50), pb.lastSearch.GetQuery().GetBudget().GetLimit())
}

func TestWorkerClientAdapter_Unimplemented(t *testing.T) {
	pb := &fakePBClient{searchErr: status.Error(codes.Unimplemented, "method LogSearch not implemented")}
	adapter := NewWorkerClientAdapter(pb)
	resp, err := adapter.Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"t1"}})
	require.NoError(t, err)
	assert.True(t, resp.Unsupported)
}

func TestWorkerClientAdapter_NextCursorIsPaginationNotTruncation(t *testing.T) {
	pb := &fakePBClient{searchResp: &workerpb.LogSearchResponse{
		View: &workerpb.LogQueryViewRef{ViewId: "wv-1"},
		Coverage: &workerpb.LogCoverage{Complete: true, Targets: []*workerpb.LogCoverageTarget{{
			TargetId: "node:1", State: workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS,
		}}},
		NextCursor: "next", Exhausted: false,
	}}
	resp, err := NewWorkerClientAdapter(pb).Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"node:1"}})
	require.NoError(t, err)
	require.False(t, resp.Truncated)
	require.False(t, resp.Exhausted)
	require.Equal(t, "next", resp.NextCursor)
}

func TestWorkerClientAdapter_ErrorMapping(t *testing.T) {
	pb := &fakePBClient{
		searchResp: &workerpb.LogSearchResponse{
			Error: &workerpb.LogError{
				Code:    workerpb.LogErrorCode_LOG_ERR_DUPLICATE_UNRESOLVED,
				Message: "duplicate unresolved",
			},
			Coverage: &workerpb.LogCoverage{
				Targets: []*workerpb.LogCoverageTarget{{
					TargetId: "t1",
					State:    workerpb.LogCoverageState_LOG_COVERAGE_PARTIAL,
				}},
			},
		},
	}
	adapter := NewWorkerClientAdapter(pb)
	resp, err := adapter.Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"t1"}})
	require.NoError(t, err)
	assert.Equal(t, "duplicate unresolved", resp.Error)
	require.Len(t, resp.Targets, 1)
	assert.Equal(t, CoveragePartial, resp.Targets[0].State)
}

func TestTunnelDialer_NotWired(t *testing.T) {
	d := NewTunnelDialer(nil)
	_, err := d.Dial(context.Background(), "w1")
	require.Error(t, err)
}

func TestTunnelDialer_Wired(t *testing.T) {
	pb := &fakePBClient{searchResp: &workerpb.LogSearchResponse{Exhausted: true}}
	d := NewTunnelDialer(func(_ context.Context, workerID string) (workerpb.WorkerServiceClient, error) {
		if workerID != "w1" {
			return nil, errors.New("unknown")
		}
		return pb, nil
	})
	cli, err := d.Dial(context.Background(), "w1")
	require.NoError(t, err)
	resp, err := cli.Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"t"}})
	require.NoError(t, err)
	assert.True(t, resp.Exhausted)
}

func TestLogCoord_SearchPersistsClosedVisibleSeqOnView(t *testing.T) {
	t1, t2 := "inst-a", "inst-b"
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: t1, WorkerID: "w1", Readiness: ReadyOnline},
		{ID: t2, WorkerID: "w2", Readiness: ReadyOffline, HistoricalHolder: true},
	}}
	w1 := &fakeWorker{
		workerID: "w1",
		searchItems: []Event{
			mkEvent("e1", "src-a", "g1", 100, "2026-09-20T12:00:00Z", "INFO", "ok"),
		},
		searchTarget: []WorkerTargetResult{
			successTarget(t1, "w1", "src-a/g1:100", "7"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	dialer.fail["w2"] = errors.New("offline")

	coord := New(resolver, dialer)
	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{t1, t2},
		Budget:              QueryBudget{Limit: 50},
	})
	require.NoError(t, err)

	// View 必须固化 coverage 上的 closed_visible_seq 向量
	require.NotNil(t, resp.View.ClosedVisibleSeq)
	assert.Equal(t, "src-a/g1:100", resp.View.ClosedVisibleSeq[t1])
	// 存储视图同步
	stored, ok := coord.GetView(resp.View.ViewID)
	require.True(t, ok)
	assert.Equal(t, "src-a/g1:100", stored.ClosedVisibleSeq[t1])
}

// M-3 回归：Worker 上报的质量必须透传到 CP，并按最差值聚合。
//
// 缺陷形态：proto 与 Worker 都携带 Quality，但 CP 的响应结构没有该字段、adapter 也不读
// 它，CP 只按 coverage 推导质量。于是当 Worker 在 coverage 仍为 complete 时上报
// conflict/partial（例如 projection 冲突），CP 会把它当 exact——而 Export 的质量闸门
// 只拦截 unresolved/conflict/partial/unavailable，不完整来源因此被放行。
func TestWorkerQualityReachesCPAndBlocksExport(t *testing.T) {
	adapter := NewWorkerClientAdapter(&fakePBClient{
		searchResp: &workerpb.LogSearchResponse{
			RequestId: "q1",
			View:      &workerpb.LogQueryViewRef{ViewId: "cv_1", OrderVersion: OrderVersion},
			Coverage: &workerpb.LogCoverage{
				// coverage 仍为 complete：质量必须独立表达问题。
				Complete: true,
				Targets: []*workerpb.LogCoverageTarget{{
					TargetId: "inst:1",
					State:    workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS,
				}},
			},
			Quality: &workerpb.LogQuality{
				DuplicateQuality: workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_CONFLICT,
				StatsQuality:     workerpb.LogStatsQuality_LOG_QUALITY_STATS_PARTIAL,
			},
			Exhausted: true,
		},
	})

	t.Run("quality_mapped_from_proto", func(t *testing.T) {
		resp, err := adapter.Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"inst:1"}})
		require.NoError(t, err)
		assert.Equal(t, DupConflict, resp.Quality.DuplicateQuality, "Worker 报的冲突不得在适配层丢失")
		assert.Equal(t, StatsQPartial, resp.Quality.StatsQuality)
	})

	t.Run("worst_value_wins_across_workers", func(t *testing.T) {
		// 一个 exact 的 Worker 不得掩盖另一个 Worker 的冲突。
		assert.Equal(t, DupConflict, worstDuplicateQuality(DupExact, DupConflict))
		assert.Equal(t, DupConflict, worstDuplicateQuality(DupConflict, DupExact))
		assert.Equal(t, DupUnresolved, worstDuplicateQuality(DupExact, DupUnresolved))
		assert.Equal(t, DupExact, worstDuplicateQuality(DupExact, DupExact))
		assert.Equal(t, StatsQUnavailable, worstStatsQuality(StatsQPartial, StatsQUnavailable))
		assert.Equal(t, StatsQPartial, worstStatsQuality(StatsQExact, StatsQPartial))
	})

	// M-3 聚合口径回归：unspecified（含零值空串、未来枚举）绝不能被 exact 掩盖。
	//
	// 缺陷形态：旧 rank 把 default（unspecified 与一切未知取值）记 0，即**比 exact 更好**，
	// 于是 worst(exact, unspecified) 返回 exact；多 Worker 场景下一个未上报质量的 Worker
	// 会被另一个 Worker 的 exact 掩盖 ⇒ QualityBlocksExport 的 fail-closed 从聚合侧被绕过。
	//
	// 转红方式（实测）：把 rank 的 unspecified 分支改回 0，本子测试中所有
	// 「合并后必须是 unspecified / 必须阻断」的断言立即红。
	t.Run("unspecified_never_masked_by_exact", func(t *testing.T) {
		// ① 显式 unspecified：双向都必须阻断。
		for _, merged := range []DuplicateQuality{
			worstDuplicateQuality(DupExact, DupUnspecified),
			worstDuplicateQuality(DupUnspecified, DupExact),
		} {
			assert.Equal(t, DupUnspecified, ParseDuplicateQuality(string(merged)),
				"unspecified 与 exact 合并后必须仍是 unspecified")
			assert.True(t, QualityBlocksExport(Quality{
				DuplicateQuality: merged, StatsQuality: StatsQExact,
			}), "重复质量 unspecified 必须阻断导出")
		}
		// ② Worker 未上报质量（proto 为 nil ⇒ 适配层给零值 Quality，字段是空串，
		//    不是字面量 "unspecified"）：同样必须阻断，这是生产最可能命中的路径。
		for _, merged := range []DuplicateQuality{
			worstDuplicateQuality(DupExact, ""),
			worstDuplicateQuality("", DupExact),
		} {
			assert.Equal(t, DupUnspecified, ParseDuplicateQuality(string(merged)),
				"空串（未上报）与 exact 合并后必须解析为 unspecified")
			assert.True(t, QualityBlocksExport(Quality{
				DuplicateQuality: merged, StatsQuality: StatsQExact,
			}), "未上报的重复质量必须阻断导出")
		}
		// ③ 未来版本的未知枚举：不得因为「不认识」而被当成无信息放行。
		assert.Equal(t, DupUnspecified,
			ParseDuplicateQuality(string(worstDuplicateQuality(DupExact, "duplicate_state_from_future_worker"))))
		assert.Equal(t, StatsQUnspecified,
			ParseStatsQuality(string(worstStatsQuality(StatsQExact, "stats_state_from_future_worker"))))
		// ④ stats 维度同口径。
		for _, merged := range []StatsQuality{
			worstStatsQuality(StatsQExact, StatsQUnspecified),
			worstStatsQuality(StatsQUnspecified, StatsQExact),
			worstStatsQuality(StatsQExact, ""),
			worstStatsQuality("", StatsQExact),
		} {
			assert.Equal(t, StatsQUnspecified, ParseStatsQuality(string(merged)),
				"未指定/未上报的统计质量与 exact 合并后必须仍是 unspecified")
			assert.True(t, QualityBlocksExport(Quality{
				DuplicateQuality: DupExact, StatsQuality: merged,
			}), "统计质量 unspecified 必须阻断导出")
		}
		// ⑤ 细分顺序：unspecified 比 exact 差、但比 unresolved 轻（后者信息更明确，
		//    合并时优先保留可归因的值），conflict 依旧最差。
		assert.Equal(t, DupUnresolved, worstDuplicateQuality(DupUnresolved, DupUnspecified))
		assert.Equal(t, DupConflict, worstDuplicateQuality(DupUnspecified, DupConflict))
		assert.Equal(t, StatsQPartial, worstStatsQuality(StatsQPartial, StatsQUnspecified))
		// ⑥ 反向约束：全 exact 仍必须精确保持 exact（防过度收紧）。
		assert.Equal(t, DupExact, worstDuplicateQuality(DupExact, DupExact))
		assert.Equal(t, StatsQExact, worstStatsQuality(StatsQExact, StatsQExact))
		assert.False(t, QualityBlocksExport(Quality{
			DuplicateQuality: worstDuplicateQuality(DupExact, DupExact),
			StatsQuality:     worstStatsQuality(StatsQExact, StatsQExact),
		}), "全 exact 必须放行")
	})

	t.Run("conflict_blocks_export", func(t *testing.T) {
		res := BuildExport(View{ViewID: "cv_1"}, Coverage{
			Complete: true,
			Targets:  []TargetCoverage{{TargetID: "inst:1", State: CoverageSuccess}},
		}, Quality{DuplicateQuality: DupConflict, StatsQuality: StatsQPartial}, nil, false, false)
		assert.True(t, res.ExportIncomplete, "质量非 exact 时不得下发成功附件")
		assert.Nil(t, res.Artifact)
	})
}
