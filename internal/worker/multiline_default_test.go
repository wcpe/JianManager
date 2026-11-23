package config

import (
	"testing"
)

// 默认开启：没配 multiline 段也必须产出规则。
func TestMultilineEnabledByDefault(t *testing.T) {
	c := &Config{}
	c.LogTailer.Enabled = true

	rule, timeout, err := c.MultilineSettings()
	if err != nil {
		t.Fatalf("默认配置不应报错: %v", err)
	}
	if rule == nil {
		t.Fatal("没配 multiline 时必须默认启用（这是本次刻意偏离 Filebeat/Fluent Bit 的地方）")
	}
	if timeout != 0 {
		t.Logf("timeout=%s（0 = 用实现包默认 5s）", timeout)
	}
	t.Logf("默认规则已启用 ✓")
}

// 显式关闭必须生效。
func TestMultilineCanBeDisabled(t *testing.T) {
	c := &Config{}
	c.LogTailer.Multiline.Disabled = true

	rule, _, err := c.MultilineSettings()
	if err != nil {
		t.Fatalf("关闭不应报错: %v", err)
	}
	if rule != nil {
		t.Fatal("Disabled=true 时必须不启用多行")
	}
	t.Logf("显式关闭生效 ✓")
}

// 自定义 pattern 必须覆盖默认。
func TestMultilineCustomPatternOverridesDefault(t *testing.T) {
	c := &Config{}
	c.LogTailer.Multiline.Pattern = `^CUSTOM`
	c.LogTailer.Multiline.Negate = false
	c.LogTailer.Multiline.Match = "after"

	rule, _, err := c.MultilineSettings()
	if err != nil {
		t.Fatalf("自定义规则不应报错: %v", err)
	}
	if rule == nil {
		t.Fatal("自定义 pattern 必须启用")
	}
	t.Logf("自定义 pattern 生效 ✓")
}

// 非法 pattern 必须启动即拒（不能静默回退默认）。
func TestMultilineInvalidPatternRejected(t *testing.T) {
	c := &Config{}
	c.LogTailer.Multiline.Pattern = `^[unclosed`

	if _, _, err := c.MultilineSettings(); err == nil {
		t.Fatal("非法 pattern 必须在启动期被拒，不能静默回退")
	} else {
		t.Logf("非法 pattern 被拒 ✓: %v", err)
	}
}
