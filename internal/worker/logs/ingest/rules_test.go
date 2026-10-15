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
		{"bungee", "proxy.log.0", "proxy.log.[0-9]*"},
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
