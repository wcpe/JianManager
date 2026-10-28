// Package hostobs 采集主机级观测数据（FR-513）。
//
// ## 为什么单独成包而不是堆进 heartbeat
//
// 这里的两项采集都是**潜在的重活**：整机进程扫描是 O(全部进程)，分挂载点要对每个
// 挂载点各 statfs 一次。它们若直接跑在心跳 goroutine 里，会让心跳本身变慢——
// 而心跳是 CP 判断节点存活和实例状态的依据，不能被观测拖累。
//
// 因此形态与既有 CPUSampler 一致：**独立 goroutine 生产、心跳只读快照**。
//
// ## 采集开销约束（spec §5 验收 4）
//
// 整机进程扫描节流到 ≤1 次/10s（可配，下限 10s）。默认与心跳 30s 周期对齐——
// 「每次心跳最多采一次」已经够用，更密只会白烧 CPU 去回答一个 30s 才被消费的问题。
package hostobs

import (
	"context"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/process"
)

// ProcessSample 整机 top-N 进程的一项。
//
// 隐私边界：**只有 comm，没有 cmdline**（spec §2「不做」明确要求）。
// cmdline 常含密码、token、内网地址；而「谁在吃 CPU」这个判断只需要进程名。
type ProcessSample struct {
	PID      uint32
	Comm     string
	CPUPct   float32
	RSSBytes uint64
}

// MountUsage 一个挂载点的磁盘用量。
type MountUsage struct {
	Mountpoint string
	TotalBytes uint64
	UsedBytes  uint64
	UsedPct    float32
}

// Snapshot 一次采集的完整结果。
type Snapshot struct {
	TopProcesses []ProcessSample
	Mounts       []MountUsage
	Valid        bool
	SampledAt    time.Time
	Error        string
}

// Reader 只暴露已发布的快照（与 CPUSampler 同款：消费方不触发采集）。
type Reader interface {
	Snapshot() Snapshot
}

// Collector 周期采集主机级观测。
type Collector struct {
	topN     int
	interval time.Duration
	// mounts 是挂载点白名单；空=自动发现物理分区。
	mounts []string

	// partitions 与 usage 允许测试注入，避免依赖真实主机（CI 上 /var 未必独立分区）。
	partitions func(all bool) ([]disk.PartitionStat, error)
	usage      func(path string) (*disk.UsageStat, error)
	processes  func() ([]*process.Process, error)

	mu   sync.RWMutex
	snap Snapshot
}

// Options 采集器配置（全部可缺省）。
type Options struct {
	// TopN 取前 N 个进程（默认 5，上限 10）。
	TopN int
	// Interval 采集间隔（默认 10s，下限 10s）。
	Interval time.Duration
	// Mounts 挂载点白名单；空=自动发现。
	Mounts []string
}

const (
	defaultTopN     = 5
	maxTopN         = 10
	defaultInterval = 10 * time.Second
	minInterval     = 10 * time.Second
)

// 伪文件系统前缀：这些不是真实磁盘，采集它们的用量只会产生噪声
// （tmpfs 用量随内存波动、overlay 在容器里指向镜像层、proc/sys 恒为 0）。
var pseudoFSTypes = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "proc": true, "sysfs": true,
	"cgroup": true, "cgroup2": true, "overlay": true, "squashfs": true,
	"devpts": true, "securityfs": true, "debugfs": true, "tracefs": true,
	"pstore": true, "bpf": true, "autofs": true, "mqueue": true,
	"hugetlbfs": true, "configfs": true, "fusectl": true, "binfmt_misc": true,
	"nsfs": true, "rpc_pipefs": true, "selinuxfs": true,
}

// New 创建采集器（不启动）。
//
// 参数归一化在这里完成：非法/缺省值回退默认，而不是报错——
// 观测配置写错不该让 Worker 起不来。
func New(opts Options) *Collector {
	topN := opts.TopN
	if topN <= 0 {
		topN = defaultTopN
	}
	if topN > maxTopN {
		topN = maxTopN
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	if interval < minInterval {
		// 下限是硬约束：再密就是拿全表扫描换一个 30s 才消费的数字。
		interval = minInterval
	}
	return &Collector{
		topN:       topN,
		interval:   interval,
		mounts:     opts.Mounts,
		partitions: disk.Partitions,
		usage:      disk.Usage,
		processes:  process.Processes,
	}
}

// Snapshot 返回最近一次采集结果。
func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap
}

// Start 启动周期采集。返回后采集在后台进行，心跳只读快照。
//
// 首次立刻采一次：否则心跳在第一个 interval 内拿不到数据，
// 而节点刚上线时的主机状态恰恰是最该被看到的。
func (c *Collector) Start(ctx context.Context) {
	if c == nil {
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

// collectOnce 采一次并发布。
func (c *Collector) collectOnce(ctx context.Context) {
	snap := Snapshot{SampledAt: time.Now()}
	procs, perr := c.collectProcesses(ctx)
	mounts, merr := c.collectMounts()
	switch {
	case perr != nil && merr != nil:
		snap.Error = "进程与磁盘采集均失败"
		c.publish(snap)
		return
	case perr != nil:
		snap.Error = "进程采集失败"
	case merr != nil:
		snap.Error = "磁盘采集失败"
	}
	snap.TopProcesses = procs
	snap.Mounts = mounts
	snap.Valid = true
	c.publish(snap)
}

func (c *Collector) publish(s Snapshot) {
	c.mu.Lock()
	// 采集失败时保留上一份有效数据，只更新错误与时间戳。
	// 否则一次瞬时失败会让面板瞬间清空，比显示略旧的数据更糟。
	if !s.Valid && c.snap.Valid {
		s.TopProcesses = c.snap.TopProcesses
		s.Mounts = c.snap.Mounts
	}
	c.snap = s
	c.mu.Unlock()
}

// collectProcesses 采整机 top-N 进程。
func (c *Collector) collectProcesses(ctx context.Context) ([]ProcessSample, error) {
	procs, err := c.processes()
	if err != nil {
		return nil, err
	}
	out := make([]ProcessSample, 0, len(procs))
	for _, p := range procs {
		// 单个进程取信息失败（权限不足/已退出）跳过即可——
		// 一个进程读不到不该让整次采集失败。
		name, nerr := p.Name()
		if nerr != nil || name == "" {
			continue
		}
		cpuPct, cerr := p.CPUPercent()
		if cerr != nil {
			cpuPct = 0 // 进程刚启动时首次采样可能失败，按 0 处理而不是丢弃
		}
		mem, merr := p.MemoryInfo()
		var rss uint64
		if merr == nil && mem != nil {
			rss = mem.RSS
		}
		out = append(out, ProcessSample{
			PID: uint32(p.Pid), Comm: name, CPUPct: float32(cpuPct), RSSBytes: rss,
		})
	}
	// 按 CPU 降序取前 N。用插入式选择避免全排序（进程数上万时排序本身也是开销）。
	if len(procs) > 0 {
		sortByCPUTopN(out, c.topN)
		if len(out) > c.topN {
			out = out[:c.topN]
		}
	}
	return out, nil
}

// sortByCPUTopN 把前 n 大的元素放到切片头部（其余顺序不重要）。
func sortByCPUTopN(items []ProcessSample, n int) {
	if n <= 0 || len(items) == 0 {
		return
	}
	for i := 0; i < n && i < len(items); i++ {
		maxIdx := i
		for j := i + 1; j < len(items); j++ {
			if items[j].CPUPct > items[maxIdx].CPUPct {
				maxIdx = j
			}
		}
		if maxIdx != i {
			items[i], items[maxIdx] = items[maxIdx], items[i]
		}
	}
}

// collectMounts 采分挂载点磁盘用量。
func (c *Collector) collectMounts() ([]MountUsage, error) {
	parts, err := c.partitions(false)
	if err != nil {
		return nil, err
	}
	// 白名单优先：容器部署时自动发现可能看到宿主挂载，白名单能把它收窄。
	targets := c.mounts
	if len(targets) == 0 {
		seen := make(map[string]bool, len(parts))
		for _, p := range parts {
			if pseudoFSTypes[p.Fstype] || p.Mountpoint == "" {
				continue
			}
			if seen[p.Mountpoint] {
				continue
			}
			seen[p.Mountpoint] = true
			targets = append(targets, p.Mountpoint)
		}
	}

	out := make([]MountUsage, 0, len(targets))
	for _, mp := range targets {
		st, uerr := c.usage(mp)
		if uerr != nil || st == nil {
			continue
		}
		out = append(out, MountUsage{
			Mountpoint: mp,
			TotalBytes: st.Total,
			UsedBytes:  st.Used,
			UsedPct:    float32(st.UsedPercent),
		})
	}
	return out, nil
}
