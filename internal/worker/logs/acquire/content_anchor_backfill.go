// 存量分段的内容身份补建（2026-10-28）。
//
// 背景（生产现场）：`content_anchor.go` 是 2026-10-21（b75b10db）才引入的。它上线前
// 登记的分段没有任何内容身份（`identity_bytes=0` / `identity_sha256=""`），于是
// `ResolveContentAnchor`（要求 `PhysicalMappingKnown && IdentitySHA256 != ""`）
// 根本不会把它们当作候选锚 ⇒ 找不到锚 ⇒ `DecideArchiveReimport` 一律回
// `ARCHIVE_CONTENT_UNKNOWN`。而 `pendingArchives` 对「ImportError 非空且文件未变」
// 的分段是永久跳过，于是形成一个**死循环**：
//
//	无身份 ⇒ 无锚 ⇒ UNKNOWN ⇒ 被跳过 ⇒ 永不重算身份 ⇒ （永远）无身份
//
// 生产 inst:317 实测：105 个分段里 **0 个**带身份，其中 89 个被标 UNKNOWN；
// 这批区间对应的字节从未进事件段（事件段有 43 处不连续、共 80822 字节空白），
// 直接后果是 reclaim 在空洞前推不动 ⇒ 积压撞限额 ⇒ 采集暂停（19 个源里 10 个）。
//
// 本文件只做**一件事**：给存量归档分段补上它**本来就该有**的内容身份。
// 身份 = 文件前 min(512B, 文件长) 的 SHA256（与 FileTailer / ContentIdentity 同口径）。
//
// 为什么这是“让判据对上事实”而不是放宽：
//   - 身份是从**盘上的真实字节**算的，不是编造的；
//   - 补上后仍走原有的 `DecideArchiveReimport`：内容未变 ⇒ `ArchiveCovered`（跳过），
//     内容变了 ⇒ 去找锚；**放不放行仍由原判据决定**，本文件不新增任何放行路径；
//   - 文件已不存在 / stat 失败一律不补（不会把“读不了”摸成“已覆盖”）。
//
// 不补 `PhysicalMappingKnown`：那是“逻辑 StartPos 与文件偏移的映射已被证明”，
// 旧账本里确实没有这个证据，不能凭空断言。代价是这类分段仍不能作为**锚提供方**；
// 但它们本来自己就找不到锚，所以不退还。

package acquire

import (
	"log/slog"
	"os"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// BackfillLegacySegmentIdentities 为缺身份的归档分段补建内容身份，返回补建条数。
//
// 幂等：已带身份的分段直接跳过；文件不在也跳过（下一轮再来）。
// 只在**文件内容仍与登记时大小一致**时补：大小变了说明槽位已被复用，
// 那时算出的身份会归属到**错的内容**上，宁可不补。
func BackfillLegacySegmentIdentities(key ledger.SourceKey, led *ledger.Ledger) int {
	if led == nil {
		return 0
	}
	entry := led.Get(key)
	if entry == nil {
		return 0
	}
	filled := 0
	for i := range entry.Segments {
		seg := entry.Segments[i]
		if !ledger.IsArchiveSegmentKind(seg.Kind) {
			continue
		}
		if seg.IdentityBytes > 0 && seg.IdentitySHA256 != "" {
			continue // 已有身份，幂等跳过
		}
		if seg.Path == "" {
			continue
		}
		info, err := os.Stat(seg.Path)
		if err != nil || info.Size() <= 0 {
			continue // 文件不在（或为空）：不补，留给下一轮/人工
		}
		// 槽位复用防护（两道，缺一不可）：
		//
		// ① `ObservedSize` 与当前不符 ⇒ 内容已换，不能拿旧分段的名义补身份。
		//
		// ② `ObservedSize == 0` ⇒ **必须整条跳过**。这是 2026-10-28 被回归用例
		// TestPendingArchivesReimportsReusedSlotWithUndeliveredTail 当场抑住的 bug：
		// 那类分段（旧账本 / 手工登记）从来没记过观察大小，本就**没有任何“内容未变”
		// 的证据**。给它补上当前文件的身份，会让 `DecideArchiveReimport` 的快路径
		// 误以为“内容就是这个”，把**尚未投递的尾巴**当成已覆盖跳过 ⇒ 正是静默丢日志。
		//
		// 换句话说：补身份只能锦上添花（让“确实未变”的旧账本段认回自己），
		// 不能给“本来就没有未变证据”的段凭空发一张证书。
		if seg.ObservedSize <= 0 {
			continue
		}
		if seg.ObservedSize != info.Size() {
			continue
		}
		idBytes, idSHA, idErr := ContentIdentity(seg.Path)
		if idErr != nil || idSHA == "" {
			continue
		}
		updated := seg
		updated.IdentityBytes = idBytes
		updated.IdentitySHA256 = idSHA
		if err := led.RegisterSegment(key, updated); err != nil {
			continue
		}
		filled++
	}
	if filled > 0 {
		slog.Info("为存量归档分段补建了内容身份（此后可参与内容锚匹配）",
			"logSourceID", key.LogSourceID, "sourceGeneration", key.SourceGeneration, "filled", filled)
	}
	return filled
}
