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
		{"bungee", "proxy.log.0", "proxy.log.[1-9]*"},
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

// TestBungeeArchiveGlobExcludesLockAndLiveFile 验证 bungee 归档 glob 只匹配真正的轮转产物
// （FR-504 真机发现的缺陷回归）。
//
// 现场：`proxy.log.[0-9]*` 会把两类**非归档**文件一并当成待导入归档——
//   - `proxy.log.0.lck`：BungeeCord 的 0 字节锁文件。实测被反复尝试导入，在账本留下
//     `ARCHIVE_GZIP_CORRUPT: EOF`（全节点唯一一条导入错误就来自它）。
//   - `proxy.log.0`：**当前活跃日志**本身。把它当归档导入等于重复摄取当前内容。
//
// 修法是把首字符类收窄为 [1-9]（轮转产物从 1 起）；多位数后缀（如 proxy.log.10）仍被覆盖，
// 因为字符类只约束首位。本用例用与 discoverSourceArchives 同一套匹配语义（filepath.Match）
// 双向断言：不该匹配的必须不匹配，该匹配的必须匹配（防"收窄过头把真归档也排除了"）。
func TestBungeeArchiveGlobExcludesLockAndLiveFile(t *testing.T) {
	glob := Lookup("bungee").ArchiveGlob
	for _, name := range []string{"proxy.log.0", "proxy.log.0.lck"} {
		matched, err := filepath.Match(glob, name)
		if err != nil {
			t.Fatalf("glob %q 非法: %v", glob, err)
		}
		if matched {
			t.Errorf("%s 不是轮转归档，不得被 %q 匹配", name, glob)
		}
	}
	for _, name := range []string{"proxy.log.1", "proxy.log.2", "proxy.log.10", "proxy.log.9"} {
		matched, err := filepath.Match(glob, name)
		if err != nil {
			t.Fatalf("glob %q 非法: %v", glob, err)
		}
		if !matched {
			t.Errorf("%s 是轮转归档，必须被 %q 匹配", name, glob)
		}
	}
}
