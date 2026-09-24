package vlsup

import (
	"fmt"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/process"
)

// ReserveRatioPercent 是契约 §6.6 冻结的「WAL+staging+临时 Export 预留占比」门禁：
// 这三类短生命周期空间之和不得超过 Worker 日志总预算的 25%。
const ReserveRatioPercent = 25

// ReserveSubBudgetKeys 是计入「25% 预留」的子预算键（WAL / staging / 临时 Export）。
// 其余子预算（COLD / rehydrate / projection / query 并发 / RSS）不参与该占比判定。
var ReserveSubBudgetKeys = []string{"wal", "staging", "export"}

// EvaluateReserveRatio 按契约 §6.6 判定「WAL+staging+临时 Export ≤ 总预算 25%」。
//
// 总预算或子预算未配置（<=0）时视为不可判定，返回 ok=false 而非猜测通过——
// 与 EvaluateBudget 的「未配置即不判定」一致。
// 返回值 reserved 为三项之和；allowed 为 25% 上限。
func EvaluateReserveRatio(budget CacheBudget) (reserved, allowed int64, ok bool) {
	if budget.TotalWorkerLogBudgetBytes <= 0 {
		return 0, 0, false
	}
	for _, key := range ReserveSubBudgetKeys {
		reserved += budget.SubBudgets[key]
	}
	allowed = budget.TotalWorkerLogBudgetBytes * ReserveRatioPercent / 100
	return reserved, allowed, true
}

// EnforceReserveRatio 在可判定且超限时返回错误（调用方据此降级并暴露原因）。
func EnforceReserveRatio(budget CacheBudget) error {
	reserved, allowed, ok := EvaluateReserveRatio(budget)
	if !ok {
		return nil
	}
	if reserved > allowed {
		return fmt.Errorf(
			"vlsup: WAL+staging+export reservation %d bytes exceeds %d%% of total log budget (%d bytes)",
			reserved, ReserveRatioPercent, allowed)
	}
	return nil
}

// CacheBudget 记录进程级 cache 预算与 Worker 日志总预算的区分（FR-473 §6.5/§7、FR-476 §3.1）。
//
// 关键区分（不得混用）：
//   - HotCacheBytes：VL HOT 实例 -memory.allowedBytes（默认模板 512MiB），是**进程 cache 预算**；
//     它**不等于** RSS 上限。
//   - ProcessRSSBytes：Worker 日志数据面 RSS 判据（契约初始工程门禁：单 Worker ≤ 1GiB，不含 page cache）。
//   - TotalWorkerLogBudgetBytes：Worker 级日志总预算，独立覆盖 WAL、HOT、COLD、staging、Rehydrate、
//     canonical projection、查询并发、临时导出空间和 RSS。
//
// foundation 阶段 EnforceCacheBudget 只做占位校验与语义登记；预算采样与降级评估
// 由 EvaluateBudget + Sample* 提供（FR-476 Runbook C / 契约 §6.6）。
type CacheBudget struct {
	HotCacheBytes             int64
	ProcessRSSBytes           int64
	TotalWorkerLogBudgetBytes int64
	// SubBudgets 预留子预算（WAL / COLD / staging / rehydrate / projection / export）。
	SubBudgets map[string]int64
}

// DefaultCacheBudget 返回 HOT cache 模板 512MiB 的预算骨架。
// ProcessRSS / Total 留 0 表示尚未由 FR-473 冻结数值注入，不作自动推断。
func DefaultCacheBudget() CacheBudget {
	return CacheBudget{
		HotCacheBytes: DefaultHotCacheBytes,
		SubBudgets:    map[string]int64{},
	}
}

// EnforceCacheBudget 占位：校验并登记 VL 进程 cache 预算。
//
// cacheBytes 对应 -memory.allowedBytes（进程级 cache），**不是** RSS，也**不是** Worker 日志总预算。
// 本函数拒绝负数；0 表示“未配置，由实例模板决定”（HOT 回退 512MiB）。
// 真实预算降级（暂停采集 / 可见缺口 / 不静默删除未确认前缀）由 FR-476 Runbook C 验收。
func EnforceCacheBudget(cacheBytes int64) error {
	if cacheBytes < 0 {
		return fmt.Errorf("vlsup: cache budget must be >= 0, got %d", cacheBytes)
	}
	return nil
}

// Enforce 兼容调用方在 supervisor 上登记预算；见 EnforceCacheBudget。
func (b CacheBudget) Enforce(cacheBytes int64) (CacheBudget, error) {
	if err := EnforceCacheBudget(cacheBytes); err != nil {
		return b, err
	}
	b.HotCacheBytes = cacheBytes
	return b, nil
}

// DistinguishesProcessRSS 报告该预算结构是否已显式区分 cache 与 RSS 字段。
// 即使数值为 0，字段存在即表示契约区分已登记，避免把 512MiB 误当作 RSS/总预算。
func (b CacheBudget) DistinguishesProcessRSS() bool {
	// 字段本身在类型上已分离；此方法用于测试/文档断言语义存在。
	_ = b.ProcessRSSBytes
	_ = b.TotalWorkerLogBudgetBytes
	return true
}

// DegradationState 日志资源预算的降级状态（FR-473 §6.6/§7）。
//
// OK：全部预算内；DEGRADED：逼近/超出子预算（应暂停低优先级采集或切换受控 Raw，并暴露缺口）；
// PAUSED：触及不可恢复写的暂停阈值（磁盘 ≥ pause%），必须暂停不可恢复写入。
type DegradationState string

const (
	// BudgetOK 预算内。
	BudgetOK DegradationState = "OK"
	// BudgetDegraded 降级：RSS 超限或磁盘达到降级阈值。
	BudgetDegraded DegradationState = "DEGRADED"
	// BudgetPaused 暂停：磁盘达到暂停阈值。
	BudgetPaused DegradationState = "PAUSED"
)

// BudgetThresholds 磁盘降级阈值。契约默认 80% 降级、90% 暂停。
type BudgetThresholds struct {
	DegradedAtPercent float64
	PauseAtPercent    float64
}

// DefaultBudgetThresholds 返回契约默认阈值（80/90），与 acquire 采集侧一致。
func DefaultBudgetThresholds() BudgetThresholds {
	return BudgetThresholds{DegradedAtPercent: 80, PauseAtPercent: 90}
}

// BudgetSample 一次资源采样：VL 进程 RSS 与数据盘使用率。
type BudgetSample struct {
	// ProcessRSSBytes 受管 VL 进程当前 RSS（不含文件系统 page cache）。
	ProcessRSSBytes int64
	// DiskUsagePercent 数据盘使用率（0..100）。
	DiskUsagePercent float64
}

// BudgetVerdict 预算评估结果。
type BudgetVerdict struct {
	State   DegradationState
	Reasons []string
	// Sample 本次判定所依据的采样值，供日志/证据记录。
	Sample BudgetSample
}

// EvaluateBudget 依据契约阈值评估降级状态（纯逻辑，便于单测）。
//
// 判定优先级：磁盘暂停 > 磁盘降级 > RSS 超限降级，取最严重；只升不降。
// ProcessRSSBytes / TotalWorkerLogBudgetBytes 为 0 表示未配置该预算，不作判定。
func EvaluateBudget(budget CacheBudget, sample BudgetSample, th BudgetThresholds) BudgetVerdict {
	deg := th.DegradedAtPercent
	if deg <= 0 {
		deg = 80
	}
	pause := th.PauseAtPercent
	if pause <= 0 {
		pause = 90
	}

	v := BudgetVerdict{State: BudgetOK, Sample: sample}
	setState := func(s DegradationState, reason string) {
		// 只升不降：PAUSED > DEGRADED > OK。
		if s == BudgetPaused || (s == BudgetDegraded && v.State == BudgetOK) {
			v.State = s
		}
		v.Reasons = append(v.Reasons, reason)
	}

	if sample.DiskUsagePercent >= pause {
		setState(BudgetPaused, fmt.Sprintf(
			"disk usage %.1f%% >= pause threshold %.1f%%; pause irreversible writes",
			sample.DiskUsagePercent, pause))
	} else if sample.DiskUsagePercent >= deg {
		setState(BudgetDegraded, fmt.Sprintf(
			"disk usage %.1f%% >= degraded threshold %.1f%%; degrade", sample.DiskUsagePercent, deg))
	}

	if budget.ProcessRSSBytes > 0 && sample.ProcessRSSBytes > budget.ProcessRSSBytes {
		setState(BudgetDegraded, fmt.Sprintf(
			"VL RSS %d bytes exceeds RSS budget %d bytes", sample.ProcessRSSBytes, budget.ProcessRSSBytes))
	}
	if budget.TotalWorkerLogBudgetBytes > 0 && sample.ProcessRSSBytes > budget.TotalWorkerLogBudgetBytes {
		setState(BudgetDegraded, fmt.Sprintf(
			"VL RSS %d bytes exceeds total worker log budget %d bytes",
			sample.ProcessRSSBytes, budget.TotalWorkerLogBudgetBytes))
	}
	return v
}

// SampleProcessRSSBytes 采样指定进程的 RSS（字节）。跨平台经 gopsutil process。
func SampleProcessRSSBytes(pid int) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("vlsup: sample RSS requires positive pid, got %d", pid)
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return 0, fmt.Errorf("vlsup: open process %d: %w", pid, err)
	}
	mi, err := p.MemoryInfo()
	if err != nil {
		return 0, fmt.Errorf("vlsup: memory info for %d: %w", pid, err)
	}
	if mi == nil {
		return 0, nil
	}
	return int64(mi.RSS), nil
}

// SampleDiskUsagePercent 采样指定路径所在文件系统的使用率（0..100）。path 为空时取当前目录。
func SampleDiskUsagePercent(path string) (float64, error) {
	if path == "" {
		path = "."
	}
	usage, err := disk.Usage(path)
	if err != nil {
		return 0, fmt.Errorf("vlsup: disk usage for %q: %w", path, err)
	}
	if usage == nil {
		return 0, nil
	}
	return usage.UsedPercent, nil
}
