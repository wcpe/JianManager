package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ADR-107 位点账本的配置加载测试。
//
// 守两件事：
//  1. **示例配置里的键名真的能被读到**——`configs/control-plane.yml` 的 `business_cursor` 段
//     若与 mapstructure tag 漂移（改了一个字却没改另一个），零配置部署就会静默走默认值，
//     运维以为调了 retention 其实没生效；
//  2. 零配置时走 ADR-107 的默认值（保留 7 天 / 队列 10000 / 批量 500 / 开启）。

// writeTempConfig 把给定内容写入临时 control-plane.yml 并返回路径。
func writeTempConfig(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "control-plane.yml")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	return p
}

// TestBusinessCursorConfig_SampleYAML 示例配置的 business_cursor 段必须被真实加载器读到。
func TestBusinessCursorConfig_SampleYAML(t *testing.T) {
	src, err := os.ReadFile("../../../configs/control-plane.yml")
	if err != nil {
		t.Fatalf("读不到示例配置: %v", err)
	}
	cfg, err := Load(writeTempConfig(t, src))
	if err != nil {
		t.Fatalf("加载示例配置失败: %v", err)
	}
	got := cfg.BusinessCursor
	if !got.Enabled || got.RetentionDays != 7 || got.QueueSize != 10000 || got.BatchSize != 500 {
		t.Fatalf("示例配置的 business_cursor 段未按预期解析（键名与 mapstructure tag 漂移？）: %+v", got)
	}
}

// TestBusinessCursorConfig_Defaults 零配置走 ADR-107 默认值。
func TestBusinessCursorConfig_Defaults(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, []byte("server:\n  port: 8080\n")))
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	got := cfg.BusinessCursor
	if !got.Enabled {
		t.Error("位点账本默认必须开启（ADR-107 §2）")
	}
	if got.RetentionDays != 7 {
		t.Errorf("保留天数默认必须是 7 天（ADR-107 §5），实得 %d", got.RetentionDays)
	}
	if got.QueueSize != 10000 {
		t.Errorf("队列容量默认必须是 10000（ADR-107 §2），实得 %d", got.QueueSize)
	}
	if got.BatchSize != 500 {
		t.Errorf("批量条数默认必须是 500（ADR-107 §2），实得 %d", got.BatchSize)
	}
}

// TestBusinessCursorConfig_EnvOverride 环境变量可覆盖（JIANMANAGER_BUSINESS_CURSOR_*）。
func TestBusinessCursorConfig_EnvOverride(t *testing.T) {
	t.Setenv("JIANMANAGER_BUSINESS_CURSOR_RETENTION_DAYS", "30")
	t.Setenv("JIANMANAGER_BUSINESS_CURSOR_ENABLED", "false")
	cfg, err := Load(writeTempConfig(t, []byte("server:\n  port: 8080\n")))
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.BusinessCursor.RetentionDays != 30 {
		t.Errorf("环境变量应覆盖保留天数，实得 %d", cfg.BusinessCursor.RetentionDays)
	}
	if cfg.BusinessCursor.Enabled {
		t.Error("环境变量应可关闭位点账本")
	}
}
