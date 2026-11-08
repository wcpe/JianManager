package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件钉住 log_catalog.* 配置面（2026-11-05 真机事故修复）。
//
// 背景：分区 Catalog journal 曾被改成纯内存，导致进程重启后该源之前所有天在查询侧
// 变成「不存在」（VL 里有行，没人知道去查）。恢复落盘后的开关必须可配、且默认必须是
// 「开」——它不是可选优化，是正确性依赖。

// TestLoad_LogCatalogJournalDefaults 未配置时：落盘开启、interval/maxBatch 走默认。
//
// 转红方式（实测）：把 JournalDisabled 改成 `c.JournalEnabled == nil`（把未设当成关闭），
// 本用例第一条断言变红。
func TestLoad_LogCatalogJournalDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.yml")
	if err := os.WriteFile(path, []byte("node:\n  name: test\n"), 0o600); err != nil {
		t.Fatalf("write worker.yml: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LogCatalog.JournalDisabled() {
		t.Fatal("未配置时 journal 落盘必须默认开启（关闭会让重启后历史天不可查）")
	}
	// 未设 interval ⇒ 调用方用 catalog 默认（2s），因此这里必须是 (0,false)。
	if d, set := cfg.LogCatalog.JournalSyncIntervalValue(); set || d != 0 {
		t.Fatalf("未配置 interval 应返回 (0,false)，实测 (%v,%v)", d, set)
	}
	if got := cfg.LogCatalog.JournalMaxBatch; got != 0 {
		t.Fatalf("未配置 max_batch 应为 0（调用方取默认），实测 %d", got)
	}
}

// TestLoad_LogCatalogJournalExplicit 显式配置可覆盖三个键。
func TestLoad_LogCatalogJournalExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.yml")
	yml := "node:\n  name: test\nlog_catalog:\n  journal_enabled: false\n  journal_sync_interval: 5s\n  journal_max_batch: 64\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatalf("write worker.yml: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.LogCatalog.JournalDisabled() {
		t.Fatal("journal_enabled: false 必须生效")
	}
	d, set := cfg.LogCatalog.JournalSyncIntervalValue()
	if !set || d != 5*time.Second {
		t.Fatalf("journal_sync_interval: 5s 应解析为 (5s,true)，实测 (%v,%v)", d, set)
	}
	if cfg.LogCatalog.JournalMaxBatch != 64 {
		t.Fatalf("journal_max_batch: 64 应为 64，实测 %d", cfg.LogCatalog.JournalMaxBatch)
	}
}

// TestLogCatalogConfig_SyncIntervalZeroMeansSync 显式 "0s" 必须与「未设」区分开。
//
// 这是本配置面唯一容易写错的地方：两者都得到 duration 0，但语义相反——
// 未设 = 用默认 2s（攒批），显式 0s = 同步写（每次追加即 fsync）。
// 用一个 bool 把两者分开，调用方才能既尊重默认、又允许运维切回同步。
func TestLogCatalogConfig_SyncIntervalZeroMeansSync(t *testing.T) {
	var unset LogCatalogConfig
	if d, set := unset.JournalSyncIntervalValue(); set || d != 0 {
		t.Fatalf("未设应返回 (0,false)，实测 (%v,%v)", d, set)
	}
	explicit := LogCatalogConfig{JournalSyncInterval: "0s"}
	if d, set := explicit.JournalSyncIntervalValue(); !set || d != 0 {
		t.Fatalf("显式 0s 应返回 (0,true)，实测 (%v,%v)", d, set)
	}
	// 非法值回退默认（与 manifest_sync_interval 同口径），不能启动失败。
	for _, bad := range []string{"abc", "-1s", "5 seconds"} {
		cfg := LogCatalogConfig{JournalSyncInterval: bad}
		if d, set := cfg.JournalSyncIntervalValue(); set || d != 0 {
			t.Fatalf("非法值 %q 应回退 (0,false)，实测 (%v,%v)", bad, d, set)
		}
	}
}

// TestLogCatalogConfig_JournalPath journal 落在数据根的 var/log 下（与历史产物同址）。
//
// 同址是刻意的：生产上已有 /home/user/jm/prod/worker/data/var/log/catalog.journal.jsonl
// （2MB / 1217 行 / 198 分区），启用后应直接在其上续写而不是另起一份。
func TestLogCatalogConfig_JournalPath(t *testing.T) {
	var cfg LogCatalogConfig
	got := cfg.JournalPath("/data/worker")
	want := filepath.Join("/data/worker", "var", "log", "catalog.journal.jsonl")
	if got != want {
		t.Fatalf("路径应为 %s，实测 %s", want, got)
	}
	if p := cfg.JournalPath(""); p != "" {
		t.Fatalf("空根目录应返回空路径，实测 %q", p)
	}
}

// TestLogCatalogConfig_BackfillDays 钉住补建窗口的配置面三态语义。
//
// 为何需要三态：0 必须保留给「未设 ⇒ 用 ingest 默认（30 天）」，否则运维无法
// 区分「我没配」与「我要关掉」。所以「显式关闭」用负值，而不是 0。
// 这与 journal_sync_interval 用 bool 区分「未设」与「显式 0s」是同一个理由。
func TestLogCatalogConfig_BackfillDays(t *testing.T) {
	// 未设（0）⇒ 用 ingest 默认值：BackfillDaysValue 返回 0，由 ingest 回落到 30。
	var unset LogCatalogConfig
	if unset.BackfillDisabled() {
		t.Fatal("未设时不得视为关闭")
	}
	if got := unset.BackfillDaysValue(); got != 0 {
		t.Fatalf("未设时应返回 0（表示用默认），实测 %d", got)
	}

	// 显式正值 ⇒ 用它。
	explicit := LogCatalogConfig{BackfillDays: 7}
	if explicit.BackfillDisabled() {
		t.Fatal("正值不得视为关闭")
	}
	if got := explicit.BackfillDaysValue(); got != 7 {
		t.Fatalf("应返回 7，实测 %d", got)
	}

	// 显式负值 ⇒ 关闭；且取值绝不能落到「全量历史」。
	disabled := LogCatalogConfig{BackfillDays: -1}
	if !disabled.BackfillDisabled() {
		t.Fatal("backfill_days=-1 必须表示显式关闭")
	}
	if got := disabled.BackfillDaysValue(); got != 0 {
		t.Fatalf("关闭时取值应为 0（而非负数导致无界回溯），实测 %d", got)
	}
	// 更小的负值也应被识别为关闭（防御手写配置）。
	if !(LogCatalogConfig{BackfillDays: -100}).BackfillDisabled() {
		t.Fatal("任意负值都应识别为关闭")
	}
}

// TestLoad_LogCatalogBackfillDays 配置键 log_catalog.backfill_days 必须能读进来。
func TestLoad_LogCatalogBackfillDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.yml")
	if err := os.WriteFile(path, []byte("node:\n  name: test\nlog_catalog:\n  backfill_days: 14\n"), 0o600); err != nil {
		t.Fatalf("write worker.yml: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.LogCatalog.BackfillDaysValue(); got != 14 {
		t.Fatalf("log_catalog.backfill_days: 14 应生效，实测 %d", got)
	}
}
