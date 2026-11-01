package ingest

// 验证查询限流旁路的回归（2026-11-02）。
//
// 钉住两件事：
//  1. 旁路只对显式标记的 ctx 生效（批量查询仍照旧过闸）
//  2. 预算预检在“跑不完”时提前拒绝，不跑一半

import (
	"context"
	"testing"
	"time"
)

func TestVerifyLimiterBypassOnlyForMarkedContext(t *testing.T) {
	if verifyLimiterBypassed(context.Background()) {
		t.Fatal("未标记的 ctx 不得旁路限流（批量查询必须照旧过闸）")
	}
	if !verifyLimiterBypassed(withVerifyLimiterBypass(context.Background())) {
		t.Fatal("标记后的 ctx 应旁路限流")
	}
	if verifyLimiterBypassed(nil) {
		t.Fatal("nil ctx 不得旁路")
	}
}

func TestVerifyBypassSurvivesCancelContext(t *testing.T) {
	// 生产路径上 ctx 通常是 WithCancel 包一层的；旁路标记必须能穿过。
	base := withVerifyLimiterBypass(context.Background())
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	if !verifyLimiterBypassed(ctx) {
		t.Fatal("旁路标记应穿过 WithCancel（生产路径正是这种嵌套）")
	}
}

func TestEnoughBudgetForPerEventFallback(t *testing.T) {
	t.Run("无 deadline 恒放行", func(t *testing.T) {
		if !enoughBudgetForPerEventFallback(context.Background(), 512) {
			t.Fatal("无 deadline 时不会因超时中断，不应拦截")
		}
	})

	t.Run("零条恒放行", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		if !enoughBudgetForPerEventFallback(ctx, 0) {
			t.Fatal("n<=0 无需任何预算")
		}
	})

	t.Run("3s 预算跑不完 512 条", func(t *testing.T) {
		// 这是生产事故的算术：512 × 20ms × 1.2 = 12.3s ≫ 3s
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if enoughBudgetForPerEventFallback(ctx, 512) {
			t.Fatal("3s 预算不可能跑完 512 条回退，必须在开始前就拒绝")
		}
	})

	t.Run("预算充足时放行", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if !enoughBudgetForPerEventFallback(ctx, 10) {
			t.Fatal("60s 跑 10 条（需 240ms）应当放行")
		}
	})

	t.Run("边界：刚好够不算够", func(t *testing.T) {
		// need = n*cost*1.2；给 need 的 99% 不够，给 101% 够。
		n := 10
		need := time.Duration(n)*perEventFallbackCost*12/10
		ctx1, c1 := context.WithTimeout(context.Background(), need*99/100)
		defer c1()
		if enoughBudgetForPerEventFallback(ctx1, n) {
			t.Fatal("刚好差一点不应放行（要留余量抵抗排队抖动）")
		}
		ctx2, c2 := context.WithTimeout(context.Background(), need*101/100)
		defer c2()
		if !enoughBudgetForPerEventFallback(ctx2, n) {
			t.Fatal("超过 need 应放行")
		}
	})
}
