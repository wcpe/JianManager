// Package directprobe 定义 Control Plane 与 Worker 之间关于「MC 直探（SLP / Query）」的共享契约值：
// 默认/上界超时、探针抓取上限、心跳节拍与单拍采集预算。
//
// 存在的理由（FR-446 复审 NEW-ISSUE A）：直探超时是**可配项**，而心跳采集有**总预算护栏**，
// 二者若各写一份字面量就会自相矛盾——运营把超时调到 CP 接受的上界，Worker 侧却按另一个上界
// （或按硬编码预算）静默把该实例的时序样本砍掉。故本包是唯一数值来源：
//
//   - **CP 写路径**校验：`internal/controlplane/service/settings.go`（validateSettingValue 拒收 > MaxTimeout）
//   - **CP 读/下发路径**钳制：`internal/controlplane/service/settings.go`（parseDurationOr → NormalizeTimeout）
//   - **Worker 归一**：`internal/worker/metrics/timeout.go`（normalizeProbeTimeout → NormalizeTimeout）
//   - **单拍采集预算**：`internal/worker/heartbeat/heartbeat.go`（CollectBudgetFor）
//
// 三处一旦引用同一常量，就不会各自漂移；跨包一致性由 contract_test.go 的不变量断言锁定。
package directprobe

import "time"

const (
	// DefaultTimeout 是 SLP / Query 直探的内置默认超时（未配置 / 老 CP 不下发时生效）。
	DefaultTimeout = 3 * time.Second

	// ProbeScrapeTimeoutCap 是 Worker 抓取 ServerProbe `/metrics` 的 HTTP 客户端超时
	// （见 internal/worker/metrics.ScrapeServerProbe）。它是**硬编码**上限，不是可配项，
	// 但在推导采集预算时必须计入（探针抓取与 SLP/Query 在同一实例任务内**串行**）。
	ProbeScrapeTimeoutCap = 5 * time.Second

	// HeartbeatInterval 是心跳上报节拍（ADR-013，30s）。apps/worker/main.go 据此启动心跳；
	// 采集预算必须显著小于本值，否则一拍未结束下一拍又来，心跳积压。
	HeartbeatInterval = 30 * time.Second

	// HeartbeatTickReserve 是「单拍采集预算」与「心跳节拍」之间必须保留的最小余量：
	// 给结果落地、gRPC 发送与调度抖动留出空间，避免本拍采集吞掉下一拍的起点。
	HeartbeatTickReserve = 4 * time.Second

	// CollectBudgetMargin 是单拍采集预算在最坏串行链之外**额外**保留的余量（协程调度、
	// 结果通道落地、时钟粒度）。1s 足够吸收这些抖动。
	CollectBudgetMargin = 1 * time.Second

	// MaxTimeout 是 SLP / Query 直探超时的上界。
	//
	// 它不是孤立的「最大值」，而是由心跳节拍护栏**反推**出来的：单实例同源串行最坏为
	//
	//	ProbeScrapeTimeoutCap + slp + query
	//
	// 单拍采集预算 = CollectBudgetMargin + 上述最坏，且必须 ≤ HeartbeatInterval − HeartbeatTickReserve。
	// 即 slp + query ≤ 30s − 4s − 1s − 5s = 20s → 两来源对称上界 ≤ 10s，故取 MaxTimeout = 10s：
	//
	//	预算上界 = 1 + 5 + 10 + 10 = 26s ≤ 30s − 4s ✓
	//
	// 用默认值（3s）时预算 = 1 + 5 + 3 + 3 = 12s，与旧硬编码 15s 同量级。
	MaxTimeout = 10 * time.Second
)

// NormalizeTimeout 把直探超时归一到「(0, MaxTimeout]」区间：非正值回退 DefaultTimeout（不把超时
// 归零成立即失败），超上界则钳制到 MaxTimeout（不让异常下发/本地 env 突破采集护栏）。
//
// CP 读/下发路径与 Worker 归一都经本函数，使「可配」与「护栏」始终自洽：任何来源的取值都不可能
// 让单实例每拍最坏阻塞量超过预算法则所容纳的范围。
func NormalizeTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	if d > MaxTimeout {
		return MaxTimeout
	}
	return d
}

// CollectBudgetFor 由**生效直探超时**推导单拍实例指标采集的总预算：
//
//	budget = CollectBudgetMargin + ProbeScrapeTimeoutCap + slp + query
//
// 入参 slp / query 先经 NormalizeTimeout 归一，故即使传入异常值，预算也不会小于「探针上限 + 两来源
// 默认超时」这一实际可能发生的串行量。预算恒 ≥ 单实例同源串行最坏（探针 5s + slp + query），
// 因此不会出现「实例指标被预算静默砍掉」的组合。
//
// 上界由常量关系保证：CollectBudgetFor(MaxTimeout, MaxTimeout) == MaxCollectBudget() ≤
// HeartbeatInterval − HeartbeatTickReserve（见 contract_test.go 的不变量断言）。
func CollectBudgetFor(slp, query time.Duration) time.Duration {
	return CollectBudgetMargin + ProbeScrapeTimeoutCap + NormalizeTimeout(slp) + NormalizeTimeout(query)
}

// MaxCollectBudget 是在两个直探来源都取到上界时单拍采集预算的最大可能值（26s）。
// 供护栏断言/文档引用：它必须 ≤ HeartbeatInterval − HeartbeatTickReserve。
func MaxCollectBudget() time.Duration {
	return CollectBudgetFor(MaxTimeout, MaxTimeout)
}

// WorstCaseSerial 返回单实例**同源串行**采集的最坏耗时（探针上限 + slp + query），
// 仅计该实例实际配置的来源（port 未配置则不占用）。预算必须 ≥ 本值，否则该实例的样本
// 每拍都会被预算截断成「不可用」。
func WorstCaseSerial(hasProbe, hasSLP, hasQuery bool, slp, query time.Duration) time.Duration {
	var worst time.Duration
	if hasProbe {
		worst += ProbeScrapeTimeoutCap
	}
	if hasSLP {
		worst += NormalizeTimeout(slp)
	}
	if hasQuery {
		worst += NormalizeTimeout(query)
	}
	return worst
}

// BudgetCoversTick 报告给定预算是否与心跳节拍护栏自洽（预算 + 余量 ≤ 节拍）。
// 供运行期防御性告警与单测共用；返回 false 表示配置已把「可配超时」与「心跳护栏」推到冲突区。
func BudgetCoversTick(budget time.Duration) bool {
	return budget+HeartbeatTickReserve <= HeartbeatInterval
}
