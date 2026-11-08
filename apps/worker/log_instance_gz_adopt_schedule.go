package main

import (
	"context"
	"log/slog"
	"time"
)

// runInstanceGzAdopt 是实例 gz 吸存的每日调度。
//
// 口径与仓库其他每日驱动器一致（保留驱动器、归档对象回收、事件段回收）：
//
//   - **启动首轮立即执行**：停机期间新轮转出来的 .gz 最需要在启动时补上；
//     首轮代价有界（只扫实例 logs/ 目录，且已吸存的文件靠 object_id 迅速幂等跳过）；
//   - 之后每日同一整点执行（本地时区）；
//   - 每轮之间可被 ctx 取消（关停时不等一轮跑完）。
//
// 失败不阻断后续轮次：单文件失败只记账，下轮会重试（幂等保证重试安全）。
func runInstanceGzAdopt(ctx context.Context, adopter *instanceGzAdopter, sweepAtHour int) {
	if adopter == nil {
		return
	}
	for {
		started := time.Now()
		stats, err := adopter.AdoptOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		logAdoptStats(stats, time.Since(started), err)

		next := nextSweepTime(time.Now(), sweepAtHour)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// nextSweepTime 返回「下一个本地整点 hour:00」；若今天已过则取明天。
func nextSweepTime(now time.Time, hour int) time.Time {
	if hour < 0 || hour > 23 {
		hour = 4
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// describeInstanceGzAdoptSchedule 给启动日志一行可读的调度说明。
func describeInstanceGzAdoptSchedule(hour int) string {
	if hour < 0 || hour > 23 {
		hour = 4
	}
	return "启动首轮立即执行，之后每天 " + twoDigit(hour) + ":00（本地时区）"
}

func twoDigit(v int) string {
	if v < 10 {
		return "0" + string(rune('0'+v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}

// logAdoptSchedule 在启动时声明本项是否启用，避免「静默不跑」。
func logAdoptSchedule(enabled bool, hour int) {
	if !enabled {
		slog.Info("实例 gz 吸存未启用（不影响已有副本，也不会删除任何东西）")
		return
	}
	slog.Info("实例 gz 吸存已启用（只增加副本、不动本地 .gz）",
		"schedule", describeInstanceGzAdoptSchedule(hour))
}
