package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件守「归档对象自身的回收」的**配置面**（FR-501 的最后一块）。
//
// 三条必须钉住的属性：
//  1. 默认关、默认无保留期（删除类能力默认全关，且「留几年」必须由运维说出口）；
//  2. 开了开关却没给保留期 ⇒ **启动即拒**（静默空转是这类功能最典型的现场）；
//  3. 三闸仍默认全关（打开本项不等于授权删除）。
//
// 这些都必须走完整链路（YAML → viper → RetentionPolicy）：本包的配置陷阱
// 此前踩过不止一次（`trigger.interval` 被静默丢弃、`90d` 让整个 Load 失败），
// 「键写在 YAML 里但没生效」在现场没有报错，只表现为「功能没按预期跑」。

// TestRetentionArchiveObjectRetentionDefaultsOff 钉住「默认部署零行为变化」。
func TestRetentionArchiveObjectRetentionDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	policy, err := cfg.RetentionPolicy()
	if err != nil {
		t.Fatalf("RetentionPolicy 失败：%v", err)
	}
	if policy.ArchiveObjectRetention.Enabled {
		t.Fatal("archive_object_retention 必须默认关闭")
	}
	// 保留期默认必须是空（= 不启用），不得凭空补一个 730d：
	// 那等于「打开开关就承诺两年后无条件开始删归档」。
	if got := policy.ArchiveObjectRetention.Retention; got != 0 {
		t.Fatalf("归档对象保留期不得有默认值，得到 %s", got)
	}
	if policy.ArchiveObjectRetention.Active() {
		t.Fatal("默认策略下归档对象回收必须不生效")
	}
	// 三闸默认必须全关（用户护栏）。
	if open, reason := policy.DeleteGatesOpen(); open {
		t.Fatalf("默认策略下删除三闸必须全关，得到 %q", reason)
	}
}

// TestRetentionArchiveObjectRetentionEnabledWithoutRetentionRejected 钉住「开了开关却没给保留期」必须拒。
//
// 若静默按未启用处理，运维看到 `enabled: true` 而归档一天天涨、日志里一个字没有——
// 那是这类功能最难查的一类现场。
func TestRetentionArchiveObjectRetentionEnabledWithoutRetentionRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_object_retention:
    enabled: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		// 在 Load 阶段就拒也满足要求（越早越好）。
		if !strings.Contains(err.Error(), "archive_object_retention.retention") {
			t.Fatalf("拒绝原因应点名 archive_object_retention.retention，得到 %v", err)
		}
		return
	}
	if _, err := cfg.RetentionPolicy(); err == nil {
		t.Fatal("启用归档对象回收但未配置保留期必须报错（不得静默降级为空转）")
	} else if !strings.Contains(err.Error(), "archive_object_retention.retention") {
		t.Fatalf("拒绝原因应点名 archive_object_retention.retention，得到 %v", err)
	}
}

// TestRetentionArchiveObjectRetentionParsesTTL 钉住「90d 这类写法真的通到策略层」。
//
// 用 `d` 后缀而不是小时数是刻意的：Go 的 time.ParseDuration 不认 `d`，
// 而归档保留期天然按天写（730d）。若这里改成 time.Duration 字段，
// viper 解码会让**整个 Load 失败**——这条用例就是那个缺陷的守门人。
func TestRetentionArchiveObjectRetentionParsesTTL(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"730d", 730 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"17520h", 730 * 24 * time.Hour},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "worker.yml")
			yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_object_retention:
    enabled: true
    retention: "` + tc.raw + `"
`
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatalf("写夹具失败：%v", err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load 失败：%v", err)
			}
			policy, err := cfg.RetentionPolicy()
			if err != nil {
				t.Fatalf("RetentionPolicy 失败：%v", err)
			}
			if got := policy.ArchiveObjectRetention.Retention; got != tc.want {
				t.Fatalf("保留期解析不符：期望 %s，得到 %s", tc.want, got)
			}
			if !policy.ArchiveObjectRetention.Active() {
				t.Fatal("给出保留期后必须生效")
			}
		})
	}
}

// TestRetentionArchiveObjectRetentionRejectsZeroAndNegative 钉住「0 / 负数」两个陷阱。
//
// `0` 在 ParseTTL 里的语义是**永久保留**，与「启用回收」自相矛盾；
// 运维写 "0" 的直觉往往相反（以为「立刻删」）。负数一律是写错。
// 两者都必须在启动阶段拒，而不是在运行时表现成「一条都不删」。
func TestRetentionArchiveObjectRetentionRejectsZeroAndNegative(t *testing.T) {
	for _, raw := range []string{"0", "-730d", "-1h"} {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "worker.yml")
			yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_object_retention:
    enabled: true
    retention: "` + raw + `"
`
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatalf("写夹具失败：%v", err)
			}
			cfg, err := Load(path)
			if err != nil {
				if !strings.Contains(err.Error(), "archive_object_retention.retention") {
					t.Fatalf("拒绝原因应点名具体键，得到 %v", err)
				}
				return
			}
			if _, err := cfg.RetentionPolicy(); err == nil {
				t.Fatalf("retention=%q 必须被拒（0 = 永久保留，与启用回收矛盾；负数 = 写错）", raw)
			} else if !strings.Contains(err.Error(), "archive_object_retention.retention") {
				t.Fatalf("拒绝原因应点名具体键，得到 %v", err)
			}
		})
	}
}

// TestRetentionArchiveObjectRetentionSweepAtHourValidation 钉住每日触发时刻的范围校验。
//
// 时刻写错只表现成「清理在业务高峰跑」，从日志里几乎看不出来——故启动即拒。
func TestRetentionArchiveObjectRetentionSweepAtHourValidation(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		wantErr bool
	}{
		{"3", false},
		{"0", false},
		{"23", false},
		{"24", true},
		{"-1", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "worker.yml")
			yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_object_retention:
    enabled: true
    retention: "730d"
    sweep_at_hour: ` + tc.raw + `
`
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatalf("写夹具失败：%v", err)
			}
			cfg, err := Load(path)
			if err != nil {
				if tc.wantErr {
					return
				}
				t.Fatalf("Load 失败：%v", err)
			}
			_, err = cfg.RetentionPolicy()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("sweep_at_hour=%s 必须被拒", tc.raw)
				}
				if !strings.Contains(err.Error(), "sweep_at_hour") {
					t.Fatalf("拒绝原因应点名 sweep_at_hour，得到 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RetentionPolicy 失败：%v", err)
			}
		})
	}
}

// TestRetentionArchiveObjectRetentionDoesNotImplyDeleteGates 钉住「开关与经济三闸分离」。
//
// 打开归档对象回收**不等于**授权删除：执行侧还要过与热层删除同一套三闸。
// 若哪天有人把它们合并成一个开关，这条用例会转红——那种合并会让
// 「打开清理」顺带获得「删热层」的权限，而两者是不同量级的不可逆操作。
func TestRetentionArchiveObjectRetentionDoesNotImplyDeleteGates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_object_retention:
    enabled: true
    retention: "730d"
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	policy, err := cfg.RetentionPolicy()
	if err != nil {
		t.Fatalf("RetentionPolicy 失败：%v", err)
	}
	if !policy.ArchiveObjectRetention.Active() {
		t.Fatal("前置条件：归档对象回收应已生效")
	}
	if open, reason := policy.DeleteGatesOpen(); open {
		t.Fatalf("启用归档对象回收不得顺带打开删除三闸，得到 %q", reason)
	}
}
