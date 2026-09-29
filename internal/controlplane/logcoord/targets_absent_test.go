package logcoord

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 回归（2026-09-30 语义修正）：Worker 响应非空时，未出现的目标表示「该目标无数据」（合法），
// 不得记为 target_missing_in_worker_response（否则归一化成 ENGINE_NOT_READY 并派生 GAP，
// 使整个日志查询面被判未就绪——生产实测即为此）。响应为空时才保留缺失判定，以维持对
// Worker 完全失能的检出。
func TestAbsentTargetReasonsOnlyWhenResponseEmpty(t *testing.T) {
	require.Equal(t, []string{"target_missing_in_worker_response"}, absentTargetReasons(nil),
		"响应为空 = Worker 完全没工作，仍应记缺失")
	require.Equal(t, []string{"target_missing_in_worker_response"}, absentTargetReasons([]WorkerTargetResult{}),
		"空切片同为空响应")

	require.Nil(t, absentTargetReasons([]WorkerTargetResult{{TargetID: "inst:142"}}),
		"响应非空时的缺失 = 该目标无数据，不得记为缺失（否则误判引擎未就绪）")
}
