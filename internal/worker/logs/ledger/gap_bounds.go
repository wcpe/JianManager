package ledger

import (
	"fmt"
	"log/slog"
	"strings"
)

// 本文件实现缺口数量级定界（缺陷 A 修复）：合并、折叠与滚动裁剪。
//
// 背景（2026-10-01/02 生产实测）：投递失败路径对**每一次**失败调用 RecordGap，
// 而登记是纯 append、既不合并也不自动消解；一次持续数小时的投递故障即可在单源上
// 累积 5 万级缺口（实测 50,863–50,864 条/源，inst:147 合计 75,457 条），
// 而 ResumeAcquire 要求「零未解决缺口」才允许恢复采集——
// 于是缺口风暴把采集焊死：持续 13+ 小时零新数据，只能靠运维按源人工解算。
//
// 这里的定界只做两件**保守方向**的事：
//  1. 合并：把同因、相邻/重叠的缺口并成一条区间。区间只扩大不缩小，缺口语义
//     （「这段可能没落库、需要补」）因此更严格，绝不引入「已确认落库」的假象。
//  2. 折叠：条数越界时把**同一原因**的旧缺口并成一条覆盖区间，同样只扩大区间；
//     不同原因绝不合并，否则会丢掉「该原因不可由投影自动消解」这类按原因判定的语义。
//
// 消解（把缺口标记为 resolved）**不在此文件**：它必须建立在外部证据（已发布投影）之上，
// 见 ResolveGapsThrough / ResolveGapsThroughExcept 的调用方。
// 缺口原因常量：跨包共享的「不可由投影自动消解」原因。
const (
	// GapReasonStdioRawWriteFailed 表示受管 Raw 暂存写入失败：原始字节可能根本没落盘，
	// 「可能没落库」无法由任何投影/重投证据证明，故自动消解路径必须显式排除它
	// （既有语义见 ingest.ResolveCoveredGaps；人工接口 ResolveCoveredGapsForSource 仍可显式确认放弃）。
	GapReasonStdioRawWriteFailed = "STDIO_RAW_WRITE_FAILED"
)

const (
	// DefaultMaxUnresolvedGapsPerSource 是单源**未解决**缺口的条数上限（按原因分组，每组一条界）。
	// 取值权衡：正常源在故障恢复窗口内产生的缺口数远小于此值（连续失败会被合并成一条），
	// 而一条上限把「单源故障」对账本、索引与内存的占用钉在常数级。
	DefaultMaxUnresolvedGapsPerSource = 64
	// DefaultMaxResolvedGapsPerSource 是单源**已解决**缺口的滚动保留上限。
	// 已解决缺口是历史审计信息（谁在什么时候确认了什么），保留最近若干条即可满足排障；
	// 无限保留会让每条成功重投都留下一条永久记录（同为 5 万级增长源）。
	DefaultMaxResolvedGapsPerSource = 64
	// gapMergeScanWindow 是「同因相邻合并」从尾部向前回溯的最大条数。
	// 投递失败是时间上连续的，可合并的邻居必在尾部附近；限定窗口使单次登记的代价为 O(1)，
	// 与缺口总量无关（否则 5 万条现场每轮每源都要线性扫描）。
	gapMergeScanWindow = 16
)

// GapStats 是单源缺口的只读观测快照（供指标/日志暴露，不参与任何判定）。
type GapStats struct {
	// Unresolved / Resolved 是当前账本内两类缺口的条数。
	Unresolved int
	Resolved   int
	// MergedTotal 是进程内因「相邻同因合并」而少登记的缺口条数（观测用，不落库）。
	MergedTotal uint64
	// FoldedTotal 是进程内因「超上限折叠」而少登记的缺口条数（观测用，不落库）。
	FoldedTotal uint64
}

// rangesTouch 判定两段源位置区间是否重叠或仅相隔 1 个字节。
//
// 为什么允许 1 字节间隙：相邻批次之间只有一个行分隔符，而 FileTailer 的规范事件范围
// 不含分隔符（acquire.Pipeline 也按此口径把 gapStart 回退到分隔符之前）。把「隔一个
// 分隔符」视为连续，才能把连续失败的批次真正合并成一条；真正的空洞（≥2 字节）不合并。
func rangesTouch(aStart, aEnd, bStart, bEnd uint64) bool {
	return bStart <= aEnd+1 && aStart <= bEnd+1
}

// recordGapLocked 登记一条缺口：先尝试并入同因相邻的既有未解决缺口，否则追加新条目。
// 调用方必须已持写锁。
func (l *Ledger) recordGapLocked(e *Entry, start, end uint64, reason, detail string) {
	if end < start {
		end = start
	}
	// 从尾部向前回溯有限条：可合并的邻居必是最近登记的同因缺口。
	from := 0
	if len(e.Gaps)-gapMergeScanWindow > from {
		from = len(e.Gaps) - gapMergeScanWindow
	}
	for i := len(e.Gaps) - 1; i >= from; i-- {
		gap := &e.Gaps[i]
		if gap.Resolved || gap.Reason != reason {
			continue
		}
		if !rangesTouch(gap.StartPos, gap.EndPos, start, end) {
			continue
		}
		if start < gap.StartPos {
			gap.StartPos = start
		}
		if end > gap.EndPos {
			gap.EndPos = end
		}
		gap.Detail = mergedGapDetail(gap.Detail, detail, gap.StartPos, gap.EndPos)
		e.gapMergedTotal++
		return
	}
	e.Gaps = append(e.Gaps, Gap{StartPos: start, EndPos: end, Reason: reason, Detail: detail})
}

// 合并/折叠写入 Detail 的固定后缀标记。
const (
	gapMergeNote = "合并同因相邻失败上报"
	gapFoldNote  = "超上限折叠"
)

// trimGapNoteSuffix 去掉上一次合并/折叠留下的说明后缀。
//
// 为什么必须去掉：Detail 里若每次合并都追加一段说明，单条缺口的 Detail 会随合并次数线性增长
// （生产实测的 5 万次失败上报足以把它推到 MB 级），而缺口数量恰恰要靠合并压下去——
// 长度必须只与「原始失败原文」有关。
func trimGapNoteSuffix(detail string) string {
	detail = strings.TrimSpace(detail)
	for _, marker := range []string{"(" + gapMergeNote, "(" + gapFoldNote} {
		if idx := strings.LastIndex(detail, marker); idx >= 0 {
			detail = strings.TrimSpace(detail[:idx])
		}
	}
	return detail
}

// mergedGapDetail 生成合并后的缺口说明：保留最早一次的原文，并标出合并事实与当前区间。
//
// 为什么保留原文而不是覆盖：缺口 Detail 是运维判断「这段为什么没落库」的第一手线索，
// 首次失败的原因最有代表性；后续失败次数与合并后的区间必须显式可见，否则无法区分
// 「一次长故障」与「多次巧合相邻的短故障」。
func mergedGapDetail(base, incoming string, start, end uint64) string {
	base = trimGapNoteSuffix(base)
	incoming = trimGapNoteSuffix(incoming)
	if base == "" {
		base = incoming
	} else if incoming != "" && incoming != base && !strings.Contains(base, incoming) && len(base) < 512 {
		// 只保留少量不同的后续原文，避免 Detail 无界增长（同一原因反复失败时原文通常相同）。
		base = base + "; " + incoming
	}
	return fmt.Sprintf("%s (%s；当前区间 %d-%d)", base, gapMergeNote, start, end)
}

// foldedGapDetail 生成折叠摘要：保留首条原文（截断），并标注本次折叠条数与覆盖区间。
func foldedGapDetail(base string, victims int, start, end uint64) string {
	base = trimGapNoteSuffix(base)
	if len(base) > 256 {
		base = base[:256] + "…"
	}
	return fmt.Sprintf("%s (%s %d 条同因缺口；区间 %d-%d)", base, gapFoldNote, victims, start, end)
}

// enforceGapBoundsLocked 在登记/消解之后压缩缺口条数。调用方必须已持写锁。
//
// 两步（顺序固定，先折叠后裁剪）：
//  1. 未解决缺口按**原因**分组，组内条数越过上限时把最旧的一批并成一条覆盖区间；
//  2. 已解决缺口只保留最新的若干条。
func (l *Ledger) enforceGapBoundsLocked(e *Entry) {
	l.foldUnresolvedGapsLocked(e)
	l.trimResolvedGapsLocked(e)
}

// foldUnresolvedGapsLocked 把「同一原因、条数越界」的未解决缺口折叠成一条覆盖区间。
//
// 为什么按原因分组而不是整体折叠：多个原因共存时，每个原因各自的处置语义不同
// （例如 STDIO_RAW_WRITE_FAILED 明确不可由投影自动消解，见 ingest.ResolveCoveredGaps），
// 混在一起折叠会让按原因判定的代码读到错误的原因。
func (l *Ledger) foldUnresolvedGapsLocked(e *Entry) {
	type bucket struct {
		indexes []int
	}
	buckets := map[string]*bucket{}
	order := make([]string, 0, 4)
	for i := range e.Gaps {
		if e.Gaps[i].Resolved {
			continue
		}
		reason := e.Gaps[i].Reason
		b, ok := buckets[reason]
		if !ok {
			b = &bucket{}
			buckets[reason] = b
			order = append(order, reason)
		}
		b.indexes = append(b.indexes, i)
	}
	for _, reason := range order {
		indexes := buckets[reason].indexes
		excess := len(indexes) - DefaultMaxUnresolvedGapsPerSource
		if excess <= 0 {
			continue
		}
		// 折叠最旧的 excess+1 条为一条，使组内条数回到上限。
		victims := indexes[:excess+1]
		merged := e.Gaps[victims[0]]
		for _, index := range victims[1:] {
			gap := e.Gaps[index]
			if gap.StartPos < merged.StartPos {
				merged.StartPos = gap.StartPos
			}
			if gap.EndPos > merged.EndPos {
				merged.EndPos = gap.EndPos
			}
			merged.Detail = mergedGapDetail(merged.Detail, gap.Detail, merged.StartPos, merged.EndPos)
		}
		merged.Detail = foldedGapDetail(merged.Detail, len(victims), merged.StartPos, merged.EndPos)
		merged.Resolved = false
		merged.Resolution = ""
		// 用删除 + 就地写回保持登记顺序：被折叠区间占据第一个受害者的位置，其余删除。
		keep := make([]Gap, 0, len(e.Gaps)-len(victims)+1)
		keep = append(keep, e.Gaps[:victims[0]]...)
		keep = append(keep, merged)
		keep = append(keep, e.Gaps[victims[len(victims)-1]+1:]...)
		e.Gaps = keep
		e.gapFoldedTotal += uint64(len(victims) - 1)
		// 只在首次越界时告警：一次长故障会把折叠重复上千次，每次都打日志等于用日志
		// 复现它本要消灭的数量级问题；后续规模由 GapObservability 的累计计数观测。
		if len(indexes) == DefaultMaxUnresolvedGapsPerSource+1 {
			slog.Warn("单源未解决缺口超过上限，已折叠为覆盖区间（区间只扩大不缩小，语义仍为“可能未落库”）",
				"logSourceID", e.Key.LogSourceID, "sourceGeneration", e.Key.SourceGeneration,
				"reason", reason, "limit", DefaultMaxUnresolvedGapsPerSource)
		}
	}
}

// trimResolvedGapsLocked 只保留最新的若干条已解决缺口（历史审计尾部），丢弃更旧的。
func (l *Ledger) trimResolvedGapsLocked(e *Entry) {
	resolved := 0
	for i := range e.Gaps {
		if e.Gaps[i].Resolved {
			resolved++
		}
	}
	excess := resolved - DefaultMaxResolvedGapsPerSource
	if excess <= 0 {
		return
	}
	keep := make([]Gap, 0, len(e.Gaps)-excess)
	for i := range e.Gaps {
		if e.Gaps[i].Resolved && excess > 0 {
			excess--
			continue
		}
		keep = append(keep, e.Gaps[i])
	}
	e.Gaps = keep
	if resolved == DefaultMaxResolvedGapsPerSource+1 {
		// 与折叠同理：只在首次越界时记录，避免与故障规模同阶的日志量。
		slog.Info("已解决缺口超过保留上限，超出部分按最旧优先裁剪（保留最近审计尾部）",
			"logSourceID", e.Key.LogSourceID, "sourceGeneration", e.Key.SourceGeneration,
			"retained", DefaultMaxResolvedGapsPerSource)
	}
}

func countUnresolved(gaps []Gap) int {
	count := 0
	for _, gap := range gaps {
		if !gap.Resolved {
			count++
		}
	}
	return count
}

// GapObservability 返回单源缺口的只读观测快照；源未注册时返回零值。
func (l *Ledger) GapObservability(key SourceKey) GapStats {
	if l == nil {
		return GapStats{}
	}
	e := l.Get(key)
	if e == nil {
		return GapStats{}
	}
	stats := GapStats{MergedTotal: e.gapMergedTotal, FoldedTotal: e.gapFoldedTotal}
	for _, gap := range e.Gaps {
		if gap.Resolved {
			stats.Resolved++
		} else {
			stats.Unresolved++
		}
	}
	return stats
}
