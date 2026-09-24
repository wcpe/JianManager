package vlsup

import (
	"os"
	"testing"
)

func TestEnforceCacheBudgetRejectsNegative(t *testing.T) {
	if err := EnforceCacheBudget(-1); err == nil {
		t.Fatal("negative cache budget must fail")
	}
	if err := EnforceCacheBudget(0); err != nil {
		t.Fatalf("0 means unset/template, got %v", err)
	}
	if err := EnforceCacheBudget(DefaultHotCacheBytes); err != nil {
		t.Fatalf("512MiB template must be accepted: %v", err)
	}
}

func TestCacheBudgetDocumentsProcessVsCache(t *testing.T) {
	b := DefaultCacheBudget()
	if b.HotCacheBytes != DefaultHotCacheBytes {
		t.Fatalf("hot cache template: %d", b.HotCacheBytes)
	}
	// cache ≠ RSS：字段分离即契约区分已登记
	if !b.DistinguishesProcessRSS() {
		t.Fatal("budget type must distinguish cache vs RSS fields")
	}
	// 512MiB 只是 HOT cache 模板，不得被文档化为 RSS 或总预算
	if b.ProcessRSSBytes == DefaultHotCacheBytes && b.TotalWorkerLogBudgetBytes == DefaultHotCacheBytes {
		t.Fatal("cache template must not be auto-copied into RSS/total budget")
	}
}

func TestBudgetEnforceMethod(t *testing.T) {
	b := DefaultCacheBudget()
	nb, err := b.Enforce(256 * 1024 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	if nb.HotCacheBytes != 256*1024*1024 {
		t.Fatalf("enforce should record cache bytes, got %d", nb.HotCacheBytes)
	}
	if _, err := b.Enforce(-5); err == nil {
		t.Fatal("negative must fail")
	}
}

func TestEvaluateBudgetThresholds(t *testing.T) {
	th := DefaultBudgetThresholds()
	if th.DegradedAtPercent != 80 || th.PauseAtPercent != 90 {
		t.Fatalf("契约默认阈值应为 80/90，got %+v", th)
	}

	// 预算充足 + 磁盘低 → OK。
	budget := CacheBudget{ProcessRSSBytes: 1 << 30 /* 1GiB */, TotalWorkerLogBudgetBytes: 2 << 30}
	if v := EvaluateBudget(budget, BudgetSample{ProcessRSSBytes: 100 << 20, DiskUsagePercent: 10}, th); v.State != BudgetOK {
		t.Fatalf("expected OK, got %s (%v)", v.State, v.Reasons)
	}

	// 磁盘 80% → 降级；90% → 暂停（优先级更高）。
	if v := EvaluateBudget(budget, BudgetSample{DiskUsagePercent: 80}, th); v.State != BudgetDegraded {
		t.Fatalf("disk 80%% must degrade, got %s", v.State)
	}
	if v := EvaluateBudget(budget, BudgetSample{DiskUsagePercent: 95}, th); v.State != BudgetPaused {
		t.Fatalf("disk 95%% must pause, got %s", v.State)
	}

	// RSS 超 1GiB 门禁 → 降级并给出原因。
	v := EvaluateBudget(budget, BudgetSample{ProcessRSSBytes: 2 << 30, DiskUsagePercent: 10}, th)
	if v.State != BudgetDegraded {
		t.Fatalf("RSS over budget must degrade, got %s", v.State)
	}
	if len(v.Reasons) == 0 {
		t.Fatal("降级必须给出可观测原因")
	}

	// 未配置 RSS 预算（0）不作 RSS 判定。
	if v := EvaluateBudget(CacheBudget{}, BudgetSample{ProcessRSSBytes: 8 << 30, DiskUsagePercent: 5}, th); v.State != BudgetOK {
		t.Fatalf("unset RSS budget must not trigger degrade, got %s", v.State)
	}
}

func TestSampleDiskUsagePercent(t *testing.T) {
	pct, err := SampleDiskUsagePercent(".")
	if err != nil {
		t.Fatalf("sample current dir disk usage: %v", err)
	}
	if pct < 0 || pct > 100 {
		t.Fatalf("disk usage percent out of range: %f", pct)
	}
	if _, err := SampleProcessRSSBytes(0); err == nil {
		t.Fatal("rss sample with non-positive pid must fail")
	}
}

func TestSampleProcessRSSBytesSelf(t *testing.T) {
	rss, err := SampleProcessRSSBytes(os.Getpid())
	if err != nil {
		t.Fatalf("sample own RSS: %v", err)
	}
	if rss <= 0 {
		t.Fatalf("own RSS should be > 0, got %d", rss)
	}
}

// TestReserveRatioGate 锁定契约 §6.6：WAL+staging+临时 Export 预留 ≤ 总日志预算 25%。
func TestReserveRatioGate(t *testing.T) {
	if ReserveRatioPercent != 25 {
		t.Fatalf("契约冻结值为 25%%，got %d", ReserveRatioPercent)
	}
	// 未配置总预算 → 不可判定（不得猜过）。
	if _, _, ok := EvaluateReserveRatio(CacheBudget{SubBudgets: map[string]int64{"wal": 1 << 30}}); ok {
		t.Fatal("unset total budget must be undecidable")
	}
	if err := EnforceReserveRatio(CacheBudget{}); err != nil {
		t.Fatalf("undecidable must not fail: %v", err)
	}

	total := int64(4 << 30) // 4GiB → 25% = 1GiB
	// 预留 800MiB < 1GiB → 通过。
	okBudget := CacheBudget{TotalWorkerLogBudgetBytes: total, SubBudgets: map[string]int64{
		"wal": 400 << 20, "staging": 300 << 20, "export": 100 << 20, "cold": 9 << 30,
	}}
	reserved, allowed, ok := EvaluateReserveRatio(okBudget)
	if !ok || allowed != total/4 {
		t.Fatalf("allowed should be 25%% of total: ok=%v allowed=%d", ok, allowed)
	}
	if reserved != 800<<20 {
		t.Fatalf("reserved should count only wal+staging+export: %d", reserved)
	}
	if err := EnforceReserveRatio(okBudget); err != nil {
		t.Fatalf("under 25%% must pass: %v", err)
	}

	// 预留 1.2GiB > 1GiB → 超限报错。
	over := okBudget
	over.SubBudgets = map[string]int64{"wal": 900 << 20, "staging": 300 << 20}
	if err := EnforceReserveRatio(over); err == nil {
		t.Fatal("over 25% must fail")
	}
}
