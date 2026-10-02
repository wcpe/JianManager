// Package ledger 实现 FR-474 采集账本：按 (log_source_id, source_generation)
// 保存四水位、投递状态、恢复分段引用、缺口与轮转关联。
// 字段与状态语义以 docs/specs/worker-log-platform-contract/spec.md 为准。
package ledger

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// SourceKey 账本主键：逻辑日志源 + 源分段 generation。
type SourceKey struct {
	LogSourceID      string `json:"log_source_id"`
	SourceGeneration string `json:"source_generation"`
}

func (k SourceKey) String() string { return k.LogSourceID + "/" + k.SourceGeneration }

// SegmentKind 源分段物理形态。
type SegmentKind string

const (
	SegmentLive    SegmentKind = "live"    // latest.log 等当前写入文件
	SegmentRotated SegmentKind = "rotated" // 轮转后的明文分段
	SegmentGzip    SegmentKind = "gzip"    // 压缩归档分段
	SegmentStdio   SegmentKind = "stdio"   // STDIO_PRIMARY 受管 Raw
)

// Segment 账本中的一个物理源分段。
type Segment struct {
	Path            string      `json:"path"`
	Kind            SegmentKind `json:"kind"`
	StartPos        uint64      `json:"start_pos"`                   // 相对逻辑源的起始位置
	EndPos          uint64      `json:"end_pos"`                     // 已登记的结束位置（exclusive）
	IdentityBytes   int64       `json:"identity_bytes,omitempty"`    // live 文件稳定前缀长度
	IdentitySHA256  string      `json:"identity_sha256,omitempty"`   // 检测同路径替换，不能参与 event_id
	ArchiveObjectID string      `json:"archive_object_id,omitempty"` // provenance only
	Imported        bool        `json:"imported"`
	ImportError     string      `json:"import_error,omitempty"`
	ObservedSize    int64       `json:"observed_size,omitempty"`
	ObservedModNano int64       `json:"observed_mod_unix_nano,omitempty"`
	ParserVersion   string      `json:"parser_version"`
}

// RotationLink latest.log → rotated → .gz 的逻辑源连续关联。
type RotationLink struct {
	FromPath string `json:"from_path"`
	ToPath   string `json:"to_path"`
	// EndPosFrom 关联建立时前段结束位置；ArchiveImporter 接管前必须已登记。
	EndPosFrom uint64 `json:"end_pos_from"`
	// Generation 轮转/压缩沿用被接管内容的 generation。
	Generation string `json:"generation"`
	LinkedAt   int64  `json:"linked_at"`
}

// Gap 采集/投递缺口：容量暂停、坏记录或权限失败时登记，禁止静默丢弃。
type Gap struct {
	StartPos   uint64 `json:"start_pos"`
	EndPos     uint64 `json:"end_pos"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
	Resolved   bool   `json:"resolved,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	// ReasonCode 是**放弃**（人工确认永久丢失）时的结构化原因码，与自动消解路径无关。
	//
	// 与 Reason/Resolution 的分工：Reason 是「当初为什么产生这个缺口」（产生侧，如 APPEND_REJECTED），
	// Resolution 是自由文本。放弃是一种**裁定**，必须能与自动消解区分开——否则事后审计无法回答
	// 「这一段是补齐了，还是被人为放弃了」。取值见 GapReasonPermanentlyLost。
	ReasonCode string `json:"reason_code,omitempty"`
	// ResolvedAtUTC 是放弃裁定的时间（RFC3339）。只对放弃路径写入。
	ResolvedAtUTC string `json:"resolved_at_utc,omitempty"`
	// ResolvedBy 是放弃裁定的操作人（来自调用方认证主体）。**必须非空**——放弃不可无痕，
	// 见 Ledger.AbandonGapsThrough 的拒绝语义。
	ResolvedBy string `json:"resolved_by,omitempty"`
}

func (l *Ledger) UnresolvedGapCount(key SourceKey) int {
	return UnresolvedGapCountOf(l.Get(key))
}

// UnresolvedGapCountOf 统计一份**已取到的**条目副本里的未解决缺口数。
//
// 为什么需要它：Get 会深拷贝整条缺口列表，而缺口数量在故障期可达万级——调用方若已经
// 持有条目副本（或本就为读其它字段而取过一次），再调 UnresolvedGapCount(key) 等于为
// 同一个判断付两次深拷贝。热路径（每批投递、每轮采集）一律用本函数。
func UnresolvedGapCountOf(entry *Entry) int {
	if entry == nil {
		return 0
	}
	return countUnresolved(entry.Gaps)
}

// ResolveGapsThrough 消解结束位置不超过 position 的未解决缺口。
//
// 语义红线（不变）：消解只表示「这段缺口已由 resolution 指名的证据证明落库/已确认放弃」，
// 调用方必须在确有证据时才调用；本方法不校验证据，只执行标记（证据由调用点负责）。
func (l *Ledger) ResolveGapsThrough(key SourceKey, position uint64, resolution string) (int, error) {
	return l.ResolveGapsThroughExcept(key, position, resolution)
}

// ResolveGapsThroughExcept 与 ResolveGapsThrough 同语义，但跳过 excludedReasons 中的原因。
//
// 为什么需要按原因排除：部分缺口原因（例：STDIO_RAW_WRITE_FAILED——原始字节写入失败）
// 的「可能没落库」无法由投影覆盖证明，既有自动路径明确拒绝消解它们
// （见 ingest.ResolveCoveredGaps）。自动消解必须保持同一拒绝语义，否则会把
// 「未经确认的写失败」静默当成已确认。
func (l *Ledger) ResolveGapsThroughExcept(key SourceKey, position uint64, resolution string, excludedReasons ...string) (int, error) {
	if resolution == "" {
		return 0, fmt.Errorf("ledger: gap resolution is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.require(key)
	if err != nil {
		return 0, err
	}
	excluded := make(map[string]bool, len(excludedReasons))
	for _, reason := range excludedReasons {
		excluded[reason] = true
	}
	resolved := 0
	for index := range entry.Gaps {
		gap := &entry.Gaps[index]
		if gap.Resolved || gap.EndPos > position || excluded[gap.Reason] {
			continue
		}
		gap.Resolved = true
		gap.Resolution = resolution
		resolved++
	}
	if resolved > 0 {
		// 已解决缺口只保留审计尾部，避免「每次成功重投留一条永久记录」再次无界增长。
		l.trimResolvedGapsLocked(entry)
	}
	return resolved, nil
}

// GapReasonPermanentlyLost 是**人工裁定**「该位置的数据永久丢失、不再补齐」的原因码。
//
// 为什么需要独立原因码：放弃是唯一允许「回收链跨过一个永远补不上的空洞」的凭据，
// 而它必须与自动消解（投影已覆盖）在数据上**可区分**——否则审计无法回答「这段是补齐了
// 还是被放弃了」。自动路径**永不**写这个码（自动放弃 = 静默丢日志，明令禁止）。
const GapReasonPermanentlyLost = "PERMANENTLY_LOST"

// GapAbandonment 是一次「放弃空洞」裁定的结构化凭据。
//
// 为什么独立于 Gap 存一份（而不是只写在 Gap 上）：已解决缺口会被 trimResolvedGapsLocked
// 按数量裁剪（只留审计尾部），若把裁定只挂在 Gap 上，「回收链凭什么跨过它」的凭据会随
// 缺口一并被裁掉。裁定的生命周期必须长于缺口记录本身——它要一直支撑 CanReclaim 的放行判定。
type GapAbandonment struct {
	// From/To 是被放弃的源位置闭区间（取裁定当时被解算的缺口区间）。
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
	// ReasonCode 固定为 GapReasonPermanentlyLost；留字段是为了将来自查与扩展。
	ReasonCode string `json:"reason_code"`
	// Reason/Detail 保留被放弃缺口当初的产生原因与细节（便于事后归因）。
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Operator 是做出裁定的操作人；空值一律拒绝（放弃不可无痕）。
	Operator string `json:"operator"`
	// AtUTC 是裁定时间（RFC3339）。
	AtUTC string `json:"at_utc"`
	// Through 是这次裁定解算到的源位置（「我确认该位置之前的缺口都不再补齐」）。
	Through uint64 `json:"through"`
}

// DefaultMaxAbandonmentsPerSource 是单源放弃裁定凭据的保留条数上限（相邻/重叠裁定会就地合并）。
//
// 与 DefaultMaxResolvedGapsPerSource 同理：裁定凭据不得随故障规模无界增长。取 64 的依据是
// 「放弃是人工动作，正常运维一年也不会做几十次」；触顶时按 From 从旧到新裁剪，
// 且裁剪**只丢弃最旧的凭据**，不影响回收链对较新空洞的放行。
const DefaultMaxAbandonmentsPerSource = 64

// PositionRange 是源位置的闭区间 [From, To]（含首含尾）。
type PositionRange struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// AbandonGapsThrough 是**人工裁定**路径：把结束位置不超过 position 的未解决缺口标记为
// 「永久丢失」，并留下结构化凭据（原因码 / 时间 / 操作人 / 区间）+ 一条独立的放弃记录。
//
// 与 ResolveGapsThroughExcept 的分工（刻意不共用实现，见下方说明）：
//   - ResolveGapsThroughExcept 是**自动消解 + 人工解算**共用的「把缺口标成已解决」通道，
//     其 `Resolution` 是自由文本、`Resolved=true` 不区分「补齐了」与「放弃了」；
//   - 本方法专供放弃：它额外写 ReasonCode/ResolvedAtUTC/ResolvedBy，并在 Entry.Abandonments
//     留下一条**比缺口记录活得更久**的凭据（缺口会被裁剪，凭据要一直支撑回收链的放行判定）。
//
// 为什么 reject 空操作人：放弃是唯一允许「回收链跨过一个永远补不上的空洞」的动作，
// 无痕放弃等于给静默丢日志开了后门。
//
// 为什么不与 ResolveGapsThroughExcept 共用遍历：后者是缺口 default-deny 判据的载体
// （按原因排除的语义必须逐字保持），共用会把两条语义的演化耦合在一起。这里的重复是
// **刻意**的：放弃路径的每一条断言都必须能被独立读出来。
func (l *Ledger) AbandonGapsThrough(key SourceKey, position uint64, ab GapAbandonment) (int, error) {
	if strings.TrimSpace(ab.Operator) == "" {
		return 0, fmt.Errorf("ledger: gap abandonment requires an operator (refusing to abandon without a trace)")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.require(key)
	if err != nil {
		return 0, err
	}
	at := ab.AtUTC
	if at == "" {
		at = time.Now().UTC().Format(time.RFC3339)
	}
	reasonCode := ab.ReasonCode
	if reasonCode == "" {
		reasonCode = GapReasonPermanentlyLost
	}
	resolved := 0
	var from, to uint64
	first := true
	for index := range entry.Gaps {
		gap := &entry.Gaps[index]
		if gap.Resolved || gap.EndPos > position {
			continue
		}
		gap.Resolved = true
		gap.Resolution = fmt.Sprintf("%s by %s at %s", reasonCode, ab.Operator, at)
		gap.ReasonCode = reasonCode
		gap.ResolvedAtUTC = at
		gap.ResolvedBy = ab.Operator
		if first || gap.StartPos < from {
			from = gap.StartPos
		}
		if gap.EndPos > to {
			to = gap.EndPos
		}
		first = false
		resolved++
	}
	if resolved == 0 {
		// 没有可放弃的缺口：不写凭据（避免留下「什么都没放弃」的空裁定）。
		return 0, nil
	}
	l.trimResolvedGapsLocked(entry)
	// 凭据区间取「本次实际被放弃的缺口并集」，而不是调用方传入的 through——
	// 后者可能远大于实际空洞（它要越过读位置才能覆盖缺口），把非缺口区间写进凭据
	// 会让「回收链凭什么跨过这里」的审计答案变宽。
	entry.Abandonments = append(entry.Abandonments, GapAbandonment{
		From: from, To: to, ReasonCode: reasonCode,
		Reason: ab.Reason, Detail: ab.Detail,
		Operator: ab.Operator, AtUTC: at, Through: position,
	})
	l.mergeAndTrimAbandonmentsLocked(entry)
	return resolved, nil
}

// mergeAndTrimAbandonmentsLocked 合并相接/重叠的放弃区间并按条数上限裁剪凭据。
//
// 为什么要合并：同一次故障可能被反复裁定（每次都只覆盖新解出的那一小段），
// 不合并会让凭据条数随裁定次数增长，而它们表达的是同一段空洞。
// 为什么裁剪按 From 从旧到新：最新的裁定才是回收链当前需要放行的那一个。
func (l *Ledger) mergeAndTrimAbandonmentsLocked(e *Entry) {
	if len(e.Abandonments) == 0 {
		return
	}
	sort.Slice(e.Abandonments, func(i, j int) bool {
		if e.Abandonments[i].From == e.Abandonments[j].From {
			return e.Abandonments[i].To < e.Abandonments[j].To
		}
		return e.Abandonments[i].From < e.Abandonments[j].From
	})
	merged := make([]GapAbandonment, 0, len(e.Abandonments))
	for _, ab := range e.Abandonments {
		if len(merged) == 0 {
			merged = append(merged, ab)
			continue
		}
		last := &merged[len(merged)-1]
		// 相接判定与缺口/投递区间同一口径（相隔 1 字节仍算连续，见 rangesTouch）。
		if ab.From <= last.To+1 {
			if ab.To > last.To {
				last.To = ab.To
			}
			if ab.Through > last.Through {
				last.Through = ab.Through
			}
			continue
		}
		merged = append(merged, ab)
	}
	if excess := len(merged) - DefaultMaxAbandonmentsPerSource; excess > 0 {
		merged = merged[excess:]
	}
	e.Abandonments = merged
}

// AbandonmentsCovering 报告位置 pos 是否落在某条放弃凭据覆盖的区间内。
//
// 语义（务必按名字读准）：它只回答「该位置被人工裁定为永久丢失」，
// **不回答**「回收可以推进」——那是 CanReclaim 与恢复分段状态机的事。
// 本函数是那个判定的**输入凭据**，不是判定本身。
func (l *Ledger) AbandonmentsCovering(key SourceKey, pos uint64) (GapAbandonment, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	entry := l.entries[key]
	if entry == nil {
		return GapAbandonment{}, false
	}
	return findAbandonmentCovering(entry, pos)
}

// abandonmentIntersectingLocked 返回与闭区间 [from, to] 相交的第一条放弃凭据。
//
// 为什么回收门禁用「相交」而不是「覆盖 reclaim 位置」：被卡住的 reclaim 位置通常落在空洞**之前**
// （门禁要求整段责任已转移，而整段里含空洞），所以按位置覆盖判定永远匹配不上——
// 真正相关的是「这一段之所以无法完成校验，是否正因为其中有个已裁定的空洞」。
// 判据取相交即表达这一点，而放行的**上界仍是该分段的 CoversTo**，不会越界。
// 调用方必须已持锁（读或写）。
func (l *Ledger) abandonmentIntersectingLocked(e *Entry, from, to uint64) (GapAbandonment, bool) {
	if e == nil {
		return GapAbandonment{}, false
	}
	for _, ab := range e.Abandonments {
		if ab.To < from || ab.From > to {
			continue
		}
		return ab, true
	}
	return GapAbandonment{}, false
}

// findAbandonmentCovering 是 AbandonmentsCovering 的加锁外实现。
func findAbandonmentCovering(e *Entry, pos uint64) (GapAbandonment, bool) {
	if e == nil {
		return GapAbandonment{}, false
	}
	for _, ab := range e.Abandonments {
		if pos >= ab.From && pos <= ab.To {
			return ab, true
		}
	}
	return GapAbandonment{}, false
}

// MergePositionRanges 把一组位置区间合并为若干**连续覆盖**的区间（升序、互不相接）。
//
// 相邻判定与缺口合并同一口径（rangesTouch：相隔 1 个字节仍视为连续）：相邻规范事件之间
// 只有一个行分隔字节，而事件区间不含分隔符，故「隔一个分隔符」必须算连续，否则一次连续
// 写入会被拆成逐事件碎片。
func MergePositionRanges(ranges []PositionRange) []PositionRange {
	out := make([]PositionRange, 0, len(ranges))
	for _, r := range ranges {
		if r.To < r.From {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	merged := make([]PositionRange, 0, len(out))
	current := out[0]
	for _, r := range out[1:] {
		// 重叠或仅隔 1 个字节（分隔符）→ 并入当前区间（区间只扩大不缩小）。
		if r.From <= current.To+1 {
			if r.To > current.To {
				current.To = r.To
			}
			continue
		}
		merged = append(merged, current)
		current = r
	}
	return append(merged, current)
}

// CoveredByPositionRanges 报告 [from,to] 是否被 ranges 中**某一段连续区间**完全包含。
//
// 为什么不是「落在 min..max 凸包内」：凸包会把两段证据之间的真实空洞也算成已覆盖，
// 而空洞恰恰是「从未进入 WAL/段」的批次（append 被拒、容量门禁暂停）留下的位置——
// 它们不可能出现在任何证据区间里，却会被凸包一并宣称「已确认落库」。
//
// **两端各容差 1 字节**（与缺口合并的 rangesTouch 同一口径 ✓）：规范事件区间不含行分隔符，
// 而登记缺口时起点会**回退到该分隔符之前**（见 acquire.Pipeline.Ingest 的 gapStart 回退），
// 故缺口起点比证据区间早 1 字节（那 1 字节就是分隔符本身）仍视为同一段连续覆盖 ✓。
//
// **终点同样容差 1 字节**（2026-10-04 现场修正 ✗✓）：原实现只放宽起点，注释断言"缺口的末端与
// 证据区间的末端同源"——现场数据证否了这条 ✗：`inst:156` 的未消解 DELIVER_ERROR 缺口
// `[68,163,086 → 68,528,091]` 与 ACKED 段 `[68,163,087 → 68,528,090]` **两端各差 1 字节** ✗，
// 全库 67 条同型（3.3× 于 APPEND_REJECTED ✗）⇒ 它们因**终点多 1 字节**永远无法被消解 ✓✓，
// 这才是"60 源死锁"的主因（不是投递没发生，而是判据差一个字节 ✗）。
//
// **不破空洞判据** ✓：容差严格为 1 字节（分隔符），真正的空洞 ≥2 字节（见 rangesTouch 的同一约定 ✓）
// ⇒ 单段区间内部有洞时仍然不通过 ✓；且容差只在**同一段**证据的端点外各放宽 1，不会跨到相邻段 ✓。
func CoveredByPositionRanges(ranges []PositionRange, from, to uint64) bool {
	if to < from {
		return false
	}
	for _, r := range ranges {
		start := r.From
		if start > 0 {
			start--
		}
		end := r.To
		if end != ^uint64(0) { // 溢出保护：To 已达上界时不再放宽 ✓
			end++
		}
		if from >= start && to <= end {
			return true
		}
	}
	return false
}

// ResolveGapsCoveredByRanges 只消解**完全落在 ranges 之一内部**、且原因在 allowedReasons 内的未解决缺口。
//
// 与 ResolveGapsThroughExcept 的差别（为什么两者都要）：
//   - Through 的判据是「缺口末端 ≤ position」，适合人工显式确认放弃这类整体裁决；
//   - 本方法的判据是「缺口区间被某一次**逐字段校验通过**的写入范围完全包含」，适合
//     「重投成功 / 已发布投影」这类局部证据——若只用末端判据，一次更靠后的成功批次会把
//     夹在中间、从未被重投的旧缺口一并解掉，等于凭空宣称「没落库的数据已落库」
//     （违反缺口语义红线）。
//
// 原因判据是**允许名单**而不是排除名单：排除名单是默认放行，任何新增原因都会在无人注意时
// 被自动消解；允许名单默认拒绝，未知原因必须显式登记才可能被自动消解。allowedReasons 为空
// 时直接报错（装配错误必须响亮，不能静默变成「什么都不做」）。
//
// 区间只做包含判定，不做相交判定：部分重叠意味着还有一段没有任何证据，必须保持未解决。
func (l *Ledger) ResolveGapsCoveredByRanges(key SourceKey, ranges []PositionRange, resolution string, allowedReasons ...string) (int, error) {
	if resolution == "" {
		return 0, fmt.Errorf("ledger: gap resolution is required")
	}
	if len(allowedReasons) == 0 {
		return 0, fmt.Errorf("ledger: gap resolution requires a non-empty reason allowlist")
	}
	if len(ranges) == 0 {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.require(key)
	if err != nil {
		return 0, err
	}
	if len(entry.Gaps) == 0 {
		return 0, nil
	}
	allowed := make(map[string]bool, len(allowedReasons))
	for _, reason := range allowedReasons {
		allowed[reason] = true
	}
	resolved := 0
	for index := range entry.Gaps {
		gap := &entry.Gaps[index]
		if gap.Resolved || !allowed[gap.Reason] {
			continue
		}
		if !CoveredByPositionRanges(ranges, gap.StartPos, gap.EndPos) {
			continue
		}
		gap.Resolved = true
		gap.Resolution = resolution
		resolved++
	}
	if resolved > 0 {
		l.trimResolvedGapsLocked(entry)
	}
	return resolved, nil
}

// RecoveryRef 受管恢复分段在账本中的引用。
type RecoveryRef struct {
	SegmentID     string                        `json:"segment_id"`
	Path          string                        `json:"path"`
	State         logtypes.RecoverySegmentState `json:"state"`
	ReleaseReason logtypes.ReleaseReason        `json:"release_reason,omitempty"`
	// ResponsibilityReceiver 责任接收者（RELEASED 前必填）。
	ResponsibilityReceiver string `json:"responsibility_receiver,omitempty"`
	// CoversFrom/CoversTo 覆盖的 WAL 前缀（exclusive end）。
	CoversFrom uint64 `json:"covers_from"`
	CoversTo   uint64 `json:"covers_to"`
	HasHold    bool   `json:"has_hold"`
	// RetentionDeadlineUnixSec 保留约束；未解除责任前不得普通到期清理。
	RetentionDeadlineUnixSec int64 `json:"retention_deadline_unix_sec,omitempty"`
}

// DeliveryBatch 批次级请求结果；位置与状态分开保存。
type DeliveryBatch struct {
	Start uint64                 `json:"start"`
	End   uint64                 `json:"end"`
	State logtypes.DeliveryState `json:"state"`
}

// Entry 单一 (log_source_id, source_generation) 账本条目。
type Entry struct {
	Key       SourceKey               `json:"key"`
	Identity  logtypes.SourceIdentity `json:"identity"`
	Positions logtypes.Positions      `json:"positions"`

	// DeliveryState 最近批次投递状态；不能用位置编码 UNKNOWN。
	DeliveryState logtypes.DeliveryState `json:"delivery_state"`
	// DeliveryBatches 批次结果；delivery_position 只反映连续前缀。
	DeliveryBatches []DeliveryBatch `json:"delivery_batches"`

	Segments  []Segment      `json:"segments"`
	Rotations []RotationLink `json:"rotations"`
	Gaps      []Gap          `json:"gaps"`
	// Abandonments 是「人工确认永久丢失」裁定的凭据列表（有界，见 DefaultMaxAbandonmentsPerSource）。
	// 它比 Gap 记录活得更久：缺口会被裁剪，而「回收链凭什么跨过那个空洞」的凭据必须留存。
	Abandonments []GapAbandonment `json:"abandonments,omitempty"`
	RecoveryRefs []RecoveryRef    `json:"recovery_refs"`
	ErrorCount   int              `json:"error_count"`
	IngestSeq    uint64           `json:"ingest_seq"`
	// AcquirePaused 容量/预算耗尽时置位；暂停期间不得静默丢数据。
	AcquirePaused bool   `json:"acquire_paused"`
	PauseReason   string `json:"pause_reason,omitempty"`

	// deliveryPrunedThrough 是该源批次最后一次被裁时的水位（进程内状态：不落库、不参与
	// JSON 序列化与索引映射，重启后归零等于强制再裁一次——裁剪幂等，重复裁无害）。
	//
	// 为什么需要它：水位是**唯一**能让既有条目变得可裁的事件（新登记的批次末位在当前读指针附近，
	// 只可能在水位之上），故「水位没变就不必再扫列表」——它把 RecordDelivery（每次投递）与
	// TryReclaim（每轮采集，250ms/源）的裁剪开销从 O(列表长度) 降到 O(1)；否则一个「水位被门禁
	// 长期挡住」的源会每轮扫描整张保留列表（积压场景下可达十万级），把空间优化变成 CPU 负担。
	deliveryPrunedThrough uint64

	// gapMergedTotal / gapFoldedTotal 是缺口定界的进程内计数（观测用，同 deliveryPrunedThrough：
	// 小写字段不落库、不参与索引映射与 JSON 快照，重启归零）。
	// 它们的意义：「缺口条数」不再等于「失败次数」，运维需要知道有多少次上报被合并/折叠掉了，
	// 才能从条数读出真实的故障规模（见 gap_bounds.go 的缺陷 A 说明）。
	gapMergedTotal uint64
	gapFoldedTotal uint64
}

// Ledger 线程安全采集账本。
type Ledger struct {
	mu      sync.RWMutex
	entries map[SourceKey]*Entry
	// nowUnix 可注入时钟，便于测试。
	nowUnix func() int64
	// revisions 记录每个源的**修订号**：任何一次改变该源账本内容的操作都让它 +1。
	//
	// 为什么需要它：上层（采集索引）必须知道「自上次持久化以来哪些源变了」，才能把持久化
	// 成本从 O(全部源) 降到 O(变更源)。判据必须由账本自己在**每个写路径**上推进——若改成
	// 「上层比较快照内容」，上层就得先把全部源的账本深拷贝一遍（含每个源的缺口切片），
	// 生产实测 74.7 万条缺口仅深拷贝就要数十毫秒起，等于把要消除的成本换个地方付；
	// 若改成「约定上层记得标记」，迟早会有写路径漏标 → 静默丢更新。
	// 这里让推进点与锁一起落在写路径内，漏标不可能发生（新增写方法时编译器不会提醒，
	// 但 TestLedgerRevisionAdvancesOnEveryMutation 会）。
	//
	// 读方法（Get/Snapshot/UnresolvedGapCount 等）**不**推进修订号。
	revisions map[SourceKey]uint64
	// batchPrune 是历史投递批次（delivery_batch）的裁剪配置，默认开启（见
	// DefaultDeliveryBatchPruneConfig 与 delivery_batch_prune.go 的判据与证明）。
	// 它是内存与索引两侧同时有界的唯一开关：账本自己持批次列表，故裁剪点必须在这里。
	batchPrune DeliveryBatchPruneConfig
}

// New 创建空账本。
func New() *Ledger {
	return &Ledger{
		entries:    make(map[SourceKey]*Entry),
		revisions:  make(map[SourceKey]uint64),
		nowUnix:    func() int64 { return 0 },
		batchPrune: DefaultDeliveryBatchPruneConfig(),
	}
}

// Revision 返回该源当前的修订号；源未注册时为 0。
//
// 调用方（采集索引）用它做「只持久化变更源」的判据：两次修订号相同即该源账本内容未变。
// 修订号只在进程内有效，不落库、不跨重启可比（持久化只关心「与上次写库相比是否变化」）。
func (l *Ledger) Revision(key SourceKey) uint64 {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.revisions[key]
}

// touch 推进该源的修订号。必须在持有写锁的写路径内调用（见 Ledger.revisions 的注释）。
func (l *Ledger) touch(key SourceKey) {
	if l.revisions == nil {
		l.revisions = make(map[SourceKey]uint64)
	}
	l.revisions[key]++
}

// Snapshot 返回全部账本条目副本，供 Worker 持久化和崩溃恢复使用。
func (l *Ledger) Snapshot() []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Entry, 0, len(l.entries))
	for _, entry := range l.entries {
		if entry != nil {
			out = append(out, *entry.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out
}

// Restore 用持久快照恢复账本。快照是 Worker 自己写出的受信格式，仍执行关键字段校验。
func (l *Ledger) Restore(entries []Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range entries {
		if entry.Key.LogSourceID == "" || entry.Key.SourceGeneration == "" {
			return fmt.Errorf("ledger: invalid restore key %q", entry.Key.String())
		}
		copy := entry.clone()
		// Recompute the derived delivery prefix after replay. A single-byte
		// source line delimiter between adjacent event spans is known coverage,
		// while larger holes remain unresolved and block the prefix.
		copy.Positions.Delivery = contiguousDeliveryEnd(copy.DeliveryBatches, copy.Positions.Reclaim)
		l.entries[entry.Key] = copy
	}
	return nil
}

// SetClock 注入时钟（测试用）。
func (l *Ledger) SetClock(fn func() int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nowUnix = fn
}

// Ensure 注册或取回账本条目。同一 key 幂等。
func (l *Ledger) Ensure(key SourceKey, identity logtypes.SourceIdentity) *Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[key]; ok {
		return e.clone()
	}
	if identity.LogSourceID == "" {
		identity.LogSourceID = key.LogSourceID
	}
	if identity.SourceGeneration == "" {
		identity.SourceGeneration = key.SourceGeneration
	}
	e := &Entry{
		Key:           key,
		Identity:      identity,
		DeliveryState: logtypes.DeliveryNotSent,
	}
	l.entries[key] = e
	return e.clone()
}

// Get 返回条目副本；不存在时返回 nil。
func (l *Ledger) Get(key SourceKey) *Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.entries[key]
	if !ok {
		return nil
	}
	return e.clone()
}

// AdvanceRead 推进运行时读指针。崩溃后允许回退重建——
// **但不得退到回收水位之下**：≤ reclaim 的事件已被证明「安全另存」（这正是 reclaim 的语义），
// 重读它们既浪费，又会让 releaseRecovery 的 from(=reclaim) >= to 恒成立、静默不再建段，
// 最终把回收永久卡死（2026-09-30 生产实证：read=2.24M < reclaim=13.4M → 源反复暂停、
// 积压 7998 永不回落）。故此处把下界夹到 reclaim：只上移，不越过任何未证安全的位置。
func (l *Ledger) AdvanceRead(key SourceKey, pos uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	if pos < e.Positions.Reclaim {
		pos = e.Positions.Reclaim
	}
	e.Positions.Read = pos
	return nil
}

// AdvanceDurable 推进 durable_position。同一 generation 内单调前进。
func (l *Ledger) AdvanceDurable(key SourceKey, pos uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	if pos < e.Positions.Durable {
		return fmt.Errorf("ledger: durable_position must be monotonic within generation: %d < %d", pos, e.Positions.Durable)
	}
	e.Positions.Durable = pos
	return nil
}

// RecordDelivery 登记批次请求级结果。
// HTTP 2xx → REQUEST_DONE；响应丢失 → UNKNOWN（保留恢复责任，不推进 reclaim）。
// delivery_position 取“所有字节均有请求结果”的连续前缀末端，乱序响应不得越过未解决空洞。
//
// 登记后按当前水位裁掉「确定不会再被需要」的历史条目（判据与证明见 delivery_batch_prune.go）：
// 水位之下的条目对该派生值恒等，裁剪不改变本函数算出的 delivery_position。
func (l *Ledger) RecordDelivery(key SourceKey, start, end uint64, state logtypes.DeliveryState) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	if end < start {
		return fmt.Errorf("ledger: invalid delivery batch [%d,%d)", start, end)
	}
	if state == "" {
		return fmt.Errorf("ledger: delivery state must not be empty; UNKNOWN is a state not a position")
	}
	e.DeliveryBatches = append(e.DeliveryBatches, DeliveryBatch{Start: start, End: end, State: state})
	e.DeliveryState = state
	l.pruneDeliveryBatchesLocked(e)
	e.Positions.Delivery = contiguousDeliveryEnd(e.DeliveryBatches, e.Positions.Reclaim)
	return nil
}

// ResolveDeliveryThroughRecovery records that a previously UNKNOWN batch was
// reconciled from a durable projection/recovery source. It preserves the
// original UNKNOWN audit entry and does not fabricate an HTTP success.
func (l *Ledger) ResolveDeliveryThroughRecovery(key SourceKey, start, end uint64) error {
	return l.RecordDelivery(key, start, end, logtypes.DeliveryReplayRequired)
}

// contiguousDeliveryEnd 计算已知请求结果覆盖的连续前缀末端。
// 乱序响应不得用最大位置越过未解决空洞。
// ContiguousDeliveryEnd 暴露派生口径：delivery_position 是「所有字节均有请求结果的连续前缀末端」
// （由投递批次与 reclaim 下界推得），不是可独立设定的真值。
//
// 导出原因：Restore 会按本函数重算 delivery_position，故索引迁移校验在逐字段比对前必须对
// 两侧统一按同一规则重算，否则「派生值」会被误判成不一致（真机现场：归档 JSON 里是写回前的
// 旧值，索引里是重算后的值，二者语义相同）。
func ContiguousDeliveryEnd(batches []DeliveryBatch, floor uint64) uint64 {
	return contiguousDeliveryEnd(batches, floor)
}

func contiguousDeliveryEnd(batches []DeliveryBatch, floor uint64) uint64 {
	if len(batches) == 0 {
		return floor
	}
	type span struct{ start, end uint64 }
	spans := make([]span, 0, len(batches))
	for _, b := range batches {
		if b.End <= b.Start {
			continue
		}
		spans = append(spans, span{b.Start, b.End})
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end < spans[j].end
		}
		return spans[i].start < spans[j].start
	})
	pos := floor
	for _, s := range spans {
		if s.start > pos {
			if s.start == pos+1 {
				pos = s.start
			} else {
				break // 空洞：不越过
			}
		}
		if s.end > pos {
			pos = s.end
		}
	}
	return pos
}

// RegisterSegment 登记物理源分段。
func (l *Ledger) RegisterSegment(key SourceKey, seg Segment) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for i, s := range e.Segments {
		if s.Path == seg.Path {
			e.Segments[i] = seg
			return nil
		}
	}
	e.Segments = append(e.Segments, seg)
	return nil
}

// LinkRotation 登记 latest.log → rotated/.gz 关联。generation 必须沿用被接管内容。
func (l *Ledger) LinkRotation(key SourceKey, fromPath, toPath string, endPosFrom uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for i, r := range e.Rotations {
		if r.FromPath == fromPath && r.ToPath == toPath {
			// 已关联：generation 不变；旧分段 EOF 成功 durable 后允许单调扩展已覆盖前缀。
			if endPosFrom > r.EndPosFrom {
				e.Rotations[i].EndPosFrom = endPosFrom
			}
			return nil
		}
	}
	e.Rotations = append(e.Rotations, RotationLink{
		FromPath:   fromPath,
		ToPath:     toPath,
		EndPosFrom: endPosFrom,
		Generation: key.SourceGeneration,
		LinkedAt:   l.nowUnix(),
	})
	// 更新分段结束位置。
	for i, s := range e.Segments {
		if s.Path == fromPath && endPosFrom > s.EndPos {
			e.Segments[i].EndPos = endPosFrom
		}
	}
	return nil
}

// RotationLinked 报告目标路径是否已作为逻辑源轮转结果登记。
func (l *Ledger) RotationLinked(key SourceKey, path string) (RotationLink, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.entries[key]
	if !ok {
		return RotationLink{}, false
	}
	for _, r := range e.Rotations {
		if r.ToPath == path || r.FromPath == path {
			return r, true
		}
	}
	// 也按 segment 登记判断。
	for _, s := range e.Segments {
		if s.Path == path && s.Imported {
			return RotationLink{ToPath: path, Generation: key.SourceGeneration, EndPosFrom: s.StartPos}, true
		}
	}
	return RotationLink{}, false
}

// RecordGap 登记缺口。容量暂停/坏记录不得静默丢弃。
//
// 登记不再是无条件 append：同因、相邻/重叠的失败会并入既有区间，条数越界时按原因折叠，
// 使「一次长故障」在账本上恒为常数条（判据与边界见 gap_bounds.go）。
// 语义不变：登记只表示「这段可能没落库、需要补」，区间只扩大不缩小。
func (l *Ledger) RecordGap(key SourceKey, start, end uint64, reason, detail string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	e.ErrorCount++
	l.recordGapLocked(e, start, end, reason, detail)
	l.enforceGapBoundsLocked(e)
	return nil
}

// PauseAcquire 容量耗尽：暂停低优先级采集并暴露原因。
func (l *Ledger) PauseAcquire(key SourceKey, reason string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	e.AcquirePaused = true
	e.PauseReason = reason
	return nil
}

// ResumeAcquire 恢复采集。
func (l *Ledger) ResumeAcquire(key SourceKey) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for _, gap := range e.Gaps {
		if !gap.Resolved {
			return fmt.Errorf("ledger: unresolved gaps still block acquisition")
		}
	}
	e.AcquirePaused = false
	e.PauseReason = ""
	return nil
}

// RegisterRecovery 登记受管恢复分段引用。
func (l *Ledger) RegisterRecovery(key SourceKey, ref RecoveryRef) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for _, r := range e.RecoveryRefs {
		if r.SegmentID == ref.SegmentID {
			if r.Path != ref.Path || r.CoversFrom != ref.CoversFrom || r.CoversTo != ref.CoversTo {
				return fmt.Errorf("ledger: recovery segment %s identity conflict", ref.SegmentID)
			}
			// Idempotent restart binding must not erase an advanced state, hold,
			// receiver or release proof.
			return nil
		}
	}
	e.RecoveryRefs = append(e.RecoveryRefs, ref)
	return nil
}

// TransitionRecovery 推进恢复分段责任状态。
// 进入 RELEASED 前必须登记合法 release_reason 与责任接收者（F-004）。
// CLEANED 只能从 RELEASED 进入；hold 中禁止释放。
func (l *Ledger) TransitionRecovery(key SourceKey, segmentID string, to logtypes.RecoverySegmentState, reason logtypes.ReleaseReason, receiver string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for i, r := range e.RecoveryRefs {
		if r.SegmentID != segmentID {
			continue
		}
		if r.HasHold && (to == logtypes.RecoveryReleased || to == logtypes.RecoveryCleaned) {
			return fmt.Errorf("ledger: cannot release recovery segment %s while hold is set", segmentID)
		}
		if recoveryRank(to) < recoveryRank(r.State) {
			return fmt.Errorf("ledger: recovery transition cannot move backward %s → %s", r.State, to)
		}
		if to == r.State {
			return nil
		}
		switch to {
		case logtypes.RecoveryReleased:
			if !logtypes.ValidReleaseReason(reason) {
				return fmt.Errorf("ledger: invalid release_reason %q", reason)
			}
			if receiver == "" || receiver == segmentID || strings.HasPrefix(receiver, "self:") {
				return fmt.Errorf("ledger: invalid responsibility receiver %q", receiver)
			}
			if r.State != logtypes.RecoveryWALResponsibilityXfer {
				return fmt.Errorf("ledger: cannot release recovery segment from state %s without WAL_RESPONSIBILITY_TRANSFERRED", r.State)
			}
		case logtypes.RecoveryCleaned:
			if r.State != logtypes.RecoveryReleased {
				return fmt.Errorf("ledger: CLEANED only allowed from RELEASED, got %s", r.State)
			}
			// 清理不要求新 reason；沿用已登记证明。
		case logtypes.RecoveryWALResponsibilityXfer:
			if r.State != logtypes.RecoveryDurableVerified && r.State != logtypes.RecoveryStaged && r.State != logtypes.RecoveryWALResponsibilityXfer {
				return fmt.Errorf("ledger: invalid recovery transition %s → %s", r.State, to)
			}
		}
		r.State = to
		if reason != "" {
			r.ReleaseReason = reason
		}
		if receiver != "" {
			r.ResponsibilityReceiver = receiver
		}
		e.RecoveryRefs[i] = r
		return nil
	}
	return fmt.Errorf("ledger: recovery segment %q not found", segmentID)
}

func recoveryRank(state logtypes.RecoverySegmentState) int {
	switch state {
	case logtypes.RecoveryStaged:
		return 1
	case logtypes.RecoveryDurableVerified:
		return 2
	case logtypes.RecoveryWALResponsibilityXfer:
		return 3
	case logtypes.RecoveryReleased:
		return 4
	case logtypes.RecoveryCleaned:
		return 5
	default:
		return 0
	}
}

// SetRecoveryHold 设置/清除恢复分段 hold；有 hold 时禁止 reclaim。
func (l *Ledger) SetRecoveryHold(key SourceKey, segmentID string, hasHold bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for i, r := range e.RecoveryRefs {
		if r.SegmentID == segmentID {
			r.HasHold = hasHold
			e.RecoveryRefs[i] = r
			return nil
		}
	}
	return fmt.Errorf("ledger: recovery segment %q not found", segmentID)
}

// TryReclaim 按 logtypes.CanReclaim 门禁推进 reclaim_position。
// HTTP 2xx / REQUEST_DONE 单独不构成回收依据。
//
// 无论回收是否放行，都用**最终**水位裁一次历史投递批次（水位是本方法唯一的输入、裁剪幂等）；
// 因此「回收被门禁挡住」的源同样会被裁剪，不会因为挡着就长期保留无用历史。
func (l *Ledger) TryReclaim(key SourceKey) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return 0, err
	}
	pos, err := l.tryReclaimLocked(e)
	l.pruneDeliveryBatchesLocked(e)
	return pos, err
}

// tryReclaimLocked 是 TryReclaim 的门禁主体。调用方必须已持写锁。
func (l *Ledger) tryReclaimLocked(e *Entry) (uint64, error) {
	// 选取覆盖当前 reclaim 前缀的恢复分段。
	var ref *RecoveryRef
	for i := range e.RecoveryRefs {
		r := &e.RecoveryRefs[i]
		if r.CoversFrom <= e.Positions.Reclaim && r.CoversTo > e.Positions.Reclaim {
			ref = r
			break
		}
	}
	if ref == nil {
		// 无恢复分段时：仅当 CanReclaim 在“无 seg”语义下不放行。
		// 契约要求受管恢复副本证明；没有分段不得回收。
		return e.Positions.Reclaim, fmt.Errorf("ledger: reclaim blocked: no recovery segment covers position %d", e.Positions.Reclaim)
	}
	// CanReclaim 是回收门禁：STAGED/DURABLE_VERIFIED/hold 一律拒绝。
	allowed := logtypes.CanReclaim(e.Positions, ref.State, ref.ReleaseReason, ref.HasHold)
	if !allowed && !ref.HasHold {
		// 凭据放行（2026-10-02，用户质疑「永久空洞的出口」）：
		// 当本分段覆盖的区间内已有人工裁定的**永久丢失**空洞（PERMANENTLY_LOST + 审计凭据）时，
		// 恢复责任**不可能**再通过校验转移（数据物理没了），常规门禁会让回收链永久卡在空洞之前
		// → WAL 积压永不回落 → 滞回永不满足 → 源永久 PAUSED。
		//
		// 这是**独立的有凭据的放行**，不是放宽门禁本身：没有凭据时依旧拒绝（default-deny 未动）。
		// 放行范围严格限定在**本分段覆盖之上界**（target 仍是 ref.CoversTo），不会因为一个凭据
		// 就把回收推到任意位置；hold 优先于凭据（hold 是运维显式钉住，凭据是裁定丢失，两者冲突时
		// 以"不许动"为准）。
		if ab, ok := l.abandonmentIntersectingLocked(e, e.Positions.Reclaim, ref.CoversTo); ok {
			slog.Warn("回收链凭「永久丢失」凭据跨过空洞（该空洞由人工裁定且留有审计凭据）",
				"logSourceID", e.Key.LogSourceID, "sourceGeneration", e.Key.SourceGeneration,
				"reclaimFrom", e.Positions.Reclaim, "segmentCoversTo", ref.CoversTo,
				"abandonedFrom", ab.From, "abandonedTo", ab.To,
				"operator", ab.Operator, "at", ab.AtUTC, "through", ab.Through)
			allowed = true
		}
	}
	if !allowed {
		return e.Positions.Reclaim, fmt.Errorf("ledger: reclaim blocked by CanReclaim: state=%s reason=%q hold=%v delivery=%d reclaim=%d",
			ref.State, ref.ReleaseReason, ref.HasHold, e.Positions.Delivery, e.Positions.Reclaim)
	}
	target := ref.CoversTo
	if e.Positions.Delivery > 0 && e.Positions.Delivery < target && ref.State == logtypes.RecoveryWALResponsibilityXfer {
		// 责任已转移：可覆盖未确认事件，允许 reclaim 推进到分段覆盖末端。
		target = ref.CoversTo
	}
	if target <= e.Positions.Reclaim {
		return e.Positions.Reclaim, nil
	}
	e.Positions.Reclaim = target
	return e.Positions.Reclaim, nil
}

// IncrementIngestSeq 递增本地单调接纳序号（重放沿用原值由调用方保证）。
func (l *Ledger) IncrementIngestSeq(key SourceKey) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return 0, err
	}
	e.IngestSeq++
	return e.IngestSeq, nil
}

func (l *Ledger) require(key SourceKey) (*Entry, error) {
	e, ok := l.entries[key]
	if !ok {
		return nil, fmt.Errorf("ledger: source %s not registered", key)
	}
	// 修订号在此推进（而不是逐个写方法里写 `l.touch(key)`）：require 是所有**写路径**
	// 的统一入口（读路径用 RLock 直取 entries），因此推进点与锁一起落在唯一的必经之地，
	// 新增写方法时不可能漏标。只有「确实改变了内容」的调用方才需要留意不误报——
	// 误报只会多写几行（正确性无损），漏报会静默丢更新，故宁可保守。
	l.touch(key)
	return e, nil
}

func (e *Entry) clone() *Entry {
	if e == nil {
		return nil
	}
	cp := *e
	cp.DeliveryBatches = append([]DeliveryBatch(nil), e.DeliveryBatches...)
	cp.Segments = append([]Segment(nil), e.Segments...)
	cp.Rotations = append([]RotationLink(nil), e.Rotations...)
	cp.Gaps = append([]Gap(nil), e.Gaps...)
	cp.Abandonments = append([]GapAbandonment(nil), e.Abandonments...)
	cp.RecoveryRefs = append([]RecoveryRef(nil), e.RecoveryRefs...)
	return &cp
}
