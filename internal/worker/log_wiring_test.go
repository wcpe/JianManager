package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件守住两处**此前完全没有装配点**的配置：`log.level` / `log.format`，
// 以及 `log_capacity.max_wal_bytes` 的有限默认值。
//
// 共同形态是「配置项存在、文档写着、但没有任何代码读它」——现场表现为
// 「改了配置没反应」，而这类缺陷不会产生任何报错，只能靠接线回归钉住。

func loadTestConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load(filepath.Join(t.TempDir(), "nonexistent-worker.yml"))
	if err != nil {
		t.Fatalf("加载默认配置失败：%v", err)
	}
	return cfg
}

// TestLogLevelIsParsedAndRejectsGarbage 钉住 `log.level` 有装配点且非法值启动即拒。
//
// 该键此前零装配：Worker 从不调用 slog.SetDefault，因此写 debug 不产生任何 DEBUG 输出、
// 写 error 也压不住 INFO。
func TestLogLevelIsParsedAndRejectsGarbage(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{" Debug ", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
	}
	for _, c := range cases {
		cfg := loadTestConfig(t)
		cfg.Log.Level = c.in
		got, err := cfg.LogLevel()
		if err != nil {
			t.Errorf("log.level=%q 应被接受，得到 %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("log.level=%q 应解析为 %v，得到 %v", c.in, c.want, got)
		}
	}
	// 写错等级会让排障所依赖的输出静默消失，必须启动即拒而不是回退。
	for _, bad := range []string{"verbose", "trace", "1", "infox"} {
		cfg := loadTestConfig(t)
		cfg.Log.Level = bad
		if _, err := cfg.LogLevel(); err == nil {
			t.Errorf("log.level=%q 非法必须被拒（回退会静默改变排障可见性）", bad)
		}
	}
}

func TestLogFormatIsParsedAndRejectsGarbage(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "text"}, {"text", "text"}, {"TEXT", "text"}, {"json", "json"}, {" Json ", "json"},
	} {
		cfg := loadTestConfig(t)
		cfg.Log.Format = c.in
		got, err := cfg.LogFormat()
		if err != nil || got != c.want {
			t.Errorf("log.format=%q 应解析为 %q，得到 %q（err=%v）", c.in, c.want, got, err)
		}
	}
	for _, bad := range []string{"yaml", "logfmt", "1"} {
		cfg := loadTestConfig(t)
		cfg.Log.Format = bad
		if _, err := cfg.LogFormat(); err == nil {
			t.Errorf("log.format=%q 非法必须被拒", bad)
		}
	}
}

// TestMaxWALBytesHasFiniteDefault 钉住「WAL 有字节上界」。
//
// 此前默认 0 = 不限，唯一的上界形同不存在（压测实测单 Worker WAL 涨到 1.9GB
// 而门禁的 PASSIVE 分支从不触发）。
func TestMaxWALBytesHasFiniteDefault(t *testing.T) {
	cfg := loadTestConfig(t)
	if cfg.LogCapacity.MaxWALBytes == 0 {
		t.Fatal("max_wal_bytes 默认必须是有限值——「不限」不是「很宽」，是「没有上界」")
	}
	if cfg.LogCapacity.MaxWALBytes != DefaultMaxWALBytes {
		t.Fatalf("默认应为 %d，得到 %d", DefaultMaxWALBytes, cfg.LogCapacity.MaxWALBytes)
	}
	// 默认值必须远高于任何正常态，否则会在稳态误触发 PAUSED 把采集停掉。
	// 真机稳态：reclaim 正常推进时 WAL 是 KB 量级（107MB → 256B）。
	const normalSteadyState = 64 << 20
	if cfg.LogCapacity.MaxWALBytes < normalSteadyState {
		t.Fatalf("默认值 %d 低于正常稳态量级 %d，会在稳态误暂停采集",
			cfg.LogCapacity.MaxWALBytes, normalSteadyState)
	}
	if notice := cfg.WALBudgetNotice(); notice != "" {
		t.Fatalf("默认不是「不限」，不应产生提醒，得到 %q", notice)
	}
}

// TestWALBudgetNoticeNamesTheUnlimitedCase：「显式不限」必须被点名。
//
// 因为 viper 默认值与显式写 0 落到同一个值上，现场光看配置值分不清
// 「我没配」与「我配成不限」——两者都该被提醒，而默认有限之后前者不再出现。
func TestWALBudgetNoticeNamesTheUnlimitedCase(t *testing.T) {
	cfg := loadTestConfig(t)
	cfg.LogCapacity.MaxWALBytes = 0
	notice := cfg.WALBudgetNotice()
	if notice == "" {
		t.Fatal("显式不限必须提醒（否则「没有上界」这一状态在现场不可见）")
	}
	for _, want := range []string{"max_wal_bytes=0", "不限", "上界"} {
		if !strings.Contains(notice, want) {
			t.Errorf("提醒应包含 %q：%s", want, notice)
		}
	}
}

// TestArchiveScanIntervalDefaultsOff 钉住「定时归档导入扫描默认关」。
//
// 为什么默认必须关：常规源在每个采集轮里已经自动发现并导入归档，定时扫描的价值只在
// 「采集轮停了、归档还在攒」的场景；默认开会在每个部署上多出一份周期性目录扫描与账本写入。
// 这条同时守住 ④ 的「定时关闭时不触发」：interval=0 时 RunArchiveScan 不启动任何 goroutine。
func TestArchiveScanIntervalDefaultsOff(t *testing.T) {
	cfg := loadTestConfig(t)
	got, err := cfg.IngestArchiveScanInterval()
	if err != nil {
		t.Fatalf("默认值不应报错: %v", err)
	}
	if got != 0 {
		t.Fatalf("log_ingest.archive_scan_interval 默认必须为 0（关闭），实测 %v", got)
	}

	// 显式 "0" 与负值同样表示关闭。
	cfg.LogIngest.ArchiveScanInterval = "0"
	if got, err := cfg.IngestArchiveScanInterval(); err != nil || got != 0 {
		t.Fatalf("显式 0 应表示关闭: got=%v err=%v", got, err)
	}
	cfg.LogIngest.ArchiveScanInterval = "-5s"
	if got, err := cfg.IngestArchiveScanInterval(); err != nil || got != 0 {
		t.Fatalf("负值应表示关闭: got=%v err=%v", got, err)
	}
	// 合法正值正常解析。
	cfg.LogIngest.ArchiveScanInterval = "10m"
	if got, err := cfg.IngestArchiveScanInterval(); err != nil || got != 10*time.Minute {
		t.Fatalf("10m 应解析为 10 分钟: got=%v err=%v", got, err)
	}
	// 非法值必须报错（不得静默回退，否则「开了」会变成隐性关闭）。
	cfg.LogIngest.ArchiveScanInterval = "十点"
	if _, err := cfg.IngestArchiveScanInterval(); err == nil {
		t.Fatal("非法时长必须报错（静默回退会让「开了」变成隐性关闭）")
	}
}

// TestResolveGapsBudgetWiringAndRejectsGarbage 钉住整节点解算预算（2026-10-02 缺陷修复）的配置面：
// 默认零配置零行为变化（零值 → ingest 包默认 256 源 / 2m），合法值原样下发，
// 非法值**启动即拒**（把「有界」配成 0/负数会让解算恒不可用，而现场只表现为「解缺口不生效」）。
func TestResolveGapsBudgetWiringAndRejectsGarbage(t *testing.T) {
	cfg := loadTestConfig(t)

	budget, err := cfg.IngestResolveGapsBudget()
	if err != nil {
		t.Fatalf("默认值不应报错: %v", err)
	}
	if budget == nil || budget.MaxSources != 0 || budget.MaxDuration != 0 {
		t.Fatalf("默认必须是零值（由 ingest 兜默认，零配置零行为变化），实测 %+v", budget)
	}

	cfg.LogIngest.ResolveGapsMaxSources = 8
	cfg.LogIngest.ResolveGapsMaxDuration = "30s"
	budget, err = cfg.IngestResolveGapsBudget()
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	if budget.MaxSources != 8 || budget.MaxDuration != 30*time.Second {
		t.Fatalf("合法值必须原样下发，实测 %+v", budget)
	}

	cfg.LogIngest.ResolveGapsMaxSources = -1
	if _, err := cfg.IngestResolveGapsBudget(); err == nil {
		t.Fatal("负数源数预算必须启动即拒（静默回退会让「有界」变成隐性失效）")
	}
	cfg.LogIngest.ResolveGapsMaxSources = 0
	cfg.LogIngest.ResolveGapsMaxDuration = "0s"
	if _, err := cfg.IngestResolveGapsBudget(); err == nil {
		t.Fatal("零时长必须启动即拒（那会让解算恒不可用）")
	}
	cfg.LogIngest.ResolveGapsMaxDuration = "两分钟"
	if _, err := cfg.IngestResolveGapsBudget(); err == nil {
		t.Fatal("非法时长必须报错（不得静默回退默认）")
	}
}
