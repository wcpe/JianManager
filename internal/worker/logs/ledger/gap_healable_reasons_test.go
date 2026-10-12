package ledger

// 「恢复后回读可愈合」白名单的**钉住测试**（2026-10-10）。
//
// 背景：该白名单曾散落在三处（acquire 的分类裁定两段循环 + ingest 的诊断函数），
// `ROTATED_SEGMENT_NOT_READY` 三处都不在 ⇒ 生产 86 个源永久停在暂停上，
// 且**没有任何报错**（无声失效 ✗）。修法是把名单收敛为这里的单一真源，
// 于是"三处一致"由类型系统保证；本文件钉住的是**名单内容本身**——
// 防止有人为了"让某个源动起来"随手把一个真实数据缺失的原因加进来。

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGapReasonsHealableOnReResumeContainsOnlyTimingAndPauseReasons 钉住白名单的**精确内容**。
//
// 为什么用逐字相等而不是"包含"：包含式断言无法发现"被顺手多加了一个原因"，
// 而那正是本名单最危险的改动方向（放行一个真实数据缺失的原因 = 静默掩盖丢数据 ✗）。
//
// 转红：① 从真源删掉任一条（如 ROTATED_SEGMENT_NOT_READY）⇒ 必红；
// ② 往真源加入任一条（如 WAL_BODY_MISSING）⇒ 必红。
func TestGapReasonsHealableOnReResumeContainsOnlyTimingAndPauseReasons(t *testing.T) {
	require.Equal(t, []string{
		GapReasonAppendRejected,
		GapReasonDeliverError,
		GapReasonDeliverErrorWorkerSource,
		// 2026-10-10 新增：轮转时序观察（区间恒为 [0,0]，不覆盖任何字节）⇒ 恢复后回读可愈合。
		GapReasonRotatedSegmentNotReady,
	}, GapReasonsHealableOnReResume(), "白名单内容是被审阅过的整体，任何增删都必须显式改这条断言 ✓")
}

// TestGapReasonsHealableOnReResumeExcludesRealDataLoss 钉住**必须留在名单外**的原因。
//
// 这些原因的共性是：回读造不出已经丢掉的字节。放行它们等于把"数据丢了"静默改写成"已补齐"——
// 缺口语义红线（见 gap_bounds.go 顶部与 ledger.ResolveGapsThroughExcept 的说明）。
//
// 转红：把其中任何一条移入真源（或让判定函数对它返回 true）⇒ 必红。
func TestGapReasonsHealableOnReResumeExcludesRealDataLoss(t *testing.T) {
	blocked := []string{
		// 真实正文缺失：WAL 引用的条目在事件段存储里找不到正文（本次修复明确保持排除 ✓）。
		GapReasonWALBodyMissing,
		// 原始字节写入失败：正文可能根本没落盘。
		GapReasonStdioRawWriteFailed,
		// WAL 提交失败：事件是否 durable 未定，需按区间证据逐次裁定。
		GapReasonWALCommitFailed,
		// 归档面：损坏是永久性的，权限/发现失败也不会因恢复采集而消失。
		GapReasonArchiveDiscoveryFailed,
		GapReasonArchiveUnreadable,
		GapReasonArchiveOpenFailed,
		GapReasonArchiveGzipCorrupt,
		GapReasonArchiveForeignGeneration,
		GapReasonArchiveReadError,
		// 被跨过的历史内容不再回读（w18 防护）。
		GapReasonFileRewritten,
		// 受管流读取出错：坏流不会因恢复采集而变好。
		GapReasonStdioReadError,
		// 未登记的空串与未知原因一律默认拒绝（fail-closed ✓）。
		"",
		"FUTURE_UNKNOWN",
	}
	for _, reason := range blocked {
		t.Run(reason, func(t *testing.T) {
			require.False(t, IsGapReasonHealableOnReResume(reason),
				"原因 %q 不属于「恢复后回读可愈合」类，绝不能被自动放行 ✓", reason)
			for _, allowed := range GapReasonsHealableOnReResume() {
				require.NotEqual(t, allowed, reason, "真源不得含 %q ✓", reason)
			}
		})
	}
}

// TestGapReasonsHealableOnReResumeReturnsCopy 钉住"观测面不得改写真源"。
//
// 为什么：真源一旦可被外部 append，就回到了「多处各持一份名单」的形态——
// 本类型的收敛前提随即失效，且失效方式是**静默**的（谁改的、何时改的都看不出来）。
func TestGapReasonsHealableOnReResumeReturnsCopy(t *testing.T) {
	first := GapReasonsHealableOnReResume()
	require.NotEmpty(t, first)
	first[0] = "TAMPERED"

	require.NotEqual(t, "TAMPERED", GapReasonsHealableOnReResume()[0], "返回的必须是副本 ✓")
	require.True(t, IsGapReasonHealableOnReResume(GapReasonAppendRejected),
		"改动副本不得影响判定 ✓")
}
