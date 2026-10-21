package logcoord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// D2：CP Stats 的失败分支必须按 worst 合并，结果不得随 target 解析顺序漂移。
//
// 修复前六个失败分支都是无条件赋值 quality.StatsQuality = ...，而 ByWorker() 保留
// resolver 返回顺序且不排序：
//   - unavailable 的 Worker 先处理、partial 在后 ⇒ partial（unavailable 被降级）；
//   - 顺序反过来 ⇒ unavailable。
//
// 同一份数据仅因解析顺序不同就给出不同质量，且前一种顺序会把「该 Worker 完全无法
// 提供统计」谎报成「只是不完整」。
func TestStatsQualityIndependentOfWorkerOrder(t *testing.T) {
	// 两个方向各跑一次：只交换 resolver 返回的目标顺序。
	for _, tc := range []struct {
		name  string
		first string // 排在解析顺序首位的 Worker
	}{
		{name: "unavailable 在前", first: "w-unavailable"},
		{name: "partial 在前", first: "w-partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// w-unavailable：Worker 声明 Stats 不受支持（Unimplemented）⇒ CP 判 unavailable。
			// w-partial：Worker 返回 Error ⇒ CP 判 partial。
			wUnavailable := &fakeWorker{
				workerID:     "w-unavailable",
				statsUnsup:   true,
				statsTargets: []WorkerTargetResult{successTarget("t-a", "w-unavailable", "1", "1")},
			}
			wPartial := &fakeWorker{
				workerID:     "w-partial",
				statsErr:     "boom",
				statsTargets: []WorkerTargetResult{successTarget("t-b", "w-partial", "1", "1")},
			}
			dialer := newFakeDialer()
			dialer.clients["w-unavailable"] = wUnavailable
			dialer.clients["w-partial"] = wPartial

			tA := TargetInfo{ID: "t-a", WorkerID: "w-unavailable", Readiness: ReadyOnline}
			tB := TargetInfo{ID: "t-b", WorkerID: "w-partial", Readiness: ReadyOnline}
			targets := []TargetInfo{tA, tB}
			if tc.first == "w-partial" {
				targets = []TargetInfo{tB, tA}
			}
			require.Equal(t, tc.first, targets[0].WorkerID, "本用例要求首位 Worker 是 %s", tc.first)

			coord := New(&fakeTargetResolver{targets: targets}, dialer)
			resp, err := coord.Stats(context.Background(), StatsQuery{
				Query: Query{
					AuthorizedTargetIDs: []string{"t-a", "t-b"},
					PrincipalKey:        "user:1|role:10|targets:test",
					Budget:              QueryBudget{Limit: 100},
				},
				GroupBy:    []string{"level"},
				TimeBucket: "1m",
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Equal(t, StatsQUnavailable, resp.Quality.StatsQuality,
				"unavailable 是最差等级，不得被另一 Worker 的 partial 冲掉")
		})
	}
}

// D2 补充：normal 路径（Worker 上报质量）与失败分支混用时，同样按 worst 收敛。
// w1 上报 unavailable，w2 走 Unsupported 分支，两种顺序都必须得到 unavailable。
func TestStatsQualityMergesWorkerReportedWithFailureBranch(t *testing.T) {
	for _, swap := range []bool{false, true} {
		wReported := &fakeWorker{
			workerID:     "w-reported",
			statsQuality: &Quality{DuplicateQuality: DupExact, StatsQuality: StatsQPartial},
			statsTargets: []WorkerTargetResult{successTarget("t-a", "w-reported", "1", "1")},
		}
		wFailed := &fakeWorker{
			workerID:     "w-failed",
			statsUnsup:   true,
			statsTargets: []WorkerTargetResult{successTarget("t-b", "w-failed", "1", "1")},
		}
		dialer := newFakeDialer()
		dialer.clients["w-reported"] = wReported
		dialer.clients["w-failed"] = wFailed

		tA := TargetInfo{ID: "t-a", WorkerID: "w-reported", Readiness: ReadyOnline}
		tB := TargetInfo{ID: "t-b", WorkerID: "w-failed", Readiness: ReadyOnline}
		targets := []TargetInfo{tA, tB}
		if swap {
			targets = []TargetInfo{tB, tA}
		}
		coord := New(&fakeTargetResolver{targets: targets}, dialer)
		resp, err := coord.Stats(context.Background(), StatsQuery{
			Query: Query{
				AuthorizedTargetIDs: []string{"t-a", "t-b"},
				PrincipalKey:        "user:1|role:10|targets:test",
				Budget:              QueryBudget{Limit: 100},
			},
			GroupBy:    []string{"level"},
			TimeBucket: "1m",
		})
		require.NoError(t, err)
		require.Equal(t, StatsQUnavailable, resp.Quality.StatsQuality,
			"swap=%v：失败分支的 unavailable 必须压过其它 Worker 上报的 partial", swap)
	}
}
