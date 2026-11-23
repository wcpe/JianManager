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

// TestPatchConfig_YAMLRefusesToClobberMapping 验证「把标量写到映射上」会被拒绝而不是覆盖。
//
// 实测过的破坏形态：BungeeCord 的 `servers` 是「子服名 → 属性」的映射，早期 schema 把它
// 当成一个字符串字段渲染；用户一旦在表单里改这个输入框并保存，两台后端连同它们的
// address/motd/restricted 会被整段替换成 `servers: 'lobby: localhost:25566'`，
// 而界面上只显示「保存成功」。
func TestPatchConfig_YAMLRefusesToClobberMapping(t *testing.T) {
	src := `servers:
  lobby:
    address: localhost:25566
    motd: 'lobby'
  game1:
    address: localhost:25567
    motd: 'game'
`
	out, err := patchConfig("yaml", src, []fieldUpdate{
		{Key: "servers", Value: "lobby: localhost:25566", Type: "string"},
	})
	if err == nil {
		t.Fatalf("把标量写到映射上应报错，实际成功并产出:\n%s", out)
	}
	if !strings.Contains(err.Error(), "映射") {
		t.Errorf("错误信息应说明该键是映射，实际: %v", err)
	}
}

// TestPatchConfig_YAMLRefusesToClobberSequence 验证「把标量写到列表上」会被拒绝。
//
// 实测过的破坏形态：`permissions.default` 在真实配置里是 `- 节点` 列表，被当成逗号分隔
// 字符串编辑后保存，列表里第一个之后的条目全部丢失。
func TestPatchConfig_YAMLRefusesToClobberSequence(t *testing.T) {
	src := `permissions:
  default:
  - bungeecord.command.server
  - bungeecord.command.list
`
	out, err := patchConfig("yaml", src, []fieldUpdate{
		{Key: "permissions.default", Value: "bungeecord.command.server", Type: "string"},
	})
	if err == nil {
		t.Fatalf("把标量写到列表上应报错，实际成功并产出:\n%s", out)
	}
	if !strings.Contains(err.Error(), "列表") {
		t.Errorf("错误信息应说明该键是列表，实际: %v", err)
	}
}

// TestPatchConfig_YAMLSequenceSingleItemDescends 验证「列表只有一个元素」时能安全进项改写。
//
// BungeeCord 的 `listeners.host` 位于列表项内部。绝大多数部署只有一个监听器，
// 此时语义无歧义，应当正常改写 host 并**保留同项的其它字段**（此前会把整块监听器配置
// 从列表变成映射，motd/max_players/query_port 全部丢失）。
func TestPatchConfig_YAMLSequenceSingleItemDescends(t *testing.T) {
	src := `listeners:
- host: 0.0.0.0:25577
  motd: '&1BungeeCord'
  max_players: 1
  query_port: -1
ip_forward: false
`
	out, err := patchConfig("yaml", src, []fieldUpdate{
		{Key: "listeners.host", Value: "0.0.0.0:25578", Type: "string"},
	})
	if err != nil {
		t.Fatalf("单元素列表应可安全写入，实际报错: %v", err)
	}
	if !strings.Contains(out, "0.0.0.0:25578") {
		t.Errorf("host 未更新:\n%s", out)
	}
	// 关键：同项的其它字段必须全部保留。
	for _, keep := range []string{"motd", "max_players", "query_port", "ip_forward"} {
		if !strings.Contains(out, keep) {
			t.Errorf("同项字段 %q 被破坏:\n%s", keep, out)
		}
	}
}

// TestPatchConfig_YAMLSequenceMultiItemRefuses 验证「列表有多个元素」时拒绝写入而不是替用户猜。
//
// 多个监听器时「改哪一个」没有正确答案，报错让用户改用文本模式，比默默改第一个安全。
func TestPatchConfig_YAMLSequenceMultiItemRefuses(t *testing.T) {
	src := `listeners:
- host: 0.0.0.0:25577
  motd: 'a'
- host: 0.0.0.0:25578
  motd: 'b'
`
	out, err := patchConfig("yaml", src, []fieldUpdate{
		{Key: "listeners.host", Value: "0.0.0.0:25579", Type: "string"},
	})
	if err == nil {
		t.Fatalf("多元素列表应报错而不是猜一个改，实际成功并产出:\n%s", out)
	}
	if !strings.Contains(err.Error(), "列表") {
		t.Errorf("错误信息应说明该键是列表，实际: %v", err)
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
