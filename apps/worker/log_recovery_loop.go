package main

import (
	"context"
	"time"
)

const logPhysicalDayRecoveryInterval = 15 * time.Second

// runLogPhysicalDayRecovery 单协程续跑既有恢复责任；done只在实际在途步骤返回后关闭。
// ctx取消只禁止新步骤，不伪造对底层物理IO的硬中断。
func runLogPhysicalDayRecovery(ctx context.Context, step func(context.Context), ticks <-chan time.Time) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if step == nil || ctx.Err() != nil {
			return
		}
		if ticks == nil {
			// 标准ticker只合并时刻，不会把耗时恢复累积成并发队列。
			ticker := time.NewTicker(logPhysicalDayRecoveryInterval)
			defer ticker.Stop()
			ticks = ticker.C
		}
		for {
			if ctx.Err() != nil {
				return
			}
			step(ctx)
			select {
			case <-ctx.Done():
				return
			case _, open := <-ticks:
				if !open {
					return
				}
			}
		}
	}()
	return done
}

// awaitLogPhysicalDayRecovery 只观察真实退出，不把等待超时误写成后台已停止。
func awaitLogPhysicalDayRecovery(ctx context.Context, done <-chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
