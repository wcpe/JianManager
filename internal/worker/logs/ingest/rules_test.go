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
		// bungee：BungeeCord 的日志在**工作目录根**，不在 logs/ 子目录（FR-502 的核心补齐）。
		// 归档 glob 恢复为 `proxy.log.[1-9]*`：轮转产物是**纯文本** proxy.log.N（无扩展名），
		// 由导入器按文件头分流到纯文本实现（见 TestBungeeRuleImportsPlainTextRotations）。
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

// TestLookupKeepsBungeeShapeDistinctFromBukkit 验证 bungee 规则的归档 glob
// 不误用 bukkit 的 *.gz 形态——BungeeCord 轮转产生的是 proxy.log.N 而非 .gz。
func TestLookupKeepsBungeeShapeDistinctFromBukkit(t *testing.T) {
	bungee := Lookup("bungee")
	if bungee.ArchiveGlob == filepath.FromSlash("logs/*.gz") {
		t.Error("bungee 规则的归档 glob 不应沿用 bukkit 的 logs/*.gz（BungeeCord 轮转为 proxy.log.N）")
	}
	if bungee.LiveRelative == filepath.FromSlash("logs/latest.log") {
		t.Error("bungee 规则的活动日志不应沿用 logs/latest.log（实际在根目录 proxy.log.0）")
	}
}

// TestBungeeRuleImportsPlainTextRotations 锁定 bungee 归档 glob 的**收窄口径**与**可导入性**。
//
// 收窄（`[1-9]` 起头）排除两样东西：
//   - `proxy.log.0`：当前活跃日志，由 FileTailer 采集；再按归档导一次就是同一份字节的双计；
//   - `proxy.log.0.lck`：java.util.logging 为**当前**日志文件持有的 0 字节锁文件。
//
// 可导入性（本项修复）：轮转产物是**纯文本**且**没有扩展名**，形态由导入器读**文件头**
// （gzip 魔数 1f 8b）判定后分流到纯文本实现（acquire.ImportTextBounded），并登记为
// kind=rotated_text 的分段。这与此前「glob 必须为空」的结论不同——那时的缺口是
// **没有纯文本实现**：任何被匹配到的文件都会以 ARCHIVE_GZIP_CORRUPT 失败，而该原因不在
// 自动消解白名单（ledger/gap_bounds.go），源一旦被暂停（积压/容量，生产常态）就永久停采。
//
// 残留形态（`proxy.log.N.lck`，0 字节）仍会被本 glob 匹配，但它不再是缺口：纯文本路径把
// 「没有内容的归档」处置为 0 事件 + 直接记为已导入，不产生任何缺口
// （见 acquire.TestImportTextEmptyRotatedFileLeavesNoGap）。
func TestBungeeRuleImportsPlainTextRotations(t *testing.T) {
	glob := Lookup(RuleKeyBungee).ArchiveGlob
	if glob != "proxy.log.[1-9]*" {
		t.Fatalf("bungee 规则的归档 glob 应为 proxy.log.[1-9]*（纯文本轮转可导入），实际 %q", glob)
	}
	for _, name := range []string{"proxy.log.1", "proxy.log.2", "proxy.log.10", "proxy.log.1.lck"} {
		matched, err := filepath.Match(glob, name)
		if err != nil {
			t.Fatalf("glob %q 非法: %v", glob, err)
		}
		if !matched {
			t.Errorf("%s 是轮转产物（纯文本），必须被 %q 匹配", name, glob)
		}
	}
	for _, name := range []string{"proxy.log.0", "proxy.log.0.lck"} {
		matched, err := filepath.Match(glob, name)
		if err != nil {
			t.Fatalf("glob %q 非法: %v", glob, err)
		}
		if matched {
			t.Errorf("%s 不是轮转历史（当前活跃日志或它的锁文件），不得被 %q 匹配"+
				"——它会被当作归档再导一次，形成同一份字节的双计", name, glob)
		}
	}
}
