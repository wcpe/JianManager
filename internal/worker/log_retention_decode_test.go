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

// 回归（B1，2026-10-12 修复）：`log_retention.trigger.interval` 必须**真的生效**。
//
// 缺陷形态：生产唯一的驱动器构造点 `retention.NewDriver` 取的是
// `policy.Trigger.Normalize().Interval`（见 driver.go），而本结构体此前**没有 Interval 字段**
// ⇒ 运维按直觉写 `trigger.interval` 会被 viper **静默丢弃**（不报错、不警告），
// 实际调度节奏恒为 `DefaultDriverInterval`（15m）。同时样例配置把 `sweep.interval`
// 当作调度旋钮呈现，而它只作用于删除执行器 `Sweeper`（本版本生产无构造点）
// ⇒ 形成「文档说的键不生效、生效的键配不了」的死旋钮。
//
// 转红方式（实测）：删掉 `LogRetentionTriggerConfig.Interval` 字段（或删掉
// `RetentionPolicy()` 里的解析块）⇒ 断言①（解析值必须等于配置值）立即红。
func TestRetentionTriggerIntervalTakesEffect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := `
log_retention:
  enabled: true
  trigger:
    interval: 3m
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	pol, err := cfg.RetentionPolicy()
	if err != nil {
		t.Fatalf("RetentionPolicy 失败：%v", err)
	}

	// ① 配置值必须被解析（此前会被静默丢弃为 0）。
	if got := pol.Trigger.Interval; got != 3*time.Minute {
		t.Fatalf("trigger.interval 未被解析：得到 %v，期望 3m（该键曾因结构体缺字段而被 viper 静默丢弃）", got)
	}
	// ② 驱动器**实际使用**的值必须与配置一致（Normalize 不得把它改回默认）。
	if got := pol.Trigger.Normalize().Interval; got != 3*time.Minute {
		t.Fatalf("驱动器实际节奏与配置不一致：Normalize 后得到 %v，期望 3m", got)
	}
}
