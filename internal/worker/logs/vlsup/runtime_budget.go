package vlsup

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DefaultColdCacheBytes 避免冷层回退到宿主内存百分比，不是性能调大。
	DefaultColdCacheBytes int64 = 128 << 20
	// DefaultRehydrateCacheBytes 为默认关闭的恢复层保留显式有限cache模板。
	DefaultRehydrateCacheBytes int64 = 128 << 20
)

// NamespaceBudget 明确区分内部cache、Go软限制和RSS判据；三者不能互相冒充硬限制。
type NamespaceBudget struct{ CacheBytes, GoMemoryLimitBytes, RSSBytes int64 }

// DefaultNamespaceBudgets 每次返回独立map，不按宿主可见内存推导冷层cache。
func DefaultNamespaceBudgets() map[Namespace]NamespaceBudget {
	return map[Namespace]NamespaceBudget{
		NamespaceHot:       {CacheBytes: DefaultHotCacheBytes, GoMemoryLimitBytes: DefaultVLMemoryLimitBytes},
		NamespaceCold:      {CacheBytes: DefaultColdCacheBytes, GoMemoryLimitBytes: DefaultVLMemoryLimitBytes},
		NamespaceRehydrate: {CacheBytes: DefaultRehydrateCacheBytes, GoMemoryLimitBytes: DefaultVLMemoryLimitBytes},
	}
}

// ReservationKind 只允许已知的短生命周期磁盘预留种类。
type ReservationKind string

const (
	// ReservationWAL 计量写前WAL责任预留，不影响存量WAL排空。
	ReservationWAL ReservationKind = "wal"
	// ReservationStaging 计量物化和归档恢复的新增副本空间。
	ReservationStaging ReservationKind = "staging"
	// ReservationExport 计量有界临时导出空间。
	ReservationExport ReservationKind = "export"
)

// RuntimeBudget 将磁盘容量与聚合RSS分别建模，预留仅从磁盘容量扣除。
type RuntimeBudget struct {
	Namespaces          map[Namespace]NamespaceBudget
	DiskBytes, RSSBytes int64
	Reservations        map[ReservationKind]int64
}

// NamespaceSample 的未知状态不能用RSS=0伪装；停服则明确known且为0。
type NamespaceSample struct {
	RSSBytes int64
	RSSKnown bool
}

// ResourceSample 同时保留数据根字节数和文件系统水位，口径不能互换。
type ResourceSample struct {
	Namespaces       map[Namespace]NamespaceSample
	WorkerRSSBytes   int64
	WorkerRSSKnown   bool
	DiskUsagePercent float64
	DiskKnown        bool
	DiskUsedBytes    int64
	DiskBytesKnown   bool
	SampledAt        time.Time
}

// WorkClass 区分受压时可拒绝的新成本与必须继续保留的WAL排空责任。
type WorkClass string

const (
	// WorkForeground 保留有限查询，明确RSS超限或90%磁盘水位时拒绝新增。
	WorkForeground WorkClass = "foreground"
	// WorkBackground 在未知或80%水位时暂停新增物化/校验成本。
	WorkBackground WorkClass = "background"
	// WorkWALDrain 保留已有WAL的有界排空，不代表批准新的空间预留。
	WorkWALDrain WorkClass = "wal_drain"
)

// ErrBudgetAdmission 使调用方能把预算拒绝映射为可重试而非数据丢弃。
var ErrBudgetAdmission = errors.New("vlsup: 资源预算拒绝准入")

// BudgetController 只控制新工作和预留；不会停止VL、删除数据或取消已有WAL责任。
type BudgetController struct {
	mu       sync.Mutex
	policy   RuntimeBudget
	sample   ResourceSample
	reserved map[ReservationKind]int64
}

// NewBudgetController 校验并复制预算；零上界保持兼容，不声称该维已强制限制。
func NewBudgetController(policy RuntimeBudget) (*BudgetController, error) {
	if policy.DiskBytes < 0 || policy.RSSBytes < 0 {
		return nil, fmt.Errorf("vlsup: 磁盘/RSS预算不能为负")
	}
	copyPolicy := RuntimeBudget{DiskBytes: policy.DiskBytes, RSSBytes: policy.RSSBytes, Namespaces: map[Namespace]NamespaceBudget{}, Reservations: map[ReservationKind]int64{}}
	for ns, b := range policy.Namespaces {
		if !ValidNamespace(ns) || b.CacheBytes < 0 || b.RSSBytes < 0 {
			return nil, fmt.Errorf("vlsup: 非法namespace预算 %q", ns)
		}
		copyPolicy.Namespaces[ns] = b
	}
	var sum int64
	for k, n := range policy.Reservations {
		if !validReservation(k) || n < 0 || n > math.MaxInt64-sum {
			return nil, fmt.Errorf("vlsup: 非法磁盘预留 %q", k)
		}
		sum += n
		copyPolicy.Reservations[k] = n
	}
	if sum > 0 && (policy.DiskBytes == 0 || sum > policy.DiskBytes/4) {
		return nil, fmt.Errorf("vlsup: WAL+staging+export预留不能超过磁盘预算25%%")
	}
	return &BudgetController{policy: copyPolicy, reserved: map[ReservationKind]int64{}}, nil
}

func validReservation(k ReservationKind) bool {
	return k == ReservationWAL || k == ReservationStaging || k == ReservationExport
}

// Update 复制采样；锁序仅controller.mu，不跨IO，临界区最多3个namespace。
func (c *BudgetController) Update(sample ResourceSample) {
	if c == nil {
		return
	}
	copySample := sample
	copySample.Namespaces = map[Namespace]NamespaceSample{}
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		if v, ok := sample.Namespaces[ns]; ok {
			copySample.Namespaces[ns] = v
		}
	}
	if copySample.SampledAt.IsZero() {
		copySample.SampledAt = time.Now()
	}
	c.mu.Lock()
	c.sample = copySample
	c.mu.Unlock()
}

// admissionLocked 不把未知采样当零；未知/降级只挡后台，前台仍由自身窗口/行字节限制约束。
func (c *BudgetController) admissionLocked(work WorkClass) error {
	if work == WorkWALDrain {
		return nil
	}
	if work != WorkForeground && work != WorkBackground {
		return fmt.Errorf("%w: 未知工作类别", ErrBudgetAdmission)
	}
	s := c.sample
	unknown := !s.WorkerRSSKnown || s.WorkerRSSBytes < 0 || !s.DiskKnown || math.IsNaN(s.DiskUsagePercent) || math.IsInf(s.DiskUsagePercent, 0) || s.DiskUsagePercent < 0 || s.DiskUsagePercent > 100 || s.SampledAt.IsZero() || time.Since(s.SampledAt) > 2*time.Minute
	overRSS := false
	total := s.WorkerRSSBytes
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		n := s.Namespaces[ns]
		if !n.RSSKnown || n.RSSBytes < 0 {
			unknown = true
			continue
		}
		if n.RSSBytes > math.MaxInt64-total {
			overRSS = true
		} else {
			total += n.RSSBytes
		}
		if limit := c.policy.Namespaces[ns].RSSBytes; limit > 0 && n.RSSBytes > limit {
			overRSS = true
		}
	}
	if c.policy.RSSBytes > 0 && total > c.policy.RSSBytes {
		overRSS = true
	}
	if overRSS {
		return fmt.Errorf("%w: RSS超过独立内存预算", ErrBudgetAdmission)
	}
	if s.DiskKnown && s.DiskUsagePercent >= 90 {
		return fmt.Errorf("%w: 文件系统达到90%%暂停水位", ErrBudgetAdmission)
	}
	if c.policy.DiskBytes > 0 && s.DiskBytesKnown && s.DiskUsedBytes > c.policy.DiskBytes {
		return fmt.Errorf("%w: 日志数据根超过磁盘字节预算", ErrBudgetAdmission)
	}
	if work == WorkBackground {
		if unknown || (c.policy.DiskBytes > 0 && (!s.DiskBytesKnown || s.DiskUsedBytes < 0)) {
			return fmt.Errorf("%w: 采样未知或过期，暂停新增后台成本", ErrBudgetAdmission)
		}
		if s.DiskUsagePercent >= 80 {
			return fmt.Errorf("%w: 文件系统达到80%%降级水位", ErrBudgetAdmission)
		}
	}
	return nil
}

// Admit 是可被查询/后台物化消费的实际准入点；WAL有界排空始终保留。
func (c *BudgetController) Admit(work WorkClass) error {
	if c == nil {
		return nil
	}
	// 锁序仅controller.mu；纯算术，最多3个namespace，不调用外部回调。
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.admissionLocked(work)
}

// Reserve 在写入前原子预留磁盘空间；释放函数幂等，只影响本次预留。
func (c *BudgetController) Reserve(kind ReservationKind, bytes int64) (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	if !validReservation(kind) || bytes < 0 {
		return nil, fmt.Errorf("%w: 非法预留参数", ErrBudgetAdmission)
	}
	// 锁序仅controller.mu；最多3项算术，绝不持锁执行写入或调用方工作。
	c.mu.Lock()
	fail := func(reason string) (func(), error) {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrBudgetAdmission, reason)
	}
	if kind != ReservationWAL {
		if err := c.admissionLocked(WorkBackground); err != nil {
			c.mu.Unlock()
			return nil, err
		}
	}
	if limit := c.policy.Reservations[kind]; limit > 0 && bytes > limit-c.reserved[kind] {
		return fail("超过该类磁盘预留")
	}
	var sum int64
	for _, n := range c.reserved {
		if n > math.MaxInt64-sum {
			return fail("预留合计溢出")
		}
		sum += n
	}
	if bytes > math.MaxInt64-sum {
		return fail("预留溢出")
	}
	// 0表示未配置上界，不把兼容默认误解成禁止所有正常Append。
	if c.policy.DiskBytes > 0 {
		if bytes > c.policy.DiskBytes/4-sum {
			return fail("超过磁盘25%合计预留")
		}
		if bytes > 0 && (!c.sample.DiskBytesKnown || c.sample.SampledAt.IsZero() || time.Since(c.sample.SampledAt) > 2*time.Minute || c.sample.DiskUsedBytes < 0 || c.sample.DiskUsedBytes > c.policy.DiskBytes || bytes > c.policy.DiskBytes-c.sample.DiskUsedBytes-sum) {
			return fail("日志磁盘采样未知或剩余容量不足")
		}
	}
	c.reserved[kind] += bytes
	c.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { c.mu.Lock(); c.reserved[kind] -= bytes; c.mu.Unlock() }) }, nil
}

// SampleResources 在锁外采样；未运行实例明确known0，活进程采样失败保留unknown。
func (s *Supervisor) SampleResources(workerPID int) ResourceSample {
	s.mu.Lock()
	root := s.dataRoot
	pids := map[Namespace]int{}
	for ns, inst := range s.instances {
		if inst.proc != nil && !inst.proc.Exited() {
			pids[ns] = inst.proc.PID()
		}
	}
	s.mu.Unlock()
	out := ResourceSample{Namespaces: map[Namespace]NamespaceSample{}, SampledAt: time.Now()}
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		pid, running := pids[ns]
		v := NamespaceSample{RSSKnown: !running}
		if running && pid > 0 {
			rss, err := SampleProcessRSSBytes(pid)
			if err == nil {
				v = NamespaceSample{RSSBytes: rss, RSSKnown: true}
			}
		}
		out.Namespaces[ns] = v
	}
	if rss, err := SampleProcessRSSBytes(workerPID); err == nil {
		out.WorkerRSSBytes = rss
		out.WorkerRSSKnown = true
	}
	if pct, err := SampleDiskUsagePercent(root); err == nil {
		out.DiskUsagePercent = pct
		out.DiskKnown = true
	}
	out.DiskUsedBytes, out.DiskBytesKnown = boundedDirectoryBytes(root)
	return out
}

// boundedDirectoryBytes 限制采样成本；超10万条或100ms时明确unknown，不把半扫描值当全量。
func boundedDirectoryBytes(root string) (int64, bool) {
	deadline := time.Now().Add(100 * time.Millisecond)
	count := 0
	var total int64
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > 100000 || time.Now().After(deadline) {
			return errors.New("预算采样达到界限")
		}
		if e.Type()&fs.ModeSymlink != 0 {
			return errors.New("目录含符号链接，不能把未跟随目标当0字节")
		}
		if !e.IsDir() {
			info, err := e.Info()
			if err != nil {
				return err
			}
			if info.Size() < 0 || info.Size() > math.MaxInt64-total {
				return errors.New("目录大小溢出")
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, false
	}
	return total, true
}
