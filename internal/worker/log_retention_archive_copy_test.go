package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件守 FR-501「导出归档」的**配置面**：开关默认关、缺目的地启动即拒、
// 三闸里第三闸（VL 侧）读的是 `log_vl.delete_enabled`。
//
// 为什么这些都要走完整链路（YAML → viper → RetentionPolicy）：
// 本包的配置陷阱此前踩过不止一次（`trigger.interval` 被静默丢弃、
// `90d` 让整个 Load 失败）。「键写在 YAML 里但没生效」在现场没有报错，
// 只表现为「功能没按预期跑」——正好是最难查的一类。

// TestRetentionArchiveCopyDefaultsOff 钉住「默认部署零行为变化」。
//
// 这条不是客套：archive_copy 会把到期数据写到另一个目的地，
// 若默认打开，升级动作本身就等于替运维做了「开始导出」的决定。
func TestRetentionArchiveCopyDefaultsOff(t *testing.T) {
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
	if policy.ArchiveCopy.Enabled {
		t.Fatal("archive_copy 必须默认关闭")
	}
	// 未显式给根目录时也不得凭空造一个默认值：归档写在哪块盘是运维的决定。
	if got := strings.TrimSpace(cfg.LogRetention.ArchiveCopy.Root); got != "" {
		t.Fatalf("archive_copy.root 不得有默认值，得到 %q", got)
	}
	// 三闸默认必须全关（用户护栏）。
	if open, reason := policy.DeleteGatesOpen(); open {
		t.Fatalf("默认策略下删除三闸必须全关，得到 %q", reason)
	}
}

// TestRetentionArchiveCopyEnabledWithoutRootRejected 钉住「开了开关但没处写」必须启动即拒。
//
// 若静默按未启用处理，运维看到的是 `enabled: true` 而实际什么都没发生，
// 到期数据继续占着热层直到盘满——正是本功能要避免的结局。
func TestRetentionArchiveCopyEnabledWithoutRootRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		// 在 Load 阶段就拒也满足要求（越早越好）。
		if !strings.Contains(err.Error(), "archive_copy.root") {
			t.Fatalf("拒绝原因应点名 archive_copy.root，得到 %v", err)
		}
		return
	}
	if _, err := cfg.RetentionPolicy(); err == nil {
		t.Fatal("启用导出归档但未配置 root 必须报错（不得静默降级为空转）")
	} else if !strings.Contains(err.Error(), "archive_copy.root") {
		t.Fatalf("拒绝原因应点名 archive_copy.root，得到 %v", err)
	}
}

// TestRetentionArchiveCopyAndVLDeleteGateTakesEffect 钉住三个键真的通到策略层：
// `archive_copy.enabled` / `archive_copy.root` / `log_vl.delete_enabled`。
//
// 第三闸尤其重要：它是「受管 VL 真的带 -delete.enable」这个**事实**在策略层的代表，
// 若它恒为 false，回收热层就永远不可达（fail-safe，但也就等于功能没做完）；
// 若它恒为 true，三闸就退化成两闸（危险方向）。两个方向都由本用例钉住。
func TestRetentionArchiveCopyAndVLDeleteGateTakesEffect(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deleteOn bool
	}{
		{"VL 删除关闭（默认）", false},
		{"VL 删除打开", true},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "worker.yml")
		// 夹具里已经有 log_vl: 段，重复写一个同名顶层键会让 YAML 直接拒解析，
		// 故用替换把键插进既有段内（这也是最贴近运维真实改法的形态）。
		base := wiringManagedReceiverYAML
		if tc.deleteOn {
			base = strings.Replace(base, "  start_cold: true", "  start_cold: true\n  delete_enabled: true", 1)
		}
		yaml := base + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
    root: /var/lib/jianmanager/log-archive
`
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatalf("%s：写夹具失败：%v", tc.name, err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("%s：Load 失败：%v", tc.name, err)
		}
		policy, err := cfg.RetentionPolicy()
		if err != nil {
			t.Fatalf("%s：RetentionPolicy 失败：%v", tc.name, err)
		}
		if !policy.ArchiveCopy.Enabled {
			t.Fatalf("%s：archive_copy.enabled 未传到策略层", tc.name)
		}
		if got := cfg.LogRetention.ArchiveCopy.Root; got != "/var/lib/jianmanager/log-archive" {
			t.Fatalf("%s：archive_copy.root 未解析，得到 %q", tc.name, got)
		}
		if policy.Sweep.VLDeleteEnabled != tc.deleteOn {
			t.Fatalf("%s：第三闸应与 log_vl.delete_enabled 一致，得到 %v",
				tc.name, policy.Sweep.VLDeleteEnabled)
		}
	}
}

// TestRetentionArchiveCopyRootFromConfig 保证 root 只被 TrimSpace 影响，不被改写。
func TestRetentionArchiveCopyRootFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	yaml := wiringManagedReceiverYAML + `
log_retention:
  enabled: true
  migration:
    copy_verify_budget: 2h
    recovery_budget: 24h
  archive_copy:
    enabled: true
    root: "/data/log-archive"
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	if _, err := cfg.RetentionPolicy(); err != nil {
		t.Fatalf("RetentionPolicy 失败：%v", err)
	}
	if got := cfg.LogRetention.ArchiveCopy.Root; got != "/data/log-archive" {
		t.Fatalf("root 应原样保留，得到 %q", got)
	}
}
