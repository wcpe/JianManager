// Package acquire 实现采集通道：FileTailer / ArchiveImporter / WAL / 容量门禁。
// 与 normalize 的边界通过 EventBoundaryHook 导出，本包不实现解析逻辑。
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
// （events的权威副本）为准（B1a）。
//
// 为什么需要：WALEntry 内联 logtypes.Event（含正文），而整份 WAL 会随 ingest.state.json
// 持久化——单源积压曾因此把状态文件撑到 1.2 GB（2026-09-28 生产事故）。
//
// 关键约束（第一次实现曾在此出错并被既有测试抓到）：段存储里**未必**有 WAL 的全部事件——
// 事件体是在投递路径（writeProjection → appendEvents）才写入段存储，故尚未投递的条目
// 只能内联保存。判据必须**按条**给出（见 SnapshotSplit 的 covered 谓词），不得按源一刀切。
type WALRef struct {
	Seq      uint64 `json:"seq"`
	Appended bool   `json:"appended"`
	Durable  bool   `json:"durable"`
	// Replay 与 WALEntry.Replay 同义：该条目来自**恢复期回放/补账**。
	//
	// 为什么必须持久化（ACQ-1）：`Replay` 只落在内存条目上时，重启后回放条目会退回
	// 「常规积压」并重新把源推过闸（`backlogLocked` 的闸视图 = 总量 − 回放）——
	// 而这恰好发生在最需要它的时刻（恢复期回放与进程重启是同一场景）。
	// 旧数据无此字段 ⇒ 零值 false = 非回放，向后兼容 ✓。
	Replay      bool   `json:"replay,omitempty"`
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
				Seq: e.Seq, Appended: e.Appended, Durable: e.Durable, Replay: e.Replay,
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
// 返回，由调用方记可见缺口并告警（与§5#1「截断可见」同口径）。
//
// 回放标记随 refs 双向往返（ACQ-1）：`WALRef.Replay` 落到重建条目的 `WALEntry.Replay` 上，
// 否则重启后回放条目会重新计入闸视图（见 WALRef.Replay 的说明 ✓）。
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
		entries = append(entries, WALEntry{Seq: r.Seq, Event: ev, Appended: r.Appended, Durable: r.Durable, Replay: r.Replay})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
	w.entries = entries
	w.recountLocked() // #66-②：整体替换 ⇒ 重算一次（O(条数)，一次性 ✓）
	for _, entry := range entries {
		if entry.Durable && entry.Event.Record.End > w.lastDurableEnd {
			w.lastDurableEnd = entry.Event.Record.End
		}
	}
	// 与 Restore 对称（ACQ-3）：恢复完立刻判一次闸。`saved.WALRefs` 非空时生产走的就是本路径
	//（见 ingest 的 refs 来源），索引里的持久积压可能已经越界 ⇒ 必须当场暂停，而不是等下一次
	// Append（现场形态：内存积压早已越界、暂停却滞后到下一次追加才发生）。
	w.enforceBacklogLimitLocked()
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
	defaultWALMaxEntries = 2000
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
	// Replay 标记该条目来自**恢复期回放/补账**（gz 归档侧补账写回原代次 backlog ✓）。
	//
	// 为什么要标记（2026-10-04 现场，用户定调方案① ✓）：恢复期回放是**设计行为**，它必然把源
	// 推过常规闸 ⇒ 用**抬高阈值**去容纳它（方案②，我此前的实现 ✗）会让闸的绝对上界一起抬高 ✗，
	// 现场表现为 WAL +15,964 / +463k 的"复活→re-append→再越闸"震荡 ✗✗。
	// 方案①：**阈值不动**，只把"回放可归因的字节"从**闸的计量**里剔除 ✓ —— 新数据（真正的洪流 ✗）
	// 仍被同一把尺挡住 ✓，回放不再自锁 ✓。标记落在条目上（而不是计数器）：属精确归因，
	// 且随剪枝自动守恒 ✓。
	Replay bool `json:"replay,omitempty"`
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
	// pruneSinceSweep 是距离上一次"完整清扫"的 prune 次数（#66-①；持 w.mu 访问 ✓）。
	pruneSinceSweep int
	// #66-②：积压计数的**增量维护**（持 w.mu ✓）—— `backlogLocked` 每次 `Append`（enforce 判闸）与
	// 每次 `Backlog()/BacklogTotal()` 读都会用它们，而旧实现是**两次 O(条数) 全表扫描** ✗✗ ⇒
	// 对 10 万+ 条的 WAL 就是采集路径上的固定税（#65 归因第 4 项 ✓）。
	totalBytes    int64 // Σ(len(Message) + walEntryOverheadBytes)：全部条目
	replayEntries int64 // 其中"回放在飞"条数
	replayBytes   int64 // 其中"回放在飞"近似字节
}

// pruneFullSweepEvery 是"完整清扫"的摊销节拍（#66-①）：头偏移窗每次只删**可回收前缀**
// （摊销 O(1)/条 ✓），但**乱序条目**（归档补账/回放会追加更早的 record 范围 ⇒ 可回收条目可能
// 落在头部之后）只有完整扫描才收得到 ⇒ 每 N 次 prune 做一次全扫，把"晚回收"界定在 N 次内
// （纯内存回收节奏，语义无影响 ✓；N 取 64：对 250ms/源的采集轮即 ≤16s）。
const pruneFullSweepEvery = 64

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
// backlogLocked 返回**闸视图**（用于越闸判定与滞回评估 ✓）：条目数与字节数**排除回放归因**
// （`entry.Replay` ✓，见 WALEntry.Replay 的注释：方案① 阈值不动、只剔计量 ✓）。
func (w *WAL) backlogLocked() (int64, int64) {
	entries, bytes := w.backlogTotalLocked()
	replayEntries, replayBytes := w.replayInFlightLocked()
	entries -= replayEntries
	if entries < 0 {
		entries = 0
	}
	bytes -= replayBytes
	if bytes < 0 {
		bytes = 0
	}
	return entries, bytes
}

// walEntryBytes 是单条条目的近似字节口径（与旧全表扫描逐字一致 ✓）。
func walEntryBytes(entry WALEntry) int64 {
	return int64(len(entry.Event.Message)) + walEntryOverheadBytes
}

// noteEntryAddedLocked / noteEntryDroppedLocked 维护 #66-② 的增量计数（调用方必须持 w.mu ✓）。
func (w *WAL) noteEntryAddedLocked(entry WALEntry) {
	b := walEntryBytes(entry)
	w.totalBytes += b
	if entry.Replay {
		w.replayEntries++
		w.replayBytes += b
	}
}

func (w *WAL) noteEntryDroppedLocked(entry WALEntry) {
	b := walEntryBytes(entry)
	w.totalBytes -= b
	if entry.Replay {
		w.replayEntries--
		w.replayBytes -= b
	}
}

// recountLocked 从当前 entries 重算三个计数（整体替换/恢复后用一次 O(条数) 是合算的 ✓）。
func (w *WAL) recountLocked() {
	w.totalBytes, w.replayEntries, w.replayBytes = 0, 0, 0
	for _, entry := range w.entries {
		w.noteEntryAddedLocked(entry)
	}
}

// backlogTotalLocked 返回**总量视图**（含回放 ✓）：观测与排障用（诊断面/日志 ✓）。
//
// #66-②：O(1)（旧实现是全表求和 ✗；该读数在每次 Append 的判闸与每次诊断读里都用 ✓）。
func (w *WAL) backlogTotalLocked() (int64, int64) {
	return int64(len(w.entries)), w.totalBytes
}

// replayInFlightLocked 返回"回放在飞"（条目数 + 近似字节）——单列观测 ✓（方案① 要求可观测 ✓）。
// #66-②：O(1)（增量维护 ✓）。
func (w *WAL) replayInFlightLocked() (int64, int64) {
	return w.replayEntries, w.replayBytes
}

// ReplayInFlight 暴露"回放在飞"读数（条目数 + 近似字节 ✓）：诊断/观测用 ✓。
func (w *WAL) ReplayInFlight() (entries int64, bytes int64) {
	if w == nil {
		return 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.replayInFlightLocked()
}

// BacklogTotal 暴露**总量视图**（含回放 ✓）与闸视图的差额，供诊断面区分"真积压 vs 回放在飞" ✓。
func (w *WAL) BacklogTotal() (entries int64, bytes int64) {
	if w == nil {
		return 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.backlogTotalLocked()
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
func (w *WAL) enforceBacklogLimitLocked() {
	// #66-③：判据只读 AcquirePaused ⇒ O(1) 轻量读（旧实现的 Get 在每次追加后再克隆一次 ✗）。
	st, ok := w.led.AcquireState(w.key)
	if !ok || st.Paused {
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
func (w *WAL) maybeResumeBacklogLocked() {
	// #66-③：热路径判据只读 Paused/PauseReason 两个标量 ⇒ O(1) 轻量读；**需要 gaps 的罕见
	// 自愈分支**保持惰性 Get（语义不变 ✓）。
	st, ok := w.led.AcquireState(w.key)
	if !ok || !st.Paused || !strings.HasPrefix(st.PauseReason, walBacklogPauseReason) {
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
		// 拒了）" ⇒ 它只能靠**回读**愈合 ⇐ 回读在暂停期间被 tailer 拒绝 ✗ ⇒
		// 「暂停 ⇒ 缺口不清 ⇒ 不许恢复 ⇒ 不回读」自锁 ✓✓。
		// 这里按**分类裁定**消解这一类（不是操作员放弃 ✗）：恢复后回读会重新产生并投递该批 ✓
		// （不丢数据；重复按 event_id 幂等 ✓）。其余原因保持拦截 ✓。
		// #66-③：仅此罕见分支惰性取整条（自愈需要 gaps ✗）——热路径不受影响 ✓。
		if ent := w.led.Get(w.key); ent != nil {
			if healed := w.resolveReReadHealableGapsLocked(ent); healed > 0 {
				if err2 := w.led.ResumeAcquire(w.key); err2 == nil {
					slog.Info("积压已回落：已按「恢复后回读重放」分类消解暂停期缺口并恢复采集",
						"logSourceID", w.key.LogSourceID, "gaps", healed, "entries", entries, "approxBytes", bytes)
					return
				}
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
// #179-①：两条"待重读"消解词的**真源导出**（既有小写名保留为别名 ⇒ 既有调用点逐字不变 ✓）。
// 为什么要导出：ingest 侧的"待重读类缝"判据与测试夹具都必须引用同一真源，绝不抄字面量 ✗。
const ResolutionReReadOnResume = "re-read on resume (append rejected while paused)"
const ResolutionReReadOnResumeDelivery = "re-read on resume (delivery error while paused)"

// ResolutionReReadOnResumeRotation 是**轮转时序类**缺口的分类裁定文本（与上两条**分账** ✓）。
//
// 为什么单独一个词（2026-10-10 现场）：`ROTATED_SEGMENT_NOT_READY` 的语义是"已看到 latest.log
// 被替换、但轮转段尚未可读"的**时序观察**（登记区间恒为 [0,0]，不覆盖任何字节 ✓），
// 用"append rejected while paused"或"delivery error while paused"记录它都是**事实错误**——
// 审计面会读到一个从未发生的失败类型。分账使"为什么被放行"在账本里逐字可读 ✓。
const ResolutionReReadOnResumeRotation = "re-read on resume (rotated segment not ready)"

// 兼容别名：既有调用点（含消解词比较处）逐字不变 ✓。
const (
	gapResolutionReReadOnResume         = ResolutionReReadOnResume
	gapResolutionReReadOnResumeDelivery = ResolutionReReadOnResumeDelivery
)

// gapResolutionReReadFallback 是白名单内「未在下面分账表中显式登记」的原因所用的兜底消解词。
//
// 为什么需要兜底而不是报错：分账表只影响**审计措辞**，而"是否可愈合"由 ledger 白名单单独回答
// （见 reReadResolutionForReason ✓）。若这里回退成"拒绝消解"，新增一个白名单原因就会表现为
// 「源永久停在暂停上且无任何报错」——正是本包要根除的无声失效 ✗。兜底保证：只要进了白名单
// 就一定能被消解 ✓，措辞不精确由下面的钉住测试盯住 ✓。
const gapResolutionReReadFallback = "re-read on resume (timing gap healable on re-read)"

// reReadResolutionForReason 把白名单内的原因分配到消解词（分账 ⇒ 审计可读 ✓）。
//
// **归属判定不在这里**：某原因是否可愈合只由 `ledger.IsGapReasonHealableOnReResume` 回答
// （单一真源 ✓）。本函数只回答"用哪个词记录"，因此对白名单外的一切输入都无意义
// （调用点已先过白名单判定 ✓）。
func reReadResolutionForReason(reason string) string {
	switch reason {
	case ledger.GapReasonAppendRejected:
		return gapResolutionReReadOnResume
	case ledger.GapReasonDeliverError, ledger.GapReasonDeliverErrorWorkerSource:
		return gapResolutionReReadOnResumeDelivery
	case ledger.GapReasonRotatedSegmentNotReady:
		return ResolutionReReadOnResumeRotation
	default:
		return gapResolutionReReadFallback
	}
}

// reReadResolutions 是「待重读」类消解词的**完整清单**（判定 `IsReReadHealableResolution` 的真源）。
//
// 为什么要集中一份：该谓词驱动破环直投步（#179）——只有被它认作"非终局"的已决缝才会被重新投递。
// 某个词若写了却不在清单里，缝会被消解而**永不再投**，表现同样是无声失效 ✗。
// 兜底词必须在此（见 gapResolutionReReadFallback 的说明 ✓）。
var reReadResolutions = []string{
	ResolutionReReadOnResume,
	ResolutionReReadOnResumeDelivery,
	ResolutionReReadOnResumeRotation,
	gapResolutionReReadFallback,
}

// IsReReadHealableResolution 判定某条缺口的消解词是否属"**待重读**"类（#179）：已 resolved 但**非终局**
// ⇒ 仍必须靠一次真实重投/重读才能闭合 ✓（179 正是这种"已消解却钉死"的形态 ✗）。行为中性：只读 ✓。
func IsReReadHealableResolution(res string) bool {
	for _, candidate := range reReadResolutions {
		if res == candidate {
			return true
		}
	}
	return false
}

// resolveReReadHealableGapsLocked 把「恢复后回读可愈合」白名单内的未消解缺口分类为待重读。
//
// **名单只有一份**：可愈合与否一律问 `ledger.IsGapReasonHealableOnReResume`（ledger 单一真源 ✓），
// 本函数不再持有任何原因清单——曾经这里有两段按原因字面量分组的循环，它们的名单与 ingest 侧
// 的诊断名单必须手工保持一致，漏改一处就表现为「该原因永远不被自动消解」的无声失效 ✗
// （2026-10-10 生产实测：86 源被不在名单里的 `ROTATED_SEGMENT_NOT_READY` 永久挡住 ✓）。
//
// 水位（through）只限制候选扫描，不作为"已落库"证据；零长度缺口（EndPos=0）同样有效
// ——消解判据是「末端 ≤ position」（见 ledger.ResolveGapsThroughAllowed ✓），
// 故 `through=0` 能消解 `[0,0]` 形态的轮转时序缺口 ✓（这正是现场 86 源的形态 ✓）。
// 分账仅体现在消解词上（见 reReadResolutionForReason ✓），不改变消解集合。
func (w *WAL) resolveReReadHealableGapsLocked(ent *ledger.Entry) int {
	if ent == nil {
		return 0
	}
	// 一轮扫描把白名单内的未消解缺口按**消解词**分组：组 → 扫描上界 + 该组出现过的原因。
	//
	// `found` 必须与 `through` 分开存：零长度缺口（`EndPos=0`，轮转时序记录正是此形态）永远
	// 不会推高上界，若用「key 是否存在」代表「有没有候选」，它就会被整组丢掉 ⇒ 消解集合漏项 ✗
	// （实测：`through=0` 本身能消解 `[0,0]`，漏的是"没把这一组送进消解调用"）。
	type healGroup struct {
		through uint64
		reasons []string
	}
	groups := map[string]*healGroup{}
	seenReason := map[string]bool{}
	for _, g := range ent.Gaps {
		if g.Resolved || !ledger.IsGapReasonHealableOnReResume(g.Reason) {
			continue
		}
		resolution := reReadResolutionForReason(g.Reason)
		group := groups[resolution]
		if group == nil {
			group = &healGroup{}
			groups[resolution] = group
		}
		if g.EndPos > group.through {
			group.through = g.EndPos
		}
		if !seenReason[g.Reason] {
			seenReason[g.Reason] = true
			group.reasons = append(group.reasons, g.Reason)
		}
	}
	total := 0
	for resolution, group := range groups {
		// 允许集只含本组原因：否则一个较大的 through 会把**别组**的缺口也一并消解，
		// 并把错误的消解词写进它的审计字段 ✗（分组的意义就在于此 ✓）。
		n, err := w.led.ResolveGapsThroughAllowed(w.key, group.through, resolution, group.reasons...)
		if err == nil {
			total += n
		}
	}
	return total
}

// BacklogAndLimits 返回当前积压（条目）与生效上限（含恢复期拓宽 ✓），供诊断面使用。
func (w *WAL) BacklogAndLimits() (entries int64, maxEntries int64) {
	if w == nil {
		return 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	e, _ := w.backlogLocked()
	mx, _ := w.limitsLocked()
	return e, mx
}

// EffectiveLimits 返回**生效**的单源积压上限（条目 + 近似字节，含恢复期拓宽与默认值归一）。
//
// 为什么需要导出（缺陷二修复，2026-10-10）：导入侧必须按「上限的分数」把一次归档导入拆成
// 有界批次，而它当时拿不到生效上限——只能硬编码一个常数，那会在上限被配置抬高/降低时
// 与真实闸门脱节（本缺陷的现场正是「代码默认 2000 条、配置生效 20000 条」这种脱节）。
// 让导入侧读**同一把尺**，批次大小就永远与闸门同步，不需要在两处各维护一个数字。
//
// 只读、不改任何状态；调用方不得据此自行"放宽/收紧"闸门（闸门仍只由本包 enforceBacklogLimitLocked
// 判定），本方法只用于**提前规划批次**，使单次追加不会把源直接推过闸。
func (w *WAL) EffectiveLimits() (maxEntries, maxBytes int64) {
	if w == nil {
		return 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.limitsLocked()
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
	// #66-③：判据只读 AcquirePaused ⇒ 轻量读（旧实现的 Get 会克隆整条 ✗）。
	st, ok := w.led.AcquireState(w.key)
	return ok && !st.Paused
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
// AppendReplay 与 Append 同语义，但把条目标记为**恢复期回放/补账**（见 WALEntry.Replay ✓）：
// 这些字节不再计入**闸视图**（方案① 阈值不动、只剔计量 ✓），但在总量视图与"回放在飞"里可观测 ✓。
//
// 调用方：恢复期回放/归档补账的写入路径（把原代次的存量写回 WAL 待投递 ✓）。常规采集**不得**
// 走本入口 ✗（否则等于给洪流开了后门 ✗ —— 红线：新数据必须仍被同一把尺挡住 ✓）。
//
// **入口名即来源**（ACQ-2）：语义经入参 `origin` 传递，而不是实例字段。旧实现用
// `appendOriginReplay` 字段在 `Append` **之外**置位/复位，两次取锁之间没有与 `Append` 的
// 整个临界区互斥 ⇒ 任何并发的常规 `Append` 只要落进这个窗口，就会被标成回放（被闸视图剔除
// ⇒ 红线失效）并走进放去重（同哈希即丢弃 ⇒ 常规采集行被静默吞掉）。
func (w *WAL) Append(events ...logtypes.Event) error {
	return w.appendLocked(originAcquire, events...)
}

// AppendReplay 见 Append 的说明（红线：常规采集不得走本入口 ✓）。
func (w *WAL) AppendReplay(events ...logtypes.Event) error {
	return w.appendLocked(originReplay, events...)
}

// walOrigin 标识一次追加的来源（**入参传递**，不在实例上留状态 ✓）。
type walOrigin int

const (
	// originAcquire 常规采集（默认）：新数据必须仍被同一把尺挡住 ✓。
	originAcquire walOrigin = iota
	// originReplay 恢复期回放/归档补账：计入"回放在飞"，不计入闸视图 ✓。
	originReplay
)

// appendLocked 是 Append / AppendReplay 的共同实现：整个追加在**同一个临界区**内完成，
// 来源由入参给出（无跨调用的可变状态 ⇒ 并发互不污染 ✓）。
func (w *WAL) appendLocked(origin walOrigin, events ...logtypes.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	replay := origin == originReplay

	// #66-③：高频路径 —— 旧实现这里 Get（深克隆）一次、判闸处再 Get 一次（每次追加两发 ✗）。
	// 本函数只读 Paused/PauseReason/Positions.Read 三个标量 ⇒ 全部可由 O(1) 轻量读覆盖 ✓。
	st, ok := w.led.AcquireState(w.key)
	if !ok {
		return fmt.Errorf("acquire: wal source %s missing", w.key)
	}
	if st.Paused {
		return fmt.Errorf("acquire: paused (%s); must record gap, not silent drop", st.PauseReason)
	}

	// #179 上游根修：回放/补账入口先与既有条目对账，杜绝"同一 id 写两遍"✗（详见函数注释 ✓）。
	// 判据取**本次入参** origin（ACQ-2）：常规采集轮不得走进放去重 ✓。
	if replay && len(events) > 0 {
		events = w.adjudicateReplayDuplicatesLocked(events)
		if len(events) == 0 {
			return nil
		}
	}

	for _, ev := range events {
		newEntry := WALEntry{
			Replay:   replay,
			Seq:      uint64(len(w.entries) + 1),
			Event:    ev,
			Appended: true,
		}
		w.entries = append(w.entries, newEntry)
		w.noteEntryAddedLocked(newEntry) // #66-②：积压计数增量维护
	}
	// read_position 可先推进；durable 等 Commit。
	end := lastRecordEnd(events, st.Positions.Read)
	if err := w.led.AdvanceRead(w.key, end); err != nil {
		return err
	}
	// 追加后检查积压上限（B1b）：越界即暂停该源采集，避免单源洪流无界增长拖垮整机。
	w.enforceBacklogLimitLocked()
	return nil
}

// adjudicateReplayDuplicatesLocked 是 #179 的**上游根修**（同一 id 写两遍 ✗）。
//
// 现场：同一段字节被两次读取（先实时、后回放），日期推断差异 ⇒ 同 event_id、同区间、异哈希；
// 旧行为把第二遍**再追加一次** ✗ ⇒ WAL 静止态长期躺着两个变体，下游一路硬失败。
//
// 本函数只在**回放/补账**入口对账（常规采集不受影响 ✓）：
//   - 同哈希 ⇒ 丢弃本批该条（真正冗余 ⇒ 不写第二遍 ✓）；
//   - 同区间且「level/stream/message 全同（仅时间不同）」⇒ 用本批**原位替换旧条目**
//     （静止态不再留异哈希 ✗）并**照常追加一份**——追加的那份走投递（更正版本需要投出去 ✓），
//     替换让 canonical/重建面只见同一哈希（同哈希 ⇒ 天然收敛 ✓）；
//   - 其余形态（含"较长/较短"的长度差）⇒ 原样追加（**本层不按长度裁定** ✗ —— 长度差不能
//     证明是同一物理行；由下游既有裁定与可见硬失败处理，判据不放宽 ✓）。
func (w *WAL) adjudicateReplayDuplicatesLocked(events []logtypes.Event) []logtypes.Event {
	if len(w.entries) == 0 {
		return events
	}
	out := make([]logtypes.Event, 0, len(events))
	for _, ev := range events {
		idx := -1
		for i := range w.entries {
			if w.entries[i].Event.EventID == ev.EventID {
				idx = i
				break
			}
		}
		if idx < 0 {
			out = append(out, ev)
			continue
		}
		existing := w.entries[idx].Event
		if existing.CanonicalHash == ev.CanonicalHash {
			continue // 同哈希 ⇒ 已在 WAL：丢弃副本，不再写第二遍 ✓
		}
		keyLabel := w.key.LogSourceID + "/" + w.key.SourceGeneration
		sameSpan := existing.Record.Start == ev.Record.Start && existing.Record.End == ev.Record.End
		sameLine := existing.Level == ev.Level && existing.Stream == ev.Stream && existing.Message == ev.Message
		if sameSpan && sameLine {
			w.entries[idx].Event = ev // 胜者原位替换：静止态收敛为同一内容 ✓
			w.entries[idx].Replay = true
			slog.Warn("回放去重：同区间仅时间不同 ⇒ 替换既有条目并追加更正版待投递",
				"key", keyLabel, "event_id", ev.EventID, "start", ev.Record.Start, "end", ev.Record.End)
			out = append(out, ev)
			continue
		}
		out = append(out, ev) // 其余形态原样追加（下游既有裁定/可见硬失败处理 ✓）
	}
	return out
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
		// #66-③：这里只需要 Positions.Reclaim ⇒ 用 #61 的 O(1) 轻量读（旧实现 Get 深克隆 ✗）。
		if p, ok := w.led.Positions(w.key); ok {
			w.pruneReclaimed(p.Reclaim)
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
		w.maybeResumeBacklogLocked()
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// #66-①：**增量** prune —— 只删"从头部起的连续可回收前缀"（条目按 seq 追加 ⇒ 正常情形下
	// "可回收"就是一段前缀 ✓）⇒ 用**头偏移窗**，并把已删前缀**置零**（窗口仍指向同一底层数组，
	// 不置零就等于继续持有已丢弃事件的正文 ✗ —— 原实现正是为此改用"新数组" ✗，但那会
	// `append([]WALEntry(nil), kept...)` **复制剩余全部** ⇒ 单次 prune 与"剩余条数"成正比，
	// 现场单跳 ~1s 的主项之一（#65 归因）✗✗。
	// 复杂度：单次 prune = O(被删条数)（每条只被置零一次 ⇒ 全生命周期摊销 O(1)/条 ✓）。
	cut := 0
	for cut < len(w.entries) {
		e := w.entries[cut]
		if !(e.Durable && e.Event.Record.End <= pos) {
			break
		}
		cut++
	}
	if cut > 0 {
		for i := 0; i < cut; i++ {
			w.noteEntryDroppedLocked(w.entries[i]) // #66-②：先把计数减掉（置零前 ✓）
			w.entries[i] = WALEntry{}              // 清引用：窗口后面的底层数组不得再持有已丢弃事件的正文
		}
		w.entries = w.entries[cut:]
	}
	// 乱序兜底（#66-①）：每 pruneFullSweepEvery 次做一次**完整清扫**（原地压缩 + 清尾部引用 ⇒
	// 既不用复制剩余全部 ✓，也不持有已丢弃事件 ✓），把乱序条目的"晚回收"界定在 N 次内 ✓。
	w.pruneSinceSweep++
	if w.pruneSinceSweep >= pruneFullSweepEvery {
		w.pruneSinceSweep = 0
		kept := w.entries[:0]
		for _, entry := range w.entries {
			if entry.Durable && entry.Event.Record.End <= pos {
				w.noteEntryDroppedLocked(entry) // #66-②：完整清扫的删除同样要记账
				continue
			}
			kept = append(kept, entry)
		}
		for i := len(kept); i < len(w.entries); i++ {
			w.entries[i] = WALEntry{} // 原地压缩会复用底层数组 ⇒ 尾部槽位必须清引用
		}
		w.entries = w.entries[:len(kept)]
	}
	// 回收后按低水位恢复采集（B1b）：积压回落即自动解暂停。
	w.maybeResumeBacklogLocked()
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
	w.recountLocked() // #66-②：整体替换 ⇒ 重算一次（O(条数)，一次性 ✓）
	for _, entry := range entries {
		if entry.Durable && entry.Event.Record.End > w.lastDurableEnd {
			w.lastDurableEnd = entry.Event.Record.End
		}
	}
	// 恢复完立刻判一次闸：索引里的**持久积压**可能已经越界，此时必须当场暂停，而不是等下一次
	// Append（现场形态：内存积压早已越界、暂停却滞后到下一次追加才发生）。
	w.enforceBacklogLimitLocked()
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
