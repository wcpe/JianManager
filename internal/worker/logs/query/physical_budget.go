package query

import (
	"context"
	"sync"
	"time"
)

// WithQueryDeadline 冻结整个请求的绝对截止时间；嵌套入口只能收紧，不能续期父截止时间。
func WithQueryDeadline(ctx context.Context, b Budget) (context.Context, context.CancelFunc) {
	if b.TimeoutMS == 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(b.TimeoutMS)*time.Millisecond)
}

type physicalBudgetKey struct{}

// 物理分片、维度和边界补查共享请求额度，不能按 range 或递归重新获得默认预算。
type physicalReadBudget struct {
	mu        sync.Mutex
	remaining uint64
	fanout    uint32
	calls     uint32
}

// WithPhysicalReadBudget 在请求上下文登记物理读取预算；派生上下文只能复用，不能扩额。
func WithPhysicalReadBudget(ctx context.Context, b Budget) context.Context {
	if _, ok := ctx.Value(physicalBudgetKey{}).(*physicalReadBudget); ok {
		return ctx
	}
	remaining := b.MaxBytes
	if remaining == 0 {
		remaining = DefaultMaxBytes
	}
	return context.WithValue(ctx, physicalBudgetKey{}, &physicalReadBudget{remaining: remaining, fanout: b.MaxFanout})
}

// ReservePhysicalRead 在实际发送前预留字节与扇出，释放时只退款未读字节，不退款请求次数。
func ReservePhysicalRead(ctx context.Context, maxBytes uint64) (uint64, func(uint64), error) {
	if err := ctx.Err(); err != nil {
		return 0, func(uint64) {}, err
	}
	b, ok := ctx.Value(physicalBudgetKey{}).(*physicalReadBudget)
	if !ok {
		return maxBytes, func(uint64) {}, nil
	}
	b.mu.Lock()
	if b.remaining == 0 || b.fanout > 0 && b.calls >= b.fanout {
		b.mu.Unlock()
		return 0, func(uint64) {}, ErrRangeQueryBudget
	}
	if maxBytes > b.remaining {
		maxBytes = b.remaining
	}
	b.remaining -= maxBytes
	b.calls++
	b.mu.Unlock()
	var once sync.Once
	return maxBytes, func(actual uint64) {
		once.Do(func() {
			b.mu.Lock()
			if actual < maxBytes {
				b.remaining += maxBytes - actual
			}
			b.mu.Unlock()
		})
	}, nil
}
