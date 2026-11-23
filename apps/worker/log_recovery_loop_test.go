package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestWiringRecoverySchedulerRetainsRetryAfterFirstFailure 直接测试生产调度器，不依赖跨模块编译窗口。
func TestWiringRecoverySchedulerRetainsRetryAfterFirstFailure(t *testing.T) {
	for _, reason := range []string{"receiver", "budget", "read-pin"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ticks := make(chan time.Time)
			steps := make(chan bool, 4)
			var allowed atomic.Bool
			done := runLogPhysicalDayRecovery(ctx, func(context.Context) { steps <- allowed.Load() }, ticks)
			select {
			case admitted := <-steps:
				if admitted {
					t.Fatal("首轮阻断条件被忽略")
				}
			case <-time.After(time.Second):
				t.Fatal("没有执行首轮恢复")
			}
			allowed.Store(true)
			select {
			case ticks <- time.Now():
			case <-done:
				t.Fatal("首轮未ready/预算拒绝/旧pin在飞后，既有恢复责任被一次性丢弃")
			case <-time.After(time.Second):
				t.Fatal("未继续消费有限恢复tick")
			}
			select {
			case admitted := <-steps:
				if !admitted {
					t.Fatal("条件恢复后仍未续跑")
				}
			case <-time.After(time.Second):
				t.Fatal("没有执行第二轮恢复")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("取消后调度器未退出")
			}
		})
	}
}

// TestWiringRecoverySchedulerCancellationWaitsForRealStep 不用提前关闭done伪造在途物理操作已退出。
func TestWiringRecoverySchedulerCancellationWaitsForRealStep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	done := runLogPhysicalDayRecovery(ctx, func(context.Context) {
		calls.Add(1)
		close(entered)
		<-release
	}, make(chan time.Time))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("恢复步骤没有开始")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("在途步骤尚未返回，不能提前关闭done")
	default:
	}
	if awaitLogPhysicalDayRecovery(ctx, done) {
		t.Fatal("等待上下文已取消不能伪装真实在途步骤已经退出")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("真实步骤退出后done未关闭")
	}
	if calls.Load() != 1 {
		t.Fatal("取消后仍下发了新步骤")
	}
	if !awaitLogPhysicalDayRecovery(ctx, done) || !awaitLogPhysicalDayRecovery(ctx, nil) {
		t.Fatal("真实退出或未装配时不应伪造残留后台")
	}
}

// TestWiringRecoverySchedulerClosedTicksAndAlreadyCancelled 有界tick源关闭或预先取消不能遗留后台。
func TestWiringRecoverySchedulerClosedTicksAndAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	done := runLogPhysicalDayRecovery(ctx, func(context.Context) { calls.Add(1) }, nil)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("预先取消未退出")
	}
	if calls.Load() != 0 {
		t.Fatal("预先取消仍下发步骤")
	}
	ticks := make(chan time.Time)
	close(ticks)
	done = runLogPhysicalDayRecovery(context.Background(), func(context.Context) { calls.Add(1) }, ticks)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tick源关闭后未退出")
	}
	if calls.Load() != 1 {
		t.Fatal("tick关闭后重复恢复")
	}
}
