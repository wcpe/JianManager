package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// startupPhase 是启动阶段计时里的一个可观测节点（FR-486）。
type startupPhase struct {
	Name      string
	Elapsed   time.Duration // 自启动起算的累计耗时
	SinceLast time.Duration // 本阶段自身耗时
}

// startupTimer 记录 Worker 启动各阶段的耗时（FR-486）。
//
// # 为什么需要它
//
// 启动慢时，运维只能看到「服务半天没起来」，判断不出卡在哪：
// 是配置解析、WL 资产校验、还是采集运行时建管道？
// 没有分段计时，排障只能靠加日志重启一遍。
//
// # 与旧设计（已废）的区别
//
// FR-486 原本还包含「加载索引分段计时/流式加载优化」。索引库改 `:memory:`
// 后重启就是空库，「加载索引」这个阶段不存在了，所以那部分已经删掉。
// 现在计的是真实存在的阶段。
//
// # 并发安全
//
// 启动是单协程路径，但 Mark 可能被并发调用（如后台预热的 goroutine）
// 所以用 mutex 而非普通字段。
type startupTimer struct {
	mu     sync.Mutex
	start  time.Time
	last   time.Time
	phases []startupPhase
}

func newStartupTimer() *startupTimer {
	now := time.Now()
	return &startupTimer{start: now, last: now}
}

// Mark 记一个阶段的结束，返回自启动起算的累计时长。
func (t *startupTimer) Mark(name string) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.phases = append(t.phases, startupPhase{
		Name:      name,
		Elapsed:   now.Sub(t.start),
		SinceLast: now.Sub(t.last),
	})
	t.last = now
	return now.Sub(t.start)
}

// startupSlowThreshold 是「启动阶段超阈」的阈值（FR-486）。
//
// 取值依据：生产实测本机冷启动约 3-8 秒（含 VL 资产校验）。
// 取值 30s 是「已经慢了但还没到故障」的量级；
// 超过它通常意味着磁盘慢或外部依赖卡住，值得让运维看见。
const startupSlowThreshold = 30 * time.Second

// Report 输出启动分段耗时。超阈时用 Warn 并点名最慢的阶段。
func (t *startupTimer) Report() {
	if t == nil {
		return
	}
	t.mu.Lock()
	phases := make([]startupPhase, len(t.phases))
	copy(phases, t.phases)
	total := time.Since(t.start)
	t.mu.Unlock()

	if len(phases) == 0 {
		return
	}

	// 找出最慢的一段：报告里最有用的就是它。
	slowest := phases[0]
	for _, p := range phases {
		if p.SinceLast > slowest.SinceLast {
			slowest = p
		}
	}

	level := slog.LevelInfo
	if total >= startupSlowThreshold {
		level = slog.LevelWarn
	}

	attrs := make([]any, 0, len(phases)*3+6)
	attrs = append(attrs, "totalMs", float64(total.Microseconds())/1000.0)
	for _, p := range phases {
		attrs = append(attrs,
			p.Name+"Ms", float64(p.SinceLast.Microseconds())/1000.0,
			p.Name+"AtMs", float64(p.Elapsed.Microseconds())/1000.0,
		)
	}
	attrs = append(attrs,
		"slowestPhase", slowest.Name,
		"slowestMs", float64(slowest.SinceLast.Microseconds())/1000.0,
	)

	slog.Log(context.Background(), level, "Worker 启动分段耗时（FR-486）", attrs...)
}
