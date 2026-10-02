package acquire

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// defaultReplayEventsPerDrain 是「暂停/恢复期排空存量」单轮的默认事件数上限。
//
// 取值 500 的取舍：与投递侧的单次 VL 插入批量上限（ingest 的 insertBatchMaxEvents=500）
// 对齐——一轮排空正好一批 HTTP 请求，恢复期不会一次攒出多个批次；而重建 5000 条积压
// 只需 10 轮，采集轮本身是秒级频率，恢复时长仍在秒级，不会因为限速而变慢到不可接受。
const defaultReplayEventsPerDrain = 500

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

// WALAppendedBytesTotal 返回本进程累计追加的估算字节（观测口径，不作门禁）。
func (p *Pipeline) WALAppendedBytesTotal() uint64 { return p.walAppendedBytesTotal }

func (p *Pipeline) SetCapacityProvider(fn func() (CapacityBudget, error)) { p.budgetProvider = fn }

// SetSourceWorker 标记 source=worker：同一管道，失败不递归写回 VL。
func (p *Pipeline) SetSourceWorker(v bool) { p.suppressRecursiveVL = v }

// WorkerSelfFailures 返回 worker 自源失败计数（本地，未入 VL）。
func (p *Pipeline) WorkerSelfFailures() int { return p.workerSelfFailures }

// Ingest 完整事件：容量门禁 → WAL append → fsync commit → delivery。
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
		_, _ = p.deliverPendingBestEffort()
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
	status, ackLost, err := p.deliver(pending, true)
	if err != nil {
		// 失败不改状态、不记 gap：源本就处于暂停，重试留给下一轮。
		p.noteSelfFailure(err.Error())
		return nil, err
	}
	if res := p.wal.RecordHTTPResult(DeliveryResult{
		StartPos:   pending[0].Record.Start,
		EndPos:     pending[len(pending)-1].Record.End,
		HTTPStatus: status,
		AckLost:    ackLost,
	}); res != nil {
		p.noteSelfFailure(res.Error())
		return nil, res
	}
	return pending, nil
}
func (p *Pipeline) noteSelfFailure(reason string) {
	p.workerSelfFailures++
	_ = reason
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
