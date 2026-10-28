// Package startup 记录 Worker 启动过程的分阶段耗时（FR-486）。
//
// ## 为什么需要它
//
// 2026-09-28 生产实测：Worker 重启后 8m39s 才注册上线，期间主控台看不到该节点，
// 且加载全程**没有任何进度日志**——平台对自己的启动过程同样无观测。
// 加载（尤其大索引的 store.Load 全表扫描）发生在注册之前，所以这段时间在 CP 侧是纯黑盒。
//
// 本包只解决「知道慢在哪一段」：把启动切成命名阶段并逐段计时。
// 它是 FR-486 范围②（注册前进度上报）的数据来源——上报的 phase 就取自这里。
//
// ## 为什么不引第三方 tracing
//
// 启动期还没有 CP 上下文，把可观测性绑到 OTel 导出器等于「最需要它的时候恰好不可用」。
// 这里只累积内存中的时长表，输出走 slog，零外部依赖。
package startup

import (
	"sort"
	"sync"
	"time"
)

// Phase 是启动阶段的稳定标识。
//
// 这些字符串会**出现在日志字段与上报契约里**，改动即为对外契约变更，不要随意重命名。
type Phase string

const (
	// PhaseConfig 配置加载与日志装配。
	PhaseConfig Phase = "config"
	// PhaseDataRoot 数据根初始化。
	PhaseDataRoot Phase = "dataroot"
	// PhaseIdentity 本地节点身份读取。
	PhaseIdentity Phase = "identity"
	// PhaseProcessRecover daemon 实例恢复。
	PhaseProcessRecover Phase = "process_recover"
	// PhaseIngestIndexOpen 采集索引打开（含旧 JSON 一次性迁移）。
	PhaseIngestIndexOpen Phase = "ingest_index_open"
	// PhaseIngestStateLoad 采集状态载入——历史瓶颈所在。
	PhaseIngestStateLoad Phase = "ingest_state_load"
	// PhaseRegister 向 CP 注册（含退避重试；与上一段分开计时，避免把退避算进加载）。
	PhaseRegister Phase = "register"
	// PhaseHeartbeat 心跳启动，此后节点可服务。
	PhaseHeartbeat Phase = "heartbeat"
)

// allPhases 是阶段全集，用于校验与汇总时的稳定输出顺序。
var allPhases = []Phase{
	PhaseConfig, PhaseDataRoot, PhaseIdentity, PhaseProcessRecover,
	PhaseIngestIndexOpen, PhaseIngestStateLoad, PhaseRegister, PhaseHeartbeat,
}

// Tracker 累积各阶段耗时。
//
// 并发安全：启动主链路是单 goroutine 串行调用，但 range② 的上报会在阶段切换时
// 从另一个 goroutine 读快照，因此取锁。锁粒度极细（一次 map 写），不构成启动开销。
type Tracker struct {
	mu     sync.Mutex
	start  time.Time
	begun  map[Phase]time.Time
	costs  map[Phase]time.Duration
	order  []Phase // 实际发生顺序（阶段可能因分支跳过）
	closed bool
}

// New 创建追踪器，以调用时刻为启动起点。
func New() *Tracker {
	return &Tracker{
		start: time.Now(),
		begun: make(map[Phase]time.Time, len(allPhases)),
		costs: make(map[Phase]time.Duration, len(allPhases)),
	}
}

// Begin 标记阶段开始。重复 Begin 未 Done 的阶段会被重置（防御性：分支重试场景）。
func (t *Tracker) Begin(p Phase) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.begun[p] = time.Now()
}

// Done 标记阶段结束并记录耗时。未 Begin 过的阶段忽略（不产生伪数据）。
//
// 返回该阶段耗时，便于调用方直接打日志，避免二次查询。
func (t *Tracker) Done(p Phase) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.begun[p]
	if !ok {
		return 0
	}
	d := time.Since(b)
	t.costs[p] = d
	t.order = append(t.order, p)
	delete(t.begun, p)
	return d
}

// Track 是 Begin/Done 的便捷包装：执行 fn 并记录其耗时。
//
// 用于「阶段边界明确、无需在阶段内插入日志」的场景。
func (t *Tracker) Track(p Phase, fn func()) time.Duration {
	t.Begin(p)
	fn()
	return t.Done(p)
}

// Elapsed 返回自启动起的墙钟总耗时。
func (t *Tracker) Elapsed() time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.start)
}

// PhaseCost 返回已记录阶段的耗时；未记录返回 0。
func (t *Tracker) PhaseCost(p Phase) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.costs[p]
}

// Snapshot 是给日志与上报用的稳定快照。
type Snapshot struct {
	// Elapsed 自启动起的总耗时。
	Elapsed time.Duration
	// Costs 已记录阶段的耗时（按发生顺序）。
	Costs []PhaseCost
	// Slowest 最慢阶段；无记录时为空。
	Slowest Phase
	// Tracked 已记录阶段的总耗时（用于与 Elapsed 对账）。
	Tracked time.Duration
}

// PhaseCost 单阶段耗时。
type PhaseCost struct {
	Phase Phase
	Cost  time.Duration
}

// Snapshot 取当前快照。按**发生顺序**返回，便于日志读者还原启动时序。
func (t *Tracker) Snapshot() Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	snap := Snapshot{Elapsed: time.Since(t.start)}
	snap.Costs = make([]PhaseCost, 0, len(t.order))
	for _, p := range t.order {
		c := t.costs[p]
		snap.Costs = append(snap.Costs, PhaseCost{Phase: p, Cost: c})
		snap.Tracked += c
		if c > t.costs[snap.Slowest] {
			snap.Slowest = p
		}
	}
	return snap
}

// AllPhases 返回阶段全集的副本（稳定顺序），供测试断言与文档生成。
func AllPhases() []Phase {
	out := make([]Phase, len(allPhases))
	copy(out, allPhases)
	return out
}

// String 便于日志与上报直接使用。
func (p Phase) String() string { return string(p) }

// SortedSummaries 把快照转成「阶段→毫秒」的字符串对，供 slog 打结构化字段。
//
// 按耗时降序：启动慢时第一眼要看的是「谁最慢」，而不是「谁先跑」。
func (s Snapshot) SortedSummaries() []string {
	items := make([]PhaseCost, len(s.Costs))
	copy(items, s.Costs)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Cost > items[j].Cost })
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Phase.String()+"="+it.Cost.String())
	}
	return out
}
