package ingest

import "context"

// 校验/对账查询的**限流旁路**（2026-11-02）。
//
// ## 它解决的问题
//
// `visibleEventsInVL`（hot_retention_window.go）先按 512 条批量查询；
// 批不齐时逐条回退确认哪些可见。而 `verifyProjectionQueryInWindow` 每次都
// `verifyLimiter.Acquire(ctx)` ⇒ 逐条回退的每一条都要**重新排队**。
//
// 算术上不可逃逸（本机实测 91% 的验证查询是 events=1 的逐条回退）：
//
//	512 条 × (1 / 30 QPS) = 17.1s  ≫  3s 硬超时（hot_retention_window.go:155）
//	⇒ 必然 context deadline exceeded ⇒ 本批 0 条可见 ⇒ 缺口 0 条消解
//
// 生产实测（2026-10-29 16:41–17:38，57 分钟）：171 条源被暂停、
// 1,295 条「源被不可分类缺口阻塞（无自动出路）」、79,981 条积压未解除。
//
// ## 为什么旁路是对的，而不是把限流调大
//
// 逐条回退不是**新负载**——它是“已经拿到许可的那个调用方”为了弄清批内哪些事件
// 可见而做的后续诊断。把它重新计入全局速率，等于对同一批数据收两次费，
// 而收费额度又与批大小无关（512 次 vs 1 次），于是批越大越必死。
//
// 调大 `MaxPerSecond` 只能把阈值往后推：512 条回退仍需要 512 次额度，
// 速度上去了但总时长仍与批大小线性相关，且会推高全局 VL 压力。
//
// ## 边界（刻意不做）
//
// - **不旁路批量查询**：批量查询仍是正常路径，照旧过闸。
// - **不旁路代次占用探测**：那是每次取新代次都发生的独立查询，不是“失败后的后续”。
// - 旁路只影响**速率/在飞计数**，不影响超时（ctx 自带 deadline 仍生效）。

type verifyBypassKey struct{}

// withVerifyLimiterBypass 标记该 ctx 下的校验查询不申请全局许可。
//
// 只能用于**已经持有许可的调用方的后续诊断步骤**（当前唯一调用方：
// `visibleEventsInVL` 的逐条回退）。新增调用方前请先回答：
// 它是否在为一个**已获批的批**做后续确认？若不是，不要用这个旁路。
func withVerifyLimiterBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, verifyBypassKey{}, true)
}

// verifyLimiterBypassed 报告当前 ctx 是否在限流旁路模式下。
func verifyLimiterBypassed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(verifyBypassKey{}).(bool)
	return v
}
