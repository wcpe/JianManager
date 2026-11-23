package logcoord

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 回归（2026-09-30 语义修正）：Worker 响应非空时，未出现的目标表示「该目标无数据」（合法），
// 不得记为缺失原因（否则归一化成 ENGINE_NOT_READY 并派生 GAP，使整个日志查询面被判未就绪）。
// 响应为空时才保留缺失判定，以维持对 Worker 完全失能的检出。
//
// 回归（2026-10-19 二次修正）：响应为空这一支**只在 RPC 成功返回时**才会走到——调用失败另有
// `search_failed` / `empty_worker_response` / `worker_view_failed` / `dial_failed` 等专属原因，
// 它们才是真正的「引擎未就绪」。因此该分支的原因记为 `target_no_data`（该目标确实没有日志），
// 不再冒充引擎故障；否则运维会被「日志查询服务未启用」引去排查 VL，而 VL 其实有数据
// （2026-10-17 dev 实测：直查 VL 有数据、vlReady=true，日志页却显示服务未启用）。
func TestAbsentTargetReasonsOnlyWhenResponseEmpty(t *testing.T) {
	require.Equal(t, []string{"target_no_data"}, absentTargetReasons(nil),
		"响应为空 = 该目标没有 Catalog 分区，记「无数据」而不是「引擎未就绪」")
	require.Equal(t, []string{"target_no_data"}, absentTargetReasons([]WorkerTargetResult{}),
		"空切片同为空响应")

	require.Nil(t, absentTargetReasons([]WorkerTargetResult{{TargetID: "inst:142"}}),
		"响应非空时的缺失 = 该目标无数据，不得记为缺失（否则误判引擎未就绪）")
}

// 回归（2026-10-19）：`target_missing_in_worker_response` 必须归一化为 TARGET_NO_DATA，
// 不得再并入 ENGINE_NOT_READY——「RPC 成功但该目标无分区」是无数据，不是引擎故障。
func TestNormalizeCoverageReasonTargetNoData(t *testing.T) {
	require.Equal(t, "TARGET_NO_DATA",
		normalizeCoverageReason("target_missing_in_worker_response"),
		"缺失目标 = 无数据，不得归一化为 ENGINE_NOT_READY")
	require.Equal(t, "TARGET_NO_DATA", normalizeCoverageReason("TARGET_NO_DATA"),
		"新原因自身应可归一化（幂等）")

	// 真正的引擎未就绪信号仍必须保留：这些才是「Worker 整体不可查询」。
	for _, reason := range []string{"not_ready", "missing_coverage", "empty_worker_response", "worker_view_failed"} {
		require.Equal(t, "ENGINE_NOT_READY", normalizeCoverageReason(reason),
			"%q 仍应判为引擎未就绪", reason)
	}
}

// 回归（2026-10-19，评审 M-2 修订）：空响应 ⇒ 保持 not_ready，但原因不再是 ENGINE_NOT_READY。
//
// 为什么不能判 success：Worker 在「没有任何匹配分区」与「有分区却枚举不出来」两种情况下都返回
// 空 targets（planner.selectKeys 为空 ⇒ coverage.Targets 也为空），现有 wire 无法区分。判 success
// 会把后一种故障静默成「无数据 + complete=true + 可导出」。
// 为什么也不能判 ENGINE_NOT_READY：前端会把 ENGINE_NOT_READY 渲染成「日志查询服务未启用」，
// 把运维引去查 VL —— 这正是 2026-10-17 dev 现场被误导的原因。
func TestAbsentTargetCoverageKeepsNotReadyWithDistinctReason(t *testing.T) {
	target := TargetInfo{ID: "node:1", Readiness: ReadyOnline}

	tc := absentTargetCoverage(target, nil)
	require.Equal(t, CoverageNotReady, tc.State,
		"空响应不能判 success：无法区分「无分区」与「枚举异常」")
	require.Equal(t, []string{"target_no_data"}, tc.Reasons,
		"原因必须是 target_no_data，不能是 ENGINE_NOT_READY 语义的缺失原因")

	cov := BuildCoverage([]TargetInfo{target}, map[string]TargetCoverage{target.ID: tc}, false, nil, EnumOpen)
	require.Contains(t, cov.PartialReasons, "TARGET_NO_DATA",
		"必须标出不完整，不能静默放行导出")
	require.NotContains(t, cov.PartialReasons, "ENGINE_NOT_READY",
		"不得让前端显示「日志查询服务未启用」")
	require.False(t, cov.Complete, "有缺口时覆盖率不得为完整")

	// 响应非空但缺该目标 ⇒ 无数据（success），这条是 2026-09-30 的既有语义，不能被本次改回去。
	nonEmpty := []WorkerTargetResult{{TargetID: "inst:142"}}
	tc2 := absentTargetCoverage(target, nonEmpty)
	require.Equal(t, CoverageSuccess, tc2.State, "响应非空时的缺失 = 该目标无数据（合法终态）")
	require.Empty(t, tc2.Reasons)
}
