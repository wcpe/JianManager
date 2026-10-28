// Package dirusage 周期统计实例工作目录占用，供心跳批量上报（FR-003）。
//
// ## 为什么独立成包、且不放在心跳路径里
//
// 统计一个实例工作目录大小 = 遍历整棵目录树（含 world 区域文件、日志、插件 jar）。
// 数十 GB 的 MC 目录遍历可达数秒到数十秒（见 worker/grpc/instance_resource_snapshot.go
// 的 instanceWorkDirSizeTimeout=10s 预算）。把这件事放进心跳 goroutine 里，就是
// 「心跳周期 = 30s，单拍统计自己吃掉十几秒」——而心跳是 CP 判定节点与实例存活的依据，
// 不能被观测拖垮。
//
// 因此形态与 hostobs（FR-513）、CPU 采样器一致：**独立 goroutine 生产、心跳只读快照**。
// 节拍也刻意与心跳对齐（默认一个心跳周期采一次），心跳不会因为「等不到新值」而阻塞：
// 读到的是最近一次已完成结果，未测到的实例不出现在快照里（CP 侧保持既有值，不回退成 0）。
//
// ## 口径必须与实时链路一致
//
// 本包的 collect 与 instance_resource_snapshot.go 的 instanceWorkDirBytes 是同一口径：
// 只累加**常规文件**尺寸，符号链接/设备文件不计（它们不占实际数据块，计入会虚高）。
// 两处口径若漂移，会出现「配额页显示的用量」与「运行期强制判定的用量」互相矛盾。
package dirusage

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Usage 单个实例的工作目录占用读数。
type Usage struct {
	// Bytes 目录内常规文件尺寸之和（Available=false 时无意义）。
	Bytes int64
	// Available 本次统计是否成功。false 的语义是**「不知道」，不是「占用为 0」**——
	// 消费方必须回退到别的估算口径，按 0 计会低估用量并导致超卖。
	Available bool
}

// Target 一个待统计的实例（由调用方从实例管理器取，本包不反向依赖 process 包）。
type Target struct {
	UUID    string
	WorkDir string
}

// Source 提供当前实例与工作目录列表。
//
// 刻意只给「实例列表」而不给管理器：本包只读，不触发任何实例生命周期动作。
type Source interface {
	// WorkDirTargets 返回当前纳入统计的实例（通常是全部已登记实例，不论运行状态）。
	//
	// 为什么包含未运行实例：停机实例的目录仍占着盘，配额是磁盘用量而不是运行时用量。
	// 漏掉它们会让「停了几个服」凭空释放配额。
	WorkDirTargets() []Target
}

// FuncSource 把任意「取实例列表」的函数适配成 Source（装配层用，避免本包反向依赖实例管理器）。
type FuncSource func() []Target

// WorkDirTargets 实现 Source。
func (f FuncSource) WorkDirTargets() []Target {
	if f == nil {
		return nil
	}
	return f()
}

// Reader 只暴露已发布的快照（与 hostobs.Reader 同款：消费方不触发采集）。
type Reader interface {
	// Usages 返回最近一次完成的统计结果，键为实例 UUID。
	//
	// 返回的 map 是**拷贝**：调用方可以安全遍历，也可以长期持有。
	Usages() map[string]Usage
}

// Options 采集器配置（全部可缺省）。
type Options struct {
	// Interval 采集间隔；缺省 30s（与心跳周期对齐），下限 10s。
	Interval time.Duration
	// Timeout 单实例统计的 budget；缺省 10s（与实时链路 instanceWorkDirSizeTimeout 同量级）。
	Timeout time.Duration
	// Walk 逐目录遍历函数，供测试注入（默认 filepath.WalkDir）。
	Walk func(root string, fn func(path string, d fs.DirEntry, err error) error) error
}

const (
	defaultInterval = 30 * time.Second
	minInterval     = 10 * time.Second
	defaultTimeout  = 10 * time.Second
)

// Collector 周期统计实例工作目录占用。
type Collector struct {
	source   Source
	interval time.Duration
	timeout  time.Duration
	walk     func(root string, fn func(path string, d fs.DirEntry, err error) error) error

	mu   sync.RWMutex
	snap map[string]Usage
}

// New 创建采集器（不启动）。
//
// 参数归一化在这里完成：非法/缺省值回退默认而不报错——观测配置写错不该让 Worker 起不来。
func New(source Source, opts Options) *Collector {
	interval := opts.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	if interval < minInterval {
		// 下限是硬约束：目录遍历是 O(文件数) 的重活，比 10s 更密就是拿全盘扫描换一个
		// 30s 才被消费的数字。
		interval = minInterval
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	walk := opts.Walk
	if walk == nil {
		walk = func(root string, fn func(path string, d fs.DirEntry, err error) error) error {
			return filepath.WalkDir(root, fs.WalkDirFunc(fn))
		}
	}
	return &Collector{
		source:   source,
		interval: interval,
		timeout:  timeout,
		walk: func(root string, fn func(path string, d fs.DirEntry, err error) error) error {
			return filepath.WalkDir(root, fs.WalkDirFunc(fn))
		},
		snap: map[string]Usage{},
	}
}

// Usages 返回最近一次完成的统计结果（拷贝，调用方无需加锁）。
func (c *Collector) Usages() map[string]Usage {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.snap) == 0 {
		return nil
	}
	out := make(map[string]Usage, len(c.snap))
	for k, v := range c.snap {
		out[k] = v
	}
	return out
}

// Start 启动周期采集（首次立刻采一次，使新上线节点不必等满一个周期）。
//
// ctx 取消即停止；返回后采集在后台进行，心跳只读快照。
func (c *Collector) Start(ctx context.Context) {
	if c == nil || c.source == nil {
		return
	}
	go func() {
		c.collectOnce(ctx)
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.collectOnce(ctx)
			}
		}
	}()
}

// collectOnce 采一轮并发布。
//
// 逐实例**串行**统计而不是并发：目录遍历是磁盘 IO 密集的，对同一块盘并发 N 路只会
// 互相争用磁头/队列，总耗时更长，还会挤占正常业务的 IO。串行下单轮耗时约等于各实例
// 之和，而一轮的周期预算（默认 30s）本就按「一轮跑完」设计。
func (c *Collector) collectOnce(ctx context.Context) {
	targets := c.source.WorkDirTargets()
	snap := make(map[string]Usage, len(targets))
	for _, t := range targets {
		if ctx.Err() != nil {
			return // 停机中：不发布半份结果
		}
		if t.UUID == "" {
			continue
		}
		bytes, available := c.measure(ctx, t.WorkDir)
		snap[t.UUID] = Usage{Bytes: bytes, Available: available}
	}
	c.publish(snap)
}

// measure 统计单个目录占用。失败返回 available=false（绝不返回 0 冒充成功）。
//
// 单实例失败不影响其余实例：每个实例独立统计、独立判定，结果各自落进快照。
func (c *Collector) measure(ctx context.Context, dir string) (int64, bool) {
	if dir == "" {
		// 实例无工作目录（极少见，如损坏记录）：不可测，而非占用 0。
		return 0, false
	}
	walkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var total int64
	err := c.walk(dir, func(_ string, entry fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		// 定期检查取消：超大目录树遍历可能持续数十秒，必须在入口处响应超时。
		if err := walkCtx.Err(); err != nil {
			return err
		}
		if entry == nil || entry.IsDir() {
			return nil
		}
		info, ierr := entry.Info()
		if ierr != nil || info == nil {
			// 单个文件读不到（权限/已删除）跳过即可——一个文件读不到不该让整个实例不可测。
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, false
	}
	return total, true
}

// publish 原子替换快照。
//
// 与 hostobs 的策略差异（**刻意**）：hostobs 采集失败时保留上一份有效数据，因为主机指标
// 是「同一批对象的新读数」；而本包按实例独立判定 available，失败实例本身就带着
// Available=false 一起发布——保留旧的「可用」值反而会让 CP 一直看到过期的大小，
// 却以为它是本拍的。故此处整份替换。
func (c *Collector) publish(snap map[string]Usage) {
	c.mu.Lock()
	c.snap = snap
	c.mu.Unlock()
}

// UUIDs 返回快照内实例 UUID 的**稳定排序**列表（测试与排障用；也便于日志对比）。
func (c *Collector) UUIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.snap))
	for k := range c.snap {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
