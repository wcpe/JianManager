package acquire

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// defaultReplayEventsPerDrain 是「暂停/恢复期排空存量」单轮的默认事件数上限。
//
// 取值 500 的取舍：与投递侧的单次 VL 插入批量上限（ingest 的 insertBatchMaxEvents=500）
// 对齐——一轮排空正好一批 HTTP 请求，恢复期不会一次攒出多个批次；而重建 5000 条积压
// 只需 10 轮，采集轮本身是秒级频率，恢复时长仍在秒级，不会因为限速而变慢到不可接受。
// DefaultReplayEventsPerDrain 是"暂停/恢复期单轮外发事件数上限"的**真源**（导出给 ingest 侧引用，
// 避免裸写字面量 ✗）。既有小写名保留为别名 ⇒ 既有调用点逐字不变 ✓。
const DefaultReplayEventsPerDrain = 500

const defaultReplayEventsPerDrain = DefaultReplayEventsPerDrain

// Pipeline 将 FileTailer / ArchiveImporter / STDIO 统一写入事件管道。
// source=worker 使用同一管道；采集失败不得递归写回自身 VL 失败日志。
type Pipeline struct {
	led *ledger.Ledger
	key ledger.SourceKey
	wal *WAL
	// deliver 模拟 VL JSON stream 请求级投递。
	// 返回 httpStatus 与 ackLost。
	deliver func(events []logtypes.Event, replay bool) (httpStatus int, ackLost bool, err error)
	// durablePersist 将已提交的 WAL/账本持久化；失败时禁止对下游发请求。
	durablePersist func() error
	// suppressRecursiveVL source=worker 时抑制采集失败写回 VL。
	suppressRecursiveVL bool
	// workerSelfFailures 本地计数（可观测，不进入 VL）。
	workerSelfFailures int
	// selfFailureReasons 是最近自愈失败原因的**有界环形缓冲**（见 noteSelfFailure ✓）。
	selfFailureReasons []string
	// budget 容量预算。
	budget         CapacityBudget
	budgetProvider func() (CapacityBudget, error)
	// walAppendedBytesTotal 是本进程累计追加的估算字节（**仅观测**）。
	//
	// 严禁用它做门禁：它只增不减，而 reclaim 推进后积压会回落到 0。拿累计量去比
	// MaxWALBytes，等于在「本进程累计追加量越过上限」那一刻永久暂停该源——而容量 PAUSE
	// 不由积压滞回清除（见 wal.go maybeResumeBacklogLocked 的原因前缀过滤），现场形态是
	// 该源此后一个字节都不再采集。真实门禁一律读 WAL.Backlog()（见 Ingest）。
	walAppendedBytesTotal uint64
	// maxReplayEventsPerDrain 是「暂停/恢复期排空存量」单轮最多外发的事件数（0 表示用默认）。
	// 见 deliverPendingBestEffort 的限速说明。
	maxReplayEventsPerDrain int
	// appendRejectedHook 是「追加被拒（暂停/上限）时该做什么」的接线点（#56）。
	//
	// 非 nil ⇒ 把「排空已 durable 存量」交给**调度者**在摄取路径之外执行；nil ⇒ 退回本包原有的
	// **同步**尽力投递（独立使用本包与既有回归的语义逐字不变，见 pipeline_drain_test.go 的
	// TestPausedIngestStillDrainsDurablePending）。
	appendRejectedHook func()
	// replayGate 是「冗余整窗重投」抑制闸（#60-Z1，见 replayGateState 的说明）。
	// replayGuard 是**叶子锁**：只保护闸内状态的读改写（绝不在持它时调用投递 ✗）。
	replayGuard sync.Mutex
	replayGate  replayGateState
	// replaySuppressBaseOverride 是退避首个间隔的测试/配置覆盖（0 ⇒ 用常量默认值）。
	replaySuppressBaseOverride time.Duration
	// drainDiagKey 是上一次「排空取批诊断」打过的读数指纹（ACQ-4：读数不变不重复打）。
	// 持 replayGuard 访问（与排空闸同锁 ⇒ 同一源的排空路径串行 ✓）。
	drainDiagKey string
}

// replaySuppressBase / replaySuppressMax 是「冗余整窗重投」抑制的退避区间（#60-Z1，父级裁定）；
// **2026-10-06 收窄**：排空已改增量语义（replay=false，单次代价 ≤ 本批上限）✗ ⇒ 原来按"159s/次整窗"
// 设的 60s 封顶已失去依据 ✗，且在"delivery==reclaim<read"的停滞形态下会**越等越不动** ✗。
// 现封顶降到 2s：冗余排空的**安全阀仍在**（同水位连续触发照样被抑制 ✓，见用例），
// 但停滞形态最多等 2s 即可再试 ⇒ 不再自锁 ✓。
const (
	replaySuppressBase = 500 * time.Millisecond
	replaySuppressMax  = 2 * time.Second
)

// 历史记录（#60-Z1 现场，opt2 采）：门队列浅（1–9 帧）⇒ 不是排队问题；单次持久化 maxMs=159,934ms ✗✗
// ⇒ 慢在「单次大 payload 提交」；投递链上相邻样本 79.7% 是新代次（+1）⇒ 几乎每次写入都在整窗 replace；
// ≥1M 批占 4.8%、峰值 126 条/h。环：排空每次只发 ≤500 条，却以 replay=true 调用投递 ⇒ ingest 侧
// replace=true ⇒ **重建权威全量 + 发新代次**（现场单次 159s）⇒ 退避闸的由来。**2026-10-06 起排空已改
// 增量语义（replay=false）⇒ 该历史形态的成本已消失，封顶收窄到 2s（见上）✓。**

// replayGateState 是"冗余整窗重投"抑制闸的状态（零值 ⇒ 从未尝试）。
//
// 水位代理 = **该源账本修订号**（`Ledger.Revision` 是 O(1)，且 `touch` 挂在所有写路径的入口 `require()`
// 内 ⇒ 任何账本写都会变）。判据 = "自上次排空尝试之后水位未推进"（含投递结果登记 / 回收推进 / 新追加）：
// 未推进 ⇒ 本次整窗重投不会带来新信息 ⇒ 退避；推进 ⇒ 立即放行 ✓（合法 replay 不被饿死 ✓）。
type replayGateState struct {
	delivery     uint64    // 上次尝试**之后**的投递水位（下一次比较只反映外部变化，不含自身写入 ✓）
	reclaim      uint64    // 上次尝试**之后**的回收水位
	hasWatermark bool      // 是否已取过水位（false ⇒ 首次，直接放行）
	attemptedAt  time.Time // 上次尝试时刻（零值 ⇒ 从未尝试）
	streak       int       // 同一水位下连续尝试次数（退避级数 2^k）
	suppressed   int64     // 被抑制的冗余次数（观测读数）
	lastLogAt    time.Time // 抑制日志节流
}

// replayBackoffFor 返回同一水位下第 streak 次尝试之后的等待间隔（2^k × base，上限 cap）。
func (p *Pipeline) replayBackoffFor(streak int) time.Duration {
	base := replaySuppressBase
	if p.replaySuppressBaseOverride > 0 {
		base = p.replaySuppressBaseOverride
	}
	cap0 := replaySuppressMax
	if base > cap0 {
		cap0 = base
	}
	d := base
	for i := 1; i < streak && d < cap0; i++ {
		d *= 2
	}
	if d > cap0 {
		d = cap0
	}
	return d
}

// replayDrainDecision 判定本次排空是否**冗余**（#60-Z1），并回报「自上次尝试以来水位是否推进」。
//
// 水位 = 该源账本的 (**投递**, **回收**) 两个位置 —— 用 `Ledger.Positions`（O(1) 只读）取，
// 不能用 `Revision`（`TryReclaim` 无条件推进它 ⇒ 判据恒为真 ✗，见 ledger.Positions 的说明）。
func (p *Pipeline) replayDrainDecision(now time.Time) (suppress bool, progressed bool) {
	pos, ok := p.led.Positions(p.key)
	p.replayGuard.Lock()
	defer p.replayGuard.Unlock()
	if !ok {
		return false, false // 账本源不存在：不抑制（交给既有错误面）
	}
	if !p.replayGate.hasWatermark {
		return false, false // 首次：放行（但**不宣称**"水位前进" —— 见 noteReplayDrain 的四场景）
	}
	progressed = pos.Delivery != p.replayGate.delivery || pos.Reclaim != p.replayGate.reclaim
	if progressed {
		return false, true // 水位真前进 ⇒ 立即放行 ✓（合法 replay 不被饿死）
	}
	if p.replayGate.streak <= 0 {
		return false, false // 上次是失败（或刚水位前进）⇒ 无退避级数 ⇒ 放行（见 noteReplayDrain）
	}
	return now.Sub(p.replayGate.attemptedAt) < p.replayBackoffFor(p.replayGate.streak), false
}

// noteReplayDrain 记录一次排空尝试并更新退避级数（#60-Z1）：
//
//   - **水位前进** ⇒ 级数归 0（下次立即放行 —— 合法 replay 不被饿死 ✓）；
//   - **失败** ⇒ 级数归 0（同上）。这条是**证据驱动的收窄**：把失败也计入退避后，端到端自愈用例
//     TestPausedSourceSelfHealsEndToEnd 转红 ✗✗（首次投递失败 ⇒ 5s 内重试被抑制 ⇒ 缺口消解链被饿死）
//     ⇒ 父级裁定"判据是水位未前进"在此按"**不饿死合法 replay**"的更高不变量收窄为
//     "**成功但水位未推进**才算冗余" ✓；
//   - **成功但水位未推进** ⇒ 级数 +1（这才是冗余整窗：现场 159s/次、代次 +1 的重复项 ✓）。
func (p *Pipeline) noteReplayDrain(progressed bool, failed bool) {
	p.replayGuard.Lock()
	defer p.replayGuard.Unlock()
	switch {
	case failed:
		// **失败必须立即可重试**（不饿死自愈链）：实测把失败计入退避后 TestPausedSourceSelfHealsEndToEnd 转红。
		p.replayGate.streak = 0
	case progressed:
		// 水位真前进 ⇒ 新水位上的这次 replay 是合法的 ⇒ 下次立即放行 ✓。
		p.replayGate.streak = 0
	case !p.replayGate.hasWatermark:
		// 首次尝试只建立基准（"水位有没有动"无从比较）⇒ 紧接的**同水位重复**即被抑制
		//（父级红证口径：同一水位下 5 次触发只允许 1 次整窗 ✓）。
		p.replayGate.streak = 1
	default:
		p.replayGate.streak++ // 成功但零推进 ⇒ 冗余整窗 ⇒ 退避加深（2^k × base，上限 60s）
	}
	if pos, ok := p.led.Positions(p.key); ok { // 基准取本次**之后**（含其自身写入）⇒ 下次比较只反映外部变化
		p.replayGate.delivery, p.replayGate.reclaim = pos.Delivery, pos.Reclaim
		p.replayGate.hasWatermark = true
	}
	p.replayGate.attemptedAt = time.Now()
}

// noteSuppressedReplayDrain 记账并（按节流）打一行 INFO —— 每个退避窗最多一条，避免刷屏。
func (p *Pipeline) noteSuppressedReplayDrain(now time.Time) {
	p.replayGuard.Lock()
	p.replayGate.suppressed++
	total := p.replayGate.suppressed
	streak := p.replayGate.streak
	quiet := now.Sub(p.replayGate.lastLogAt) < replaySuppressMax
	if !quiet {
		p.replayGate.lastLogAt = now
	}
	p.replayGuard.Unlock()
	if quiet {
		return
	}
	slog.Info("冗余整窗重投已抑制（水位未推进 ⇒ 退避后再试）",
		"logSourceID", p.key.LogSourceID, "generation", p.key.SourceGeneration,
		"streak", streak, "backoff", p.replayBackoffFor(streak).String(), "suppressedTotal", total)
}

// SetReplaySuppressBase 覆盖"冗余整窗重投"抑制的退避首间隔（供配置接线与测试使用；0 ⇒ 常量默认）。
func (p *Pipeline) SetReplaySuppressBase(d time.Duration) { p.replaySuppressBaseOverride = d }

// SuppressedReplayDrains 返回被抑制的"冗余整窗重投"累计次数（#60-Z1 观测读数）。
func (p *Pipeline) SuppressedReplayDrains() int64 {
	if p == nil {
		return 0
	}
	p.replayGuard.Lock()
	defer p.replayGuard.Unlock()
	return p.replayGate.suppressed
}

// NewPipeline 创建管道。deliver 为 nil 时不投递（仅 WAL）。
func NewPipeline(led *ledger.Ledger, key ledger.SourceKey, wal *WAL) *Pipeline {
	return &Pipeline{
		led:    led,
		key:    key,
		wal:    wal,
		budget: DefaultCapacityBudget(),
	}
}

// SetDeliver 注入投递函数（HTTP 2xx / ACK 丢失模拟）。
func (p *Pipeline) SetDeliver(fn func(events []logtypes.Event, replay bool) (int, bool, error)) {
	p.deliver = fn
}

// SetDurablePersist 将磁盘提交插入 WAL.Commit 与投递之间。
func (p *Pipeline) SetDurablePersist(fn func() error) { p.durablePersist = fn }

// SetCapacityBudget 设置资源预算。
func (p *Pipeline) SetCapacityBudget(b CapacityBudget) { p.budget = b }

// SetMaxReplayEventsPerDrain 覆盖「单轮排空存量」的事件数上限（0 表示沿用默认）。
// 供配置接线与测试使用。
func (p *Pipeline) SetMaxReplayEventsPerDrain(n int) { p.maxReplayEventsPerDrain = n }

// SetAppendRejectedHook 注入「追加被拒」处理（#56，见 appendRejectedHook 的说明）。
// 生产由 ingest 层接成"调度一次路径外排空"；未接线时保持本包原有的同步语义。
func (p *Pipeline) SetAppendRejectedHook(fn func()) { p.appendRejectedHook = fn }

// handleAppendRejected 处理一次「追加被拒」（暂停/上限）：把「排空已 durable 存量」交给调度者，
// **不在摄取路径内当场执行**。
//
// 为什么必须调度（现场 opt2 三栈定名）：此处原先同步调用 deliverPendingBestEffort ⇒ 整窗 replay
// （replay=true，百万级批）把 emit→ingest 整条链按住 ⇒ 采集轮读不再前进、goroutine 越堆越多
// （挂死期 183→242 活锁堆积），而 m.mu 全程无慢持锁记录 ⇒ 典型的**在途阻塞**（in-path blocking）。
// 语义不变量：存量仍会被投递（调度者及时触发一次），只是不再阻塞摄取路径；未接调度者时
// （本包独立使用/回归）行为与 #56 之前逐字一致 ⇒ 不静默改变既有契约。
func (p *Pipeline) handleAppendRejected() {
	if p.appendRejectedHook != nil {
		p.appendRejectedHook()
		return
	}
	_, _ = p.deliverPendingBestEffort()
}

// WALAppendedBytesTotal 返回本进程累计追加的估算字节（观测口径，不作门禁）。
func (p *Pipeline) WALAppendedBytesTotal() uint64 { return p.walAppendedBytesTotal }

func (p *Pipeline) SetCapacityProvider(fn func() (CapacityBudget, error)) { p.budgetProvider = fn }

// SetSourceWorker 标记 source=worker：同一管道，失败不递归写回 VL。
func (p *Pipeline) SetSourceWorker(v bool) { p.suppressRecursiveVL = v }

// WorkerSelfFailures 返回 worker 自源失败计数（本地，未入 VL）。
func (p *Pipeline) WorkerSelfFailures() int { return p.workerSelfFailures }

// Ingest 完整事件：容量门禁 → WAL append → fsync commit → delivery。
// IngestReplay 与 Ingest 同语义，但把写入标记为**恢复期回放/补账**（见 WAL.AppendReplay 的注释 ✓）。
//
// 接缝意义（2026-10-04 用户指令）：所有 WAL 追加都经本包的 Ingest 入口 ✓ ⇒ 把"回放/补账"与
// "常规采集"分成两个显式入口，调用方各取所需 ✓；**常规采集必须继续用 Ingest** ✗（否则等于给
// 洪流开后门 ✗ —— 闸的绝对上界就废了 ✓）。
func (p *Pipeline) IngestReplay(events []logtypes.Event) error {
	if p == nil || p.wal == nil {
		return fmt.Errorf("acquire: pipeline not initialized")
	}
	if _, err := p.wal.TryReclaim(); err != nil {
		p.noteSelfFailure(err.Error())
	}
	if err := p.wal.AppendReplay(events...); err != nil {
		p.handleAppendRejected() // #56：改为调度（不在摄取路径内同步整窗重投）
		return err
	}
	return nil
}

func (p *Pipeline) Ingest(events []logtypes.Event) error {
	if len(events) == 0 {
		return nil
	}
	if p.budgetProvider != nil {
		budget, err := p.budgetProvider()
		if err != nil {
			p.noteSelfFailure(err.Error())
			return fmt.Errorf("acquire: sample capacity: %w", err)
		}
		p.budget = budget
	}
	ent := p.led.Get(p.key)
	gapCount := 0
	if ent != nil {
		for _, gap := range ent.Gaps {
			if !gap.Resolved {
				gapCount++
			}
		}
	}
	// 门禁读**当前积压**（WAL 真实水位），绝不读累计追加量：
	// 累计量只增不减，用它判定会在越过上限那一刻把源永久钉在暂停上，而真实积压可能
	// 早已被 reclaim 清空。积压口径还有个必要性质——它随回收回落，暂停才有自愈的前提
	// （字节口径高水位本身由 WAL 自身的积压上限负责，那条路径带滞回，见 wal.go）。
	_, backlogBytes := p.wal.Backlog()
	backlog := uint64(0)
	if backlogBytes > 0 {
		backlog = uint64(backlogBytes)
	}
	dec := EvaluateCapacity(p.budget, backlog, gapCount)
	var gapStart, gapEnd uint64
	if len(events) > 0 {
		gapStart = events[0].Record.Start
		gapEnd = events[len(events)-1].Record.End
		// FileTailer 的规范事件范围不包含行分隔符；当新批次紧接
		// delivery 前缀且只隔一个分隔字节时，把该已读分隔符纳入
		// delivery span，避免合法连续事件被误判为空洞。真正超过一个
		// 字节的间隔仍保持 UNKNOWN/缺口语义，不得越过恢复责任。
		if ent != nil && gapStart > ent.Positions.Delivery && gapStart-ent.Positions.Delivery <= 1 {
			gapStart = ent.Positions.Delivery
		}
	}
	if err := ApplyCapacity(p.led, p.key, dec, gapStart, gapEnd); err != nil {
		return err
	}
	if dec.Action == CapacityPaused {
		// 暂停：必须已有 gap；不静默丢。
		p.noteSelfFailure(dec.Reason)
		return fmt.Errorf("acquire: capacity paused: %s", dec.Reason)
	}

	// 采集循环每轮都会经过此处：追加前先尝试回收，使「积压回落 → 低水位放行 → 自动恢复」
	// 这条链路对**已暂停**的源同样成立。不能放进 WAL.Append：那里持锁，而 TryReclaim
	// 要拿同一把锁（不可重入）。
	//
	// 2026-09-30 生产实证：缺了本调用，已投递条目的回收永不推进 → 积压停在 5999 一动不动 →
	// 滞回阈值（上限一半 = 2500）永远打不开 → 源可在 paused 上一停数小时且毫无自愈迹象。
	if _, err := p.wal.TryReclaim(); err != nil {
		p.noteSelfFailure(err.Error())
	}
	if err := p.wal.Append(events...); err != nil {
		// append 失败（含已暂停）：补 gap，禁止静默。
		// 被暂停（含积压上限）时，仍把 WAL 里**已 durable、未确认投递**的条目发出去：
		// 投递不能挂在摄取上，否则暂停即断流 → 已读未投的积压永不外发 →
		// 回收位置不动 → 段无从覆盖 → 滞回永不满足（2026-09-30 生产实证：残留 7998 条卡死数小时）。
		//
		// #56：这件事**不再在本路径内同步执行** —— 改为向调度者登记一次"该源需排空"并立即返回
		// （见 handleAppendRejected 的现场证据：同步整窗 replay 把 emit→ingest 按住 ⇒ 采集轮停摆、
		// goroutine 堆积在途）。存量仍会被投递（及时触发、单飞且合并同源重复请求），故上面那条
		// "不能挂在摄取上"的语义**保留**：只是把"挂在"从**同步执行**换成**调度执行**。
		p.handleAppendRejected()
		_ = p.led.RecordGap(p.key, gapStart, gapEnd, ledger.GapReasonAppendRejected, err.Error())
		p.noteSelfFailure(err.Error())
		return err
	}
	for _, ev := range events {
		p.walAppendedBytesTotal += uint64(len(ev.Message) + len(ev.EventID) + 64)
	}

	// durable：fsync/equivalent commit。
	if err := p.wal.Commit(); err != nil {
		_ = p.led.RecordGap(p.key, gapStart, gapEnd, ledger.GapReasonWALCommitFailed, err.Error())
		p.noteSelfFailure(err.Error())
		return err
	}
	if p.durablePersist != nil {
		if err := p.durablePersist(); err != nil {
			p.noteSelfFailure(err.Error())
			return fmt.Errorf("acquire: persist durable WAL: %w", err)
		}
	}

	if p.deliver == nil {
		return nil
	}
	status, ackLost, err := p.deliver(events, false)
	if err != nil && p.suppressRecursiveVL {
		// worker 自源：失败只计本地，不递归写 VL。
		p.noteSelfFailure(err.Error())
		_ = p.led.RecordGap(p.key, gapStart, gapEnd, ledger.GapReasonDeliverErrorWorkerSource, err.Error())
		return nil
	}
	if err != nil {
		_ = p.led.RecordGap(p.key, gapStart, gapEnd, ledger.GapReasonDeliverError, err.Error())
		return err
	}
	// HTTP 2xx → REQUEST_DONE only；AckLost → UNKNOWN（恢复责任保留）。
	res := p.wal.RecordHTTPResult(DeliveryResult{
		StartPos:   gapStart,
		EndPos:     gapEnd,
		HTTPStatus: status,
		AckLost:    ackLost,
	})
	if res != nil {
		return res
	}
	// 投递结果落账后**顺手推进回收**：这是正常投递路径里应有的回收点。
	//
	// 缺了它会怎样（2026-09-30 生产实证）：积压计数＝已投递但未回收的条目，
	// 只在「恢复分段完成」流程里才被推进 → 数字只增不减 → WAL 滞回
	// （须回落到上限一半以下才自动恢复）永远打不开 → 源永久停在 paused，
	// 表现为「一个字节都不动」的死滞。TryReclaim 自带账本 CanReclaim 门禁，
	// 不会提前回收未投递数据；失败只记录、不返回，避免把「回收没成功」
	// 误报成「投递失败」。
	if _, err := p.wal.TryReclaim(); err != nil {
		p.noteSelfFailure(err.Error())
	}
	return nil
}

// EvaluateResume 暴露「推进回收 + 按滞回评估恢复」的只读入口（见 WAL.EvaluateResume）。
//
// 为什么必须由外部低频触发（2026-10-03 现场）：恢复的唯一检查点原本挂在**采集轮**上
// （Append 前 TryReclaim / 投递后 TryReclaim），而采集轮在暂停期间只做"只投递不读取"的自愈——
// 一旦投递也停下（或上游全堵住），就再没有任何人触发回收 ⇒ 「积压回落 → 低水位放行 → 自动恢复」
// 这条链彻底停摆 ✗（现场：13 源/10 源成批停在 paused 不回落）。本入口让容量兜底循环
// **不依赖采集轮**地重试一次。
func (p *Pipeline) EvaluateResume() bool {
	if p == nil || p.wal == nil {
		return false
	}
	return p.wal.EvaluateResume()
}

// DeliverPending 是 deliverPendingBestEffort 的导出版：对**已暂停**的源执行一次自愈尝试。
//
// 为什么必须由外部触发（缺陷 A）：采集暂停期间 FileTailer 直接拒绝读取（见 tailer.go 的
// AcquirePaused 判定），于是「读 → 投递 → 回收 → 恢复评估」这条链整条停摆——存量永远发不出去、
// 积压永远不会回落、缺口也永远不会因为一次成功重投而被消解。生产实证：源停在 paused 上
// 持续 13+ 小时零新数据。本方法让采集轮可以**只投递、不读取**地推动存量外发。
//
// 返回本次真正外发的事件集合（nil 表示无可投递条目或投递失败）与错误。
// 返回事件集合而不是布尔值：投递成功是「已确认落库」的证据，上层要用它的区间消解缺口、
// 并据此登记恢复责任证明（与正常投递路径同构）。
func (p *Pipeline) DeliverPending() ([]logtypes.Event, error) {
	if p == nil || p.deliver == nil {
		return nil, nil
	}
	pending, err := p.deliverPendingBestEffort()
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}
	// 投递后顺手推进回收：回收点同样只在采集轮里出现，而暂停时采集轮不再产生新批次。
	if _, reclaimErr := p.wal.TryReclaim(); reclaimErr != nil {
		p.noteSelfFailure(reclaimErr.Error())
	}
	return pending, nil
}

// noteSelfFailure 本地可观测失败；source=worker 禁止递归 VL。
// deliverPendingBestEffort 尽力投递「已 durable 但尚未确认」的条目。
//
// 只在摄取被拒（暂停）时调用：此时没有新批次可投，但 WAL 里的存量仍需外发，
// 否则积压只增不减、回收滞回永远打不开。语义上等价于「暂停只停摄取，不停排空」。
// 只读 WAL 快照、只发已落盘条目；重复投递由 WAL 位置账本自防（RecordHTTPResult 幂等推进）。
//
// **限速（本轮补）**：单轮最多外发 maxReplayEventsPerDrain 条（默认 defaultReplayEventsPerDrain）。
// 为什么必须限速：VL 变慢/刚恢复时存量可能有数千条，一次全塞进去会把恢复瞬间变成一次
// 自我制造的流量尖峰——VL 刚缓过来就被打回慢状态，形成「恢复 → 打爆 → 再暂停」的振荡。
// 限速的口径是「每采集轮一批」：采集轮持续在跑，故这只是把一次尖峰摊成若干轮，
// **不丢数据**（未发的条目留在 WAL、位置账本不变，下一轮接着发）。
func (p *Pipeline) deliverPendingBestEffort() ([]logtypes.Event, error) {
	if p.deliver == nil {
		return nil, nil
	}
	entries := p.wal.Snapshot()
	cap0 := p.replayDrainLimit()
	pending := make([]logtypes.Event, 0, len(entries))
	for _, e := range entries {
		if !e.Durable {
			continue
		}
		pending = append(pending, e.Event)
		if len(pending) >= cap0 {
			break // 限速：余下条目下一轮再发，账本位置不动，不丢。
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}
	// #60-Z1：冗余整窗重投抑制（见 replayGateState 的说明）——排空是投递面**唯一**走 replay=true 的入口，
	// 而 replay=true 在 ingest 侧 = 整窗 replace（重建权威全量 + 发新代次）⇒ 滞留期重复触发 = 反复重投同一段。
	// 失败判定与投递结果同批记账（见 noteReplayDrain：失败 ⇒ 不退避 ⇒ 立即可重试）。
	// 声明必须在 defer 之前（闭包捕获的是变量本身 ✓）。
	outcomeFailed := false
	now := time.Now()
	suppress, progressed := p.replayDrainDecision(now)
	// #7 复盘定点（**只观测、零判据改动** ✗）：滞留期排空"碎批 2–840"的根因三判一 ——
	//   抑制跳轮 ✗ ／ 快照碎批（snapshot/durable 大而 pending 小）✗ ／ 读数口径错（碎批其实来自常规轮）✓。
	//
	// **日志纪律（ACQ-4）**：本函数是暂停源自愈排空的主路径，维护环每轮每源至少调一次；
	// 长期停在暂停上的源会持续触发。原实现无脑打一条 **Info** ⇒ 长期刷屏（本仓库对日志面
	// 成本有纪律，见 noteSelfFailure 的有界环形缓冲）。现改为：① 降为 **Debug**；② 再加
	// **读数变化才打**（同一 (pending, snapshot, durable, cap, suppress, progressed) 组合不重复产出），
	// 于是"一次跑即可判"的取证能力完整保留，而静止形态下不再有逐轮日志。
	{
		durableN := 0
		for _, e := range entries {
			if e.Durable {
				durableN++
			}
		}
		p.logDrainDiag(len(pending), len(entries), durableN, cap0, suppress, progressed)
	}
	if suppress {
		p.noteSuppressedReplayDrain(now)
		return nil, nil
	}
	defer func() { p.noteReplayDrain(progressed, outcomeFailed) }()
	// **2026-10-06 快验回归修复**：排空原以 `replay=true` 投递 ⇒ ingest 侧 `replace=true`
	// ⇒ **每次排空都重建该源权威全量并重发**（60 源现场：单源 175 万事件被每轮物化一次，
	// 兜底环单轮 61 分钟、其余源全部饿死 ✗✗）。常规排空**不需要**该语义：补齐已确认数据
	// 走 append 幂等即可（幂等由位置账本＋消费端 event_id 去重保证 ✓）；真正需要"重建权威"
	// 的修复场景由 ingest 侧自身的 `PublicationPending`／代次耗尽判据触发 ✓。故传 `false`。
	status, ackLost, err := p.deliver(pending, false)
	if err != nil {
		// 失败不改状态、不记 gap：源本就处于暂停，重试留给下一轮（失败不进入退避 ⇒ 节奏不变 ✓）。
		outcomeFailed = true
		p.noteSelfFailure(err.Error())
		return nil, err
	}
	if res := p.wal.RecordHTTPResult(DeliveryResult{
		StartPos:   pending[0].Record.Start,
		EndPos:     pending[len(pending)-1].Record.End,
		HTTPStatus: status,
		AckLost:    ackLost,
	}); res != nil {
		outcomeFailed = true // 结果登记失败同样属"未推进"：不得进退避
		p.noteSelfFailure(res.Error())
		return nil, res
	}
	return pending, nil
}

// logDrainDiag 打「排空取批诊断」一行（#7 复盘定点取证，见 deliverPendingBestEffort ✓）。
//
// 两道约束（ACQ-4）：
//   - **Debug 级**：默认 Info 门下零日志面成本；
//   - **读数是变化才打**：同一指纹不重复产出 ⇒ 长期停滞的源不再逐轮刷日志，
//     而"三判一"所需的 pending/snapshot/durable/cap/suppress/progressed 一并俱全，
//     每次读数变化仍有一条完整取证。
//
// 指纹与 lastLogKey 都持 replayGuard（与排空闸同锁 ⇒ 同一源串行 ✓）。
func (p *Pipeline) logDrainDiag(pendingN, snapshotN, durableN int, cap0 int, suppress, progressed bool) {
	key := fmt.Sprintf("%d/%d/%d/%d/%t/%t", pendingN, snapshotN, durableN, cap0, suppress, progressed)
	p.replayGuard.Lock()
	changed := p.drainDiagKey != key
	p.drainDiagKey = key
	p.replayGuard.Unlock()
	if !changed {
		return
	}
	slog.Debug("排空取批诊断", "pending", pendingN, "snapshot", snapshotN, "durable", durableN,
		"cap", cap0, "suppress", suppress, "progressed", progressed)
}

func (p *Pipeline) noteSelfFailure(reason string) {
	p.workerSelfFailures++
	// **原因必须可读**（2026-10-05 ✓）：此前是 `_ = reason` ⇒ 自愈失败只留一个计数、原因永远查不到 ✗
	// —— 与"实现存在但不可达"同族，这次是"**值存在但被丢弃**" ✓。收进**有界环形缓冲**（沿用
	// persist 采样环型 ✓：只留最近 selfFailureReasonCap 条 ⇒ 不随失败次数增长 ✓）。
	if reason == "" {
		return
	}
	if len(p.selfFailureReasons) >= selfFailureReasonCap {
		copy(p.selfFailureReasons, p.selfFailureReasons[1:])
		p.selfFailureReasons = p.selfFailureReasons[:len(p.selfFailureReasons)-1]
	}
	p.selfFailureReasons = append(p.selfFailureReasons, reason)
}

// selfFailureReasonCap 是"最近自愈失败原因"的保留条数（有界 ✓，防无界增长 ✗）。
const selfFailureReasonCap = 8

// SelfFailureReasons 返回**最近若干条**自愈失败原因（新→旧 ✓，有界 ✓）。
//
// 为什么导出（用户 2026-10-05 ✓）：现场只能看到一个 `workerSelfFailures` 计数 ✗ ⇒ 无法回答
// "最近为什么自愈失败" ✓。与本会话主线一致：**把猜测变读数** ✓。
func (p *Pipeline) SelfFailureReasons() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.selfFailureReasons))
	for i := len(p.selfFailureReasons) - 1; i >= 0; i-- {
		out = append(out, p.selfFailureReasons[i])
	}
	return out
}

// replayDrainLimit 返回生效的单轮排空上限。
func (p *Pipeline) replayDrainLimit() int {
	if p.maxReplayEventsPerDrain > 0 {
		return p.maxReplayEventsPerDrain
	}
	return defaultReplayEventsPerDrain
}

// Ledger 返回账本。
func (p *Pipeline) Ledger() *ledger.Ledger { return p.led }

// WAL 返回 WAL。
func (p *Pipeline) WAL() *WAL { return p.wal }
