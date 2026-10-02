// Package acquire 实现 FR-474 采集通道：FileTailer / ArchiveImporter / WAL / 容量门禁。
// 与 normalize 的边界通过 EventBoundaryHook 导出，本包不实现 FR-475 解析逻辑。
package acquire

import (
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// WALRef 是 WAL 条目的**引用形式**：只保存身份与位置，正文以 canonical 事件段存储
// （events/，FR-484 的权威副本）为准（B1a）。
//
// 为什么需要：WALEntry 内联 logtypes.Event（含正文），而整份 WAL 会随 ingest.state.json
// 持久化——单源积压曾因此把状态文件撑到 1.2 GB（2026-09-28 生产事故）。
//
// 关键约束（第一次实现曾在此出错并被既有测试抓到）：段存储里**未必**有 WAL 的全部事件——
// 事件体是在投递路径（writeProjection → appendEvents）才写入段存储，故尚未投递的条目
// 只能内联保存。判据必须**按条**给出（见 SnapshotSplit 的 covered 谓词），不得按源一刀切。
type WALRef struct {
	Seq         uint64 `json:"seq"`
	Appended    bool   `json:"appended"`
	Durable     bool   `json:"durable"`
	EventID     string `json:"event_id"`
	RecordStart uint64 `json:"record_start"`
	RecordEnd   uint64 `json:"record_end"`
}

// SnapshotSplit 按 covered 谓词把 WAL 快照切分为两部分（B1a）：
//   - refs：covered(ev) 为 true——该事件体已 durable 在段存储中，持久化只需引用；
//   - inline：其余条目——正文尚未入库（未投递）或存储不可用，必须内联保存，绝不能丢。
//
// covered 传 nil 时全部内联（等价于旧的 Snapshot）。
func (w *WAL) SnapshotSplit(covered func(logtypes.Event) bool) (refs []WALRef, inline []WALEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range w.entries {
		if covered != nil && covered(e.Event) {
			refs = append(refs, WALRef{
				Seq: e.Seq, Appended: e.Appended, Durable: e.Durable,
				EventID: e.Event.EventID, RecordStart: e.Event.Record.Start, RecordEnd: e.Event.Record.End,
			})
			continue
		}
		inline = append(inline, e)
	}
	return refs, inline
}

// RestoreMixed 恢复内存 WAL：inline 为内联正文条目，refs 为引用条目（正文经 hydrate 取回）。
//
// 合并后按 Seq 升序排列，保持投递顺序。取不到正文的引用**不得静默丢弃**：以 missing 原样
// 返回，由调用方记可见缺口并告警（与 FR-474 §5#1「截断可见」同口径）。
func (w *WAL) RestoreMixed(inline []WALEntry, refs []WALRef, hydrate func(eventID string) (logtypes.Event, bool)) []WALRef {
	w.mu.Lock()
	defer w.mu.Unlock()
	entries := append([]WALEntry(nil), inline...)
	var missing []WALRef
	for _, r := range refs {
		ev, ok := hydrate(r.EventID)
		if !ok {
			missing = append(missing, r)
			continue
		}
		entries = append(entries, WALEntry{Seq: r.Seq, Event: ev, Appended: r.Appended, Durable: r.Durable})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
	w.entries = entries
	for _, entry := range entries {
		if entry.Durable && entry.Event.Record.End > w.lastDurableEnd {
			w.lastDurableEnd = entry.Event.Record.End
		}
	}
	return missing
}

// WAL 积压上限（B1b，由 2026-09-28 生产事故驱动）。
//
// 背景：某源投递/校验长时间失败 → reclaim 永不推进 → 未投递条目无界累积；而每条 WALEntry
// 内联完整事件正文、且随 ingest.state.json 整体持久化，生产实测单源把该文件撑到 1.2 GB，
// 每次持久化全量重写 → 持续 117 MB/s、Worker CPU 138%、整机 iowait 90%，进而拖慢同机 CP 的
// SQLite（慢查询 0.4–2.4 s）。故必须为「单源积压」定界，使一个源的洪流不能拖垮整机。
//
// 处置语义刻意选择**暂停而非丢弃**：超上限经账本 PauseAcquire 暂停该源采集（FileTailer 与
// 本 WAL 都据此停止），回落至低水位后由本包自动恢复——与 ledger「暂停期间不得静默丢数据」
// 的既有约定一致；暂停期间源文件继续增长，恢复后按游标继续读，不丢数据。
const (
	// defaultWALMaxEntries / defaultWALMaxBytes 是单源的默认积压上限（0 表示用默认值）。
	// 取值的取舍：正常源（约 1 事件/秒）可积压约 80 分钟，足以覆盖 VL 短暂不可用；
	// 而最坏情况下每源对状态的贡献被限定在十几 MB 级，不再出现 GB 级状态文件。
	defaultWALMaxEntries = 5000
	defaultWALMaxBytes   = 16 << 20 // 16 MiB

	// walBacklogPauseReason 是暂停原因的前缀，用于『只自动恢复本包造成的暂停』判别——
	// 容量门禁等其它路径设置的暂停不得被本包清除（见 maybeResumeBacklogLocked）。
	walBacklogPauseReason = "wal backlog limit exceeded"
)

// walEntryOverheadBytes 是每条 WALEntry 除正文外的近似 JSON 开销
// （seq/appended/durable + event 的 id/hash/level/stream/record 等字段）。
// 仅用于积压字节数的近似定界，不参与任何持久化或校验语义。
const walEntryOverheadBytes = 320

// WALEntry WAL 中的一条待投递/已耐久事件记录。
type WALEntry struct {
	Seq      uint64         `json:"seq"`
	Event    logtypes.Event `json:"event"`
	Appended bool           `json:"appended"`
	Durable  bool           `json:"durable"`
}

// DeliveryResult 请求级投递结果模拟。
type DeliveryResult struct {
	StartPos uint64
	EndPos   uint64
	// HTTPStatus 2xx → REQUEST_DONE；AckLost → UNKNOWN。
	HTTPStatus int
	AckLost    bool
}

// WAL 本地预写日志：append → fsync/等效提交 → durable → delivery → reclaim。
type WAL struct {
	mu  sync.Mutex
	led *ledger.Ledger
	key ledger.SourceKey

	entries []WALEntry
	// fsync 持久提交钩子；nil 视为成功（测试可注入失败）。
	fsync func() error

	// lastDurableEnd 已耐久前缀末端（源位置）。
	lastDurableEnd uint64
	// pendingDelivery 已 append/耐久但尚无请求结果的源位置前缀。
	pendingStart uint64

	// recoverySegs WAL 关联的恢复分段登记（由 pipeline 更新账本）。
	recoverySegID string

	// maxEntries / maxBytes 是单源积压上限（B1b）；0 表示用本包默认常量。
	// 作为字段以便测试用小额度覆盖真实限额路径，而非只断言常量本身。
	maxEntries int64
	maxBytes   int64
	// recoveryWiden 是**恢复期独立配额**的拓宽系数（≤1 = 常规闸；见 SetRecoveryWiden ✓）。
	recoveryWiden float64
}

// SetLimits 覆盖单源积压上限（0 表示沿用默认）。供测试与配置化使用。
func (w *WAL) SetLimits(maxEntries, maxBytes int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.maxEntries = maxEntries
	w.maxBytes = maxBytes
}

// ApplyLimits 按结构体形式应用积压上限（配置接线用；零值字段沿用本包默认）。
func (w *WAL) ApplyLimits(l WALLimits) { w.SetLimits(l.MaxEntries, l.MaxBytes) }

// WALLimits 是单源 WAL **真实积压**上限（条目数 + 近似字节），用于配置接线。
//
// 与「累计追加量」严格区分：上限只约束**当前待投递积压**（backlog），因为只有它会随 reclaim
// 推进回落，超限后的暂停才有可能被滞回自动清除。累计量只增不减，拿它当门禁会把源永久钉在
// 暂停上（形态与现场后果见 acquire/pipeline.go 的 walAppendedBytesTotal 注释）。
type WALLimits struct {
	// MaxEntries 单源积压条目数上限（0 沿用包内默认 defaultWALMaxEntries）。
	MaxEntries int64
	// MaxBytes 单源积压近似字节上限（0 沿用包内默认 defaultWALMaxBytes）。
	MaxBytes int64
}

// Backlog 返回**当前**积压读数（条目数, 近似字节）。
//
// 这是唯一可用于容量门禁的 WAL 口径：它随 reclaim 推进而回落，因此在积压回落后
// 由滞回解除暂停是可能的；累计追加量不具此性质。
func (w *WAL) Backlog() (int64, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.backlogLocked()
}

// limitsLocked 返回生效的上限（调用方需持 w.mu）。
// SetRecoveryWiden 设置**恢复期独立配额**的拓宽系数（≤1 或 NaN ⇒ 关闭，回归常规闸 ✓）。
//
// 为什么需要（2026-10-04 现场，用户定调方案②）：恢复期的回放/补账是**设计行为**（gz 归档侧补账
// 写回原代次 backlog 待投递 ✓），它必然把源推过常规闸 ⇒ 常规闸在恢复期**注定自锁**：
// 「越闸暂停 ⇒ 更排不空 ⇒ 更回放不了」✗✓。因此恢复期按**独立预算**判（默认 4× 常规闸 ✓，可配 ✓），
// 恢复完成后回归常规闸 ✓（回归由 ingest 侧的兜底环按恢复状态驱动 ✓）。
//
// **有界性红线**：拓宽依然是**有限值** ✓ ⇒ `source_wal` 的无界防护不破 ✗（4× 也只是一个更大的界 ✓）。
func (w *WAL) SetRecoveryWiden(factor float64) {
	if w == nil {
		return
	}
	if factor < 1 || factor != factor { // <1 或 NaN ⇒ 关闭
		factor = 1
	}
	w.mu.Lock()
	w.recoveryWiden = factor
	w.mu.Unlock()
}

// recoveryWidenLocked 返回生效的拓宽系数（默认 1 = 常规闸 ✓）。
func (w *WAL) recoveryWidenLocked() float64 {
	if w.recoveryWiden > 1 {
		return w.recoveryWiden
	}
	return 1
}

func (w *WAL) limitsLocked() (int64, int64) {
	entries, bytes := w.maxEntries, w.maxBytes
	if entries <= 0 {
		entries = defaultWALMaxEntries
	}
	if f := w.recoveryWidenLocked(); f > 1 {
		// 恢复期独立配额：两维同倍拓宽（有界 ✓）。条目维至少加 1，避免小上限被取整成"没拓宽" ✗。
		if entries > 0 {
			entries = int64(float64(entries) * f)
		}
		if bytes > 0 {
			bytes = int64(float64(bytes) * f)
		}
	}
	switch {
	case bytes < 0:
		// **负值 = 字节维度不设上限**（键 log_capacity.max_wal_bytes=0 的语义）。
		//
		// 为什么必须有这个哨兵（2026-10-03 现场 16 MiB 之谜）：配置侧 0 表示"字节维度不限"
		// （见 Config.WALBudgetNotice 的语义说明），而本处在 0 时回退**硬编码 16 MiB** ✗——
		// 于是「不设上限」被静默实现成「16 MiB」，现场表现为一批源停在 32.6 MiB（条目/字节双闸）
		// 而另一批停在 512 MiB（显式配了值）✓✓。语义必须能被表达，而不是靠默认值顶替 ✗。
		bytes = math.MaxInt64
	case bytes == 0:
		bytes = defaultWALMaxBytes
	}
	return entries, bytes
}

// backlogLocked 统计当前积压条目数与近似字节数（调用方需持 w.mu）。
func (w *WAL) backlogLocked() (int64, int64) {
	var bytes int64
	for _, entry := range w.entries {
		bytes += int64(len(entry.Event.Message)) + walEntryOverheadBytes
	}
	return int64(len(w.entries)), bytes
}

// IsBacklogPauseReason 报告某条暂停原因是否由本包的**积压上限**造成（B1b 条目/字节闸）。
//
// 为什么要导出（2026-10-03 现场 D4）：积压暂停带**滞回**（积压回落到上限一半以下才自动恢复），
// 因此任何「越权 ResumeAcquire」都会让条目闸失效——现场实测积压涨到 113.9 万条（名义上限 5000）。
// 调用方（解缺口、放弃裁定等收尾路径）据此避免清除不属于它的暂停。
func IsBacklogPauseReason(reason string) bool {
	return strings.HasPrefix(reason, walBacklogPauseReason)
}

// enforceBacklogLimitLocked 在追加后检查积压是否越界；越界即暂停该源采集（B1b）。
//
// 刻意不在这里丢弃任何条目、也不返回错误：本批事件已入 WAL 且已推进 read_position，
// 返回错误会让调用方误判为「追加失败」并重试。暂停对**后续**追加生效——Append 的既有
// 首判据（AcquirePaused）会拒绝并提示必须记 gap，FileTailer 同样据此停止读取。
// 调用方需持 w.mu。
func (w *WAL) enforceBacklogLimitLocked(ent *ledger.Entry) {
	if ent == nil || ent.AcquirePaused {
		return
	}
	entries, bytes := w.backlogLocked()
	maxEntries, maxBytes := w.limitsLocked()
	if entries <= maxEntries && bytes <= maxBytes {
		return
	}
	reason := fmt.Sprintf("%s (entries=%d/%d, approx_bytes=%d/%d); acquisition paused to bound recovery state",
		walBacklogPauseReason, entries, maxEntries, bytes, maxBytes)
	if err := w.led.PauseAcquire(w.key, reason); err != nil {
		slog.Warn("暂停采集失败（积压仍越界，下轮重试）", "logSourceID", w.key.LogSourceID, "error", err)
		return
	}
	slog.Warn("单源积压超过上限，已暂停该源采集（不丢数据，回落低水位后自动恢复）",
		"logSourceID", w.key.LogSourceID, "entries", entries, "maxEntries", maxEntries,
		"approxBytes", bytes, "maxBytes", maxBytes)
}

// maybeResumeBacklogLocked 在回收后按低水位恢复采集。
//
// 恢复条件（三选一不满足即不恢复）：① 当前暂停确实由本包造成（原因前缀匹配，
// 不越权清除容量门禁等其它路径的暂停）；② 积压已回落至上限的一半以下（滞回，避免抖动）；
// ③ 账本无未解决 gap（ResumeAcquire 自身会拒绝，这里先判以免噪声）。调用方需持 w.mu。
func (w *WAL) maybeResumeBacklogLocked(ent *ledger.Entry) {
	if ent == nil || !ent.AcquirePaused || !strings.HasPrefix(ent.PauseReason, walBacklogPauseReason) {
		return
	}
	entries, bytes := w.backlogLocked()
	maxEntries, maxBytes := w.limitsLocked()
	if entries > maxEntries/2 || bytes > maxBytes/2 {
		return
	}
	if err := w.led.ResumeAcquire(w.key); err != nil {
		// **打破环死**（2026-10-04 现场：60 源零积压仍无一动 ✗）：`ResumeAcquire` 对任何未消解
		// 缺口一律拒绝 ✗，而 `APPEND_REJECTED` 的定义是"该批从未进入 WAL（采集被暂停/容量门禁
		// 拒了）"⇒ 它只能靠**回读**愈合 ⇐ 回读在暂停期间被 tailer 拒绝 ✗ ⇒
		// 「暂停 ⇒ 缺口不清 ⇒ 不许恢复 ⇒ 不回读」自锁 ✓✓。
		// 这里按**分类裁定**消解这一类（不是操作员放弃 ✗）：恢复后回读会重新产生并投递该批 ✓
		// （不丢数据；重复按 event_id 幂等 ✓）。其余原因保持拦截 ✓。
		if healed := w.resolveReReadHealableGapsLocked(ent); healed > 0 {
			if err2 := w.led.ResumeAcquire(w.key); err2 == nil {
				slog.Info("积压已回落：已按「恢复后回读重放」分类消解暂停期缺口并恢复采集",
					"logSourceID", w.key.LogSourceID, "gaps", healed, "entries", entries, "approxBytes", bytes)
				return
			}
		}
		slog.Info("积压已回落但暂不能恢复采集", "logSourceID", w.key.LogSourceID, "error", err)
		return
	}
	slog.Info("积压已回落至低水位，已恢复该源采集",
		"logSourceID", w.key.LogSourceID, "entries", entries, "approxBytes", bytes)
}

// gapResolutionReReadOnResume 是本包对「暂停期从未进 WAL 的批」的**分类裁定**。
//
// 与"操作员放弃"（AbandonGaps）严格区分 ✓：放弃是人的裁定（FreeText），本标记是**机械推论**——
// 恢复后回读必然重新产生该批 ✓，因此它不构成"已确认落库"，只是"将在恢复后重放" ✓。
const gapResolutionReReadOnResume = "re-read on resume (append rejected while paused)"

// gapResolutionReReadOnResumeDelivery 是投递失败类缺口的分类裁定文本（与上一条**明确分账** ✓）。
//
// 为什么这一类也进白名单（2026-10-04 现场，用户批准的红线修正 ✓）：`DELIVER_ERROR` 的语义是
// "投递时 VL 不可达"（`connect: connection refused` ✓）⇒ 恢复后**回读会让恢复链重投那段
// `UNSENT basis=gap` 区间**（现场 `inst:156` 覆盖 119.78MB ✓、紧贴水位 ✓）⇒ 它**恰恰是可愈合的** ✓✓。
// 我上批假设"投递失败类回读治不了 ⇒ 必须永远挡住恢复" ✗ 被现场 67 条恒等不变**证否** ✓（那 67 条
// 所属源已暂停 ⇒ 活投递事件结构性不可达 ⇒ 两种证据都拿不到 ⇒ 永久停死 ✗）。
const gapResolutionReReadOnResumeDelivery = "re-read on resume (delivery error while paused)"

// gapResolutionExcludedReasons 是**绝不能被本条路径放行**的缺口原因（fail-closed 名单 ✓）。
//
// 抽成包级变量（而不是内联字面量）是为了让"放开一因 ⇒ 红"**可被变异触达** ✓：内联时该列表只会在
// 处理 APPEND_REJECTED 时被读到 ⇒ 任何"删一项"的变异都不可达 ✗（实测：删项后用例仍绿 ✗✗），
// 于是那条红线只有断言、没有证据 ✗。名单语义不变：**未列出者一律不放行** ✓（新增原因默认排除 ✓）。
var gapResolutionExcludedReasons = []string{
	// STDIO_RAW_WRITE_FAILED：原始字节**从未落盘**（写原始文件就失败了）⇒ 重投无据 ⇒ 必须挡住 ✓
	// （这是"源文件对应字节已不再可得"的等价形态 ✓ —— 语义断言见对应用例 ✓）。
	ledger.GapReasonStdioRawWriteFailed,
	// WAL_COMMIT_FAILED：提交阶段的失败没有"内容确实存在"的依据 ⇒ 同样挡住 ✓。
	ledger.GapReasonWALCommitFailed,
	// DELIVER_ERROR / DELIVER_ERROR_WORKER_SOURCE 已于 2026-10-04 移入**白名单**
	// （见 gapResolutionReReadOnResumeDelivery ✓，用户批准的红线修正 ✓）。
}

// resolveReReadHealableGapsLocked 只消解 REASON=APPEND_REJECTED 的未消解缺口（见调用点注释）。
//
// fail-closed（关键）：其余四种已知原因**显式排除** ⇒ 将来若新增原因，默认同样被排除 ✓
// （不会被这条路径悄悄放行 ✗）。返回消解条数。
func (w *WAL) resolveReReadHealableGapsLocked(ent *ledger.Entry) int {
	if ent == nil {
		return 0
	}
	// 两类各自成批消解（文本分账 ✓），**其余原因一律默认排除** ✓（fail-closed：每条调用都显式
	// 列出排除集 ✓，将来新增原因不会被任何一条悄悄放行 ✓）。
	total := 0
	through := uint64(0)
	for _, g := range ent.Gaps {
		if g.Resolved || g.Reason != ledger.GapReasonAppendRejected {
			continue
		}
		if g.EndPos > through {
			through = g.EndPos
		}
	}
	if through > 0 {
		n, err := w.led.ResolveGapsThroughExcept(w.key, through, gapResolutionReReadOnResume,
			gapResolutionExcludedReasons...,
		)
		if err == nil {
			total += n
		}
	}

	deliverThrough := uint64(0)
	for _, g := range ent.Gaps {
		if g.Resolved {
			continue
		}
		if g.Reason != ledger.GapReasonDeliverError && g.Reason != ledger.GapReasonDeliverErrorWorkerSource {
			continue
		}
		if g.EndPos > deliverThrough {
			deliverThrough = g.EndPos
		}
	}
	if deliverThrough > 0 {
		n, err := w.led.ResolveGapsThroughExcept(w.key, deliverThrough, gapResolutionReReadOnResumeDelivery,
			gapResolutionExcludedReasons...,
		)
		if err == nil {
			total += n
		}
	}
	return total
}

// EvaluateResume 尝试一次「推进回收 + 按滞回条件评估恢复」，返回**账本当前是否不在暂停态**。
//
// 用在「缺口刚被消解、源仍处于暂停」的时刻：恢复的唯一检查点挂在回收路径上，而回收路径
// 又要求「有可剪条目」或「有新批次」——暂停期间两者都不会出现，缺了本入口，
// 消解缺口之后源仍会一直停在暂停上。
//
// 先 TryReclaim 再评估：缺口消解后 CanReclaim 往往才放行（回收门禁要求责任已转移），
// 不推进回收就永远看不到「积压回落至低水位」这个恢复条件。
//
// 返回值口径（与实现严格对齐，勿按名字误读）：它只回答「账本此刻有没有处于暂停」，
// **不做原因过滤**。具体地：
//   - 本包造成的积压暂停（原因前缀 walBacklogPauseReason）会由 maybeResumeBacklogLocked
//     在积压回落至上限一半以下时清除，清掉后本方法返回 true；
//   - 容量门禁等**其它路径**设置的暂停不会被本方法清除（那是越权），但只要它已被别处解除，
//     本方法同样会返回 true；反之若仍处于暂停，则返回 false。
//
// 因此调用方可以用它判断「恢复是否已发生」，但不得把它当成「本次调用清除了暂停」的证据。
func (w *WAL) EvaluateResume() bool {
	// TryReclaim 的失败是常态而非异常（无恢复分段 / 责任未转移时 CanReclaim 本就不放行），
	// 而它无论成败都会按**当前**水位剪枝并在剪枝后做一次滞回恢复评估；这里忽略返回值，
	// 只按最终状态回答「是否已恢复」。
	_, _ = w.TryReclaim()
	entry := w.led.Get(w.key)
	return entry != nil && !entry.AcquirePaused
}

// NewWAL 创建绑定账本条目的 WAL。
func NewWAL(led *ledger.Ledger, key ledger.SourceKey) *WAL {
	led.Ensure(key, logtypes.SourceIdentity{
		LogSourceID:      key.LogSourceID,
		SourceGeneration: key.SourceGeneration,
		ParserVersion:    "acquire-v1",
	})
	return &WAL{
		led:          led,
		key:          key,
		pendingStart: 0,
	}
}

// SetFsync 注入持久提交实现（失败则 durable 不推进）。
func (w *WAL) SetFsync(fn func() error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fsync = fn
}

// Append 将事件写入 WAL（尚未 durable）。
// 容量门禁：暂停时拒绝 append，由调用方记 gap；禁止静默丢弃。
func (w *WAL) Append(events ...logtypes.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	ent := w.led.Get(w.key)
	if ent == nil {
		return fmt.Errorf("acquire: wal source %s missing", w.key)
	}
	if ent.AcquirePaused {
		return fmt.Errorf("acquire: paused (%s); must record gap, not silent drop", ent.PauseReason)
	}

	for _, ev := range events {
		w.entries = append(w.entries, WALEntry{
			Seq:      uint64(len(w.entries) + 1),
			Event:    ev,
			Appended: true,
		})
	}
	// read_position 可先推进；durable 等 Commit。
	end := lastRecordEnd(events, ent.Positions.Read)
	if err := w.led.AdvanceRead(w.key, end); err != nil {
		return err
	}
	// 追加后检查积压上限（B1b）：越界即暂停该源采集，避免单源洪流无界增长拖垮整机。
	w.enforceBacklogLimitLocked(w.led.Get(w.key))
	return nil
}

// Commit fsync/等效提交后推进 durable_position。
func (w *WAL) Commit() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.fsync != nil {
		if err := w.fsync(); err != nil {
			return fmt.Errorf("acquire: wal fsync failed, durable not advanced: %w", err)
		}
	}
	var maxEnd uint64
	for i := range w.entries {
		if w.entries[i].Durable {
			continue
		}
		w.entries[i].Durable = true
		if w.entries[i].Event.Record.End > maxEnd {
			maxEnd = w.entries[i].Event.Record.End
		}
	}
	if maxEnd <= w.lastDurableEnd {
		maxEnd = w.lastDurableEnd
	}
	if maxEnd > 0 {
		if err := w.led.AdvanceDurable(w.key, maxEnd); err != nil {
			return err
		}
		w.lastDurableEnd = maxEnd
	}
	if w.pendingStart == 0 {
		w.pendingStart = maxEnd
	}
	return nil
}

// RecordHTTPResult 登记请求级结果。
// HTTP 2xx 只写 REQUEST_DONE，不推进 reclaim_position。
// AckLost → UNKNOWN，保留恢复责任。
func (w *WAL) RecordHTTPResult(res DeliveryResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var state logtypes.DeliveryState
	switch {
	case res.AckLost:
		state = logtypes.DeliveryUnknown
	case res.HTTPStatus >= 200 && res.HTTPStatus < 300:
		state = logtypes.DeliveryRequestDone
	default:
		// 非 2xx 且非 AckLost：仍按请求结果登记，保持可审计。
		state = logtypes.DeliveryUnknown
	}
	if err := w.led.RecordDelivery(w.key, res.StartPos, res.EndPos, state); err != nil {
		return err
	}
	// REQUEST_DONE / UNKNOWN 都不直接 reclaim。
	return nil
}

// BindRecoverySegment 将 WAL 前缀绑定到受管恢复分段（STAGED）。
func (w *WAL) BindRecoverySegment(segID, path string, coversFrom, coversTo uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recoverySegID = segID
	return w.led.RegisterRecovery(w.key, ledger.RecoveryRef{
		SegmentID:  segID,
		Path:       path,
		State:      logtypes.RecoveryStaged,
		CoversFrom: coversFrom,
		CoversTo:   coversTo,
	})
}

// RecoverySegmentID 返回当前绑定的恢复分段 ID。
func (w *WAL) RecoverySegmentID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recoverySegID
}

// TryReclaim 经账本 CanReclaim 门禁尝试推进 reclaim_position，并回收 WAL 中已被
// reclaim 前缀覆盖的事件（否则内存会随采集总量线性增长——真机 64 源实测曾致 Worker RSS 2GiB）。
func (w *WAL) TryReclaim() (uint64, error) {
	pos, err := w.led.TryReclaim(w.key)
	if err != nil {
		// 账本拒绝**推进**（典型：回收位置恰好触到恢复分段边界，CoversTo > Reclaim 不成立）时，
		// 仍按**账本当前**的回收位置剪枝：该前缀已被 CanReclaim 判过安全，保留它们只会让积压
		// 永不回落 → 滞回（≤上限一半）永不满足 → 源永久停在 paused。
		// 2026-09-30 生产实证：这里一错即返，prune 便永不执行，7998 条卡死数小时、
		// 积压数字一个字节不动。剪枝上界仍是账本自己的水位，不越过任何未裁定安全的位置。
		if cur := w.led.Get(w.key); cur != nil {
			w.pruneReclaimed(cur.Positions.Reclaim)
		}
		return pos, err
	}
	w.pruneReclaimed(pos)
	return pos, nil
}

// pruneReclaimed 丢弃 Event.Record.End <= pos 的 WAL 条目：该前缀的恢复责任已由受管恢复分段/
// 投影承担（CanReclaim 门禁已放行），保留它们只占用内存。未耐久或超出 reclaim 前缀的事件一律保留。
//
// pos == 0 时没有可剪的条目，但**仍要评估一次恢复**（缺陷 A 自愈链的一环）：暂停源在缺口被
// 消解后唯一会经过的恢复检查点就是这里，若因「没有可剪条目」直接返回，源就再没有机会
// 走出暂停——现场表现正是「积压已回落但暂不能恢复采集」之后永远停住。
// 该分支只多一次 O(条目数) 的积压统计，而它本就被本方法的调用路径（每轮采集一次）覆盖。
func (w *WAL) pruneReclaimed(pos uint64) {
	if pos == 0 {
		// 该分支不剪条目，但仍要按同一临界区访问、并与 maybeResumeBacklogLocked 的
		// 「调用方需持 w.mu」契约保持一致：否则它与 Append/backlogLocked 并发时
		// 会读到撕裂的积压状态（且 Go 竞态检测会直接报数据竞争）。
		w.mu.Lock()
		defer w.mu.Unlock()
		w.maybeResumeBacklogLocked(w.led.Get(w.key))
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	kept := w.entries[:0]
	for _, entry := range w.entries {
		if entry.Durable && entry.Event.Record.End <= pos {
			continue
		}
		kept = append(kept, entry)
	}
	// 用新底层数组避免长期持有已被丢弃事件的引用。
	w.entries = append([]WALEntry(nil), kept...)
	// 回收后按低水位恢复采集（B1b）：积压回落即自动解暂停。
	w.maybeResumeBacklogLocked(w.led.Get(w.key))
}

// Snapshot 返回 WAL 内事件（测试/恢复用）。
func (w *WAL) Snapshot() []WALEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]WALEntry, len(w.entries))
	copy(out, w.entries)
	return out
}

// Restore 将持久 WAL 快照恢复到内存。账本水位由 Ledger.Restore 恢复，
// WAL 只重建待核验事件集合，避免重启后依赖进程内存判断投递结果。
func (w *WAL) Restore(entries []WALEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, entry := range entries {
		if entry.Event.Source.LogSourceID != "" && entry.Event.Source.LogSourceID != w.key.LogSourceID {
			return fmt.Errorf("acquire: wal restore source mismatch %q", entry.Event.Source.LogSourceID)
		}
		if entry.Event.Source.SourceGeneration != "" && entry.Event.Source.SourceGeneration != w.key.SourceGeneration {
			return fmt.Errorf("acquire: wal restore generation mismatch %q", entry.Event.Source.SourceGeneration)
		}
	}
	w.entries = append([]WALEntry(nil), entries...)
	for _, entry := range entries {
		if entry.Durable && entry.Event.Record.End > w.lastDurableEnd {
			w.lastDurableEnd = entry.Event.Record.End
		}
	}
	// 恢复完立刻判一次闸：索引里的**持久积压**可能已经越界，此时必须当场暂停，而不是等下一次
	// Append（现场形态：内存积压早已越界、暂停却滞后到下一次追加才发生）。
	w.enforceBacklogLimitLocked(w.led.Get(w.key))
	return nil
}

func lastRecordEnd(events []logtypes.Event, fallback uint64) uint64 {
	maxEnd := fallback
	for _, ev := range events {
		if ev.Record.End > maxEnd {
			maxEnd = ev.Record.End
		}
	}
	return maxEnd
}
