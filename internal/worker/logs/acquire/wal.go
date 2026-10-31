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

// WAL 条目引用/落盘形态（WALRef、SnapshotSplit、Restore*、WALCommitSnapshot/Index、
// SnapshotSplitCommitted*、restoreAcknowledged）已于 2026-10-29 删除：
// WAL 改为**纯内存队列**后不再落盘，也没有恢复路径。可靠性改由「持久化的 position 水位 +
// 重启重读」承担（见 ingest/wal_reboot_replay.go 的 rollbackReadToReclaim）。
//
// 队列自身的生命周期仍全部在本文件：Append / Commit / TrimBacklog / SnapshotPending /
// CaptureDelivery* / TryReclaim* 等（这些不依赖任何落盘面）。

// SnapshotWindow只捕获原投递记录窗，硬限条数；未提交、跨边界或新增变体均不得借旧hint确认。
func (w *WAL) SnapshotWindow(from, to uint64, limit int, filters ...func(logtypes.Event) bool) ([]WALEntry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []WALEntry
	for _, entry := range w.entries {
		if entry.Event.Record.End <= from || entry.Event.Record.Start >= to {
			continue
		}
		// 谓词只见私有副本：调用方留存或改写map都不得反写活WAL正文。
		if len(filters) > 0 && !filters[0](cloneWALEvent(entry.Event)) {
			continue
		}
		if !entry.Durable || entry.Event.Record.Start < from || entry.Event.Record.End > to || len(out) >= limit {
			return nil, fmt.Errorf("acquire: 原written记录窗已变化或未提交")
		}
		entry.reservation = nil
		entry.Event = cloneWALEvent(entry.Event)
		out = append(out, entry)
	}
	return out, nil
}

type walWindow struct {
	mu           sync.RWMutex
	id, from, to uint64
	mutation     bool
}

// AppendReservation在采样前原子预留原记录窗，阻止检查后插入发布lease；仅是短期准入，不授耐久或ACK资格。
type AppendReservation struct {
	owner  *WAL
	window *walWindow
}

// ReserveAppend不修改条目或水位；持有期间只有显式携带该凭据的Append能修改原窗。
func (w *WAL) ReserveAppend(events []logtypes.Event) (*AppendReservation, error) {
	window := &walWindow{mutation: true}
	for i, event := range events {
		if i == 0 || event.Record.Start < window.from {
			window.from = event.Record.Start
		}
		if event.Record.End > window.to {
			window.to = event.Record.End
		}
	}
	// 汇总可能承接离散输入，准入覆盖其整个标量窗，不能漏掉输入间隙中的既有发布lease。
	span := logtypes.Event{Record: logtypes.RecordRange{Start: window.from, End: window.to}}
	unlock := w.lockMutation([]logtypes.Event{span}, false)
	if unlock == nil {
		return nil, ErrWALWindowBusy
	}
	defer unlock()
	if w.windows == nil {
		w.windows = make(map[*walWindow]struct{})
	}
	w.nextWindow++
	window.id = w.nextWindow
	window.mu.Lock()
	w.windows[window] = struct{}{}
	w.windowVersion++
	return &AppendReservation{owner: w, window: window}, nil
}

// Release结束本次采样到追加的原子准入，重复调用无副作用；不得跨网络或持久化IO持有。
func (reservation *AppendReservation) Release() {
	if reservation == nil || reservation.owner == nil {
		return
	}
	w := reservation.owner
	w.mu.Lock()
	if _, ok := w.windows[reservation.window]; ok {
		delete(w.windows, reservation.window)
		w.windowVersion++
		reservation.window.mu.Unlock()
	}
	w.mu.Unlock()
}

// ErrWALWindowChanged表示当前正文已变化，旧接收证明不能推进发布或责任。
var ErrWALWindowChanged = fmt.Errorf("acquire: WAL原窗完整身份已变化")

// ErrWALWindowBusy为可重试发布窗背压，不是容量暂停；调用者保留原物和精确责任后续读。
var ErrWALWindowBusy = fmt.Errorf("acquire: WAL发布窗暂忙，请保留原物并重试")

// lockMutation在等待同窗发布IO前先释放WAL锁；窗口注册版本复验消除新租约与追加的准入竞态。
func (w *WAL) lockMutation(events []logtypes.Event, all bool, reservations ...*AppendReservation) func() {
	for {
		w.mu.Lock()
		var reserved *walWindow
		if len(reservations) > 0 && reservations[0] != nil {
			reservation := reservations[0]
			_, valid := w.windows[reservation.window]
			if reservation.owner != w || !valid {
				w.mu.Unlock()
				return nil
			}
			reserved = reservation.window
			for _, event := range events {
				if event.Record.Start < reserved.from || event.Record.End > reserved.to {
					w.mu.Unlock()
					return nil
				}
			}
		}
		version := w.windowVersion
		var windows []*walWindow
		for window := range w.windows {
			if window == reserved {
				continue
			}
			intersects := all
			for _, event := range events {
				if event.Record.Start < window.to && event.Record.End > window.from {
					intersects = true
					break
				}
			}
			if intersects {
				windows = append(windows, window)
			}
		}
		w.mu.Unlock()
		sort.Slice(windows, func(i, j int) bool { return windows[i].id < windows[j].id })
		for i, window := range windows {
			if all {
				window.mu.Lock()
				continue
			}
			if !window.mu.TryLock() {
				for j := i - 1; j >= 0; j-- {
					windows[j].mu.Unlock()
				}
				return nil
			}
		}
		w.mu.Lock()
		if w.windowVersion == version {
			return func() {
				w.mu.Unlock()
				for i := len(windows) - 1; i >= 0; i-- {
					windows[i].mu.Unlock()
				}
			}
		}
		w.mu.Unlock()
		for i := len(windows) - 1; i >= 0; i-- {
			windows[i].mu.Unlock()
		}
	}
}

// EnableQualifiedReclaim启用新组合的完整正文ACK闸；只内存状态，默认同步组合不变。
func (w *WAL) EnableQualifiedReclaim() { w.mu.Lock(); w.qualifiedReclaim = true; w.mu.Unlock() }

// RecordCurrentResults在同一WAL临界区复核当前完整正文并登记精确响应，避免核对后再记ACK的竞态。
func (w *WAL) RecordCurrentResults(events []logtypes.Event, results []DeliveryResult, cuts ...*walCapture) error {
	expected := make(map[CommitIdentity]struct{}, len(events))
	wanted := make(map[string]struct{}, len(events))
	for _, event := range events {
		id, err := CommitIdentityOf(event)
		if err != nil {
			return err
		}
		expected[id] = struct{}{}
		wanted[id.EventID] = struct{}{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	matched := make([]int, 0, len(events))
	for i, entry := range w.entries {
		_, sameID := wanted[entry.Event.EventID]
		overlaps := false
		for _, event := range events {
			if event.Record.Start < entry.Event.Record.End && event.Record.End > entry.Event.Record.Start {
				overlaps = true
				break
			}
		}
		if !sameID && !overlaps {
			continue
		}
		// 每条真实响应都要把全表过一遍（同 ID / 区间重叠双重筛），用缓存避免重算。
		id, err := w.entries[i].eventIdentityLocked()
		if err != nil {
			return err
		}
		if _, ok := expected[id]; !ok {
			if len(cuts) > 0 && cuts[0] != nil && cuts[0].owner == w && entry.appendRevision <= cuts[0].revision {
				continue
			}
			return ErrWALWindowChanged
		}
		if !entry.Durable {
			return ErrWALWindowChanged
		}
		matched = append(matched, i)
	}
	for _, result := range results {
		if err := w.recordHTTPResultLocked(result); err != nil {
			return err
		}
	}
	acked := len(results) > 0
	for _, result := range results {
		acked = acked && result.HTTPStatus >= 200 && result.HTTPStatus < 300 && !result.AckLost
	}
	if acked {
		for _, i := range matched {
			id, err := w.entries[i].eventIdentityLocked()
			if err != nil {
				return err
			}
			w.entries[i].acknowledged = &id
		}
	}
	return nil
}

// AcquireCurrentWindow只为最终本地CAS/转段取得区间读租约；调用方禁止在租约内做VL查询或退避。
// 完整身份在租约取得后同一WAL快照重验；不存在全局采集锁跨Catalog IO，窗外新尾可继续Append/Commit。
func (w *WAL) AcquireCurrentWindow(events []logtypes.Event, requirePresent bool, filter func(logtypes.Event) bool, protectPrefix ...bool) (func(), error) {
	prefix := len(protectPrefix) > 0 && protectPrefix[0]
	return w.AcquireCapturedWindow(events, requirePresent, filter, prefix, nil)
}

// AcquireCapturedWindow在已完成的canonical裁定之后使用原捕获边界；旧兄弟只保留责任，不授证或跳债务。
func (w *WAL) AcquireCapturedWindow(events []logtypes.Event, requirePresent bool, filter func(logtypes.Event) bool, prefix bool, capture *DeliveryCapture) (func(), error) {
	expected := make(map[CommitIdentity]struct{}, len(events))
	wanted := make(map[string]struct{}, len(events))
	var from, to uint64
	for i, event := range events {
		id, err := CommitIdentityOf(event)
		if err != nil {
			return nil, err
		}
		expected[id] = struct{}{}
		wanted[id.EventID] = struct{}{}
		if i == 0 || id.Record.Start < from {
			from = id.Record.Start
		}
		if id.Record.End > to {
			to = id.Record.End
		}
	}
	window := &walWindow{from: from, to: to}
	if prefix {
		window.from = 0
	}
	w.mu.Lock()
	for existing := range w.windows {
		if existing.mutation && window.from < existing.to && window.to > existing.from {
			w.mu.Unlock()
			return nil, ErrWALWindowBusy
		}
	}
	if w.windows == nil {
		w.windows = make(map[*walWindow]struct{})
	}
	w.nextWindow++
	window.id = w.nextWindow
	// window尚未暴露，无竞争的读锁必须与注册同一元数据临界区完成，mutation不能穿过间隙。
	window.mu.RLock()
	w.windows[window] = struct{}{}
	w.windowVersion++
	w.mu.Unlock()
	if w.windowRegistered != nil {
		w.windowRegistered()
	}
	release := func() { window.mu.RUnlock(); w.mu.Lock(); delete(w.windows, window); w.windowVersion++; w.mu.Unlock() }
	w.mu.Lock()
	seen := make(map[CommitIdentity]struct{}, len(expected))
	var checkErr error
	members := 0
	for _, entry := range w.entries {
		if prefix && entry.Event.Record.Start < from && (filter == nil || filter(cloneWALEvent(entry.Event))) && !w.reclaimEntryQualifiedLocked(entry) {
			checkErr = ErrWALWindowChanged
			break
		}
		_, sameID := wanted[entry.Event.EventID]
		inWindow := entry.Event.Record.Start < to && entry.Event.Record.End > from && (filter == nil || filter(cloneWALEvent(entry.Event)))
		if !sameID && !inWindow {
			continue
		}
		id, err := CommitIdentityOf(entry.Event)
		if err != nil {
			checkErr = err
			break
		}
		if _, ok := expected[id]; !ok {
			if capture != nil && capture.owner == w && entry.appendRevision <= capture.revision {
				continue
			}
			checkErr = ErrWALWindowChanged
			break
		}
		if !entry.Durable {
			checkErr = ErrWALWindowChanged
			break
		}
		seen[id] = struct{}{}
		if capture != nil && capture.owner == w && capture.token != 0 && entry.captureToken == capture.token {
			if entry.captureBody != id.BodyDigest {
				checkErr = ErrWALWindowChanged
				break
			}
			members++
		}
	}
	if capture != nil && capture.owner == w && capture.token != 0 {
		// 完整actual集合可含已回收历史；WAL只要求原标记活成员全在且正文未变。
		if members != capture.members {
			checkErr = ErrWALWindowChanged
		}
	} else if requirePresent && len(seen) != len(expected) {
		checkErr = ErrWALWindowChanged
	}
	w.mu.Unlock()
	if checkErr != nil {
		release()
		return nil, checkErr
	}
	return release, nil
}

// 积压上限（B1b；2026-10-29 改为**丢弃语义**）。
//
// ## 历史与本次变更
//
// 原背景（2026-09-28 生产事故）：某源投递/校验长时间失败 → reclaim 永不推进 →
// 未投递条目无界累积，单源把状态文件撑到 1.2 GB，持续 117 MB/s、CPU 138%。
// 当时的处置语义刻意选了**暂停而非丢弃**（“暂停期间不丢数据”）。
//
// 2026-10-29 用户裁定改成**内存满即丢、不暂停、不背压**：
// WAL 将改为纯内存队列，机械盘是拖累；而暂停的实际后果已经付过两次代价
// （生产 19 个源里 10 个因闸停采 20+ 小时，其中一段对数据的保护价值远小于
// 新日志全部停摆的代价）。
//
// ## 丢弃不“静默”
//
// 丢≠偷偷丢：每丢掉一段前缀就登记一条 `WAL_BACKLOG_DROPPED` 缺口凭据
// （带真实位置区间与原因），可在 `GapObservability` 与查询面看到，
// 且**不在**任何自动消解白名单里（真丢不回来的字节不应被后续对账抹掉）。
// 这与 2026-10-28 的「超热层保留期放弃」同一套机制（结构化凭据 + 原因码），
// 区别是那里由人裁定、这里由容量事实裁定。
const (
	// defaultWALMaxEntries / defaultWALMaxBytes 是单源默认积压上限。
	//
	// 取值的取舍（2026-10-29 重新定）：WAL 改纯内存后它直接决定进程内存上界。
	// 2000 条 × 约 1KB 均值 ≈ 2MB/源；60 源约 120MB，对 251GB 机器完全可接受，
	// 而且远比原来保守（原字节闸是 16MiB/源 ⇒ 62 源可达 1GB）。
	defaultWALMaxEntries = 2000
	defaultWALMaxBytes   = 16 << 20 // 16 MiB

	// walBacklogDropReason 是「积压越界丢弃」缺口的原因码。
	//
	// 不复用 `APPEND_REJECTED`（那是“该批从未进 WAL，可回读补投”）——
	// 丢弃的字节**确实进过 WAL、又因容量被扔掉了**，回读也补不回来
	//（tailer 的读游标已越过它）。两者处置方向相反，必须分开。
	walBacklogDropReason = "WAL_BACKLOG_DROPPED"

	// legacyWALBacklogPauseReason 是旧二进制写入账本的**暂停原因前缀**。
	//
	// 2026-10-29 积压闸改为丢弃后，本包不再产生这个暂停；但存量库里的
	// `acquire_paused=1` 记录仍然存在，且会阻止 tailer 读。不认它，
	// 这些源就永远停在暂停上（没人能匹配出原因）。因此本常量是**只读兼容**用的：
	// 仅用于 `IsBacklogPauseReason` 与 `maybeResumeBacklogLocked` 的识别，绝不用于写入。
	legacyWALBacklogPauseReason = "wal backlog limit exceeded"
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
	// reservation 仅属于本进程；不再有快照恢复路径，故只在 Append 生命周期内存在。
	reservation *walByteReservation
	// 只属于本进程的真实ACK完整身份；不落盘、不跨重启恢复，因此不存在从旧位置水位重造的路径。
	acknowledged *CommitIdentity `json:"-"`
	// 与本次完整身份捕获边界比较，区分早已存在的未选兄弟和网络后新增/替换者。
	appendRevision uint64   `json:"-"`
	captureToken   uint64   `json:"-"`
	captureBody    [32]byte `json:"-"`
	// identity / identityOK 是本条目 Event 的**完整身份缓存**。
	//
	// 为什么必须有（2026-10-29 pprof 实测）：`CommitIdentityOf` 要对整条 event 做
	// json.Marshal + SHA256（单条约 6µs）。而 WAL 快照类入口是**逐条**算身份的：
	//   · `HasPendingDelivery(limit=1)` —— 只想探「有没有待投递」，旧实现却把**整个 WAL**
	//     (积压满时 5000 条) 全算一遍；
	//   · `CommittedIdentities` —— 每次排空后的回执裁剪同样全表算一遍。
	// 生产实测这两条合计吃掉 **63% CPU**（30s profile：snapshotPending 34% +
	// committedIdentities 32%，全部落在 json.Marshal/sha256）。节流毫无意义 —— 每次结果逐字相同。
	//
	// 失效面极窄且完整：Event 只在三处变化 —— 追加时构造（:1155）、回放去重原位替换
	// （:1217 `adjudicateReplayDuplicatesLocked`）、以及被 prune 移出切片（条目消失）。
	// 因此所有写 Event 的地方都必须走 `setEventLocked`。切片本身按值拷贝（Snapshot 的
	// `copy`、snapshotPending 的 `entry := w.entries[index]`）⇒ 缓存随之复制，跨拷贝仍有效 ✓。
	identity   CommitIdentity
	identityOK bool
}

// setEventLocked 是**唯一**允许改写条目正文的入口：正文变了，身份缓存必须一起失效。
//
// 为什么收成一个函数而不是各处手动清：`CommitIdentity` 是对 Event 的完整摘要，
// 漏清一处就会让下游拿到**上一个版本的**身份 —— 那是「用旧身份冒充新正文」的静默数据错误，
// 比不缓存更糟。故把「改正文」与「清缓存」绑成一次不可分的写（调用方须持 w.mu）。
func (entry *WALEntry) setEventLocked(event logtypes.Event) {
	entry.Event = event
	entry.identity = CommitIdentity{}
	entry.identityOK = false
}

// eventIdentityLocked 返回本条目 Event 的完整身份（命中缓存则直接返回）。
//
// 调用方须持 w.mu：缓存只在写锁下的 setEventLocked 里失效，读过晚会在并发窗口内
// 拿到「旧正文的身份」，而那正是这个缓存唯一可能出错的形态。
func (entry *WALEntry) eventIdentityLocked() (CommitIdentity, error) {
	if entry.identityOK {
		return entry.identity, nil
	}
	id, err := CommitIdentityOf(entry.Event)
	if err != nil {
		return CommitIdentity{}, err
	}
	entry.identity = id
	entry.identityOK = true
	return id, nil
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
	// 仅最终发布/责任转移的短期区间租约；不持正文、不跨VL查询，窗外追加不等待它。
	windows          map[*walWindow]struct{}
	windowVersion    uint64
	nextWindow       uint64
	qualifiedReclaim bool
	revision         uint64
	nextCapture      uint64
	// pendingEpoch仅重定位进程内选批槽位；删项/恢复/替换才推进，纯追加保留轮转公平性。
	pendingEpoch uint64

	entries        []WALEntry
	reserveBytes   func(int64) (func(), error)
	reservationErr error

	// 磁盘缓冲（FR-510 P1.3）。spoolCh 为 nil 即未挂载，此时行为与改造前逐字一致。
	//
	// 设计要点：**持 w.mu 时只入 channel（非阻塞），磁盘 IO 由后台协程做**。
	// 持锁做磁盘 IO 会让整个源的采集停摆，那是 2026-10-29 裁定要避免的形态。
	spoolSink    SpoolSink
	spoolCh      chan spoolJob
	spoolSaved   int64
	spoolFailed  int64
	spoolSkipped int64
	// windowRegistered仅为确定性交错测试观测注册线性化点；生产nil，不改变锁或正文语义。
	windowRegistered func()
	// fsync 持久提交钩子；nil 视为成功（测试可注入失败）。
	fsync func() error

	// lastDurableEnd 已耐久前缀末端（源位置）。
	lastDurableEnd uint64
	// pendingDelivery 已 append/耐久但尚无请求结果的源位置前缀。
	pendingStart uint64

	// recoverySegs WAL 关联的恢复分段登记（由 pipeline 更新账本）。

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
	// droppedEntries / droppedBytes 是**累计丢弃**计数（只增不减，仅供观测）。
	//
	// 为什么必须暴露（对齐 Fluent Bit `memrb`，2026-10-29）：Fluent Bit 的 memory
	// ring buffer 模式在“丢最旧”的同时提供 `memrb_dropped_chunks` /
	// `memrb_dropped_bytes`，官方文档明写“You can monitor these metrics to understand
	// how much data is being discarded”。理由是：一旦选了“不背压”，丢弃就是设计内的
	// 正常行为，**它必须可观测**——否则运维只看到“日志少了”，却分不清是丢了还是源没写。
	//
	// 不参与任何判定（累计量只增不减，绝不能拿它做门禁）。
	droppedEntries int64
	droppedBytes   int64
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

// DropStats 返回**累计丢弃**读数（条目数, 近似字节）。
//
// 对齐 Fluent Bit `memrb_dropped_chunks` / `memrb_dropped_bytes`：选了“不背压”之后，
// 丢弃是设计内的正常行为，但**必须可观测**——否则运维只能看到“日志少了”，
// 分不清是该源本就没写、还是丢在了队列里。
//
// 只读、只增不减：不得用它做任何门禁（累计量不回落）。
func (w *WAL) DropStats() (entries int64, bytes int64) {
	if w == nil {
		return 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.droppedEntries, w.droppedBytes
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
	w.pendingEpoch++
	entry.reservation.close()
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

// IsBacklogPauseReason 报告某条暂停原因是否曾经由本包的**积压闸**造成。
//
// 2026-10-29：积压闸已改成「丢弃」而不再产生暂停 —— 但本函数**不能删**：
// 存量数据库里可能还留着 `wal backlog limit exceeded ...` 的暂停记录（旧二进制写下的），
// 重启后这些源仍然是暂停态，必须有人能把它们认出来并清除。
// 直接改成恒 false 会让它们永远无法自动解除。
func IsBacklogPauseReason(reason string) bool {
	return strings.HasPrefix(reason, legacyWALBacklogPauseReason)
}

// TrimBacklog 在**批次已被取走且落库之后**裁剪积压（越界则丢最旧）。
//
// 为什么需要独立入口（2026-10-29，这轮踩了三次的坑）：
// 丢弃会从 `entries` 删条目，而调用方的顺序固定是
//
//	Append → Commit → captureCommitted（按事件逐个从 entries 找回）→ 持久化 → 投递
//
// 在 captureCommitted **之前**丢任何一条，都会得到
// “batch contains an event without a matching WAL commit”——而那批数据刚读到、完全能投。
// 所以裁剪只能放在持久化**之后**：那时本批已进索引库，内存副本被削也能从索引恢复。
//
// 调用点：pipeline.persistCommittedBatchContext 完成持久化后。
func (w *WAL) TrimBacklog() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// protectFrom 传 0：本批已被取走且已落库，现在没有条目需要特殊保护。
	// “保护”在这里反而有害——持续只入不出的源会永远停在超限状态。
	w.enforceBacklogLimitLocked(0)
}

// enforceBacklogLimitLocked 在提交后检查积压是否越界；越界就**丢弃最旧的条目**（2026-10-29）。
//
// ## 为什么从「暂停」改为「丢弃」
//
// 用户裁定（2026-10-29）：WAL 改纯内存，内存到上限直接丢弃，不暂停采集、不背压。
// 暂停的实际代价已经付过：生产 19 个源里 10 个因积压闸停采 20+ 小时，而保住的
// 只是“已经投不出去”的旧条目——新日志反而全停了。
//
// ## 丢弃的具体语义
//
//  1. **只丢头部（最旧）**：按 seq 升序排列，从 0 开始砍。丢弃尾部等于丢最新数据，
//     那才是真正有价值的。
//  2. **不越界不丢**：只丢到**刚好回到上限以下**为止，不多丢一条。
//  3. **每丢一段就登记一条 `WAL_BACKLOG_DROPPED` 缺口**：区间是被丢条目的真实位置，
//     因而可查、可告警、不会被后续对账当成“正常前缀”抹掉。
//     这是“不背压”的必要前提——不背压就必须有人在数据被丢时能看到它。
//  4. ****不碰 `Read`/`Durable` 水位**：读游标已越过这些字节（tailer 已经读过了），
//     回退它会让 tailer 重读已投递内容（真机实测 30 倍重复）。丢的是“待投递副本”，
//     不是“源文件游标”。
//  5. **只丢已 APPEND 但未 ACK 的条目**：已 ACK 的不在这条路径上（它们会被
//     pruneReclaimed 正常回收）；这里扔掉的是“投不出去”的票。
//  6. **不丢本批刚进来的事件**（除非本批自己就装不下）：调用方传 `protectFrom`
//     （本批追加前的长度）作为优先保护下限，丢优先发生在它之前的前缀。
//     否则调用方紧接着的 captureCommitted 会拿到一个假错误
//     （“batch contains an event without a matching WAL commit”），而那批数据其实刚读到、
//     完全能投。
//     **但保护不是无限的**：若清完旧积压仍越界（典型：单批就超上限，或持续只入不出），
//     那就必须继续往后丢，否则队列会永远停在超限状态——而“队列恒有界”正是不背压的前提。
//     此时本批可能被部分丢弃，调用方会拿到 captureCommitted 错误，由既有路径记缺口
//     （这正是真实的“这批装不下”而不是误报）。
//
// 调用方需持 w.mu。
func (w *WAL) enforceBacklogLimitLocked(protectFrom int) {
	if len(w.entries) == 0 {
		return
	}
	if protectFrom < 0 {
		protectFrom = 0
	}
	if protectFrom > len(w.entries) {
		protectFrom = len(w.entries)
	}
	maxEntries, maxBytes := w.limitsLocked()
	entries, bytes := w.backlogLocked()
	if entries <= maxEntries && bytes <= maxBytes {
		return
	}
	// 记下**丢弃前**的读数：缺口 detail 要写的是「当时用了多少」，
	// 下面循环会把 entries/bytes 一路减到裁剪后的值，减完再拼串就只能拼出残余量。
	// （2026-10-29 修正：原实现写成 `bytes + int64(0)`，审计面上读到的分子恒等于残余字节，
	//  分母明明是上限却与分子语义不匹配——数字根本无法解释。）
	entriesBefore, bytesBefore := entries, bytes

	// 第一轮：只砍 protectFrom 之前的旧积压。
	cut := 0
	for cut < protectFrom && (entries > maxEntries || bytes > maxBytes) {
		entry := w.entries[cut]
		entries--
		bytes -= walEntryBytes(entry)
		cut++
	}
	// 第二轮：旧积压清完仍越界 ⇒ 必须继续往后砍，保证队列恒有界。
	for cut < len(w.entries) && (entries > maxEntries || bytes > maxBytes) {
		entry := w.entries[cut]
		entries--
		bytes -= walEntryBytes(entry)
		cut++
	}
	if cut == 0 {
		return
	}

	// FR-510 P1.3：丢弃前先把这批条目交给磁盘缓冲。
	//
	// **这里只做内存拷贝与非阻塞 channel 发送，不做磁盘 IO**——
	// 本函数由调用方持 w.mu 调用，持锁做磁盘 IO 会让整个源的采集停摆。
	// 真正写盘在 runSpoolWriter 里（不持锁）。
	//
	// 未挂载缓冲时（spoolCh == nil）offerSpoolLocked 直接返回 0，
	// 行为与改造前逐字一致。
	spooled := w.offerSpoolLocked(cut)

	// 先算丢弃区间的真实位置（半开区间，取被丢条目首尾）。
	var from, to uint64
	dropped := 0
	var bytesDropped int64
	for i := 0; i < cut; i++ {
		entry := w.entries[i]
		if i == 0 || entry.Event.Record.Start < from {
			from = entry.Event.Record.Start
		}
		if entry.Event.Record.End > to {
			to = entry.Event.Record.End
		}
		bytesDropped += walEntryBytes(entry)
		w.noteEntryDroppedLocked(entry)
		w.entries[i] = WALEntry{} // 清引用：底层数组不得继续持有已丢弃事件的正文
		dropped++
	}
	w.entries = w.entries[cut:]
	// 累计丢弃计数（对齐 Fluent Bit memrb_dropped_chunks / memrb_dropped_bytes）：
	// 丢弃是设计内行为，必须可观测，否则运维只能看到“日志少了”，分不清是丢还是源没写。
	w.droppedEntries += int64(dropped)
	w.droppedBytes += bytesDropped

	// 缺口凭据：区间是真实的字节位置，因此可查、可审计。
	// 登记失败不回滚丢弃（已经丢了，回滚只会让内存超界）——但必须如实报错。
	if to > from {
		detail := fmt.Sprintf("WAL backlog exceeded (entries=%d/%d, approx_bytes=%d/%d); dropped %d oldest entries to stay in memory",
			entriesBefore, maxEntries, bytesBefore, maxBytes, dropped)
		if spooled > 0 {
			// 有 spool 时必须如实区分“真丢”与“交给磁盘缓冲” ——
			// 否则缺口审计面会把已经救回来的数据也报成损失。
			detail = fmt.Sprintf("WAL backlog exceeded (entries=%d/%d, approx_bytes=%d/%d); dropped %d oldest entries to stay in memory; %d handed to disk spool",
				entriesBefore, maxEntries, bytesBefore, maxBytes, dropped, spooled)
		}
		if err := w.led.RecordGap(w.key, from, to, walBacklogDropReason, detail); err != nil {
			slog.Warn("积压越界丢弃后登记缺口失败（丢弃已生效，但审计面缺凭据）",
				"logSourceID", w.key.LogSourceID, "dropped", dropped, "from", from, "to", to, "error", err)
		}
	}
	slog.Warn("单源积压超过上限，已丢弃最旧条目（不暂停采集、不背压）",
		"logSourceID", w.key.LogSourceID, "dropped", dropped, "from", from, "to", to,
		"spooled", spooled,
		"remainingEntries", len(w.entries), "maxEntries", maxEntries, "maxBytes", maxBytes)
}

// maybeResumeBacklogLocked 在回收后尝试解除**存量**积压暂停。
//
// 2026-10-29：积压闸已改为丢弃，本包**不再产生**新的积压暂停；
// 但存量数据库可能仍带着旧二进制写下的暂停记录，所以这里保留一个收敛路径。
//
// **为什么仍要保留「按回读分类消解缺口」那一步**（本次一度想删掉，被回归拦住）：
// 旧暂停的解除不能跳过缺口分类——暂停期间被拒的追加会留下
// `APPEND_REJECTED` / `DELIVER_ERROR` / `ROTATED_SEGMENT_NOT_READY` 这几类缺口，
// 它们的语义是「恢复后回读会重新产生并投递这段字节」，因此必须先按分类裁定
// 把它们消解掉，`ResumeAcquire` 才可能放行。跳过这一步的后果就是
// “存量暂停永远解不开”——与本次改造的目的正好相反。
// 调用方需持 w.mu。
func (w *WAL) maybeResumeBacklogLocked() {
	st, ok := w.led.AcquireState(w.key)
	if !ok || !st.Paused || !strings.HasPrefix(st.PauseReason, legacyWALBacklogPauseReason) {
		return
	}
	entries, bytes := w.backlogLocked()
	if err := w.led.ResumeAcquire(w.key); err != nil {
		// 旧实现里的「更新换代后仍自锁」形态：`ResumeAcquire` 对任何仍阻塞的缺口一律拒绝，
		// 而那些缺口恰恰因为暂停期无法回读而无法消解 ⇒ 死锁。
		// 这里按分类裁定消解可愈合类（不是操作员放弃）：恢复后回读会重新产生并投递该批。
		if ent := w.led.Get(w.key); ent != nil {
			if healed := w.resolveReReadHealableGapsLocked(ent); healed > 0 {
				if err2 := w.led.ResumeAcquire(w.key); err2 == nil {
					slog.Info("存量积压暂停已解除：已按「恢复后回读重放」分类消解暂停期缺口",
						"logSourceID", w.key.LogSourceID, "gaps", healed, "entries", entries, "approxBytes", bytes)
					return
				}
			}
		}
		slog.Info("存量积压暂停未解除（其余判据仍拒绝恢复）",
			"logSourceID", w.key.LogSourceID, "error", err)
		return
	}
	slog.Info("已解除存量积压暂停（积压闸已改为丢弃语义，不再暂停采集）",
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
//   - **存量**积压暂停（旧二进制写的，前缀 `legacyWALBacklogPauseReason`）会由
//     maybeResumeBacklogLocked 在能消解掉阻塞缺口后清除（**2026-10-29 起不再有
//     “积压回落至上限一半以下”这个条件**）；清掉后本方法返回 true；
//   - 容量门禁等**其它路径**设置的暂停不会被本方法清除（那是越权），但只要它已被别处解除，
//     本方法同样会返回 true；反之若仍处于暂停，则返回 false。
//
// 因此调用方可以用它判断「恢复是否已发生」，但不得把它当成「本次调用清除了暂停」的证据。
func (w *WAL) EvaluateResume() bool {
	// TryReclaim 的失败是常态而非异常（无恢复分段 / 责任未转移时 CanReclaim 本就不放行），
	// 而它无论成败都会按**当前**水位剪枝，并在 `pos == 0` 分支里做一次存量暂停评估；
	// 这里忽略返回值，只按最终状态回答「是否已恢复」。
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
	return w.appendLocked(originAcquire, nil, events...)
}

// AppendReserved仅消费本次记录窗的采样准入，仍经过原容量、正文预留与账本追加判据。
func (w *WAL) AppendReserved(reservation *AppendReservation, events ...logtypes.Event) error {
	return w.appendLocked(originAcquire, reservation, events...)
}

// AppendReplay 见 Append 的说明（红线：常规采集不得走本入口 ✓）。
func (w *WAL) AppendReplay(events ...logtypes.Event) error {
	return w.appendLocked(originReplay, nil, events...)
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
func (w *WAL) appendLocked(origin walOrigin, reservation *AppendReservation, events ...logtypes.Event) error {
	private := make([]logtypes.Event, len(events))
	for i, event := range events {
		private[i] = cloneWALEvent(event)
	}
	events = private
	unlock := w.lockMutation(events, false, reservation)
	if unlock == nil {
		return ErrWALWindowBusy
	}
	defer unlock()
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

	if w.revision == ^uint64(0) {
		return fmt.Errorf("acquire: WAL捕获序号已耗尽，拒绝回绕")
	}
	w.revision++
	// #179 上游根修：回放/补账入口先与既有条目对账，杜绝"同一 id 写两遍"✗（详见函数注释 ✓）。
	// 判据取**本次入参** origin（ACQ-2）：常规采集轮不得走进放去重 ✓。
	if replay && len(events) > 0 {
		events = w.adjudicateReplayDuplicatesLocked(events)
		if len(events) == 0 {
			return nil
		}
	}

	if w.reservationErr != nil {
		return w.reservationErr
	}
	newEntries := make([]WALEntry, len(events))
	for i, ev := range events {
		newEntries[i] = WALEntry{Replay: replay, Seq: uint64(len(w.entries) + i + 1), Event: ev, Appended: true, appendRevision: w.revision}
	}
	leases, err := reserveWALEntries(newEntries, w.reserveBytes)
	if err != nil {
		return err
	}
	for i := range newEntries {
		newEntries[i].reservation = leases[i]
		w.entries = append(w.entries, newEntries[i])
		w.noteEntryAddedLocked(newEntries[i]) // 预算与积压都跟随实际条目生命周期。
	}
	// read_position 可先推进；durable 等 Commit。
	end := lastRecordEnd(events, st.Positions.Read)
	if err := w.led.AdvanceRead(w.key, end); err != nil {
		return err
	}
	// 追加后**不**在这里丢：本批紧接着要被 captureCommitted / 投递，
	// 在此处丢会让调用方拿到“batch contains an event without a matching WAL commit”
	// 的**假错误**（那批数据刚读到、完全能投）。
	// 裁剪点放在 Commit 之后（此时本批已 durable，captureCommitted 不再依赖较老的前缀），
	// 见 Commit 末尾。
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
			w.pendingEpoch++
			w.entries[idx].setEventLocked(ev) // 胜者原位替换：静止态收敛为同一内容 ✓
			w.entries[idx].Replay = true
			w.entries[idx].acknowledged = nil
			w.entries[idx].appendRevision = w.revision
			w.entries[idx].captureToken = 0
			w.entries[idx].captureBody = [32]byte{}
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
	if w.reservationErr != nil {
		return w.reservationErr
	}

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
	// **账本可能已经比本 WAL 实例更靠前**（2026-10-22 真机缺陷）：
	// `lastDurableEnd` 只活在内存里，重启后由「当前 WAL 条目」重建（见 L87/L961）——
	// 而重启时那些条目往往**已被 reclaim 回收**，于是进程内计数从小值开始；另一边
	// 账本里的 durable 仍是重启前的末端（生产实测 4592494）。此时用小值去推进会撞上
	// `durable_position must be monotonic`，而该错误被登记为 `WAL_COMMIT_FAILED` 缺口：
	//   · 采集每轮失败 ⇒ 缺口每轮重建 ⇒ 「登记 → 消解 → 再登记」活循环
	//     （生产实测 65 条同区间缺口的 id 0..64，每 10s 一转）；
	//   · 期间该源被标成「不可分类缺口阻塞」，积压不推进。
	//
	// 语义上这不是错误而是**正常形态**：durable 是「已耐久到至少这个位置」的下界，
	// 账本更靠前就说明这些字节早已耐久（reclaim 的前提就是「已证明安全另存」）。
	// 故先取账本的当前值兜底：推进量只会单调，绝不会因为重启而回退。
	if p, ok := w.led.Positions(w.key); ok && p.Durable > maxEnd {
		// **不能静默**（2026-10-22 复审 P2-6）：这条分支意味着「进程内的 durable 计数
		// 落后于账本」。正常重启后由 WAL 条目重建时不会有落差（落差 = 条目已被 reclaim），
		// 而**截断重锚**（同 inode 尺寸回退）也会走到这里 —— 那是真实的状态分歧，
		// 修复前会以 monotonic 错误响亮报出，修复后必须留下痕迹才不至于变成静默。
		// 生产 v9/v10 正是此分支在生效（reclaim 已推进而进程内计数回退），此前一行日志都没有。
		slog.Warn("WAL durable 计数落后于账本，按账本兜底（不回退已耐久水位）",
			"key", w.key.String(), "walMaxEnd", maxEnd, "ledgerDurable", p.Durable)
		maxEnd = p.Durable
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
	// 积压定界**不在 Commit 里做**（2026-10-29，这轮最深的坑）：
	//
	// Commit 返回后，调用方的下一步就是 `captureCommitted`——按事件**逐个**从 entries
	// 把本批找回来。只要此刻削掉本批的任意一条，那一步就会返回
	// “batch contains an event without a matching WAL commit”，而那批数据刚读到、完全能投。
	//
	// 而“保护本批”与“队列恒有界”本质冲突：若单批本身就超上限，两者不可能同时成立。
	// 结论：裁剪必须放在**本批被取走且已落库之后**（pipeline 的持久化之后，见 WAL.TrimBacklog）。
	// 代价：调用方必须配合（直接调 Append+Commit 而不走 pipeline 的路径不会自动裁剪）——
	// 但那些路径本来就没有“刚采的数据还在飞”的语义，也不该在 Commit 里被动挨刀。
	return nil
}

// RecordHTTPResult 登记请求级结果。
// HTTP 2xx 只写 REQUEST_DONE，不推进 reclaim_position。
// AckLost → UNKNOWN，保留恢复责任。
func (w *WAL) RecordHTTPResult(res DeliveryResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recordHTTPResultLocked(res)
}

func (w *WAL) recordHTTPResultLocked(res DeliveryResult) error {
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

// TryReclaim 经账本 CanReclaim 门禁尝试推进 reclaim_position，并回收 WAL 中已被
// reclaim 前缀覆盖的事件（否则内存会随采集总量线性增长——真机 64 源实测曾致 Worker RSS 2GiB）。
func (w *WAL) TryReclaim() (uint64, error) { return w.TryReclaimThrough(^uint64(0)) }

// TryReclaimThrough供当前发布证明的责任转移出口限定实际目标，不以旧宽分段越过证明外沿。
func (w *WAL) TryReclaimThrough(through uint64) (uint64, error) {
	w.mu.Lock()
	if w.qualifiedReclaim {
		// 此WAL短临界只做内存身份复核/账本推进/剪枝；窗外采集最多等这段内存工作，不跨任何IO。
		unknown := make([]logtypes.RecordRange, 0, len(w.entries))
		for _, pending := range w.entries {
			if !w.reclaimEntryQualifiedLocked(pending) {
				unknown = append(unknown, pending.Event.Record)
			}
		}
		var abandoned []ledger.GapAbandonment
		pos, err := w.led.TryReclaimIf(w.key, func(proof ledger.ReclaimTarget) bool {
			if proof.Target > through {
				return false
			}
			abandoned = proof.Abandonments
			for _, record := range unknown {
				if record.Start < proof.Target && !walRecordAbandoned(record, proof.Abandonments) {
					return false
				}
			}
			return true
		})
		if abandoned == nil {
			if entry := w.led.Get(w.key); entry != nil {
				abandoned = entry.Abandonments
			}
		}
		if err == nil {
			w.pruneReclaimedLocked(pos, abandoned)
		} else if err != ledger.ErrReclaimPrecondition {
			if positions, ok := w.led.Positions(w.key); ok {
				w.pruneReclaimedLocked(positions.Reclaim, abandoned)
			}
		}
		w.mu.Unlock()
		return pos, err
	}
	w.mu.Unlock()
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
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneReclaimedLocked(pos, nil)
}

func walEntryAbandoned(entry WALEntry, proofs []ledger.GapAbandonment) bool {
	return walRecordAbandoned(entry.Event.Record, proofs)
}

func walRecordAbandoned(record logtypes.RecordRange, proofs []ledger.GapAbandonment) bool {
	if record.End <= record.Start {
		return false
	}
	// 人工凭据是闭区间，record为半开；只按精确并集推进，不填凸包的健康洞，也不做无保护to+1。
	through := record.Start
	for {
		next := through
		for _, proof := range proofs {
			if proof.ReasonCode != ledger.GapReasonPermanentlyLost || proof.Operator == "" || proof.From > through || proof.To < through {
				continue
			}
			if proof.To >= record.End-1 {
				return true
			}
			if proof.To+1 > next {
				next = proof.To + 1
			}
		}
		if next == through {
			return false
		}
		through = next
	}
}

func (w *WAL) pruneReclaimedLocked(pos uint64, abandoned []ledger.GapAbandonment) {
	if pos == 0 {
		w.maybeResumeBacklogLocked()
		return
	}
	// #66-①：**增量** prune —— 只删"从头部起的连续可回收前缀"（条目按 seq 追加 ⇒ 正常情形下
	// "可回收"就是一段前缀 ✓）⇒ 用**头偏移窗**，并把已删前缀**置零**（窗口仍指向同一底层数组，
	// 不置零就等于继续持有已丢弃事件的正文 ✗ —— 原实现正是为此改用"新数组" ✗，但那会
	// `append([]WALEntry(nil), kept...)` **复制剩余全部** ⇒ 单次 prune 与"剩余条数"成正比，
	// 现场单跳 ~1s 的主项之一（#65 归因）✗✗。
	// 复杂度：单次 prune = O(被删条数)（每条只被置零一次 ⇒ 全生命周期摊销 O(1)/条 ✓）。
	cut := 0
	for cut < len(w.entries) {
		e := w.entries[cut]
		if !e.Durable || e.Event.Record.End > pos || (!w.reclaimEntryQualifiedLocked(e) && !walEntryAbandoned(e, abandoned)) {
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
			if entry.Durable && entry.Event.Record.End <= pos && (w.reclaimEntryQualifiedLocked(entry) || walEntryAbandoned(entry, abandoned)) {
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

// reclaimEntryQualifiedLocked只认当前完整正文的实际ACK，内存stamp不能由恢复范围或最大位置重造。
func (w *WAL) reclaimEntryQualifiedLocked(entry WALEntry) bool {
	if !w.qualifiedReclaim {
		return true
	}
	if entry.acknowledged == nil {
		return false
	}
	id, err := CommitIdentityOf(entry.Event)
	return err == nil && id == *entry.acknowledged
}

// Snapshot 返回 WAL 内事件（测试/恢复用）。
func (w *WAL) Snapshot() []WALEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]WALEntry, len(w.entries))
	copy(out, w.entries)
	for i := range out {
		out[i].reservation = nil
		out[i].Event = cloneWALEvent(out[i].Event)
	}
	return out
}

// cloneWALEvent只复制可变字段；正文字符串只读共享，不另造正文队列。
func cloneWALEvent(event logtypes.Event) logtypes.Event {
	if event.Fields != nil {
		fields := make(map[string]string, len(event.Fields))
		for key, value := range event.Fields {
			fields[key] = value
		}
		event.Fields = fields
	}
	return event
}

// DeliveryCapture只携带原WAL实例、同锁捕获版号和实际写入成员token；不携正文或逐ID表。
type DeliveryCapture struct {
	owner    *WAL
	revision uint64
	token    uint64
	members  int
}
type walCapture = DeliveryCapture

// CapturedSiblings只返回本批ID在原捕获前已存在的完整兄弟，供既有canonical裁定；新变体不能借cut变旧。
func (w *WAL) CapturedSiblings(events []logtypes.Event, capture *DeliveryCapture) ([]logtypes.Event, error) {
	if capture == nil || capture.owner != w {
		return nil, ErrWALWindowChanged
	}
	wanted := make(map[string]struct{}, len(events))
	for _, event := range events {
		wanted[event.EventID] = struct{}{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []logtypes.Event
	for _, entry := range w.entries {
		if _, ok := wanted[entry.Event.EventID]; !ok {
			continue
		}
		if entry.appendRevision > capture.revision || !entry.Durable {
			return nil, ErrWALWindowChanged
		}
		out = append(out, cloneWALEvent(entry.Event))
	}
	return out, nil
}

// CaptureAcknowledgedPrefix同锁捕获有界、连续、实际耐久且完整正文已ACK的前缀及原cut。
// 不看恢复段或hold，避免无段覆盖的修复反过来依赖既有段；返回正文只含私有Fields副本。
func (w *WAL) CaptureAcknowledgedPrefix(from, to uint64, limit int) ([]logtypes.Event, *DeliveryCapture, error) {
	if limit <= 0 || limit > 500 {
		return nil, nil, fmt.Errorf("acquire: ACK前缀成员超出有界窗口")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	capture := &DeliveryCapture{owner: w, revision: w.revision}
	window := make([]WALEntry, 0, limit)
	for _, entry := range w.entries {
		if entry.Event.Record.End <= from || entry.Event.Record.Start >= to {
			continue
		}
		if len(window) >= limit {
			break
		}
		window = append(window, entry)
	}
	sort.Slice(window, func(i, j int) bool { return window[i].Event.Record.Start < window[j].Event.Record.Start })
	events := make([]logtypes.Event, 0, len(window))
	end := from
	for _, entry := range window {
		if entry.Event.Record.Start < end {
			// 同位置旧兄弟也不能借已选正文的ACK；保留歧义位置之后的全部责任，只留下其前面的连续段。
			for len(events) > 0 && events[len(events)-1].Record.End > entry.Event.Record.Start {
				events = events[:len(events)-1]
			}
			break
		}
		if entry.Event.Record.Start != end || entry.Event.Record.End <= end || entry.Event.Record.End > to || !entry.Durable || !w.reclaimEntryQualifiedLocked(entry) || entry.acknowledged == nil {
			break
		}
		events = append(events, cloneWALEvent(entry.Event))
		end = entry.Event.Record.End
	}
	if len(events) == 0 {
		return events, capture, nil
	}
	ok, err := w.capturedMembersAcknowledgedLocked(events, capture)
	if err != nil || !ok {
		return nil, nil, ErrWALWindowChanged
	}
	return events, capture, nil
}

// CapturedMembersAcknowledged只读核对原cut中的完整正文ACK；应在既有窗口租约内、登记Verified之前调用。
// 锁序仅WAL.mu，不取ledger锁、不查段/hold、不写ACK或水位；不会泄露任何内部切片或可写正文。
func (w *WAL) CapturedMembersAcknowledged(events []logtypes.Event, capture *DeliveryCapture) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.capturedMembersAcknowledgedLocked(events, capture)
}

func (w *WAL) capturedMembersAcknowledgedLocked(events []logtypes.Event, capture *DeliveryCapture) (bool, error) {
	if capture == nil || capture.owner != w || len(events) == 0 || len(events) > 500 {
		return false, ErrWALWindowChanged
	}
	expected := make(map[CommitIdentity]struct{}, len(events))
	wanted := make(map[string]struct{}, len(events))
	var from, to uint64
	for i, event := range events {
		id, err := CommitIdentityOf(event)
		if err != nil {
			return false, err
		}
		expected[id] = struct{}{}
		wanted[id.EventID] = struct{}{}
		if i == 0 || id.Record.Start < from {
			from = id.Record.Start
		}
		to = max(to, id.Record.End)
	}
	seen := make(map[CommitIdentity]struct{}, len(expected))
	for _, entry := range w.entries {
		_, sameID := wanted[entry.Event.EventID]
		if !sameID && (entry.Event.Record.End <= from || entry.Event.Record.Start >= to) {
			continue
		}
		id, err := CommitIdentityOf(entry.Event)
		if err != nil {
			return false, err
		}
		if _, ok := expected[id]; !ok || entry.appendRevision > capture.revision || !entry.Durable || entry.acknowledged == nil || id != *entry.acknowledged {
			return false, nil
		}
		seen[id] = struct{}{}
	}
	return len(seen) == len(expected), nil
}

// CaptureDelivery把不透明捕获边界与完整身份在同一WAL快照取得，不能由重试当前版号重造原边界。
func (w *WAL) CaptureDelivery(events []logtypes.Event) ([]logtypes.Event, []CommitIdentity, *DeliveryCapture, error) {
	capture := &DeliveryCapture{}
	body, ids, err := w.captureCommitted(events, capture)
	return body, ids, capture, err
}

// CaptureDeliverySubset在同一快照区分实际候选的活WAL与缺席历史，缺席正文仍须由调用者证明真源。
// 继承原cut时绝不以当前revision洗白捕获后的新变体；未提交的命中也不能冒充历史。
func (w *WAL) CaptureDeliverySubset(events []logtypes.Event, original *DeliveryCapture) ([]logtypes.Event, []logtypes.Event, []CommitIdentity, *DeliveryCapture, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	capture := &DeliveryCapture{owner: w, revision: w.revision}
	if original != nil {
		if original.owner != w {
			return nil, nil, nil, nil, ErrWALWindowChanged
		}
		*capture = *original
	}
	wanted := make(map[CommitIdentity]struct{}, len(events))
	wantedIDs := make(map[string]struct{}, len(events))
	for _, event := range events {
		id, err := CommitIdentityOf(event)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		wanted[id] = struct{}{}
		wantedIDs[id.EventID] = struct{}{}
	}
	found := make(map[CommitIdentity]logtypes.Event, len(wanted))
	for i := range w.entries {
		if _, sameID := wantedIDs[w.entries[i].Event.EventID]; !sameID {
			continue
		}
		if w.entries[i].appendRevision > capture.revision {
			return nil, nil, nil, nil, ErrWALWindowChanged
		}
		id, err := w.entries[i].eventIdentityLocked()
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if _, selected := wanted[id]; !selected {
			continue
		}
		if !w.entries[i].Durable || w.entries[i].appendRevision > capture.revision {
			return nil, nil, nil, nil, ErrWALWindowChanged
		}
		found[id] = w.entries[i].Event
	}
	var active, historical []logtypes.Event
	var ids []CommitIdentity
	for _, event := range events {
		id, err := CommitIdentityOf(event)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if body, ok := found[id]; ok {
			active = append(active, cloneWALEvent(body))
			ids = append(ids, id)
		} else {
			historical = append(historical, cloneWALEvent(event))
		}
	}
	return active, historical, ids, capture, nil
}

// MarkDeliveryMembers只在唯一投递owner真正准备物理写入时标记本批WAL成员，不影响新采集qualification。
func (w *WAL) MarkDeliveryMembers(events []logtypes.Event, capture *DeliveryCapture) (*DeliveryCapture, error) {
	if capture == nil || capture.owner != w {
		return nil, ErrWALWindowChanged
	}
	expected := make(map[CommitIdentity]struct{}, len(events))
	for _, event := range events {
		id, err := CommitIdentityOf(event)
		if err != nil {
			return nil, err
		}
		expected[id] = struct{}{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	found := make(map[CommitIdentity]struct{}, len(expected))
	matched := make(map[int]CommitIdentity)
	for i := range w.entries {
		// 本循环跑在**每批投递**路径上，且扫的是全表：用身份缓存后，只有本批新追加的
		// 那些条目会付出一次 Marshal+SHA256（之前每批都把全表重算一遍）。
		id, err := w.entries[i].eventIdentityLocked()
		if err != nil {
			return nil, err
		}
		if _, ok := expected[id]; ok && w.entries[i].Durable {
			if w.entries[i].appendRevision > capture.revision {
				return nil, ErrWALWindowChanged
			}
			found[id] = struct{}{}
			matched[i] = id
		}
	}
	if len(found) != len(expected) {
		return nil, ErrWALWindowChanged
	}
	w.nextCapture++
	marked := *capture
	marked.token = w.nextCapture
	marked.members = len(matched)
	for i, id := range matched {
		w.entries[i].captureToken = marked.token
		w.entries[i].captureBody = id.BodyDigest
	}
	return &marked, nil
}

// SnapshotDeliveryMembers只重取原实际写入成员；未选旧兄弟不计入count，新替换清mark后必使证明失败。
func (w *WAL) SnapshotDeliveryMembers(capture *DeliveryCapture, count int) ([]WALEntry, error) {
	if capture == nil || capture.owner != w || capture.token == 0 {
		return nil, ErrWALWindowChanged
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []WALEntry
	for i := range w.entries {
		if w.entries[i].captureToken != capture.token {
			continue
		}
		entry := w.entries[i]
		id, err := w.entries[i].eventIdentityLocked()
		if err != nil {
			return nil, err
		}
		if !entry.Durable || id.BodyDigest != entry.captureBody || len(out) >= count {
			return nil, ErrWALWindowChanged
		}
		entry.Event = cloneWALEvent(entry.Event)
		entry.reservation = nil
		out = append(out, entry)
	}
	if len(out) != count {
		return nil, ErrWALWindowChanged
	}
	return out, nil
}

func (w *WAL) ClearDeliveryMembers(capture *DeliveryCapture) {
	if capture == nil || capture.owner != w || capture.token == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.entries {
		if w.entries[i].captureToken == capture.token {
			w.entries[i].captureToken = 0
			w.entries[i].captureBody = [32]byte{}
		}
	}
}

// CaptureCommitted在同一WAL快照中逐条匹配完整身份，不按EventID或最大位置借用资格。
func (w *WAL) CaptureCommitted(events []logtypes.Event) ([]logtypes.Event, []CommitIdentity, error) {
	return w.captureCommitted(events, nil)
}

func (w *WAL) captureCommitted(events []logtypes.Event, cut *walCapture) ([]logtypes.Event, []CommitIdentity, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	required := make([]CommitIdentity, len(events))
	wanted := make(map[string]struct{}, len(events))
	for i, event := range events {
		id, err := CommitIdentityOf(event)
		if err != nil {
			return nil, nil, err
		}
		required[i] = id
		wanted[event.EventID] = struct{}{}
	}
	found := make(map[CommitIdentity]logtypes.Event, len(events))
	for i := range w.entries {
		if !w.entries[i].Durable {
			continue
		}
		if _, ok := wanted[w.entries[i].Event.EventID]; !ok {
			continue
		}
		id, err := w.entries[i].eventIdentityLocked()
		if err != nil {
			return nil, nil, err
		}
		found[id] = w.entries[i].Event
	}
	captured := make([]logtypes.Event, 0, len(events))
	for _, id := range required {
		stored, ok := found[id]
		if !ok {
			return nil, nil, fmt.Errorf("acquire: batch contains an event without a matching WAL commit: %s", id.EventID)
		}
		captured = append(captured, cloneWALEvent(stored))
	}
	if cut != nil {
		cut.owner = w
		cut.revision = w.revision
	}
	return captured, required, nil
}

// SnapshotPending有界复制正文并轮转选择；精确ACK身份独立于reclaim，hold旧头不阻塞新尾。
func (w *WAL) SnapshotPending(limit, cursor int, acknowledged map[CommitIdentity]struct{}) ([]WALEntry, int, map[CommitIdentity]struct{}, error) {
	return w.snapshotPending(limit, cursor, acknowledged, nil)
}

// 排空宿主在同一WAL锁内复核槽位版本，旧索引不能对收缩后len取模而跨过未选前缀。
// epoch不是提交或ACK凭据；资格、实际成员与已发布正文仍由原闸逐项核验。
func (w *WAL) snapshotPending(limit, cursor int, acknowledged map[CommitIdentity]struct{}, epoch *uint64) ([]WALEntry, int, map[CommitIdentity]struct{}, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if epoch != nil {
		if *epoch != w.pendingEpoch {
			cursor = 0
		}
		*epoch = w.pendingEpoch
	}
	live := make(map[CommitIdentity]struct{}, len(acknowledged))
	if len(w.entries) == 0 {
		return nil, 0, live, nil
	}
	cursor %= len(w.entries)
	out := make([]WALEntry, 0, min(limit, len(w.entries)))
	next := cursor
	selectedSuffix := false
	for i := 0; i < len(w.entries); i++ {
		index := (cursor + i) % len(w.entries)
		entry := w.entries[index]
		if !entry.Durable {
			continue
		}
		// 算完写回**原切片**：`entry` 是值拷贝，只在拷贝上置位等于每次重算（缓存会失效）。
		id, err := entry.eventIdentityLocked()
		if err != nil {
			return nil, cursor, nil, err
		}
		w.entries[index].identity, w.entries[index].identityOK = entry.identity, entry.identityOK
		if _, done := acknowledged[id]; done {
			live[id] = struct{}{}
			continue
		}
		if len(out) >= limit || index < cursor && selectedSuffix {
			continue
		}
		if index >= cursor {
			selectedSuffix = true
		}
		entry.reservation = nil
		entry.Event = cloneWALEvent(entry.Event)
		out = append(out, entry)
		next = (index + 1) % len(w.entries)
	}
	return out, next, live, nil
}

// CommittedIdentities 返回仍保留在 WAL 中且已 durable 的条目完整身份集合（用来有界裁剪本进程提交回执）。
//
// 2026-10-29：原有一个“报告实际完整编码次数”的双返回值变体，只服务于 WAL 行的 SQL 编码
// 回归；source_wal 表删除后它失去对象，统一为单值。
func (w *WAL) CommittedIdentities() (map[CommitIdentity]struct{}, error) {
	return w.committedIdentities(nil)
}

func (w *WAL) committedIdentities(copied func()) (map[CommitIdentity]struct{}, error) {
	w.mu.Lock()
	out := make(map[CommitIdentity]struct{}, len(w.entries))
	for i := range w.entries {
		if !w.entries[i].Durable {
			continue
		}
		// 直接用条目的身份缓存：这是本函数过去的最大开销（全表 json.Marshal + SHA256），
		// 而条目正文在两次 Append 之间不变 ⇒ 缓存命中率接近 100%。
		identity, err := w.entries[i].eventIdentityLocked()
		if err != nil {
			w.mu.Unlock()
			return nil, err
		}
		out[identity] = struct{}{}
	}
	w.mu.Unlock()
	if copied != nil {
		copied()
	}
	return out, nil
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
