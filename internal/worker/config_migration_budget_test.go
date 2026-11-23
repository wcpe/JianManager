package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件钉住一条**升级兼容性**契约：不写 `log_retention.migration.*` 的存量配置
// 必须仍能启动。
//
// 背景（2026-10-27 生产事故）：`62b96270` 把这两项改成「必须显式声明正的有限时长」，
// 但 `SetDefault` 给的是空串、`configs/worker.yml` 样例也没写 ⇒ **任何升级前合法的存量配置
// （包括正在生产跑的那份）升级后都启动失败**。现象是 worker 单元反复重启后进入 failed
// （`Start request repeated too quickly`），日志里只有一行
// `log_retention.migration.copy_verify_budget 必须显式声明正的有限时长`。
//
// 此前没有一条用例能接住它：所有夹具都显式写了 `copy_verify_budget: 2h`，
// 于是「未声明」这个**最常见的真实形态**从未被跑到。

// TestMigrationBudgetsAbsentStillLoads 验证两项都未声明时能正常加载并拿到默认值。
func TestMigrationBudgetsAbsentStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("存量配置（未声明 migration 预算）必须可加载，实际失败：%v", err)
	}
	if cfg == nil {
		t.Fatal("配置为 nil")
	}
}

// TestMigrationBudgetsZeroOrNegativeStillRejected 钉住“显式写错要拒”的边界：
// 未声明用默认，但写了 0 / 负数必须拒——静默改成默认等于把运维的意图掉包。
func TestMigrationBudgetsZeroOrNegativeStillRejected(t *testing.T) {
	for _, raw := range []string{"0", "0s", "-1h"} {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "worker.yml")
			yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: ` + raw + `
    recovery_budget: 24h
`
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatalf("写夹具失败：%v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("显式写 %q 必须拒绝，实际通过了", raw)
			}
		})
	}
}

// TestMigrationBudgetDefaultsAreBounded 钉住默认值本身是「正的有限时长」：
// 它们会进 `required = 24h + DriverInterval + CopyVerify + Recovery` 的余量评估，
// 取太大就会把「hot 27d / native 30d」这类只剩 3d 余量的存量配置误拒。
func TestMigrationBudgetDefaultsAreBounded(t *testing.T) {
	for name, d := range map[string]time.Duration{
		"defaultCopyVerifyBudget": defaultCopyVerifyBudget,
		"defaultRecoveryBudget":   defaultRecoveryBudget,
	} {
		if d <= 0 {
			t.Fatalf("%s 必须为正的有限时长，得到 %s", name, d)
		}
		if d > 24*time.Hour {
			t.Fatalf("%s=%s 过大：会把余量偏紧的存量配置误拒", name, d)
		}
	}
}
