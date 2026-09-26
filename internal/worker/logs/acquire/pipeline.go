package acquire

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// Pipeline 将 FileTailer / ArchiveImporter / STDIO 统一写入事件管道。
// source=worker 使用同一管道；采集失败不得递归写回自身 VL 失败日志。
type Pipeline struct {
	led *ledger.Ledger
	key ledger.SourceKey
	wal *WAL
	// deliver 模拟 VL JSON stream 请求级投递。
	// 返回 httpStatus 与 ackLost。
	deliver func(events []logtypes.Event) (httpStatus int, ackLost bool, err error)
	// durablePersist 将已提交的 WAL/账本持久化；失败时禁止对下游发请求。
	durablePersist func() error
	// suppressRecursiveVL source=worker 时抑制采集失败写回 VL。
	suppressRecursiveVL bool
	// workerSelfFailures 本地计数（可观测，不进入 VL）。
	workerSelfFailures int
	// budget 容量预算。
	budget         CapacityBudget
	budgetProvider func() (CapacityBudget, error)
	// walBytes 当前 WAL 估算字节。
	walBytes uint64
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
func (p *Pipeline) SetDeliver(fn func(events []logtypes.Event) (int, bool, error)) {
	p.deliver = fn
}

// SetDurablePersist 将磁盘提交插入 WAL.Commit 与投递之间。
func (p *Pipeline) SetDurablePersist(fn func() error) { p.durablePersist = fn }

// SetCapacityBudget 设置资源预算。
func (p *Pipeline) SetCapacityBudget(b CapacityBudget) { p.budget = b }

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
	dec := EvaluateCapacity(p.budget, p.walBytes, gapCount)
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

	if err := p.wal.Append(events...); err != nil {
		// append 失败（含已暂停）：补 gap，禁止静默。
		_ = p.led.RecordGap(p.key, gapStart, gapEnd, "APPEND_REJECTED", err.Error())
		p.noteSelfFailure(err.Error())
		return err
	}
	for _, ev := range events {
		p.walBytes += uint64(len(ev.Message) + len(ev.EventID) + 64)
	}

	// durable：fsync/equivalent commit。
	if err := p.wal.Commit(); err != nil {
		_ = p.led.RecordGap(p.key, gapStart, gapEnd, "WAL_COMMIT_FAILED", err.Error())
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
	status, ackLost, err := p.deliver(events)
	if err != nil && p.suppressRecursiveVL {
		// worker 自源：失败只计本地，不递归写 VL。
		p.noteSelfFailure(err.Error())
		_ = p.led.RecordGap(p.key, gapStart, gapEnd, "DELIVER_ERROR_WORKER_SOURCE", err.Error())
		return nil
	}
	if err != nil {
		_ = p.led.RecordGap(p.key, gapStart, gapEnd, "DELIVER_ERROR", err.Error())
		return err
	}
	// HTTP 2xx → REQUEST_DONE only；AckLost → UNKNOWN（恢复责任保留）。
	return p.wal.RecordHTTPResult(DeliveryResult{
		StartPos:   gapStart,
		EndPos:     gapEnd,
		HTTPStatus: status,
		AckLost:    ackLost,
	})
}

// noteSelfFailure 本地可观测失败；source=worker 禁止递归 VL。
func (p *Pipeline) noteSelfFailure(reason string) {
	p.workerSelfFailures++
	_ = reason
}

// Ledger 返回账本。
func (p *Pipeline) Ledger() *ledger.Ledger { return p.led }

// WAL 返回 WAL。
func (p *Pipeline) WAL() *WAL { return p.wal }
