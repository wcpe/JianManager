package vlsup

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// knownResourceSample 构造「全部已知且健康」的采样；受管实例只有一个（NamespaceHot）。
func knownResourceSample() ResourceSample {
	return ResourceSample{Namespaces: map[Namespace]NamespaceSample{NamespaceHot: {RSSKnown: true, RSSBytes: 10}}, WorkerRSSKnown: true, WorkerRSSBytes: 10, DiskKnown: true, DiskUsagePercent: 10, DiskBytesKnown: true, DiskUsedBytes: 10}
}

func TestRuntimeBudgetSeparatesDiskRSSAndUnknown(t *testing.T) {
	c, err := NewBudgetController(RuntimeBudget{DiskBytes: 1000, RSSBytes: 100, Namespaces: map[Namespace]NamespaceBudget{NamespaceHot: {RSSBytes: 50}}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Admit(WorkBackground) == nil {
		t.Fatal("初始未知不得当0放行后台")
	}
	if c.Admit(WorkWALDrain) != nil {
		t.Fatal("未知时仍须保留WAL排空")
	}
	s := knownResourceSample()
	c.Update(s)
	if err := c.Admit(WorkBackground); err != nil {
		t.Fatal(err)
	}
	s.Namespaces[NamespaceHot] = NamespaceSample{RSSKnown: true, RSSBytes: 51}
	c.Update(s)
	if c.Admit(WorkForeground) == nil || c.Admit(WorkWALDrain) != nil {
		t.Fatal("超RSS必须拒绝新查询但不杀VL或阻止排空")
	}
	s = knownResourceSample()
	s.DiskUsagePercent = 85
	c.Update(s)
	if c.Admit(WorkBackground) == nil || c.Admit(WorkForeground) != nil {
		t.Fatal("80%只挡后台成本")
	}
	s.DiskUsagePercent = 90
	c.Update(s)
	if c.Admit(WorkForeground) == nil || c.Admit(WorkWALDrain) != nil {
		t.Fatal("90%挡新前台但保留有界排空")
	}
	for _, pct := range []float64{math.NaN(), math.Inf(1), -1, 101} {
		s = knownResourceSample()
		s.DiskUsagePercent = pct
		c.Update(s)
		if c.Admit(WorkBackground) == nil {
			t.Fatal("非法采样不能放行")
		}
	}
	s = knownResourceSample()
	s.Namespaces[NamespaceHot] = NamespaceSample{}
	c.Update(s)
	if c.Admit(WorkBackground) == nil {
		t.Fatal("受管实例自身采样未知不能由 Worker 聚合采样掩盖")
	}
	s = knownResourceSample()
	s.SampledAt = time.Now().Add(-3 * time.Minute)
	c.Update(s)
	if c.Admit(WorkBackground) == nil {
		t.Fatal("过期采样不得长期放行后台")
	}
	// 全局disk不是RSS：20RSS可大于10字节disk预算，只按磁盘实占判定。
	c, _ = NewBudgetController(RuntimeBudget{DiskBytes: 10})
	s = knownResourceSample()
	s.DiskUsedBytes = 1
	c.Update(s)
	if err := c.Admit(WorkForeground); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReservationAtomicCapacityAndIdempotentRelease(t *testing.T) {
	c, err := NewBudgetController(RuntimeBudget{DiskBytes: 400, Reservations: map[ReservationKind]int64{ReservationStaging: 100}})
	if err != nil {
		t.Fatal(err)
	}
	c.Update(knownResourceSample())
	var won atomic.Int32
	var wg sync.WaitGroup
	var releasesMu sync.Mutex
	var releases []func()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := c.Reserve(ReservationStaging, 10)
			if err == nil {
				won.Add(1)
				releasesMu.Lock()
				releases = append(releases, release)
				releasesMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won.Load() != 10 {
		t.Fatalf("原子预留必须恰好10个成功，got=%d", won.Load())
	}
	for _, release := range releases {
		release()
		release()
	}
	r, err := c.Reserve(ReservationStaging, 100)
	if err != nil {
		t.Fatal(err)
	}
	r()
	s := knownResourceSample()
	s.DiskUsedBytes = 399
	c.Update(s)
	if _, err := c.Reserve(ReservationStaging, 2); err == nil {
		t.Fatal("真实剩余容量不足时不能只看预留子额度")
	}
	s.DiskBytesKnown = false
	c.Update(s)
	if _, err := c.Reserve(ReservationWAL, 1); err == nil {
		t.Fatal("配置了磁盘硬预算时未知实占不能证明可预留")
	}
	if _, err := NewBudgetController(RuntimeBudget{DiskBytes: 400, Reservations: map[ReservationKind]int64{ReservationWAL: 101}}); err == nil {
		t.Fatal("25%预留门必须在构造时拒绝")
	}
	c, _ = NewBudgetController(RuntimeBudget{})
	if c.Admit(WorkBackground) == nil {
		t.Fatal("未配预算不是已取得健康采样")
	}
	r, err = c.Reserve(ReservationWAL, 100)
	if err != nil {
		t.Fatalf("兼容默认未配置不是拒绝所有Append: %v", err)
	}
	r()
}

func TestNamespaceBudgetDefaultsAreIndependentAndSampleUnknown(t *testing.T) {
	a, b := DefaultNamespaceBudgets(), DefaultNamespaceBudgets()
	a[NamespaceHot] = NamespaceBudget{CacheBytes: 1}
	if b[NamespaceHot].CacheBytes != DefaultHotCacheBytes {
		t.Fatal("默认map不能共享")
	}
	s := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, false)
	v := s.SampleResources(os.Getpid())
	if !v.WorkerRSSKnown || v.WorkerRSSBytes <= 0 || !v.DiskKnown {
		t.Fatalf("有效自进程应有采样: %+v", v)
	}
	for ns, p := range v.Namespaces {
		if !p.RSSKnown || p.RSSBytes != 0 {
			t.Fatalf("未运行namespace应明确known0: %s %+v", ns, p)
		}
	}
	if n, ok := boundedDirectoryBytes("/nonexistent/constructed-data-root"); ok || n != 0 {
		t.Fatal("目录采样错误必须未知")
	}
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "constructed-link")); err != nil {
		t.Fatal(err)
	}
	if _, ok := boundedDirectoryBytes(root); ok {
		t.Fatal("符号链接目标未采样时不能冒充零占用")
	}
	f := &fakeFactory{nextPID: -1}
	s2 := newTestSupervisor(t, f, &RecordingSink{}, false)
	if err := s2.Start(context.Background(), NamespaceHot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.StopAll(context.Background()) })
	if got := s2.SampleResources(os.Getpid()).Namespaces[NamespaceHot]; got.RSSKnown {
		t.Fatal("活句柄PID无效必须unknown，不是停服known0")
	}
}

// TestSupervisorNamespaceBudgetsWireCacheAndGoTarget 锁定 cache 与 Go 软目标确实落到唯一的受管实例上：
// typed NamespaceBudgets 覆盖兼容 cache 字段，其 GoMemoryLimitBytes 覆盖全局 MemoryLimitBytes。
func TestSupervisorNamespaceBudgetsWireCacheAndGoTarget(t *testing.T) {
	bin, sha := writeFakeBinary(t)
	s, err := New(Options{BinaryPath: bin, AssetSHA256: sha, DataRoot: t.TempDir(), AuthUsername: "jm", AuthPassword: "constructed", MemoryAllowedBytes: map[Namespace]int64{NamespaceHot: 256 << 20}, MemoryLimitBytes: 600 << 20, NamespaceBudgets: map[Namespace]NamespaceBudget{NamespaceHot: {CacheBytes: 64 << 20, GoMemoryLimitBytes: 128 << 20, RSSBytes: 300 << 20}}})
	if err != nil {
		t.Fatal(err)
	}
	hot, err := s.Config(NamespaceHot)
	if err != nil {
		t.Fatal(err)
	}
	if hot.EffectiveCacheBytes() != 64<<20 {
		t.Fatalf("typed覆盖必须优先于兼容cache字段: %d", hot.EffectiveCacheBytes())
	}
	factory := s.factory.(RealFactory)
	if strings.Join(factory.NamespaceEnv[NamespaceHot], ",") != "GOMEMLIMIT=134217728B" {
		t.Fatalf("Go软目标必须对唯一的受管实例生效: %v", factory.NamespaceEnv)
	}
	if len(factory.NamespaceEnv) != len(managedNamespaces()) {
		t.Fatalf("不得为已废除的层注入env: %v", factory.NamespaceEnv)
	}
	if _, err := NewBudgetController(RuntimeBudget{Namespaces: map[Namespace]NamespaceBudget{NamespaceHot: {RSSBytes: -1}}}); err == nil {
		t.Fatal("RSS负预算非法")
	}
}

func TestRuntimeBudgetAllowsReadOnlyQueryAtDiskPause(t *testing.T) {
	c, err := NewBudgetController(RuntimeBudget{DiskBytes: 1000})
	requireNoErrorBudget(t, err)
	s := knownResourceSample()
	s.DiskUsagePercent = 95
	s.DiskUsedBytes = 999
	c.Update(s)
	if err := c.Admit(WorkReadOnly); err != nil {
		t.Fatalf("磁盘保护只应阻止新增写入/物化，已有日志的有界只读查询应可继续: %v", err)
	}
	if err := c.Admit(WorkForeground); err == nil {
		t.Fatal("连续/高成本前台工作仍须在90%水位拒绝")
	}
}

func requireNoErrorBudget(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
