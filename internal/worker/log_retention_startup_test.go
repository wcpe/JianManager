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
		{"冷层长于实际", "log_retention:\n  enabled: true\n  cold_retention: 1000d\n", "cold_retention_period"},
		{"热层空字符串回退30天", "log_retention:\n  enabled: true\nlog_vl:\n  retention_period: ''\n", "retention_period"},
		{"冷层空白回退热层", "log_retention:\n  enabled: true\nlog_vl:\n  cold_retention_period: '   '\n", "cold_retention_period"},
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
		"log_retention:\n  enabled: true\n",
		"log_retention:\n  enabled: true\nlog_vl:\n  retention_period: '3'\n  cold_retention_period: 24M\n",
		"log_retention:\n  enabled: true\nlog_vl:\n  retention_period: 13w\n  cold_retention_period: 2y\n",
		"log_retention:\n  enabled: true\n  hot_retention: 30d\n  cold_retention: 30d\nlog_vl:\n  retention_period: ''\n  cold_retention_period: ' '\n",
		"log_retention:\n  enabled: false\n  hot_retention: 180d\n  cold_retention: 1000d\nlog_vl:\n  retention_period: ''\n  cold_retention_period: ''\n",
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
