package metrics

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

// CPUSnapshot 是同一采样窗口发布的只读 CPU 指标，占比范围为 0..1。
// 无有效窗口时 Valid=false 且占比为零；现有 RPC 标量仍以零降级，不能表达缺测。
type CPUSnapshot struct {
	CPUUsage float32
	IOWait   float32
	// Breakdown 是 CPU 时间构成（FR-513），各分量占比 0..1、按同一窗口总量归一。
	//
	// 为什么需要它：只给 CPUUsage + IOWait 无法回答「谁在吃 CPU」——
	// iowait 高是 IO 瓶颈、system 高是内核态开销、user 高才是应用自身忙，
	// 三者的处置动作完全不同。此前只能看到聚合值，运维得再登录机器 ps/top。
	Breakdown CPUBreakdown
	Valid     bool
	SampledAt time.Time
	Error     string
}

// CPUBreakdown CPU 时间构成占比（0..1，同一窗口增量归一，各分量之和 ≈ 1）。
//
// 刻意**不含** guest / guest_nice：/proc/stat 有这两列，但它们在非虚拟化宿主恒为 0，
// 加上只会让前端多两个永远为空的行（spec §6 待定 2 的取舍，默认不含）。
type CPUBreakdown struct {
	User    float32
	Nice    float32
	System  float32
	Idle    float32
	IRQ     float32
	SoftIRQ float32
	Steal   float32
}

// CPUReader 只允许消费已发布快照，不暴露累计计数和采样基线。
type CPUReader interface {
	Snapshot() CPUSnapshot
}

// CPUTimeReader 读取全机聚合累计时间；测试可注入计数，不依赖主机负载。
type CPUTimeReader func(context.Context, bool) ([]cpu.TimesStat, error)

// CPUSampler 是 Worker 持有的唯一 CPU 采样 owner，独占时钟、前后样本和窗口。
// 消费者只使用 CPUReader；Sample 仅供 owner 的周期循环与确定性测试驱动。
type CPUSampler struct {
	interval time.Duration
	read     CPUTimeReader
	now      func() time.Time

	sampleMu     sync.Mutex
	previous     cpu.TimesStat
	hasPrevious  bool
	lastAttempt  time.Time
	windowOrigin time.Time
	lastWindow   int64
	hasAttempt   bool
	snapshotMu   sync.RWMutex
	snapshot     CPUSnapshot
	startOnce    sync.Once
	stopOnce     sync.Once
	stopCh       chan struct{}
	doneCh       chan struct{}
	lifecycleMu  sync.Mutex
	started      bool
	cancel       context.CancelFunc
}

// NewCPUSampler 创建未启动的 Worker CPU owner，由主进程统一管理生命周期。
func NewCPUSampler(interval time.Duration) *CPUSampler {
	return NewCPUSamplerWithReader(interval, cpu.TimesWithContext, time.Now)
}

// NewCPUSamplerWithReader 注入累计计数读取与时钟，零或负间隔使用一秒窗口。
func NewCPUSamplerWithReader(interval time.Duration, read CPUTimeReader, now func() time.Time) *CPUSampler {
	if interval <= 0 {
		interval = time.Second
	}
	if read == nil {
		read = cpu.TimesWithContext
	}
	if now == nil {
		now = time.Now
	}
	return &CPUSampler{
		interval: interval, read: read, now: now,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
}

// Snapshot 返回值拷贝，不读系统计数、不推进基线，也不等待正在进行的系统读取。
func (s *CPUSampler) Snapshot() CPUSnapshot {
	// 仅持 snapshotMu 读取值拷贝；不获取 sampleMu，无系统 IO，临界区为固定字段复制。
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return s.snapshot
}

func (s *CPUSampler) publish(snapshot CPUSnapshot) {
	s.snapshotMu.Lock()
	s.snapshot = snapshot
	s.snapshotMu.Unlock()
}

// Sample 每窗口最多读一次累计计数，失败也消耗该窗口，禁止消费者触发重试。
func (s *CPUSampler) Sample(ctx context.Context) {
	// 锁序 sampleMu → snapshotMu；sampleMu 只串行化 owner，不在任何消费路径获取。
	// 持有时长为一次本机累计计数读取（上下文预算一秒）及固定字段差分；快照锁不跨系统 IO。
	s.sampleMu.Lock()
	defer s.sampleMu.Unlock()
	now := s.now()
	if s.hasAttempt {
		if now.Before(s.lastAttempt) {
			s.lastAttempt, s.windowOrigin, s.lastWindow, s.hasPrevious = now, now, 0, false
			s.publish(CPUSnapshot{SampledAt: now, Error: "CPU 采样时钟回退，重新预热"})
			return
		}
		// 按固定窗口去重，避免 ticker 的微小调度抖动让相邻一拍被误认为不足间隔。
		window := int64(now.Sub(s.windowOrigin) / s.interval)
		if window <= s.lastWindow {
			return
		}
		s.lastWindow = window
	} else {
		s.windowOrigin = now
	}
	s.lastAttempt, s.hasAttempt = now, true
	invalid := func(reason string) {
		s.hasPrevious = false
		s.publish(CPUSnapshot{SampledAt: now, Error: reason})
	}
	if err := ctx.Err(); err != nil {
		invalid(fmt.Sprintf("CPU 采样已取消: %v", err))
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	times, err := s.read(readCtx, false)
	if err != nil {
		invalid(fmt.Sprintf("CPU 累计时间读取失败: %v", err))
		return
	}
	if err := readCtx.Err(); err != nil {
		invalid(fmt.Sprintf("CPU 累计时间读取取消或超时: %v", err))
		return
	}
	if len(times) != 1 || !validCPUTimes(times[0]) {
		invalid("CPU 聚合累计时间缺失或无效")
		return
	}
	current := times[0]
	if !s.hasPrevious {
		s.previous, s.hasPrevious = current, true
		s.publish(CPUSnapshot{SampledAt: now, Error: "CPU 首样本预热"})
		return
	}
	previous := s.previous
	s.previous = current
	delta, ok := cpuTimesDelta(current, previous)
	if !ok {
		s.publish(CPUSnapshot{SampledAt: now, Error: "CPU 累计计数回退，重新预热"})
		return
	}
	total := cpuTotal(delta)
	if math.IsNaN(total) || math.IsInf(total, 0) {
		invalid("CPU 窗口无有效增量")
		return
	}
	if total <= 0 {
		s.publish(CPUSnapshot{SampledAt: now, Error: "CPU 窗口无有效增量"})
		return
	}
	// 沿用忙碌口径：idle 和 iowait 均不算 CPU 忙碌；guest 已计入 user/nice，不重复累计。
	busy := delta.User + delta.System + delta.Nice + delta.Irq + delta.Softirq + delta.Steal
	s.publish(CPUSnapshot{
		CPUUsage: float32(busy / total), IOWait: IOWaitRatio(delta),
		// 各分量与 busy/total 用**同一个 total** 归一，保证「各分量之和 ≈ 1」成立；
		// 若各自除以不同分母，前端把构成画成占比条时会出现加起来不到 100% 的怪象。
		Breakdown: CPUBreakdown{
			User:    float32(delta.User / total),
			Nice:    float32(delta.Nice / total),
			System:  float32(delta.System / total),
			Idle:    float32(delta.Idle / total),
			IRQ:     float32(delta.Irq / total),
			SoftIRQ: float32(delta.Softirq / total),
			Steal:   float32(delta.Steal / total),
		},
		Valid: true, SampledAt: now,
	})
}

func cpuTotal(t cpu.TimesStat) float64 {
	return t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
}

func validCPUTimes(t cpu.TimesStat) bool {
	for _, value := range []float64{t.User, t.System, t.Idle, t.Nice, t.Iowait, t.Irq, t.Softirq, t.Steal} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func cpuTimesDelta(current, previous cpu.TimesStat) (cpu.TimesStat, bool) {
	delta := cpu.TimesStat{
		User: current.User - previous.User, System: current.System - previous.System,
		Idle: current.Idle - previous.Idle, Nice: current.Nice - previous.Nice,
		Iowait: current.Iowait - previous.Iowait, Irq: current.Irq - previous.Irq,
		Softirq: current.Softirq - previous.Softirq, Steal: current.Steal - previous.Steal,
	}
	return delta, validCPUTimes(delta)
}

// Start 同步读取首个预热样本后启动唯一周期循环，重复调用不会新增采样者。
func (s *CPUSampler) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		s.lifecycleMu.Lock()
		ctx, s.cancel = context.WithCancel(ctx)
		s.started = true
		s.lifecycleMu.Unlock()
		select {
		case <-s.stopCh:
			s.cancel()
		default:
		}
		s.Sample(ctx)
		go func() {
			defer close(s.doneCh)
			ticker := time.NewTicker(s.interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-s.stopCh:
					return
				case <-ticker.C:
					s.Sample(ctx)
				}
			}
		}()
	})
}

// Stop 幂等停止 owner，并等待周期循环退出；读取器遵循传入的取消上下文。
func (s *CPUSampler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.lifecycleMu.Lock()
	started, cancel := s.started, s.cancel
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		<-s.doneCh
	}
}
