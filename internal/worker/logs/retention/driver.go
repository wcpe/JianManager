package retention

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// 触发原因（观测面据此区分「按年龄搬」与「因盘吃紧提前搬」）。
const (
	TriggerAge  = "age"
	TriggerDisk = "disk"
)

// Trigger 是「何时可以搬运」的触发口径：**年龄 + 磁盘水位取先到**（用户决策 D3）。
//
// 为什么需要磁盘水位这一路：只按年龄的话，盘先满了不会触发任何动作，
// 系统会一路走到容量门禁的 PAUSED（90%）而把采集停掉——那是「因为省不下空间所以不采了」，
// 与保留策略的目的正好相反。磁盘水位触发让「快满」成为提前归档的理由。
//
// 但提前归档必须有下限（MinAge）：盘再满也不搬「刚写进来」的分区，
// 否则会在「写入正在进行的日分区」与「把它搬走」之间打架，
// 而那个分区的权威副本一旦被搬，正在写的路径就指向了错误的位置。
type Trigger struct {
	// DiskPercent 磁盘水位阈值；<=0 表示不启用磁盘触发（只按年龄）。
	// 与容量门禁的 DegradedAtPercent 同口径，读数也复用同一真源。
	DiskPercent float64 `mapstructure:"disk_percent"`
	// MinAge 磁盘触发时的最小分区年龄（自当天结束起算）；<=0 取 DefaultTriggerMinAge。
	MinAge time.Duration `mapstructure:"min_age"`
}

// DefaultTriggerMinAge 是磁盘触发时的最小分区年龄。
//
// 取 7 天的理由：它必须明显长于「写入还在进行」的窗口（当天 + 迟到事件补录通常几天内完成），
// 又必须明显短于热层窗口（30d），才能让「提前搬」真的提前。
const DefaultTriggerMinAge = 7 * 24 * time.Hour

// Normalize 补齐缺省。
func (t Trigger) Normalize() Trigger {
	if t.DiskPercent > 0 && t.MinAge <= 0 {
		t.MinAge = DefaultTriggerMinAge
	}
	return t
}

// Enabled 报告磁盘水位触发是否可用。
func (t Trigger) Enabled() bool { return t.Normalize().DiskPercent > 0 }

// PartitionRef 是一个候选日分区（由调用方从 catalog 读出，不在此重复建模）。
type PartitionRef struct {
	StorageNamespace string
	// UTCDay 形如 20060102。
	UTCDay string
	// Owner 当前拥有者（OwnerColdName 表示已在冷层；本包不 import catalog，
	// 由调用方把 catalog 的 owner 映射成本字段）。
	Owner string
	// InFlight 报告该分区已有在途迁移。
	//
	// 为什么必须有这个字段：驱动器是周期跑的，而一次迁移可能要跑几轮。
	// 若不看这个标记就反复下发，第二轮会对同一个分区再次 BeginMigration，
	// 而 Catalog 的状态机不允许在途迁移被重新开始（会返回错误并污染状态）。
	InFlight bool
}

// eligibleAt 返回该分区最早可离开热层的时刻。
//
// 语义：分区覆盖的是**一整天**，故以当天结束（次日 00:00 UTC）为起点加保留期——
// 用当天开始会让最后几小时的数据少留一天。
//
// 日期同时接受 `20060102` 与 catalog 的 `2006-01-02` 两种写法：分区的真源在 catalog
// （用后者），而运维与策略面自然写前者。只认一种的话，另一种会静默变成「日期不可解析」
// 而被跳过——表现为「策略配了但永远不搬」，且没有任何报错。多认一种写法的代价是 3 行。
func (part PartitionRef) eligibleAt(hot time.Duration) (time.Time, bool) {
	day, ok := parseUTCDay(part.UTCDay)
	if !ok {
		return time.Time{}, false
	}
	return day.Add(24 * time.Hour).Add(hot), true
}

// parseUTCDay 解析分区日期，兼容两种写法。
func parseUTCDay(raw string) (time.Time, bool) {
	for _, layout := range []string{"20060102", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// PlanArchiveWithTrigger 在年龄口径之外叠加磁盘水位触发（取先到）。
//
// diskPercent <= 0 或未达阈值时，行为与 PlanArchive 完全一致（纯年龄）。
func (p Policy) PlanArchiveWithTrigger(now time.Time, partitions []PartitionRef, moverAvailable bool, diskPercent float64) []ArchivePlanTarget {
	out := p.PlanArchive(now, partitions, moverAvailable)
	if len(out) == 0 {
		return out
	}
	trig := p.Trigger.Normalize()
	diskFiring := trig.Enabled() && diskPercent >= trig.DiskPercent
	if !diskFiring {
		return out
	}
	for i := range out {
		// 磁盘吃紧 ⇒ 放宽口径：只要分区年龄过了下限，即使还没到热层窗口也提前搬。
		// 下限不可省：盘再满也不搬「刚写进来」的分区，否则会在「正在写的日分区」
		// 与「把它搬走」之间打架，而权威副本一旦被搬，正在写的路径就指向了错误位置。
		if out[i].Due {
			out[i].DiskPressure = true
			continue
		}
		age, ok := out[i].ageAt(now)
		if !ok || age < trig.MinAge {
			continue
		}
		out[i].Due = true
		out[i].TriggerReason = TriggerDisk
	}
	return out
}

// PartitionLister 提供候选分区（生产由 catalog 适配，测试用假实现）。
type PartitionLister interface {
	// ListPartitions 列出全部已知分区。
	ListPartitions() ([]PartitionRef, error)
}

// ListerFunc 函数适配。
type ListerFunc func() ([]PartitionRef, error)

// ListPartitions 实现 PartitionLister。
func (f ListerFunc) ListPartitions() ([]PartitionRef, error) { return f() }

// DiskReader 提供磁盘使用率（与容量门禁同一真源）。
type DiskReader func() (float64, error)

// DriverResult 是一轮驱动的结果（观测面：谁、何时、多少、去哪）。
type DriverResult struct {
	// Listed 本轮的候选分区数。
	Listed int
	// Skipped 因已在冷层或在途而未纳入计划的分区数。
	Skipped int
	// Due 到点（按年龄或磁盘水位）的分区数。
	Due int
	// Moved / KeptOriginal / Discarded / NotDue 见 Archive* 常量。
	Moved        int
	KeptOriginal int
	Discarded    int
	NotDue       int
	// Outcomes 逐分区结果。
	Outcomes []ArchiveOutcome
	// At 本轮时刻。
	At time.Time
}

// Driver 周期性地把 catalog 里的日分区按保留策略搬到冷层。
//
// 「周期」的实现刻意与「单轮」分离（Step 可单独调用）：周期循环是薄薄一层调度，
// 所有判定与执行都在 Step 里，于是「到点必搬」「搬运失败留存告警」这些性质
// 可以直接对 Step 做断言，而不必去驱动一个带 ticker 的循环。
type Driver struct {
	policy   Policy
	lister   PartitionLister
	exec     *ArchiveExecutor
	disk     DiskReader
	interval time.Duration
}

// NewDriver 创建驱动器。disk 为 nil 时只按年龄触发。
func NewDriver(policy Policy, lister PartitionLister, mover Mover, disk DiskReader) *Driver {
	return &Driver{
		policy:   policy,
		lister:   lister,
		exec:     NewArchiveExecutor(policy, mover),
		disk:     disk,
		interval: policy.Normalize().Sweep.Interval,
	}
}

// SetPolicy 替换策略。
func (d *Driver) SetPolicy(p Policy) {
	if d == nil {
		return
	}
	d.policy = p
	if d.exec != nil {
		d.exec.SetPolicy(p)
	}
	d.interval = p.Normalize().Sweep.Interval
}

// Executor 返回内部执行器（观测面读 Stats 用）。
func (d *Driver) Executor() *ArchiveExecutor {
	if d == nil {
		return nil
	}
	return d.exec
}

// Interval 返回调度间隔。
func (d *Driver) Interval() time.Duration {
	if d == nil || d.interval <= 0 {
		return DefaultSweepInterval
	}
	return d.interval
}

// Step 执行一轮：列分区 → 过滤 → 算计划（年龄 + 磁盘取先到）→ 执行。
//
// 错误口径：**列不出分区不是致命错误**（catalog 为空、还没建立任何分区都是正常状态），
// 但要如实返回，使观测面能区分「没有任何分区」与「读不出来」。
func (d *Driver) Step(ctx context.Context, now time.Time) (DriverResult, error) {
	var res DriverResult
	res.At = now
	if d == nil || !d.policy.Enabled || d.lister == nil {
		return res, nil
	}
	all, err := d.lister.ListPartitions()
	if err != nil {
		return res, fmt.Errorf("retention: 列举日志分区失败：%w", err)
	}
	res.Listed = len(all)

	// 过滤：已在冷层、或在途迁移的分区不纳入本轮计划。
	candidates := make([]PartitionRef, 0, len(all))
	for _, part := range all {
		if part.Owner == OwnerColdName || part.InFlight {
			res.Skipped++
			continue
		}
		candidates = append(candidates, part)
	}

	diskPercent := 0.0
	if d.disk != nil {
		if v, derr := d.disk(); derr == nil {
			diskPercent = v
		}
		// 读数拿不到就退回纯年龄口径：磁盘读数失败不是「盘满了」的证据，
		// 用猜测去触发提前归档会让「紧急」这个词失去意义。
	}
	plan := d.policy.PlanArchiveWithTrigger(now, candidates, d.exec != nil && d.exec.mover != nil, diskPercent)

	// 计划里未到点的目标交给执行器（它会记 NotDue）；到点的按动作执行。
	outcomes := d.exec.ExecutePlanned(ctx, now, plan)
	res.Outcomes = outcomes
	for _, o := range outcomes {
		switch o.Result {
		case ArchiveMoved:
			res.Moved++
		case ArchiveKeptOriginal:
			res.KeptOriginal++
		case ArchiveDiscarded:
			res.Discarded++
		case ArchiveNotDue:
			res.NotDue++
		}
		if o.Target.Due {
			res.Due++
		}
	}
	return res, nil
}

// Run 周期调度 Step，直到 ctx 结束。
//
// 首轮**立即执行**（不等待第一个 tick）：启动时正是「上一次停机期间攒下的过期分区」
// 最需要处理的时刻，等一个间隔才动手会让停机越久恢复越慢。
func (d *Driver) Run(ctx context.Context) {
	if d == nil || !d.policy.Enabled {
		return
	}
	interval := d.Interval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		res, err := d.Step(ctx, time.Now())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("日志保留驱动器本轮失败（下轮重试）", "error", err)
		} else if res.Listed > 0 {
			slog.Info("日志保留驱动器完成一轮",
				"listed", res.Listed, "skipped", res.Skipped, "due", res.Due,
				"moved", res.Moved, "keptOriginal", res.KeptOriginal, "notDue", res.NotDue)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
