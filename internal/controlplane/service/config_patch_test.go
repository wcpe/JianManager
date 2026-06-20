package service

import (
	"strings"
	"testing"
)

// TestPatchConfig_PropertiesPreservesComments 验证 properties 表单补丁保留注释/顺序，只改值。
func TestPatchConfig_PropertiesPreservesComments(t *testing.T) {
	src := "# 顶部注释\nserver-port=25565\n# 中间注释\nonline-mode=true\n\n# 尾部\n"
	out, err := patchConfig("properties", src, []fieldUpdate{
		{Key: "server-port", Value: "25570", Type: "int"},
		{Key: "online-mode", Value: "false", Type: "bool"},
	})
	if err != nil {
		t.Fatalf("patchConfig err: %v", err)
	}
	if !strings.Contains(out, "# 顶部注释") || !strings.Contains(out, "# 中间注释") || !strings.Contains(out, "# 尾部") {
		t.Errorf("注释丢失:\n%s", out)
	}
	if !strings.Contains(out, "server-port=25570") {
		t.Errorf("server-port 未更新:\n%s", out)
	}
	if !strings.Contains(out, "online-mode=false") {
		t.Errorf("online-mode 未更新:\n%s", out)
	}
	// 顺序：server-port 行应在 online-mode 行之前
	if strings.Index(out, "server-port") > strings.Index(out, "online-mode") {
		t.Errorf("键顺序被破坏:\n%s", out)
	}
}

// TestPatchConfig_YAMLNestedPreservesComments 验证 yaml 嵌套点路径补丁保留注释、改对应标量。
func TestPatchConfig_YAMLNestedPreservesComments(t *testing.T) {
	src := "# spigot 配置\nsettings:\n  # 是否接入代理\n  bungeecord: false\n  spam-protector: 1\n"
	out, err := patchConfig("yaml", src, []fieldUpdate{
		{Key: "settings.bungeecord", Value: "true", Type: "bool"},
	})
	if err != nil {
		t.Fatalf("patchConfig err: %v", err)
	}
	if !strings.Contains(out, "# spigot 配置") || !strings.Contains(out, "# 是否接入代理") {
		t.Errorf("yaml 注释丢失:\n%s", out)
	}
	if !strings.Contains(out, "bungeecord: true") {
		t.Errorf("settings.bungeecord 未更新为 true:\n%s", out)
	}
	if !strings.Contains(out, "spam-protector: 1") {
		t.Errorf("未改动的键丢失:\n%s", out)
	}
}

// TestPatchConfig_YAMLCreatesMissingLeaf 验证缺失叶子键会在已有父节点下新增。
func TestPatchConfig_YAMLCreatesMissingLeaf(t *testing.T) {
	src := "proxies:\n  velocity:\n    enabled: false\n"
	out, err := patchConfig("yaml", src, []fieldUpdate{
		{Key: "proxies.velocity.secret", Value: "abc123", Type: "string"},
	})
	if err != nil {
		t.Fatalf("patchConfig err: %v", err)
	}
	if !strings.Contains(out, "secret: abc123") {
		t.Errorf("缺失叶子未新增:\n%s", out)
	}
	if !strings.Contains(out, "enabled: false") {
		t.Errorf("原有键丢失:\n%s", out)
	}
}

// TestPatchConfig_TOMLPreservesComments 验证 toml 顶层键行级补丁保留注释，字符串加引号。
func TestPatchConfig_TOMLPreservesComments(t *testing.T) {
	src := "# velocity 配置\nbind = \"0.0.0.0:25577\"\n# 转发模式\nplayer-info-forwarding-mode = \"none\"\n"
	out, err := patchConfig("toml", src, []fieldUpdate{
		{Key: "player-info-forwarding-mode", Value: "modern", Type: "string"},
	})
	if err != nil {
		t.Fatalf("patchConfig err: %v", err)
	}
	if !strings.Contains(out, "# velocity 配置") || !strings.Contains(out, "# 转发模式") {
		t.Errorf("toml 注释丢失:\n%s", out)
	}
	if !strings.Contains(out, `player-info-forwarding-mode = "modern"`) {
		t.Errorf("toml 值未更新（应带引号）:\n%s", out)
	}
	if !strings.Contains(out, `bind = "0.0.0.0:25577"`) {
		t.Errorf("toml 未改动键丢失:\n%s", out)
	}
}

// TestPatchConfig_TOMLBoolIntUnquoted 验证 toml 中 bool/int 不加引号。
func TestPatchConfig_TOMLBoolIntUnquoted(t *testing.T) {
	src := "enabled = true\n"
	out, err := patchConfig("toml", src, []fieldUpdate{
		{Key: "enabled", Value: "false", Type: "bool"},
	})
	if err != nil {
		t.Fatalf("patchConfig err: %v", err)
	}
	if !strings.Contains(out, "enabled = false") {
		t.Errorf("toml bool 应不带引号:\n%s", out)
	}
}
