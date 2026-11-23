package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
)

// sampleLogTailStats 周期性打印新采集器的读数（每 60s 一行）。
//
// # 为何必须有（切默认的前提）
//
// 新采集器的典型故障是「静默无数据」：路径推导错、绑定没到达、VL 拒收——
// 三者都不会报错，表现都是“日志页空了”。旧链路靠 resume diagnostics /
// 持久化耗时等服务面支撑排障；新采集器若一行读数都没有，运维只能猜。
//
// # 为何不每轮都打
//
// 采集轮是秒级（默认 progress 2s），逐轮打会把日志本身刷爆。
// 60s 一行、只打标量，量级可忽。
//
// # 为何 always 打而不是“有异常才打”
//
// 「零采集」本身就是要看的信号：lines=0 持续多轮就意味着没有任何文件被采。
// 只在异常时打会让「一直没采」变成“没什么可报的”。
func sampleLogTailStats(ctx context.Context, runner *logtail.Runner, interval time.Duration) {
	if runner == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	prev := runner.Stats()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := runner.Stats()
			h := runner.Harvesters()
			slog.Info("日志采集读数",
				"harvesters", h,
				"linesDelta", cur.Lines-prev.Lines,
				"writtenDelta", cur.Written-prev.Written,
				"failedDelta", cur.Failed-prev.Failed,
				"linesTotal", cur.Lines,
				"writtenTotal", cur.Written,
				"failedTotal", cur.Failed,
				"unresolvedSourceTotal", cur.UnresolvedSource,
				"gapsTotal", cur.Gaps,
				// 多行合并因 max_lines 丢弃的行数。非零意味着**日志在采集侧被截断**：
				// 日志页看到的堆栈会比应用实际打的短，且不会有任何报错。
				"multilineDroppedTotal", runner.DroppedLines())
			prev = cur
		}
	}
}
