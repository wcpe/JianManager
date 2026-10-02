package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRetentionDurationsDecodeThroughLoad 钉住一条**曾经真的踩过**的陷阱：
// 保留期写成 `90d` 时，若字段类型是 time.Duration，viper 解码会失败，
// 而失败的不是这一个键——**整个 Config.Load 都会返回错误**，Worker 直接起不来。
//
// 根因：Go 的 time.ParseDuration 不认 `d`/`w`，而本项目的保留期天然按天写
// （用户口径就是「热 90 天 / 冷 730 天」）。故这里一律用字符串 + retention.ParseTTL。
// 本用例走的是完整链路（YAML → viper → RetentionPolicy → 时长），
// 只测 ParseTTL 是抓不住这个缺陷的。
func TestRetentionDurationsDecodeThroughLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := `
log_retention:
  enabled: true
  hot_retention: 90d
  cold_retention: 730d
  trigger:
    disk_percent: 85
    min_age: 7d
  by_level:
    debug: 3d
    error: 90d
  sources:
    - match: "inst:147"
      by_level:
        debug: 1d
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("含 90d/730d 的保留配置必须能加载（time.Duration 字段会让整个 Load 失败）：%v", err)
	}
	policy, err := cfg.RetentionPolicy()
	if err != nil {
		t.Fatalf("策略装配失败：%v", err)
	}
	if policy.HotRetention != 90*24*time.Hour {
		t.Errorf("hot_retention 应为 90d，得到 %s", policy.HotRetention)
	}
	if policy.ColdRetention != 730*24*time.Hour {
		t.Errorf("cold_retention 应为 730d，得到 %s", policy.ColdRetention)
	}
	if policy.Trigger.DiskPercent != 85 {
		t.Errorf("trigger.disk_percent 应为 85，得到 %v", policy.Trigger.DiskPercent)
	}
	if got := policy.Trigger.Normalize().MinAge; got != 7*24*time.Hour {
		t.Errorf("trigger.min_age 应为 7d，得到 %s", got)
	}
	if got := policy.EffectiveTTL("inst:147", "DEBUG"); got != 24*time.Hour {
		t.Errorf("来源覆盖 debug 应为 1d，得到 %s", got)
	}
	if got := policy.EffectiveTTL("inst:151", "DEBUG"); got != 3*24*time.Hour {
		t.Errorf("全局 debug 应为 3d，得到 %s", got)
	}
	// 冷层不得按级别分档：冷层是「所有级别统一」。
	n := policy.Normalize()
	if n.ColdRetention != 730*24*time.Hour {
		t.Errorf("冷层保留期应统一为 730d，得到 %s", n.ColdRetention)
	}
}
