package acquire

// 隔离撤销（2026-10-28）：当 `ARCHIVE_CONTENT_UNKNOWN` 不再成立时，
// 把当初为隔离而写的两份东西同时清掉：
//  1. 账本里的**诊断缺口**（`ARCHIVE_CONTENT_UNKNOWN`，零宽，位置 = segment.EndPos）；
//  2. 分段上的 `ImportError`（它让 `pendingArchives` 永久跳过该文件）。
//
// ## 为什么必须成对，且为什么不能只靠白名单
//
// `ledger.ResumeAcquire` 对**任何**未解决 gap 一律拒绝：
//
//	for _, gap := range e.Gaps {
//	    if !gap.Resolved { return fmt.Errorf("unresolved gaps still block acquisition") }
//	}
//
// 而 `ARCHIVE_CONTENT_UNKNOWN` **不在** `gapAutoResolvableReasons` 白名单里——
// 那是故意的：它原本属于“真实数据缺失”类，回读造不出已经丢掉的字节。
//
// 所以不能把它加进白名单（那会把真正的数据丢失也一起放行）。唯一正确的做法是：
// **当且仅当证据重新证明“它并没丢”时**，逐条核对并撤销当初那条误判。
//
// 这也是业界做法（Filebeat `clean_inactive` / Fluent Bit `db.lock` 维护）的共同点：
// 隔离状态不是终态，它随真实状态变化而回收。

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// clearUnknownQuarantine 撑销某个分段当初因 UNKNOWN 而留下的缺口与隔离标记。
//
// **精确匹配，不批量清**：只消“原因 = ARCHIVE_CONTENT_UNKNOWN ∧ 位置 = 该分段
// EndPos ∧ 详情里含该路径”的那几条。同源其它原因/其它位置的缺口一律不动——
// 它们可能是真丢数据的证据。
//
// 幂等：没有匹配的缺口 / ImportError 已空 ⇒ 不多做任何事，返回 nil。
func (a *ArchiveImporter) clearUnknownQuarantine(path string, segment *ledger.Segment) error {
	if a == nil || a.led == nil || segment == nil {
		return nil
	}
	cleanPath := fmt.Sprintf("path=%q", cleanArchivePath(path))
	// 先消缺口（它才是恢复的硬闸）。
	resolved, err := a.led.ResolveUnknownArchiveGaps(a.key, segment.EndPos, cleanPath)
	if err != nil {
		return err
	}
	// 再清分段上的隔离标记。Imported 保持原值——它描述“这份内容是否已入库”，
	// 与隔离无关；隔离只是“归属没证明”。
	if segment.ImportError != "" {
		updated := *segment
		updated.ImportError = ""
		if err := a.led.RegisterSegment(a.key, updated); err != nil {
			return err
		}
	}
	if resolved > 0 {
		slog.Info("归档归属已恢复，撤销隔离",
			"logSourceID", a.key.LogSourceID, "path", cleanArchivePath(path),
			"resolvedGaps", resolved, "disposition", "covered")
	}
	return nil
}

// cleanArchivePath 与 AssessArchive 写入 detail 时的规范化口径保持一致（filepath.Clean），
// 否则精确匹配会因路径写法不同而失效。
func cleanArchivePath(path string) string {
	return filepath.Clean(path)
}
