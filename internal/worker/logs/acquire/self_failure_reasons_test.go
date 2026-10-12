package acquire

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestSelfFailureReasonsAreReadableAndBounded（② 的红证 ✓）：**自愈失败的原因必须可读且有界**。
//
// 现场痛点（2026-10-05 ✓）：`noteSelfFailure` 此前是 `_ = reason` ✗ ⇒ 只有 `workerSelfFailures`
// 一个计数、原因永远查不到 ✓（"值存在但被丢弃" = 本会话第 6 个同族变体 ✓）。
//
// 转红方式（实测，**有效变异** ✓：先验编译 ✓、路径可达 ✓）：把 `noteSelfFailure` 改回 `_ = reason`
// ⇒ 本用例在「读数必须含该 reason」处红 ✓✓。
func TestSelfFailureReasonsAreReadableAndBounded(t *testing.T) {
	key := testKey("inst:self-failure", "g1")
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})
	pipe := NewPipeline(led, key, NewWAL(led, key))

	pipe.noteSelfFailure("transport down")
	reasons := pipe.SelfFailureReasons()
	require.Len(t, reasons, 1, "原因必须可读（此前被 `_ = reason` 丢弃 ✗）")
	require.Equal(t, "transport down", reasons[0])

	// 有界且**新→旧** ✓：压 10 条 ⇒ 只留最后 selfFailureReasonCap 条、最新在最前 ✓。
	for i := 0; i < 10; i++ {
		pipe.noteSelfFailure(fmt.Sprintf("fail-%d", i))
	}
	got := pipe.SelfFailureReasons()
	require.Len(t, got, selfFailureReasonCap, "必须有界（不随失败次数增长 ✗），实测 %d", len(got))
	require.Equal(t, "fail-9", got[0], "最新一条必须在最前（新→旧 ✓）")
	require.NotContains(t, got, "transport down", "最旧的一条应被滑窗挤出 ✓")
}
