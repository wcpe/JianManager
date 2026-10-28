package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadLogPolicyFixture(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker.yml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

// 必须在 Load 阶段拒绝，而不是受管进程已启动后仅关闭驱动器。
func TestLoadRejectsRetentionMismatchAndRawPolicy(t *testing.T) {
	cases := []struct{ name, body, errorKey string }{
		{"热层长于实际", "log_retention:\n  enabled: true\n  hot_retention: 180d\n", "retention_period"},
		// 2026-10-29：`log_vl.cold_retention_period` 已删除；`log_retention.cold_retention` 现在
		// 与 hot_retention 一起取最大值去校 `log_vl.retention_period`，故错误键是 retention_period。
		{"长期声明长于实际", "log_retention:\n  enabled: true\n  cold_retention: 1000d\n", "retention_period"},
		// 空串回退到 vlsup.DefaultRetentionPeriod（2026-10-21 起为 120d）。
		// 该回退值本身**长于**热窗口 90d，故不再被拒 —— 本用例改为显式给一个
		// 短于热窗口的值，继续锁住「VL retention 短于热窗口必须启动即拒」这一红线。
		{"热层短于实际", "log_retention:\n  enabled: true\nlog_vl:\n  retention_period: '30d'\n", "retention_period"},
		// 2026-10-29：原「冷层空白回退热层」用例随 `cold_retention_period` 一起失效——
		// 单实例下「空白」回退到唯一的默认保留期（180d），与声明相等，因此**不该**被拒；
		// 「显式给一个短于声明的值必须拒」已由上面的「热层短于实际」覆盖。
		{"未知等级", "log_retention:\n  enabled: true\n  by_level:\n    mystery: 7d\n", "未知级别"},
		{"来源非法", "log_retention:\n  enabled: true\n  sources:\n    - match: 'inst:1|*'\n", "match"},
		{"热窗口原始负值", "log_retention:\n  enabled: true\n  hot_retention: -1d\n", "hot_retention"},
		{"负扫描间隔", "log_retention:\n  enabled: true\n  sweep:\n    interval: -1h\n", "interval"},
		{"非法水位", "log_retention:\n  enabled: true\n  trigger:\n    disk_percent: 101\n", "disk_percent"},
		{"月不能写分钟", "log_retention:\n  enabled: true\nlog_vl:\n  retention_period: 90m\n", "retention_period"},
		{"运行时非法数", "log_retention:\n  enabled: true\nlog_vl:\n  retention_period: NaN\n", "retention_period"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadLogPolicyFixture(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.errorKey) {
				t.Fatalf("Load 必须启动即拒并定位 %s，得到 %v", tc.errorKey, err)
			}
		})
	}
}

func TestLoadRetentionDefaultsMonthsAndDisabledCompatibility(t *testing.T) {
	for _, body := range []string{
		"{}\n",
		"log_retention:\n  enabled: false\n  hot_retention: 180d\n  cold_retention: 1000d\nlog_vl:\n  retention_period: ''\n  cold_retention_period: ''\n",
		wiringValidLayerYAML(),
		strings.Replace(wiringValidLayerYAML(), "  start_cold: true", "  start_cold: true\n  retention_period: '3'\n  cold_retention_period: 24M", 1),
		// 13w=91d 不能覆盖 UTC 日边界与复制恢复 SLA，改用仍合法且足额的周单位。
		strings.Replace(wiringValidLayerYAML(), "  start_cold: true", "  start_cold: true\n  retention_period: 14w\n  cold_retention_period: 2y", 1),
	} {
		if _, err := loadLogPolicyFixture(t, body); err != nil {
			t.Errorf("兼容配置不得被拒: %q: %v", body, err)
		}
	}
}

func TestLoadRejectsRawSamplingPolicy(t *testing.T) {
	for _, body := range []string{
		"min_level: unknown", "burst_window: -1s", "burst_window: 1s\n    burst_threshold: -1",
		"budget_max_events_per_window: -1", "max_aggregate_events: -1",
		"degrade_enabled: true\n    degrade_disk_percent: 101",
	} {
		_, err := loadLogPolicyFixture(t, "log_ingest:\n  sampling:\n    enabled: true\n    "+body+"\n")
		if err == nil || !strings.Contains(err.Error(), "sampling") {
			t.Fatalf("必须拒绝采样原值 %q，得到 %v", body, err)
		}
	}
}

func TestLoadSamplingPolicyPreservesRawValues(t *testing.T) {
	cfg, err := loadLogPolicyFixture(t, "log_ingest:\n  sampling:\n    enabled: true\n    min_level: WARN\n    burst_window: 2s\n    burst_threshold: 3\n    max_signatures: 4\n    budget_max_events_per_window: 5\n    budget_window: 6s\n    budget_keep_every: 7\n    max_aggregate_events: 8\n    degrade_enabled: true\n    degrade_target_level: ERROR\n    degrade_disk_percent: 85\n    degrade_hold: 9s\n")
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.SamplingPolicy()
	if !p.Enabled || p.Level.MinLevel != "WARN" || p.Burst.Threshold != 3 || p.Burst.MaxSignatures != 4 || p.Budget.KeepEvery != 7 || p.MaxAggregateEvents != 8 || !p.Degrade.Enabled || p.Degrade.DiskPercent != 85 {
		t.Fatalf("采样策略接线丢字段: %+v", p)
	}
	cfg, err = loadLogPolicyFixture(t, "log_ingest:\n  sampling:\n    enabled: false\n    max_aggregate_events: -1\n")
	if err != nil || cfg.SamplingPolicy().Enabled {
		t.Fatalf("关闭保持兼容: %v", err)
	}
}
