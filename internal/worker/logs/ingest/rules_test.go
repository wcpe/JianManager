package ingest

import (
	"path/filepath"
	"testing"
)

// TestLookupBuiltinRules 验证四条内置规则各自解析正确（FR-502）。
func TestLookupBuiltinRules(t *testing.T) {
	cases := []struct {
		key         string
		wantLive    string
		wantArchive string
	}{
		// bukkit：Paper / Spigot / Bukkit 系（现状行为，必须逐字不变）
		{"bukkit", filepath.FromSlash("logs/latest.log"), filepath.FromSlash("logs/*.gz")},
		// bungee：BungeeCord 的日志在**工作目录根**，不在 logs/ 子目录（FR-502 的核心补齐）
		// 归档 glob 为空：BungeeCord 的轮转产物 proxy.log.N 是纯文本，gzip-only 导入器接不了
		// （见 TestBungeeRuleProducesNoArchiveGlob）。
		{"bungee", "proxy.log.0", ""},
		// waterfall：与 Bungee 同源，但按 Bukkit 布局输出
		{"waterfall", filepath.FromSlash("logs/latest.log"), filepath.FromSlash("logs/*.gz")},
		// velocity：独立日志文件名
		{"velocity", filepath.FromSlash("logs/velocity.log"), filepath.FromSlash("logs/*.gz")},
	}
	for _, c := range cases {
		got := Lookup(c.key)
		if got.Key != c.key {
			t.Errorf("Lookup(%q).Key = %q，期望 %q", c.key, got.Key, c.key)
		}
		if got.LiveRelative != c.wantLive {
			t.Errorf("Lookup(%q).LiveRelative = %q，期望 %q", c.key, got.LiveRelative, c.wantLive)
		}
		if got.ArchiveGlob != c.wantArchive {
			t.Errorf("Lookup(%q).ArchiveGlob = %q，期望 %q", c.key, got.ArchiveGlob, c.wantArchive)
		}
	}
}

// TestLookupFallsBackToBukkit 验证未知 key 与空 key 均回退 bukkit 规则。
//
// 这是 FR-502 的向后兼容判据：旧 CP 不下发 log_rule_key（空串），
// 必须与历史行为逐字一致（logs/latest.log + logs/*.gz）。
func TestLookupFallsBackToBukkit(t *testing.T) {
	bukkit := Lookup("bukkit")
	for _, key := range []string{"", "unknown-core", "PAPER", "forge"} {
		got := Lookup(key)
		if got.LiveRelative != bukkit.LiveRelative || got.ArchiveGlob != bukkit.ArchiveGlob {
			t.Errorf("Lookup(%q) = {Live:%q Archive:%q}，期望回退 bukkit {Live:%q Archive:%q}",
				key, got.LiveRelative, got.ArchiveGlob, bukkit.LiveRelative, bukkit.ArchiveGlob)
		}
	}
}

// TestLookupReturnsNoArchiveGlobForUnknownShape 验证 bungee 规则的归档 glob
// 不误用 bukkit 的 *.gz 形态——BungeeCord 轮转产生的是 proxy.log.N 而非 .gz。
func TestLookupReturnsNoArchiveGlobForUnknownShape(t *testing.T) {
	bungee := Lookup("bungee")
	if bungee.ArchiveGlob == filepath.FromSlash("logs/*.gz") {
		t.Error("bungee 规则的归档 glob 不应沿用 bukkit 的 logs/*.gz（BungeeCord 轮转为 proxy.log.N）")
	}
	if bungee.LiveRelative == filepath.FromSlash("logs/latest.log") {
		t.Error("bungee 规则的活动日志不应沿用 logs/latest.log（实际在根目录 proxy.log.0）")
	}
}

// TestBungeeRuleProducesNoArchiveGlob 验证 bungee 规则**不产出归档 glob**（2026-10-15 复核）。
//
// Rule.ArchiveGlob 的口径是「空 = 该核心不产出归档」，而 BungeeCord 正属此列：
// 它的轮转产物 `proxy.log.1..N` 是**纯文本**，而归档导入器（acquire.ImportGzipBounded）
// **只支持 gzip**，discoverSourceArchives 也不做形态过滤。两版 glob 的结局：
//   - `proxy.log.[0-9]*`：还把 `proxy.log.0`（当前活跃日志）与 `proxy.log.0.lck`（0 字节锁文件）
//     一并当成待导入归档；
//   - `proxy.log.[1-9]*`：躲开了上面两者，但**形态本身就导不进来**——每个轮转文件都被登记为
//     kind=gzip 段并记一条 `ARCHIVE_GZIP_CORRUPT` 缺口，该原因**不在自动消解白名单**
//     （ledger/gap_bounds.go），而 ResumeAcquire 对任何未消解缺口一律拒绝
//     ⇒ bungee 源一旦被暂停（积压/容量，生产常态）就**永久停采**，只能人工裁定。
//
// 因此如实置空。要采集纯文本轮转历史需另立 FR（属新功能，不在本项范围）。
// 本用例同时保留原「不误匹配锁文件与当前活跃日志」的意图：空 glob 下没有任何文件名会被匹配。
func TestBungeeRuleProducesNoArchiveGlob(t *testing.T) {
	glob := Lookup(RuleKeyBungee).ArchiveGlob
	if glob != "" {
		t.Errorf("bungee 规则的归档 glob 必须为空（Rule.ArchiveGlob 注释：空 = 该核心不产出归档），"+
			"实际 %q；BungeeCord 的轮转产物 proxy.log.N 是纯文本，gzip-only 导入器只会留下"+
			"不可消解的 ARCHIVE_GZIP_CORRUPT 缺口并使源被暂停后永久停采", glob)
	}
	for _, name := range []string{"proxy.log.0", "proxy.log.0.lck", "proxy.log.1", "proxy.log.10"} {
		matched, err := filepath.Match(glob, name)
		if err != nil {
			t.Fatalf("glob %q 非法: %v", glob, err)
		}
		if matched {
			t.Errorf("%s 不可能是可导入的 gzip 归档，不得被 %q 匹配", name, glob)
		}
	}
}
