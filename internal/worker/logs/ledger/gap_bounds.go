package ledger

import (
	"fmt"
	"log/slog"
	"sort"
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
// 缺口原因常量：跨包共享的「可否由自动消解出口处置」的判据来源。
//
// 为什么集中在这里：自动消解是**允许名单**语义（见 ResolveGapsCoveredByRanges 的说明），
// 而名单里的名字必须与登记点写入的名字**逐字相同**；散落的字符串字面量一旦有一处拼错，
// 表现就是「该原因永远不被自动消解」这种无声失效（源会一直停在暂停上）。故登记点
// （acquire 包）与判定点（ingest 包）共用这里的常量。
const (
	// GapReasonStdioRawWriteFailed 表示受管 Raw 暂存写入失败：原始字节可能根本没落盘，
	// 「可能没落库」无法由任何投影/重投证据证明，故自动消解路径必须显式排除它
	// （既有语义见 ingest.ResolveCoveredGaps；人工接口 ResolveCoveredGapsForSource 仍可显式确认放弃）。
	GapReasonStdioRawWriteFailed = "STDIO_RAW_WRITE_FAILED"

	// GapReasonDeliverError 表示 VL 投递失败：该批的 WAL append 与 fsync 提交**都已成功**，
	// 只有投递这一步失败。事件在 WAL 中 durable，必然出现在后续写入面（重投/重放）里，
	// 因此「投递成功 + 区间被逐字段校验覆盖」可以为其作证。
	GapReasonDeliverError = "DELIVER_ERROR"
	// GapReasonDeliverErrorWorkerSource 同 GapReasonDeliverError，只用于 source=worker 的自源
	// （只计本地、不递归写回 VL）。事件同样已在 WAL 中 durable。
	GapReasonDeliverErrorWorkerSource = "DELIVER_ERROR_WORKER_SOURCE"
	// GapReasonWALCommitFailed 表示 WAL append 成功、fsync 提交失败：事件已进 WAL（read 已推进）
	// 但 durable 未推进。是否可由投递证据消解取决于这些事件后来是否真的 durable 并进入写入面，
	// 故登记为**可判**原因，由「区间连续覆盖」这一独立判据逐次裁定（覆盖不到就不消解）。
	GapReasonWALCommitFailed = "WAL_COMMIT_FAILED"
	// GapReasonAppendRejected 表示 WAL append 被拒（采集已暂停 / 容量门禁）：该批**从未进入 WAL**，
	// 因此不可能出现在任何写入面里。登记为显式常量是为了让自动消解出口能明确拒绝它
	// （需要重读补投或人工确认，绝不能靠一次「别处成功」的投递证明它已落库）。
	GapReasonAppendRejected = "APPEND_REJECTED"

	// GapReasonRotatedSegmentNotReady 表示「已检测到 latest.log 被替换，但对应的轮转段尚未可读」
	// 的**时序观察记录**：登记位置恒为 [0,0] 零长度区间，**不覆盖任何日志字节**
	// （见 ingest.pollSource 的轮转分支）。轮转段就绪后的一次回读会自然覆盖这段时间，
	// 因此它属于「恢复后回读可愈合」类，由恢复前的分类裁定自动消解
	// （见 GapReasonsHealableOnReResume）——**不是**「已确认落库」。
	GapReasonRotatedSegmentNotReady = "ROTATED_SEGMENT_NOT_READY"

	// GapReasonWALBodyMissing 表示**真实正文缺失**：WAL 引用的条目在事件段存储中找不到正文，
	// 登记区间是真实的 from/to（确有日志字节没被恢复）。它**不在**恢复白名单里——
	// 回读造不出已经丢掉的正文，只有人工裁定能处置；自动放行等于静默掩盖丢数据。
	GapReasonWALBodyMissing = "WAL_BODY_MISSING"

	// GapReasonArchiveDiscoveryFailed 表示归档发现失败（列举目录出错）：该源本轮根本没有归档可用，
	// 是否丢字节无法由回读证明 ⇒ 不在白名单，需人工裁定。
	GapReasonArchiveDiscoveryFailed = "ARCHIVE_DISCOVERY_FAILED"
	// GapReasonArchiveUnreadable 表示归档不可读（权限 / 哈希取不到）：字节仍在盘上但本次读不了，
	// 回读不必然成功（权限问题不会因恢复采集而消失）⇒ 不在白名单，需人工裁定。
	GapReasonArchiveUnreadable = "ARCHIVE_UNREADABLE"
	// GapReasonArchiveOpenFailed 表示归档打开失败：同上，属归档面 IO 故障 ⇒ 不在白名单。
	GapReasonArchiveOpenFailed = "ARCHIVE_OPEN_FAILED"
	// GapReasonArchiveGzipCorrupt 表示归档 gzip 已损坏：损坏是**永久性**的，没有任何可重读原始字节
	// ⇒ 不在白名单，必须人工裁定（放弃或从备份补）。
	GapReasonArchiveGzipCorrupt = "ARCHIVE_GZIP_CORRUPT"
	// GapReasonArchiveForeignGeneration 表示归档属于其它代次、被拒绝归账：这是**刻意的拒绝**而不是丢数据，
	// 但放行它需要代次归属裁定 ⇒ 不在白名单，保持可见。
	GapReasonArchiveForeignGeneration = "ARCHIVE_FOREIGN_GENERATION"
	// GapReasonArchiveReadError 表示读取归档内容出错（读失败 / 截断）：缺的字节在归档里，回读同一份
	// 损坏归档不会变好 ⇒ 不在白名单，需人工裁定（既有回归 archive_truncation_gap_test 依赖此语义）。
	GapReasonArchiveReadError = "ARCHIVE_READ_ERROR"
	// GapReasonFileRewritten 表示日志文件在采集途中被重写（大小回退 / 身份变化），游标已重锚到 EOF：
	// 被跨过的历史内容**不再回读**（w18 防护），故它不可能靠回读愈合 ⇒ 不在白名单。
	GapReasonFileRewritten = "FILE_REWRITTEN"
	// GapReasonStdioReadError 表示受管 stdio 流读取出错：字节是否已产生由下游进程决定，
	// 坏流不会因恢复采集而变好 ⇒ 不在白名单。
	GapReasonStdioReadError = "STDIO_READ_ERROR"
)

// GapReasonsHealableOnReResume 是「恢复后回读可愈合」原因的**单一真源**白名单（默认拒绝）。
//
// 判据：名单内的原因都是**时序类/暂停类**观察记录——恢复采集后的一次回读会重新产生并投递那段
// 字节（`APPEND_REJECTED` / `DELIVER_ERROR*`），或该记录本就不覆盖任何字节（`ROTATED_SEGMENT_NOT_READY`
// 恒为 [0,0]）。因此它们不需要「已落库」证据即可放行恢复，消解词也只是**非终局**的
// 「待重读」类（见 acquire.IsReReadHealableResolution）。
//
// 红线：**真实数据缺失**类原因永远不得进入本名单——`WAL_BODY_MISSING`（正文在段存储里找不到）、
// `STDIO_RAW_WRITE_FAILED`（原始字节可能没落盘）、`ARCHIVE_*`（归档损坏/不可读）、
// `FILE_REWRITTEN`（跨过的历史内容不再回读）、`WAL_COMMIT_FAILED`（是否 durable 未定）
// 都不满足上述判据，回读造不出已经丢掉的字节，放行等于静默掩盖丢数据。
//
// 为什么要单一真源：名单若散落在登记点与判定点两处，任何一处拼错或漏改的表现都是
// 「该原因永远不被自动消解」——源会一直停在暂停上且**没有任何报错**（无声失效 ✗，
// 生产实测 2026-10-10：86 源被 `ROTATED_SEGMENT_NOT_READY` 永久挡住）。
// 故 acquire 侧的分类裁定与 ingest 侧的阻塞诊断都必须只经由本名单判定。
var gapReasonsHealableOnReResume = []string{
	GapReasonAppendRejected,
	GapReasonDeliverError,
	GapReasonDeliverErrorWorkerSource,
	GapReasonRotatedSegmentNotReady,
}

// GapReasonsHealableOnReResume 返回白名单的副本（观测与测试用）。
//
// 返回副本而不是切片本身：真源一旦被外部 append/改写，就会回到「多处各持一份名单」的形态，
// 本类型存在的前提随即失效。改动副本不影响任何判定。
func GapReasonsHealableOnReResume() []string {
	return append([]string(nil), gapReasonsHealableOnReResume...)
}

// IsGapReasonHealableOnReResume 报告某原因是否属于「恢复后回读可愈合」白名单（未知名默认拒绝）。
func IsGapReasonHealableOnReResume(reason string) bool {
	for _, allowed := range gapReasonsHealableOnReResume {
		if reason == allowed {
			return true
		}
	}
	return false
}

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

// normalizeFoldSpans 归一化折叠子区间：按起点排序 + 合并相接/重叠项（同一口径 rangesTouch）。
//
// 为什么要归一化：折叠是**反复发生**的（每次越界折叠最旧一批），被折叠出的凸包条目可能在后续
// 轮次里再次成为受害者——此时它的 FoldSpans 已是子区间集合，必须与新区间一起重新归一化，
// 才能保持「子区间互不相接」的不变式（否则展开落凭据时会产出冗余的重叠凭据）。
func normalizeFoldSpans(spans []PositionRange) []PositionRange {
	if len(spans) == 0 {
		return nil
	}
	out := append([]PositionRange(nil), spans...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].From == out[j].From {
			return out[i].To < out[j].To
		}
		return out[i].From < out[j].From
	})
	kept := out[:1]
	for _, sp := range out[1:] {
		last := &kept[len(kept)-1]
		if rangesTouch(last.From, last.To, sp.From, sp.To) {
			if sp.To > last.To {
				last.To = sp.To
			}
			continue
		}
		kept = append(kept, sp)
	}
	return kept
}

// DefaultMaxFoldSpansPerGap 是单个折叠缺口保留的精确子区间条数上限。
//
// 为什么必须有上限：折叠的**全部意义**是把单源缺口占用钉在常数级（见文件头：现场曾达
// 5 万条/源）。若子区间列表无界，反复折叠会让它随故障规模线性增长 ⇒ 内存界失效，
// 等于把刚修好的问题从「缺口条数」搬到了「子区间条数」。
//
// 取值：与 DefaultMaxUnresolvedGapsPerSource 同级（折叠只发生在越界时，单次最多并掉
// excess+1 条）。触及上限时的退化方向**必须仍是保守的**——见 capFoldSpans。
const DefaultMaxFoldSpansPerGap = DefaultMaxUnresolvedGapsPerSource

// capFoldSpans 归一化并封顶精确子区间，返回 (子区间, 被合并掉的条数)。
//
// **触发上限时的退化方向（这是本函数唯一需要审慎的地方）**：把多余子区间并成一条**凸包**，
// 该凸包会覆盖夹间健康位置。这看似与 LEDGER-2 的修复目标冲突，但两者优先级不同：
//   - 内存有界是**硬约束**（无界会让进程 OOM，后果是整机失效）；
//   - 凭据精确性是**正确性约束**，但只在「人工放弃」这一条路径上被消费，且退化后仍有
//     显式痕迹（MergedSpans>0 写进 Detail 并计入观测），运维据此知道该凭据是粗粒度裁定。
//
// 也就是说：退化不是静默的，且只在极端规模（单缺口 >64 段精确子区间）下发生。
func capFoldSpans(spans []PositionRange) ([]PositionRange, int) {
	kept := normalizeFoldSpans(spans)
	if len(kept) <= DefaultMaxFoldSpansPerGap {
		return kept, 0
	}
	// 保留最新的若干段（与凭据裁剪同向：最新裁定最相关），其余并成一条凸包置于最前。
	cut := len(kept) - (DefaultMaxFoldSpansPerGap - 1)
	tail := kept[cut:]
	head := PositionRange{From: kept[0].From, To: kept[cut-1].To}
	out := make([]PositionRange, 0, DefaultMaxFoldSpansPerGap)
	out = append(out, head)
	out = append(out, tail...)
	return normalizeFoldSpans(out), cut - 1
}

// gapEvidenceSpans 返回该缺口的**精确**证据区间：折叠产生的缺口按其子区间展开，其余缺口
// 即其自身区间。放弃裁定的凭据必须按本函数逐段落，绝不用凸包冒充（LEDGER-2）。
func gapEvidenceSpans(g Gap) []PositionRange {
	if len(g.FoldSpans) == 0 {
		return []PositionRange{{From: g.StartPos, To: g.EndPos}}
	}
	return append([]PositionRange(nil), g.FoldSpans...)
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
	// 所有桶先在原始切片上登记离散删除与替换位置，再统一重建；不能让前一个桶改写后一个桶的索引。
	removed := make(map[int]bool)
	replacements := make(map[int]Gap)
	for _, reason := range order {
		indexes := buckets[reason].indexes
		excess := len(indexes) - DefaultMaxUnresolvedGapsPerSource
		if excess <= 0 {
			continue
		}
		// 折叠最旧的 excess+1 条为一条，使组内条数回到上限。
		victims := indexes[:excess+1]
		merged := e.Gaps[victims[0]]
		// **保留精确子区间**（LEDGER-2，2026-10-12 修复）：凸包只用于「区间只扩大不缩小」的
		// 保守判据（WAL 完整性/重投范围），但**不得**被当作「这段全是永久丢失」的证据——
		// 折叠前各受害者之间可能夹着健康位置。故在折叠时把每条受害者的原始区间记进被保留的
		// 那条缺口（FoldSpans），供 AbandonGapsThrough 落凭据时逐段展开。
		//
		// 起点必须取 `merged` 的**精确子区间**而不是它的凸包（StartPos/EndPos）：反复折叠时
		// merged 本身可能已是上一次折叠的产物，用凸包会把上次的夹间健康位置重新吞回来
		// （实测形态：100 条缺口折叠后 spans 退化成 [{10,3869},{3970,3979}]，第 1 段即凸包）。
		spans := gapEvidenceSpans(merged)
		for _, index := range victims[1:] {
			gap := e.Gaps[index]
			// 同理：受害者自身可能也已折叠过，展开它的精确子区间。
			spans = append(spans, gapEvidenceSpans(gap)...)
			if gap.StartPos < merged.StartPos {
				merged.StartPos = gap.StartPos
			}
			if gap.EndPos > merged.EndPos {
				merged.EndPos = gap.EndPos
			}
			merged.Detail = mergedGapDetail(merged.Detail, gap.Detail, merged.StartPos, merged.EndPos)
		}
		merged.FoldSpans, merged.FoldSpanMerges = capFoldSpans(spans)
		merged.Detail = foldedGapDetail(merged.Detail, len(victims), merged.StartPos, merged.EndPos)
		if merged.FoldSpanMerges > 0 {
			// 退化不得静默（见 FoldSpanMerges 的说明）：把粗粒度裁定的痕迹写进唯一的可读面。
			merged.Detail = fmt.Sprintf("%s（精确子区间已合并 %d 段：该条目为粗粒度裁定，放弃时按整段记凭据）",
				merged.Detail, merged.FoldSpanMerges)
		}
		merged.Resolved = false
		merged.Resolution = ""
		// 折叠区间占据第一个受害者的位置，只删除同因其余受害者，保留夹间审计与其他原因。
		replacements[victims[0]] = merged
		for _, index := range victims[1:] {
			removed[index] = true
		}
		e.gapFoldedTotal += uint64(len(victims) - 1)
		// 只在首次越界时告警：一次长故障会把折叠重复上千次，每次都打日志等于用日志
		// 复现它本要消灭的数量级问题；后续规模由 GapObservability 的累计计数观测。
		if len(indexes) == DefaultMaxUnresolvedGapsPerSource+1 {
			slog.Warn("单源未解决缺口超过上限，已折叠为覆盖区间（区间只扩大不缩小，语义仍为“可能未落库”）",
				"logSourceID", e.Key.LogSourceID, "sourceGeneration", e.Key.SourceGeneration,
				"reason", reason, "limit", DefaultMaxUnresolvedGapsPerSource)
		}
	}
	if len(removed) == 0 {
		return
	}
	keep := make([]Gap, 0, len(e.Gaps)-len(removed))
	for index, gap := range e.Gaps {
		if removed[index] {
			continue
		}
		if replacement, ok := replacements[index]; ok {
			gap = replacement
		}
		keep = append(keep, gap)
	}
	e.Gaps = keep
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
