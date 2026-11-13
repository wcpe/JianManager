package logutil

// Policy 的纯计算派生方法。
//
// 2026-11-10：从已删除的 `logs/retention` 包迁来（archive.go / driver.go）。
// 这些方法只做算术推导，不接触任何存储或执行器，因此随数据模型保留。

import "time"

// MaxHotRetention 返回各级别保留期的最大值。
//
// 为何取**最长**那一档作为整个日分区的热层保留期：
// 日分区里混着所有级别，而搬运的最小粒度就是「一天」。若按最短档搬走，
// 保留期最长的那一级（通常是 ERROR 90d）会被提前请出热层——它在热层
// 「可快查」的承诺就失效了。按最长档搬的代价是 DEBUG 级会跟着多留（成本没省到头），
// 但**承诺不失守**。
func (p Policy) MaxHotRetention() time.Duration {
	n := p.Normalize()
	var max time.Duration
	for _, key := range levelKeys {
		if v := n.ByLevel[key]; v > max {
			max = v
		}
	}
	return max
}

// HotWindow 返回热层保留窗口。显式配置优先，否则按级别 TTL 取最长档。
func (p Policy) HotWindow() time.Duration {
	if p.HotRetention > 0 {
		return p.HotRetention
	}
	return p.MaxHotRetention()
}

// SweepWaitBudget 返回「分区到点 → 驱动器真的开始搬」的最坏等待预算（启层余量校验用）。
//
// 每日固定时刻调度下最坏等待是**一整天**：分区若在当天目标时刻后一瞬到点，
// 就要等到明天的整点。余量校验必须按这个上界预留——若仍按 ticker 的 15m 计算，
// 「原生 TTL 只比热窗口多一两天」的配置会通过校验，然后在真机上被 VL 原生淘汰抢先删掉，
// 而这正是这条校验存在的理由。
func (p Policy) SweepWaitBudget() time.Duration {
	trig := p.Trigger.Normalize()
	if trig.SweepAtHour >= 0 {
		return 24 * time.Hour
	}
	return trig.Interval
}
