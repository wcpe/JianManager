package ingest

// 两名单一致性回归（2026-11-02）。
//
// 背景：`gapReasonsHealableOnReResume`（可自愈 → 不阻塞恢复）与
// `gapAutoResolvableReasons`（允许自动消解）是两个**独立维护**的名单。
// 若某原因只在前者、不在后者，后果是：
//
//	阻塞恢复（源进暂停） + 永不消解（UnresolvedGapCount 不为 0）
//	⇒ 源**永久卡死**，且无任何报错（无声失效）
//
// 这正是 `ledger/gap_bounds.go` 里那段事故记录的形态：
// "生产实测 2026-10-10：86 源被 ROTATED_SEGMENT_NOT_READY 永久挡住"。
//
// 本组用例钉住“可自愈的原因必须也能被自动消解”（个别刻意排除需显式登记）。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// gapAutoResolveIntentionallyExcluded 是「在可自愈名单、但**刻意**不进自动消解名单」的原因。
//
// 为何 `APPEND_REJECTED` 属于此类（2026-10-02 P1-1 修复）：
// append 被拒可能意味着**该批根本未进 WAL**，那种情况下“凸包覆盖”不代表数据真的落库。
// 它不会因此永久卡死——重读后新事件走投递成功路径重新登记覆盖，那时缺口会被正常消解。
var gapAutoResolveIntentionallyExcluded = map[string]bool{
	ledger.GapReasonAppendRejected: true,
}

func TestHealableGapReasonsAreAutoResolvable(t *testing.T) {
	healable := ledger.GapReasonsHealableOnReResume()
	require.NotEmpty(t, healable, "可自愈名单不得为空（否则本用例失去意义）")

	autoSet := make(map[string]bool, len(gapAutoResolvableReasons))
	for _, r := range gapAutoResolvableReasons {
		autoSet[r] = true
	}

	for _, reason := range healable {
		if gapAutoResolveIntentionallyExcluded[reason] {
			continue
		}
		if !autoSet[reason] {
			t.Errorf("原因 %q 在可自愈名单里（会阻塞恢复）却不在自动消解名单里 ⇒ 源会永久卡死；"+
				"若确实应该排除，请加进 gapAutoResolveIntentionallyExcluded 并写明理由", reason)
		}
	}
}

func TestRotatedSegmentNotReadyIsAutoResolvable(t *testing.T) {
	// 单独钉一条：这个原因曾造成 86 源永久卡死（见 ledger/gap_bounds.go 事故记录）。
	found := false
	for _, r := range gapAutoResolvableReasons {
		if r == ledger.GapReasonRotatedSegmentNotReady {
			found = true
			break
		}
	}
	require.True(t, found,
		"ROTATED_SEGMENT_NOT_READY 必须在自动消解名单里：它是零宽记录、不覆盖任何字节，"+
			"加进来无凸包风险；不在名单里会造成源永久卡死（生产 2026-10-10：86 源被挡住）")
}
