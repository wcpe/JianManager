package sampling

import (
	"fmt"
	"strings"
	"time"
)

// 默认值。默认一律「不启用」：本包的引入必须是零行为变化，
// 没有显式配置就不得改变任何一条日志的去留。
const (
	// DefaultBurstWindow 同源同消息计数窗口。
	DefaultBurstWindow = time.Second
	// DefaultBurstThreshold 窗口内前 N 条放行原文，之后折叠。
	DefaultBurstThreshold = 20
	// DefaultMaxSignatures 模式表上限（有界：绝不让采样自身成为无界增长点）。
	DefaultMaxSignatures = 4096
	// DefaultBudgetWindow 每源预算的统计窗口。
	DefaultBudgetWindow = time.Second
	// DefaultKeepEvery 预算用尽后的采样比（每 N 条保留 1 条原文）。
	DefaultKeepEvery = 10
	// DefaultDegradeHold 触发信号消失后，需连续保持这么久才恢复。
	DefaultDegradeHold = 30 * time.Second
	// DefaultMaxAggregateEvents 单条汇总事件最多承接多少条被抑制的原文。
	// 上限存在的理由：等级过滤是「按级别整批折叠」，若不给上限，一个长期只有 DEBUG
	// 输出的源会攒出一条跨度数小时的汇总，查询面反而看不到「哪一段被压掉了」。
	DefaultMaxAggregateEvents = 4096
	// maxKeepEvery 采样比上界：再大就等于「只剩汇总」，没有继续拉大的意义，
	// 且能防住把 1/100000 配成事实上的静默丢弃。
	maxKeepEvery = 10000
)

// 抑制规则名。写进汇总事件的字段与观测计数，是「为什么要折叠」的唯一来源。
const (
	RuleLevelFilter   = "level_filter"
	RuleBurstSuppress = "burst_suppress"
	RuleBudgetSample  = "budget_sample"
	RuleStormDegrade  = "storm_degrade"
)

// LevelFilter 按级别抑制（等级过滤）。
type LevelFilter struct {
	// MinLevel 保留该级别及以上的原文；低于它的折叠为汇总事件。
	// 空串 = 不启用（零行为变化）。无法判定的级别一律放行（见 Rank）。
	MinLevel string `mapstructure:"min_level"`
}

// Burst 同源同消息高频抑制。
type Burst struct {
	// Window 计数窗口；<=0 表示不启用。
	Window time.Duration `mapstructure:"window"`
	// Threshold 窗口内允许原文落库的条数；超出部分折叠为汇总事件。
	// <=0 视为 DefaultBurstThreshold。
	Threshold int `mapstructure:"threshold"`
	// MaxSignatures 模式表上限；<=0 视为 DefaultMaxSignatures。
	MaxSignatures int `mapstructure:"max_signatures"`
}

// Budget 每源预算（quota → 采样比例）。
type Budget struct {
	// MaxEventsPerWindow 窗口内允许原文落库的事件数；超出后按 KeepEvery 采样。
	// <=0 表示不启用。
	MaxEventsPerWindow int `mapstructure:"max_events_per_window"`
	// Window 统计窗口；<=0 视为 DefaultBudgetWindow。
	Window time.Duration `mapstructure:"window"`
	// KeepEvery 预算用尽后的采样比：每 N 条保留 1 条原文，其余折叠。
	// <=0 视为 DefaultKeepEvery。
	KeepEvery int `mapstructure:"keep_every"`
}

// Degrade 风暴自动降级。
type Degrade struct {
	// Enabled 开关。关闭时不降级（默认关闭 = 零行为变化）。
	Enabled bool `mapstructure:"enabled"`
	// TargetLevel 降级目标级别：降级期间只保留该级别及以上的原文。
	// 空串视为 ERROR（压到 error-only）。
	TargetLevel string `mapstructure:"target_level"`
	// DiskPercent 触发阈值：节点磁盘使用率达到即降级。<=0 视为默认 80（与
	// acquire 契约的 DegradedAtPercent 同口径）。
	DiskPercent float64 `mapstructure:"disk_percent"`
	// BacklogBytes 触发阈值：WAL 积压字节达到即降级。<=0 表示不以此触发。
	BacklogBytes uint64 `mapstructure:"backlog_bytes"`
	// Hold 触发信号消失后连续保持多久才恢复；<=0 视为 DefaultDegradeHold。
	Hold time.Duration `mapstructure:"hold"`
}

// Policy 采集侧采样与降级策略（纯配置，不含运行状态）。
type Policy struct {
	// Enabled 总开关。false 时 Process 恒等返回入参（零行为变化）。
	Enabled bool `mapstructure:"enabled"`
	Level   LevelFilter
	Burst   Burst
	Budget  Budget
	Degrade Degrade
	// MaxAggregateEvents 单条汇总事件承接的原文条数上限；<=0 视为 DefaultMaxAggregateEvents。
	MaxAggregateEvents int `mapstructure:"max_aggregate_events"`
}

// Normalize 把 <=0 / 空串的字段补齐为默认值，返回补齐后的副本。
// 不做合法性判定（那是 Validate 的职责），因此可安全用于「只填了一半」的配置。
func (p Policy) Normalize() Policy {
	if p.MaxAggregateEvents <= 0 {
		p.MaxAggregateEvents = DefaultMaxAggregateEvents
	}
	if p.Burst.Window > 0 {
		if p.Burst.Threshold <= 0 {
			p.Burst.Threshold = DefaultBurstThreshold
		}
		if p.Burst.MaxSignatures <= 0 {
			p.Burst.MaxSignatures = DefaultMaxSignatures
		}
	}
	if p.Budget.MaxEventsPerWindow > 0 {
		if p.Budget.Window <= 0 {
			p.Budget.Window = DefaultBudgetWindow
		}
		if p.Budget.KeepEvery <= 0 {
			p.Budget.KeepEvery = DefaultKeepEvery
		}
	}
	if p.Degrade.Enabled {
		if strings.TrimSpace(p.Degrade.TargetLevel) == "" {
			p.Degrade.TargetLevel = LevelNameError
		}
		if p.Degrade.DiskPercent <= 0 {
			p.Degrade.DiskPercent = 80
		}
		if p.Degrade.Hold <= 0 {
			p.Degrade.Hold = DefaultDegradeHold
		}
	}
	return p
}

// Active 报告策略是否会改变任何一条日志的去留。
func (p Policy) Active() bool {
	if !p.Enabled {
		return false
	}
	n := p.Normalize()
	return n.Level.MinLevel != "" || n.Burst.Window > 0 || n.Budget.MaxEventsPerWindow > 0
}

// Validate 校验策略取值。
//
// 为什么非法值不静默回退：采样配错的后果与 timezone/charset 同类——日志**静默少存**，
// 且现场只会表现为「查不到」，不会表现为报错。宁可启动即拒，让运维在部署时发现。
//
// 校验读的是**原始取值**而不是 Normalize 之后的取值：Normalize 会把 0 补成默认值，
// 若在它之后校验，「阈值填 -1」这种明确写错的配置会被悄悄当成「没填」，校验就成了死代码。
// 因此这里的口径是：0 = 没填（走默认），负数 = 写错（拒绝）。
func (p Policy) Validate() error {
	if !p.Enabled {
		return nil
	}
	if p.Level.MinLevel != "" && Rank(p.Level.MinLevel) == rankUnknown {
		return fmt.Errorf("sampling: 未知的 level.min_level %q（支持 TRACE/DEBUG/INFO/WARN/ERROR）", p.Level.MinLevel)
	}
	if p.Burst.Window < 0 {
		return fmt.Errorf("sampling: burst.window 不能为负，得到 %s", p.Burst.Window)
	}
	if p.Burst.Window > 0 {
		if p.Burst.Window < time.Millisecond {
			return fmt.Errorf("sampling: burst.window 过小 %s（最小 1ms）", p.Burst.Window)
		}
		if p.Burst.Threshold < 0 {
			return fmt.Errorf("sampling: burst.threshold 不能为负，得到 %d（留空/0 表示用默认值 %d）",
				p.Burst.Threshold, DefaultBurstThreshold)
		}
		if p.Burst.MaxSignatures < 0 {
			return fmt.Errorf("sampling: burst.max_signatures 不能为负，得到 %d", p.Burst.MaxSignatures)
		}
	}
	if p.Budget.Window < 0 {
		return fmt.Errorf("sampling: budget.window 不能为负，得到 %s", p.Budget.Window)
	}
	if p.Budget.MaxEventsPerWindow < 0 {
		return fmt.Errorf("sampling: budget.max_events_per_window 不能为负，得到 %d", p.Budget.MaxEventsPerWindow)
	}
	if p.Budget.MaxEventsPerWindow > 0 {
		if p.Budget.KeepEvery < 0 {
			return fmt.Errorf("sampling: budget.keep_every 不能为负，得到 %d", p.Budget.KeepEvery)
		}
		if p.Budget.KeepEvery > maxKeepEvery {
			return fmt.Errorf("sampling: budget.keep_every=%d 过大（上限 %d）——再大就不是采样而是静默丢弃，请改用等级过滤",
				p.Budget.KeepEvery, maxKeepEvery)
		}
	}
	if p.MaxAggregateEvents < 0 {
		return fmt.Errorf("sampling: max_aggregate_events 不能为负，得到 %d", p.MaxAggregateEvents)
	}
	if p.Degrade.Enabled {
		if p.Degrade.TargetLevel != "" && Rank(p.Degrade.TargetLevel) == rankUnknown {
			return fmt.Errorf("sampling: 未知的 degrade.target_level %q", p.Degrade.TargetLevel)
		}
		if p.Degrade.DiskPercent < 0 || p.Degrade.DiskPercent > 100 {
			return fmt.Errorf("sampling: degrade.disk_percent 必须在 0..100，得到 %v", p.Degrade.DiskPercent)
		}
		if p.Degrade.Hold < 0 {
			return fmt.Errorf("sampling: degrade.hold 不能为负，得到 %s", p.Degrade.Hold)
		}
	}
	return nil
}

// Describe 返回策略摘要（启动日志与观测面用；便于现场一眼确认「配的到底是什么」）。
func (p Policy) Describe() string {
	if !p.Enabled {
		return "disabled"
	}
	n := p.Normalize()
	parts := make([]string, 0, 4)
	if n.Level.MinLevel != "" {
		parts = append(parts, "minLevel="+n.Level.MinLevel)
	}
	if n.Burst.Window > 0 {
		parts = append(parts, fmt.Sprintf("burst=%d/%s", n.Burst.Threshold, n.Burst.Window))
	}
	if n.Budget.MaxEventsPerWindow > 0 {
		parts = append(parts, fmt.Sprintf("budget=%d/%s keepEvery=%d",
			n.Budget.MaxEventsPerWindow, n.Budget.Window, n.Budget.KeepEvery))
	}
	if n.Degrade.Enabled {
		parts = append(parts, fmt.Sprintf("degrade→%s@disk%v%% hold=%s",
			n.Degrade.TargetLevel, n.Degrade.DiskPercent, n.Degrade.Hold))
	}
	if len(parts) == 0 {
		return "enabled(no-rule)"
	}
	return "enabled " + strings.Join(parts, " ")
}
