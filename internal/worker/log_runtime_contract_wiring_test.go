package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// TestWiringRuntimeRetentionDefaultSingleSource 防止加载器与子进程默认值重新漂移。
func TestWiringRuntimeRetentionDefaultSingleSource(t *testing.T) {
	cfg, err := loadLogPolicyFixture(t, "{}\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogVL.RetentionPeriod != vlsup.DefaultRetentionPeriod {
		t.Fatalf("Load native默认=%q，运行时真源=%q", cfg.LogVL.RetentionPeriod, vlsup.DefaultRetentionPeriod)
	}
	if cfg.LogRetention.Enabled || cfg.LogVL.StartCold || cfg.LogVL.StartRehydrate || cfg.LogVL.DeleteEnabled {
		t.Fatal("零配置不得开启分层、冷层、恢复层或删除")
	}
}

// TestWiringLayeringRequiresReceiverAndHeadroom 启层不能只打开策略开关。
func TestWiringLayeringRequiresReceiverAndHeadroom(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"缺受管接收端", "log_retention:\n  enabled: true\n", "log_vl"},
		{"相等native窗口", "log_retention:\n  enabled: true\nlog_vl:\n  retention_period: 90d\n", "retention_period"},
		{"不足UTC日与驱动余量", "log_retention:\n  enabled: true\nlog_vl:\n  retention_period: 91d\n", "retention_period"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadLogPolicyFixture(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应拒绝并定位 %s，得到 %v", tc.want, err)
			}
		})
	}
}

// TestWiringHotOnlyKeepsFiniteNativeRetention 关闭应用删除不等于关闭引擎原生淘汰。
func TestWiringHotOnlyKeepsFiniteNativeRetention(t *testing.T) {
	cfg, err := loadLogPolicyFixture(t, "log_vl:\n  retention_period: 30d\nlog_retention:\n  enabled: false\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogVL.RetentionPeriod != "30d" || cfg.LogRetention.Enabled || cfg.LogVL.DeleteEnabled {
		t.Fatalf("合法HOT-only组合被改写: %+v", cfg.LogVL)
	}
}

const wiringManagedReceiverYAML = `
log_vl:
  binary_path: /isolated/victorialogs
  asset_sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  username: fixture-user
  password: fixture-password
  start_cold: true
`

func wiringValidLayerYAML() string {
	return wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
`
}

// TestWiringLayeringHasExplicitFiniteSLA 只认可有接收端、调度周期和有限余量的分层组合。
func TestWiringLayeringHasExplicitFiniteSLA(t *testing.T) {
	if _, err := loadLogPolicyFixture(t, wiringValidLayerYAML()); err != nil {
		t.Fatalf("完整分层声明应通过: %v", err)
	}
	for _, tc := range []struct{ name, from, to, want string }{
		{"接收端未启", "start_cold: true", "start_cold: false", "start_cold"},
		{"缺复制校验预算", "copy_verify_budget: 2h", "copy_verify_budget: ''", "copy_verify_budget"},
		{"恢复预算非正", "recovery_budget: 24h", "recovery_budget: 0", "recovery_budget"},
		{"鉴权不完整", "password: fixture-password", "password: ''", "log_vl"},
		{"未校验资产", "asset_sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "asset_sha256: ''", "asset_sha256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadLogPolicyFixture(t, strings.Replace(wiringValidLayerYAML(), tc.from, tc.to, 1))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应拒绝并定位 %s，得到 %v", tc.want, err)
			}
		})
	}
	insufficient := strings.Replace(wiringValidLayerYAML(), "  start_cold: true", "  start_cold: true\n  retention_period: 92d", 1)
	if _, err := loadLogPolicyFixture(t, insufficient); err == nil || !strings.Contains(err.Error(), "retention_period") {
		t.Fatalf("必须计入UTC日+周期+复制校验+恢复预算，得到 %v", err)
	}
}

// TestWiringNamespaceBudgetsRespectLegacyKeys 新键按层优先，旧内存键仍能控制未覆盖字段。
func TestWiringNamespaceBudgetsRespectLegacyKeys(t *testing.T) {
	cfg, err := loadLogPolicyFixture(t, `
log_vl:
  hot_cache_bytes: 268435456
  memory_limit_bytes: 402653184
  namespace_budgets:
    cold:
      cache_bytes: 100663296
      go_memory_limit_bytes: 335544320
      rss_bytes: 469762048
  budget:
    disk_bytes: 8589934592
    rss_bytes: 1073741824
    reservations:
      wal: 536870912
      staging: 536870912
      export: 268435456
`)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := cfg.LogVL.RuntimeBudget()
	if err != nil {
		t.Fatal(err)
	}
	hot, cold, rehydrate := budget.Namespaces[vlsup.NamespaceHot], budget.Namespaces[vlsup.NamespaceCold], budget.Namespaces[vlsup.NamespaceRehydrate]
	if hot.CacheBytes != 256<<20 || hot.GoMemoryLimitBytes != 384<<20 || cold.CacheBytes != 96<<20 || cold.GoMemoryLimitBytes != 320<<20 || cold.RSSBytes != 448<<20 || rehydrate.CacheBytes != vlsup.DefaultRehydrateCacheBytes || rehydrate.GoMemoryLimitBytes != 384<<20 {
		t.Fatalf("兼容归并错误: hot=%+v cold=%+v rehydrate=%+v", hot, cold, rehydrate)
	}
	if budget.DiskBytes != 8<<30 || budget.RSSBytes != 1<<30 || budget.Reservations[vlsup.ReservationWAL] != 512<<20 {
		t.Fatalf("磁盘/RSS/预留接线混淆: %+v", budget)
	}
	disabledSoftLimit, err := (LogVLConfig{MemoryLimitBytes: -1}).RuntimeBudget()
	if err != nil {
		t.Fatal(err)
	}
	for ns, limits := range disabledSoftLimit.Namespaces {
		if limits.GoMemoryLimitBytes != -1 {
			t.Fatalf("旧负值禁注入语义丢失: %s=%+v", ns, limits)
		}
	}
	t.Setenv("JIANMANAGER_LOG_VL_NAMESPACE_BUDGETS_COLD_CACHE_BYTES", "67108864")
	cfg, err = loadLogPolicyFixture(t, "{}\n")
	if err != nil {
		t.Fatal(err)
	}
	budget, err = cfg.LogVL.RuntimeBudget()
	if err != nil || budget.Namespaces[vlsup.NamespaceCold].CacheBytes != 64<<20 {
		t.Fatalf("新键环境覆盖失效: %+v, %v", budget, err)
	}
}

// TestWiringRetentionCapabilityNoticeIsFiniteAndSecretFree 不把关闭删除或有限缓冲包装成永久副本。
func TestWiringRetentionCapabilityNoticeIsFiniteAndSecretFree(t *testing.T) {
	var output bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	const password = "constructed-retention-secret"
	_, err := loadLogPolicyFixture(t, "log_vl:\n  retention_period: 30d\n  username: fixture-user\n  password: "+password+"\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"自动搬运关闭", "有限 TTL", "关闭应用删除不等于永久保留"} {
		if !strings.Contains(output.String(), message) {
			t.Fatalf("缺少真实能力边界 %q: %s", message, output.String())
		}
	}
	if strings.Contains(output.String(), password) {
		t.Fatal("留存能力摘要泄露了构造凭据")
	}
}

// TestWiringBudgetAndReadLimitsFailClosed 维度错误或负上界必须在启动前拒绝。
func TestWiringBudgetAndReadLimitsFailClosed(t *testing.T) {
	for _, body := range []string{
		"log_vl:\n  namespace_budgets:\n    typo:\n      cache_bytes: 1\n",
		"log_vl:\n  namespace_budgets:\n    hot:\n      rss_bytes: -1\n",
		"log_vl:\n  budget:\n    disk_bytes: -1\n",
		"log_vl:\n  budget:\n    disk_bytes: 100\n    reservations:\n      staging: 26\n",
		"log_vl:\n  budget:\n    reservations:\n      export: 1\n",
		"log_ingest:\n  read_limits:\n    max_line_bytes: -1\n",
		"log_ingest:\n  read_limits:\n    max_line_bytes: 64\n    max_bytes_per_poll: 64\n",
	} {
		if _, err := loadLogPolicyFixture(t, body); err == nil {
			t.Fatalf("必须拒绝非法预算: %s", body)
		}
	}
	cfg, err := loadLogPolicyFixture(t, "log_ingest:\n  read_limits:\n    max_line_bytes: 1024\n    max_bytes_per_poll: 4096\n")
	if err != nil {
		t.Fatal(err)
	}
	limits, err := cfg.IngestReadLimits()
	if err != nil || limits.MaxLineBytes != 1024 || limits.MaxBytesPerPoll != 4096 {
		t.Fatalf("读取预算装配错误: %+v %v", limits, err)
	}
	defaults, err := loadLogPolicyFixture(t, "{}\n")
	if err != nil {
		t.Fatal(err)
	}
	budget, err := defaults.LogVL.RuntimeBudget()
	if err != nil {
		t.Fatal(err)
	}
	controller, err := vlsup.NewBudgetController(budget)
	if err != nil {
		t.Fatal(err)
	}
	release, err := controller.Reserve(vlsup.ReservationWAL, 128)
	if err != nil {
		t.Fatalf("未配全局预算不能禁用默认有限WAL: %v", err)
	}
	release()
	if err := controller.Admit(vlsup.WorkBackground); err == nil {
		t.Fatal("未采样不能伪装已知零以放行昂贵后台")
	}
	if err := controller.Admit(vlsup.WorkWALDrain); err != nil {
		t.Fatalf("未知采样仍须允许有界WAL排空: %v", err)
	}
}
