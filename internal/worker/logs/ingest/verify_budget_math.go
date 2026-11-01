package ingest

import (
	"context"
	"time"
)

// perEventFallbackCost 是单条回退确认的**保守耗时估计**。
//
// 取值依据：VL 查询本身实测 2.0 ms（2026-11-02 采样 10 次 0.0018–0.0033s），
// 加上 HTTP 往返与逐行 JSON 解析，单次保守取 20 ms（10 倍余量）。
// 这是**预算预检**用的估计值，不参与任何判定：估小了只是让预检更宽松（仍超时），
// 估大了会让本可完成的批被提前放弃（保守方向，不会造假）。
const perEventFallbackCost = 20 * time.Millisecond

// enoughBudgetForPerEventFallback 报告当前 ctx 的剩余时间是否够跑完 n 条逐条回退。
//
// 为何需要它：逐条回退是 N 次 HTTP 往返。中途超时会让**已确认的那几条**
// 被当成“整批已确认”（调用方拿到 error 前的部分结果语义模糊），
// 而缺口消解直接把“可见集合”当证据 ⇒ 半夜真造假。
//
// 所以宁可在**开始前**判定“跑不完”，返回 DeadlineExceeded（上层据此
// 走“本轮不消解任何缺口”的既有分支，语义与超时一致），也不要跑一半。
func enoughBudgetForPerEventFallback(ctx context.Context, n int) bool {
	if n <= 0 {
		return true
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		// 无 deadline ⇒ 不会因超时中断，预检不拦截。
		return true
	}
	// 留 20% 余量：不要“刚好够”就开跑，排队抖动会把尾部推送超时。
	need := time.Duration(n) * perEventFallbackCost * 12 / 10
	return time.Until(deadline) >= need
}
