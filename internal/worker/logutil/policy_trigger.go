package logutil

// 搬运触发与归档对象保留的策略数据模型。
// 2026-11-10：从已删除的 logs/retention 包迁来（只迁数据模型，执行器随冷热分层一并删除）。

import (
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

// 反过来，`List` 返回**错误**（读不出来）必须算失败——「读不出来」不等于「里面没东西」。

// ArchiveObjectRetentionPolicy 是归档对象自身回收的策略面。
//
// 保留期从**归档时刻**（ArchiveEvidence.ArchivedAt）起算，而不是从热层回收时刻：
// 运维配 `730d` 的语义是「这份归档副本留两年」，起点必须是它诞生那一刻。
type ArchiveObjectRetentionPolicy struct {
	// Enabled 是否允许按保留期清除归档对象（默认 false）。
	Enabled bool `mapstructure:"enabled"`
	// Retention 归档对象保留期；<=0 表示未启用（即使 Enabled 为真也不动）。
	Retention time.Duration `mapstructure:"retention"`
	// SweepAtHour 每日触发的本地整点（0..23）；越界回退 DefaultSweepAtHour。
	SweepAtHour int `mapstructure:"sweep_at_hour"`
}

// Active 报告本策略是否真的会清除归档对象。
//
// 两个条件的**与**，缺一不可：只开开关而不给保留期 ⇒ 没有「到期」这个概念，
// 删任何一天都是在猜；只给保留期而不开开关 ⇒ 默认关是用户护栏，不可默认打开。
// 配置层已对「开了开关但保留期为空」启动即拒（见 internal/worker 的
// archive_object_retention 校验），这里的判定是运行期最后一道，防止热更绕过。
func (p ArchiveObjectRetentionPolicy) Active() bool {
	return p.Enabled && p.Retention > 0
}

// Normalize 补齐缺省（越界小时数回退默认：配错就按默认时刻跑，而不是半夜 0 点跑）。
func (p ArchiveObjectRetentionPolicy) Normalize() ArchiveObjectRetentionPolicy {
	out := p
	if out.SweepAtHour < 0 || out.SweepAtHour > 23 {
		out.SweepAtHour = DefaultSweepAtHour
	}
	return out
}
