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
	// Interval 冷层**搬运**的调度周期（`Driver.Run` 在 ticker 兼容档下的间隔）；<=0 取 DefaultDriverInterval。
	//
	// 为什么必须独立于 `sweep.interval`（RET-4，2026-10-11）：`log_retention.sweep.interval`
	// 的语义是「删除执行器的扫描间隔」，而搬运与删除是两条独立的路径。复用同一个键意味着
	// 运维为了调删除节奏去改它，会连带改变搬运节奏（反之亦然），调参副作用不可见。
	// 两者的合理取值也不同：删除扫描 1h 足够（删是幂等的，间隔只影响过期数据多留多久），
	// 而搬运是「到期数据的默认动作」，间隔即「到期后最多多久才搬」，长间隔会让热层超期占用。
	//
	// 本键**只在** `sweep_at_hour` 显式配成 SweepAtHourIntervalTicker(-1) 时才决定搬运节奏；
	// 默认的「每日固定时刻」调度不看它（见 SweepAtHour）。
	Interval time.Duration `mapstructure:"interval"`
	// SweepAtHour 每日搬运触发的**整点小时**（进程本地时区，0..23）；缺省 DefaultSweepAtHour（凌晨 3 点）。
	//
	// 为什么从「固定间隔 ticker」改成「每天固定时刻」（用户口径：「不是 30 天每时每刻都在搬运，
	// 应该做到一天检查搬一次」「需要凌晨 3 点」）：日志按 **UTC 日**分区，一天只产生一个新分区，
	// 于是间隔 ticker（旧默认 15m）的绝大多数轮次都在空转——列完分区、算出计划、发现全部未到点，
	// 然后过一会儿再来一遍。而「到期后最多多久才搬」的真实粒度本来就是**天**：分区以整天为界，
	// 早几小时的搬运换不来任何收益，只换来无意义的扫描与日志噪声。
	//
	// 时区选择：用**进程本地时区**的整点，而不是 UTC。理由：
	//   - 用户说的「凌晨 3 点」是运维的墙上时间，运维按本地时区作息（凌晨 3 点是业务低峰），
	//     若解释成 UTC，在东八区会落到北京时间 11:00——业务高峰，正是最不该搬的时候；
	//   - 与分区口径的关系：分区边界是 UTC 日，本地凌晨 3 点**必然落在**「刚结束的 UTC 日之后
	//     若干小时」（东八区 = UTC 19:00，即 UTC 日切后 19h；西五区 = UTC 08:00，即 UTC 日切后 8h），
	//     两种偏移下「昨天的分区」都已封口完毕，既不会碰到正在写入的当前 UTC 分区，
	//     也不会让刚封口的分区等太久。故本地整点是安全且更贴合运维直觉的选择。
	//   - 代价：多节点跨时区部署时各节点在不同墙上时刻搬运。可接受——搬运是各节点独立动作，
	//     不需要节点间同步；需要严格对齐时把各节点进程时区统一（或统一写 UTC 的 19 点）。
	SweepAtHour int `mapstructure:"sweep_at_hour"`
}

// SweepAtHourIntervalTicker 是 SweepAtHour 的**兼容档**取值：不启用每日固定时刻调度，
// 回退到按 Trigger.Interval 的间隔 ticker（本项引入前的旧行为）。
//
// 保留它而不是直接删掉 Interval 键：存量配置里 `trigger.interval` 是真实存在且被文档化过的键，
// 删掉会让它被 viper 静默丢弃（本包已经吃过一次「键配了不生效」的亏，见 B1），
// 而「静默丢弃」比「多一个显式的兼容档」危险得多。
const SweepAtHourIntervalTicker = -1

// DefaultSweepAtHour 每日搬运触发的默认小时：凌晨 3 点（本地时区）。
//
// 取 3 点的理由：业务低峰、且离本地午夜有 3 小时余量——UTC 日分区的封口、迟到事件的补录、
// 上一轮未完成的迁移都还在「最近几小时」的窗口内，凌晨 3 点动手不会与正在写入的路径抢同一分区。
const DefaultSweepAtHour = 3

// DefaultTriggerMinAge 是磁盘触发时的最小分区年龄。
//
// 取 7 天的理由：它必须明显长于「写入还在进行」的窗口（当天 + 迟到事件补录通常几天内完成），
// 又必须明显短于热层窗口（30d），才能让「提前搬」真的提前。
const DefaultTriggerMinAge = 7 * 24 * time.Hour

// DefaultDriverInterval 是冷层搬运在 ticker 兼容档下的默认调度周期。
//
// 取 15 分钟而不是沿用删除扫描的 1h：搬运是**到期数据的默认动作**，间隔即「到期后最多
// 多久才搬」，而热层 TTL 与 VL 原生 retention 之间没有余量，长间隔会让超期数据继续占着热层。
// 单轮只是列举分区 + 算计划（只有到点分区才真的搬），代价与间隔不成问题。
//
// 默认调度是「每天固定时刻」（见 DefaultSweepAtHour），本值只在 `sweep_at_hour: -1`
// 的兼容档下生效；保留它是为了让存量的 `trigger.interval` 配置不变成静默失效的死键。
const DefaultDriverInterval = 15 * time.Minute

// Normalize 补齐缺省。
func (t Trigger) Normalize() Trigger {
	if t.DiskPercent > 0 && t.MinAge <= 0 {
		t.MinAge = DefaultTriggerMinAge
	}
	if t.Interval <= 0 {
		t.Interval = DefaultDriverInterval
	}
	// 越界值归一为默认值，保证运行期不会出现「每天 25 点」这类怪行为；
	// 真正的拒错在 Validate（启用时启动即拒）。
	// 注意 0 是**有效值**（本地午夜 0 点），所以「未配置」不能用 0 表达：
	// 生产装配路径（DefaultPolicy / config.RetentionPolicy）总会填入具体值。
	if t.SweepAtHour < SweepAtHourIntervalTicker || t.SweepAtHour > 23 {
		t.SweepAtHour = DefaultSweepAtHour
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
	// Retryable 表示已有迁移的可重试失败；必须续跑，不能再 Begin。
	// 优先于 OwnerCold：切权威后仍可能因租约/清理失败留下旧副本。
	Retryable bool
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
// archiverAvailable 语义与 PlanArchive 的第四个参数一致。
func (p Policy) PlanArchiveWithTrigger(now time.Time, partitions []PartitionRef, moverAvailable, archiverAvailable bool, diskPercent float64) []ArchivePlanTarget {
	out := p.PlanArchive(now, partitions, moverAvailable, archiverAvailable)
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
	// sweepAtHour 每日触发时刻（本地整点）；SweepAtHourIntervalTicker 表示回退 ticker 兼容档。
	sweepAtHour int
}

// NewDriver 创建驱动器。disk 为 nil 时只按年龄触发。
//
// 调度口径取 `trigger.sweep_at_hour`（每天该本地整点跑一轮，默认凌晨 3 点）；
// 只有把该键显式配成 -1 时才回退 `trigger.interval` 的间隔 ticker（旧行为）。
// 两者都**不再**复用 `sweep.interval`（删除扫描间隔）：语义不同，共用会变成不可见的调参耦合
// （见 Trigger.Interval 的说明）。
func NewDriver(policy Policy, lister PartitionLister, mover Mover, disk DiskReader) *Driver {
	trig := policy.Trigger.Normalize()
	return &Driver{
		policy:      policy,
		lister:      lister,
		exec:        NewArchiveExecutor(policy, mover),
		disk:        disk,
		interval:    trig.Interval,
		sweepAtHour: trig.SweepAtHour,
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
	trig := p.Trigger.Normalize()
	d.interval = trig.Interval
	d.sweepAtHour = trig.SweepAtHour
}

// Executor 返回内部执行器（观测面读 Stats 用）。
func (d *Driver) Executor() *ArchiveExecutor {
	if d == nil {
		return nil
	}
	return d.exec
}

// Interval 返回调度间隔。
//
// 只在 ticker 兼容档（SweepAtHour = -1）下决定节奏；每日固定时刻调度不看它。
func (d *Driver) Interval() time.Duration {
	if d == nil || d.interval <= 0 {
		return DefaultSweepInterval
	}
	return d.interval
}

// SweepAtHour 返回每日触发时刻（本地整点 0..23）；返回 SweepAtHourIntervalTicker 表示 ticker 兼容档。
func (d *Driver) SweepAtHour() int {
	if d == nil {
		return DefaultSweepAtHour
	}
	return d.sweepAtHour
}

// DescribeSchedule 返回调度口径的一句话说明（启动日志用）。
//
// 为什么要有它：改成每日固定时刻之后，日志里若还只打「interval=15m」，读到的人会以为
// 仍是按分钟级周期在搬——调度口径是本功能的**主要参数**，读日志必须能一眼确认它。
func (d *Driver) DescribeSchedule() string {
	if d == nil {
		return "未装配"
	}
	if h := d.SweepAtHour(); h >= 0 {
		return fmt.Sprintf("每天 %02d:00（进程本地时区 %s）执行一轮", h, localZoneLabel())
	}
	return fmt.Sprintf("按间隔 %s 执行一轮（ticker 兼容档）", d.Interval())
}

// localZoneLabel 返回本地时区标识，取不到名字时退回偏移量。
func localZoneLabel() string {
	if name, _ := time.Now().Zone(); name != "" {
		return name
	}
	// 容器里常拿不到时区名（无 /etc/localtime、无 TZ），退回偏移量——
	// 至少能看出「本地 3 点」与 UTC 差多少，不至于让人以为按 UTC 在跑。
	return time.Now().Format("-07:00")
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

	// 可重试失败优先续跑；仅稳定冷层和真正活动/手工迁移跳过。
	candidates := make([]PartitionRef, 0, len(all))
	for _, part := range all {
		if !part.Retryable && (part.Owner == OwnerColdName || part.InFlight) {
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
	// 两条归档路径的可用性分别传入：冷层断了但归档还通，是必须能表达的真实状态。
	// 归档侧用 ArchiverAvailable()（= 装配了出口 **且** 策略开关打开），
	// 与执行阶段同一判据，避免「计划说能导、执行说不能」。
	var archiverAvailable bool
	if d.exec != nil {
		archiverAvailable = d.exec.ArchiverAvailable()
	}
	plan := d.policy.PlanArchiveWithTrigger(now, candidates, d.exec != nil && d.exec.mover != nil, archiverAvailable, diskPercent)

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

// Run 按调度口径反复执行 Step，直到 ctx 结束。
//
// 调度口径（默认）：**每天在本地时间的 SweepAtHour 整点跑一轮**（默认凌晨 3 点）。
// 只有 SweepAtHour 配成 SweepAtHourIntervalTicker(-1) 时才回退到 Interval 间隔 ticker。
//
// 为什么不是间隔 ticker（见 Trigger.SweepAtHour 的完整说明）：分区按 UTC 天切，
// 一天只产生一个新分区，分钟级/小时级 ticker 的轮次几乎全部空转。
//
// 首轮**立即执行**（不等第一个目标时刻），这是**有意保留**的既有语义，不是没改干净：
//   - 原设计的理由仍然成立且更重要：启动时正是「上一次停机期间攒下的过期分区」最需要
//     处理的时刻；若首轮也要等到明天凌晨 3 点，一次重启就可能让超期分区多压一整天。
//   - 而且迁移失败的重试也需要「启动即续跑」——Retryable 的迁移只有在后续轮次里才会被续跑，
//     等一整天等于把一次失败放大成一天的中断。
//   - 故：**每次进程启动先跑一轮，之后按每日固定时刻跑**。热层因此至多多留一天（一次重启），
//     这是可接受的代价，而「重启即刻恢复」是不可省的。
func (d *Driver) Run(ctx context.Context) {
	if d == nil || !d.policy.Enabled {
		return
	}
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
		if !d.waitNext(ctx, time.Now()) {
			return
		}
	}
}

// waitNext 等到下一个触发时刻，返回 false 表示 ctx 已结束。
//
// 用 time.Timer 而不是 time.Sleep：等待期间必须能被 ctx 及时打断（进程退出、
// 采集运行时重建），否则一天的长等待会让关闭流程挂住最多 24 小时。
func (d *Driver) waitNext(ctx context.Context, now time.Time) bool {
	timer := time.NewTimer(d.untilNext(now))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// untilNext 返回从 now 到下一次触发还有多久。
func (d *Driver) untilNext(now time.Time) time.Duration {
	if h := d.SweepAtHour(); h >= 0 {
		return nextSweepAt(now, h).Sub(now)
	}
	// ticker 兼容档：等价于旧行为的下一次 tick。
	return d.Interval()
}

// SweepWaitBudget 返回「分区到点 → 驱动器真的开始搬」的最坏等待预算（启层余量校验用）。
//
// 每日固定时刻调度下最坏等待是**一整天**：分区若在当天目标时刻后一瞬到点，就要等到明天的整点。
// 余量校验必须按这个上界预留——若仍按 ticker 的 15m 计算，
// 「原生 TTL 只比热窗口多一两天」的配置会通过校验，然后在真机上被 VL 原生淘汰抢先删掉，
// 而这正是这条校验存在的理由。
func (p Policy) SweepWaitBudget() time.Duration {
	trig := p.Trigger.Normalize()
	if trig.SweepAtHour >= 0 {
		return 24 * time.Hour
	}
	return trig.Interval
}

// nextSweepAt 返回 now 之后的下一个 hour 整点（时区跟随 now 的时区）。
//
// 「之后」是**严格**大于：轮次跑完时若正好落在整点（或整点后的极短时间），
// 用严格大于可以避免「刚跑完就立刻再跑一轮」的空转；首轮的立即执行已经覆盖了
// 「启动时刻恰好就是目标时刻」这一场景，故不会漏掉当天的那一轮。
//
// 跨日顺延用 AddDate(0, 0, 1) 而不是 +24h：前者保持墙上时刻的整点不变，
// 在有夏令时的时区里不会漂成 2 点或 4 点。
func nextSweepAt(now time.Time, hour int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
