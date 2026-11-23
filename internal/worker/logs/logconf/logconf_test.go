package logconf

// 本文件锤死搬移后的时区与字符集语义。
//
// # 为何要重测一遍已经能跑的东西
//
// 这两个函数是从旧包**挪位置**的。挪移最容易出的事是“看着一样但细微行为变了”
// （比如把 local 分支漏了、把空串当成非法）。而时区配错的历史后果是
// **整源时间轴偏移 8 小时**且只能靠人眼在查询结果里发现。
// 搬一次就要钉一次，成本极低。

import (
	"testing"
	"time"
)

// TestParseTimeZoneCases 逐格验证取值语义（与旧实现一字不差）。
func TestParseTimeZoneCases(t *testing.T) {
	cases := []struct {
		raw  string
		want string // 期望的 Location 名
	}{
		{"", "UTC"},                          // 空串 = UTC（未配置的源零行为变化）
		{"utc", "UTC"},                       // 显式 UTC
		{"UTC", "UTC"},                       // 大小写不敏感
		{"z", "UTC"},                         // 别名
		{"gmt", "UTC"},                       // 别名
		{"Asia/Hong_Kong", "Asia/Hong_Kong"}, // IANA 名（生产实际配置）
		{"Asia/Shanghai", "Asia/Shanghai"},
		{" local ", ""}, // local 特殊：期望按本机时区，下面单独断言
	}
	for _, tc := range cases {
		loc, err := ParseTimeZone(tc.raw)
		if err != nil {
			t.Errorf("ParseTimeZone(%q) 报错: %v", tc.raw, err)
			continue
		}
		if loc == nil {
			t.Errorf("ParseTimeZone(%q) 返回 nil（应为 UTC 而非 nil）", tc.raw)
			continue
		}
		if tc.want != "" && loc.String() != tc.want {
			t.Errorf("ParseTimeZone(%q) = %q，期望 %q", tc.raw, loc.String(), tc.want)
		}
	}
}

// TestParseTimeZoneLocalFollowsProcess 验证 local/node/host 三个写法都指向本机时区。
func TestParseTimeZoneLocalFollowsProcess(t *testing.T) {
	for _, raw := range []string{"local", "node", "host"} {
		loc, err := ParseTimeZone(raw)
		if err != nil {
			t.Fatalf("ParseTimeZone(%q): %v", raw, err)
		}
		if loc != time.Local {
			t.Errorf("ParseTimeZone(%q) 应返回 time.Local，实际 %q", raw, loc)
		}
	}
}

// TestParseTimeZoneRejectsGarbage 验非法值**必须**报错，不能静默回退 UTC。
//
// # 为何这条是关键
//
// 静默回退会让「我明明配了 Asia/Hong_Kong」变成「实际上跑 UTC」，
// 而表现是“时间怎么全偏了 8 小时”、排查方向被完全带偏。
func TestParseTimeZoneRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"Mars/Olympus", "Asia/Nowhere", "12345", "+08:00"} {
		if _, err := ParseTimeZone(raw); err == nil {
			t.Errorf("ParseTimeZone(%q) 应当报错（不能静默回退 UTC）", raw)
		}
	}
	if IsValidTimeZone("Mars/Olympus") {
		t.Error("IsValidTimeZone 对非法值应返回 false")
	}
	if !IsValidTimeZone("Asia/Hong_Kong") {
		t.Error("IsValidTimeZone 对合法 IANA 名应返回 true")
	}
	if !IsValidTimeZone("") {
		t.Error("IsValidTimeZone 对空串应返回 true（= UTC）")
	}
}

// TestIsValidCharsetDelegatesToNormalize 验字符集校验确实转到 normalize。
//
// 真源在 normalize（判定与解码的唯一实现），这里只是一层转发。若哪天有人
// 在这里另写一份白名单，就会出现“两边接受不同取值”的分叉。
func TestIsValidCharsetDelegatesToNormalize(t *testing.T) {
	for _, ok := range []string{"", "auto", "utf-8", "gbk", "gb18030", "GB2312", "cp936"} {
		if !IsValidCharset(ok) {
			t.Errorf("IsValidCharset(%q) 应为 true", ok)
		}
	}
	for _, bad := range []string{"latin-1", "utf16", "bogus"} {
		if IsValidCharset(bad) {
			t.Errorf("IsValidCharset(%q) 应为 false", bad)
		}
	}
}

// TestInstanceRegistrationCarriesPathIntent 锤住登记结构体**携带路径意图**。
//
// RuleKey / RelativePath 是 CP 下发的路径意图。新采集器目前靠 WorkDir 推导，
// 但登记层必须忠实携带——否则将来要应用规则/覆盖时得改协议。
func TestInstanceRegistrationCarriesPathIntent(t *testing.T) {
	reg := InstanceRegistration{
		UUID: "u1", TargetID: "inst:1", Generation: "g1", Mode: "FILE_PRIMARY",
		WorkDir: "/srv/a", RuleKey: "bungee", RelativePath: "proxy.log.0",
	}
	if reg.RuleKey != "bungee" || reg.RelativePath != "proxy.log.0" {
		t.Fatalf("路径意图丢失：%+v", reg)
	}
}
