package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// config_sensitive_regression_test.go —— 敏感字段遮罩/还原/溯源的表驱动回归。
//
// 合并了原先的 restore_mask_probe_test.go（7 条）与 trace_leak_probe_test.go（4 条），
// 并补齐 2026-11-14 复审指出的 4 类实际红分支：
//   - 父对象敏感路径：traced 里展开的子键也得遮
//   - 还原时保留原类型：数值 / 空串 / 父对象，不能只认非空字符串
//   - JSON 整数精度：大整数不得因 float64 丢精度
//   - properties：只认**完整值**占位符，值里含 *** 不等于占位符

// ---------------------------------------------------------------------------
// 一、遮罩 → 编辑 → 还原 往返（保留原类型）
// ---------------------------------------------------------------------------

// TestSensitiveMaskRestoreRoundTrip 验证「用户什么都没改」时，敏感值必须原样回来。
//
// 这是最容易被忽略、后果最严重的一类：用户打开配置、改了无关一行、保存，
// 敏感字段就被 `***` 永久覆盖。每行一个场景，断言都用同一个模板。
func TestSensitiveMaskRestoreRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		format string
		paths  []string
		// original 是磁盘上的真实内容（head）。
		original string
		// mutate 在遮罩后的文本上做无关修改，返回改后文本。
		mutate func(masked string) string
		// mustContain 还原后必须出现的片段（证明真值回来了）。
		mustContain []string
		// mustNotContain 还原后不得出现的片段。
		mustNotContain []string
	}{
		{
			name:   "嵌套对象路径-多行JSON",
			format: "json",
			paths:  []string{"database.password"},
			original: `{
  "database": {"password": "realDB", "host": "localhost"},
  "motd": "before"
}`,
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"realDB", "after", "localhost"},
		},
		{
			// 复审场景：紧凑 JSON 没有换行，行级匹配完全失效。
			name:        "紧凑JSON",
			format:      "json",
			paths:       []string{"database.password"},
			original:    `{"database":{"password":"reviewSentinelA"},"motd":"before"}`,
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"reviewSentinelA", "after"},
		},
		{
			name:        "同名叶键-只还原声明的那一个",
			format:      "json",
			paths:       []string{"database.password"},
			original:    `{"database":{"password":"realDB"},"cache":{"password":"realCache"}}`,
			mutate:      func(s string) string { return s },
			mustContain: []string{"realDB", "realCache"},
		},
		{
			name:        "数组下标路径",
			format:      "json",
			paths:       []string{"servers.0.pass"},
			original:    `{"servers":[{"pass":"p0"},{"pass":"p1"}]}`,
			mutate:      func(s string) string { return s },
			mustContain: []string{"p0", "p1"},
		},
		{
			// 复审场景：数值敏感值（otp=123456）不能变成字符串 "***"。
			name:        "数值敏感值保留类型",
			format:      "json",
			paths:       []string{"otp"},
			original:    `{"otp":123456,"motd":"before"}`,
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"123456", "after"},
			// 不得把数字变成字符串
			mustNotContain: []string{`"123456"`},
		},
		{
			// 复审场景：空串敏感值也要能还原。
			name:        "空串敏感值",
			format:      "json",
			paths:       []string{"secret"},
			original:    `{"secret":"","motd":"before"}`,
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"after"},
		},
		{
			// 复审场景：父对象路径（database）被整体遮罩后要能整体还原。
			name:        "父对象路径整体还原",
			format:      "json",
			paths:       []string{"database"},
			original:    `{"database":{"password":"reviewParentSecret","host":"db.example"},"motd":"before"}`,
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"reviewParentSecret", "db.example", "after"},
		},
		{
			name:        "YAML嵌套路径",
			format:      "yaml",
			paths:       []string{"database.password"},
			original:    "database:\n  password: realDB\n  host: localhost\nmotd: before\n",
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"realDB", "localhost", "after"},
		},
		{
			name:        "properties整条路径即键",
			format:      "properties",
			paths:       []string{"rcon.password"},
			original:    "rcon.password=real\nmotd=before\n",
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"rcon.password=real", "motd=after"},
		},
		{
			// 复审场景：properties 行带前导缩进（2 空格）。
			// 缺陷：restoreMaskedProperties 把 sep（基于原始 line 的下标）拿去切 trimmed，
			// 缩进后偏移全错 → 无法还原；8 空格时直接 slice bounds panic。
			name:        "properties带2空格缩进",
			format:      "properties",
			paths:       []string{"password"},
			original:    "  password=real-secret\n  motd=before\n",
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"real-secret", "after"},
		},
		{
			// 同上，8 空格时旧实现会 panic（slice bounds out of range）。
			name:        "properties带8空格缩进",
			format:      "properties",
			paths:       []string{"password"},
			original:    "        password=real-secret\n        motd=before\n",
			mutate:      func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain: []string{"real-secret", "after"},
		},
		{
			// 复审场景：JSON null 作为敏感值。lookup 返回 ok=true 已区分“缺失”，
			// 但旧实现又用 realVal==nil 把真实 null 当成缺失 → null 变 "***"。
			name:           "JSON null 敏感值",
			format:         "json",
			paths:          []string{"secret"},
			original:       `{"secret":null,"motd":"before"}`,
			mutate:         func(s string) string { return strings.Replace(s, "before", "after", 1) },
			mustContain:    []string{"after"},
			mustNotContain: []string{`"***"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 1) 遮罩：产出用户真正看到的内容。
			masked := maskSensitiveInContent(tc.format, tc.original, tc.paths)
			// 2) 用户做无关修改。
			edited := tc.mutate(masked)
			// 3) 还原。
			got := restoreMaskedInContent(tc.format, edited, tc.original, tc.paths)

			for _, want := range tc.mustContain {
				require.Contains(t, got, want, "还原后必须包含 %q，实际:\n%s", want, got)
			}
			for _, notWant := range tc.mustNotContain {
				require.NotContains(t, got, notWant, "还原后不得包含 %q，实际:\n%s", notWant, got)
			}
		})
	}
}

// TestRestoreMaskedKeepsUserEdits 用户真的改了敏感值（不再是占位符）时不得被旧值覆盖。
func TestRestoreMaskedKeepsUserEdits(t *testing.T) {
	cases := []struct {
		name, format string
		paths        []string
		head, edited string
		mustContain  string
		mustNotHave  string
	}{
		{
			name:   "json用户改了新密码",
			format: "json", paths: []string{"db.pass"},
			head: `{"db":{"pass":"oldValue"}}`, edited: `{"db":{"pass":"brandNew"}}`,
			mustContain: "brandNew", mustNotHave: "oldValue",
		},
		{
			// 复审场景4：值里含 *** 但整体不等于占位符 → 是用户输入，必须保留。
			name:   "properties值含星号但不是占位符",
			format: "properties", paths: []string{"password"},
			head: "password=old-value\n", edited: "password=new***value\n",
			mustContain: "new***value", mustNotHave: "old-value",
		},
		{
			// 用户把一个敏感值改成空串 = 主动清空，不得被还原。
			name:   "json用户清空敏感值",
			format: "json", paths: []string{"db.pass"},
			head: `{"db":{"pass":"oldValue"}}`, edited: `{"db":{"pass":""}}`,
			mustNotHave: "oldValue",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := restoreMaskedInContent(tc.format, tc.edited, tc.head, tc.paths)
			if tc.mustContain != "" {
				require.Contains(t, got, tc.mustContain, "用户输入必须保留，实际:\n%s", got)
			}
			if tc.mustNotHave != "" {
				require.NotContains(t, got, tc.mustNotHave, "不得用旧值覆盖用户输入，实际:\n%s", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 二、JSON 整数精度
// ---------------------------------------------------------------------------

// TestSensitiveMaskRestoreKeepsJSONIntegerPrecision 遮罩/还原两端都不得让大整数丢精度。
//
// 复审场景3：accountId=9007199254740993（>2^53）经 float64 会变成 ...992。
// 这是**与敏感字段无关的旁路损坏**——用户只改了 motd，accountId 却变了。
func TestSensitiveMaskRestoreKeepsJSONIntegerPrecision(t *testing.T) {
	const original = `{"password":"reviewLargeSecret","accountId":9007199254740993,"motd":"before"}`
	paths := []string{"password"}

	masked := maskSensitiveInContent("json", original, paths)
	require.NotContains(t, masked, "reviewLargeSecret", "遮罩必须生效")
	require.Contains(t, masked, "9007199254740993",
		"遮罩阶段就丢了整数精度（slide 到 ...992），实际: %s", masked)

	edited := strings.Replace(masked, "before", "after", 1)
	got := restoreMaskedInContent("json", edited, original, paths)
	require.Contains(t, got, "reviewLargeSecret", "敏感值必须还原")
	require.Contains(t, got, "9007199254740993",
		"还原阶段丢了整数精度（slide 到 ...992），实际: %s", got)
	require.NotContains(t, got, "9007199254740992")
	require.Contains(t, got, "after")
}

// ---------------------------------------------------------------------------
// 三、Effective 溯源脱敏
// ---------------------------------------------------------------------------

func newTracePreview(content string, traced map[string]TracedValue) *EffectivePreview {
	return &EffectivePreview{
		InstanceID: 1, ConfigFileID: 1, Format: string(model.ConfigFormatJSON),
		Content: content, Traced: traced,
	}
}

// TestEffectiveTraceMasking 验证 traced 里不得残留敏感原文（含父对象展开的子键）。
func TestEffectiveTraceMasking(t *testing.T) {
	cases := []struct {
		name        string
		paths       []string
		content     string
		traced      map[string]TracedValue
		mustKeep    []string // 非敏感字段的溯源必须保留
		mustNotHave []string // 敏感原文不得出现
	}{
		{
			name:    "数组父键展开的叶必须遮",
			paths:   []string{"servers.0.pass"},
			content: `{"servers":[{"pass":"secret0","host":"h0"},{"pass":"secret1","host":"h1"}]}`,
			traced: map[string]TracedValue{"servers": {Value: []any{
				map[string]any{"pass": "secret0", "host": "h0"},
				map[string]any{"pass": "secret1", "host": "h1"},
			}}},
			mustKeep: []string{"h0"}, mustNotHave: []string{"secret0"},
		},
		{
			name:    "嵌套对象父键展开的叶必须遮",
			paths:   []string{"database.password"},
			content: `{"database":{"password":"dbSecret","host":"localhost"}}`,
			traced: map[string]TracedValue{"database": {Value: map[string]any{
				"password": "dbSecret", "host": "localhost",
			}}},
			mustKeep: []string{"localhost"}, mustNotHave: []string{"dbSecret"},
		},
		{
			// 复审场景1：敏感路径是**父对象**，traced 里却把子键展开了。
			name:    "敏感路径为父对象-展开的子键全遮",
			paths:   []string{"database"},
			content: `{"database":{"password":"reviewParentSecret","host":"db.example"},"motd":"before"}`,
			traced: map[string]TracedValue{
				"database.password": {Value: "reviewParentSecret"},
				"database.host":     {Value: "db.example"},
				"motd":              {Value: "before"},
			},
			mustKeep: []string{"before"}, mustNotHave: []string{"reviewParentSecret", "db.example"},
		},
		{
			name:    "深层数组下标",
			paths:   []string{"a.0.b.1.pass"},
			content: `{"a":[{"b":[{"pass":"p0"},{"pass":"deepSecret"}]}]}`,
			traced: map[string]TracedValue{"a": {Value: []any{map[string]any{"b": []any{
				map[string]any{"pass": "p0"}, map[string]any{"pass": "deepSecret"},
			}}}}},
			mustNotHave: []string{"deepSecret"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preview := newTracePreview(tc.content, tc.traced)
			sensitiveJSON, err := json.Marshal(tc.paths)
			require.NoError(t, err)
			applySensitiveMask(string(sensitiveJSON), preview)

			raw, err := json.Marshal(preview.Traced)
			require.NoError(t, err)
			for _, notWant := range tc.mustNotHave {
				require.NotContains(t, string(raw), notWant,
					"traced 泄漏 %q，实际: %s", notWant, string(raw))
			}
			for _, keep := range tc.mustKeep {
				require.Contains(t, string(raw), keep,
					"非敏感字段的溯源不应被误杀: %q", keep)
			}
		})
	}
}

// TestEffectiveTraceExactKeyStillMasked 精确标量键（properties 常见）不得回退。
func TestEffectiveTraceExactKeyStillMasked(t *testing.T) {
	preview := &EffectivePreview{
		InstanceID: 1, ConfigFileID: 1, Format: string(model.ConfigFormatProperties),
		Content: "rcon.password=rconSecret\n",
		Traced:  map[string]TracedValue{"rcon.password": {Value: "rconSecret"}},
	}
	applySensitiveMask(`["rcon.password"]`, preview)
	require.Equal(t, maskedSecretPlaceholder, preview.Traced["rcon.password"].Value)
	require.NotContains(t, preview.Content, "rconSecret")
}
