package schema

import (
	"strings"
	"testing"

	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestParseProperties_PreservesOrder(t *testing.T) {
	src := "# 端口\nserver-port=25565\nmotd=A Server\n"
	fields := parseProperties(src)
	if len(fields) != 2 {
		t.Fatalf("期望 2 个字段, 实际 %d", len(fields))
	}
	if fields[0].Key != "server-port" || fields[1].Key != "motd" {
		t.Fatalf("字段顺序错误: %+v", fields)
	}
}

func TestParseFlatYAML_DotPath(t *testing.T) {
	src := "settings:\n  bungeecord: true\n  spawn-radius: 16\n"
	fields := parseFlatYAML(src)
	if len(fields) != 2 {
		t.Fatalf("期望 2 个字段, 实际 %d", len(fields))
	}
	want := map[string]string{
		"settings.bungeecord":   "true",
		"settings.spawn-radius": "16",
	}
	for _, f := range fields {
		if v, ok := want[f.Key]; !ok || v != f.Value {
			t.Fatalf("字段 %s=%s 不符合期望", f.Key, f.Value)
		}
	}
}

func TestParseFlatTOML_Section(t *testing.T) {
	src := "motd = \"Velocity\"\n\n[server]\nbind = \"0.0.0.0:25577\"\n"
	fields := parseFlatTOML(src)
	if len(fields) != 2 {
		t.Fatalf("期望 2 个字段, 实际 %d", len(fields))
	}
	if fields[0].Key != "motd" || fields[0].Value != "Velocity" {
		t.Fatalf("顶层 motd 字段错误: %+v", fields[0])
	}
	if fields[1].Key != "server.bind" || fields[1].Value != "0.0.0.0:25577" {
		t.Fatalf("section 字段未拼接前缀: %+v", fields[1])
	}
}

func TestApplyTypes_CoercesBoolAndInt(t *testing.T) {
	model := ModelSchema{
		Fields: map[string]FieldSchema{
			"online-mode": {Key: "online-mode", Type: "bool"},
			"port":        {Key: "port", Type: "int"},
		},
	}
	fields := []*workerpb.ConfigField{{Key: "online-mode", Value: "yes"}, {Key: "port", Value: " 257 "}}
	out := ApplyTypes(fields, &model)
	if out[0].Value != "true" {
		t.Fatalf("yes 应规范为 true, 实际 %s", out[0].Value)
	}
	if out[1].Value != "257" {
		t.Fatalf("257 应规范为 257, 实际 %s", out[1].Value)
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"server.properties", "server.properties"},
		{"plugins/velocity/config.yml", "velocity.toml"},
		{"plugins/bungeecord/config.yml", "config.yml"},
		{"unknown.ini", ""},
	}
	for _, c := range cases {
		m := MatchPath(c.path)
		got := ""
		if m != nil {
			got = m.Name
		}
		if got != c.want {
			t.Fatalf("MatchPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestServerPropertiesModel_HasCoreKeys(t *testing.T) {
	m := ServerPropertiesModel()
	for _, k := range []string{"server-port", "online-mode", "rcon.port", "query.port"} {
		if _, ok := m.Fields[k]; !ok {
			t.Fatalf("server.properties schema 缺少字段 %s", k)
		}
	}
}

func TestCrossFileConsistency_PortUnique(t *testing.T) {
	cfgs := []ParsedConfig{
		{Path: "server.properties", Fields: []*workerpb.ConfigField{{Key: "server-port", Value: "25565"}}},
		{Path: "server.properties", Fields: []*workerpb.ConfigField{{Key: "server-port", Value: "25565"}}},
		{Path: "server.properties", Fields: []*workerpb.ConfigField{{Key: "rcon.port", Value: "25565"}}},
	}
	issues := CheckPortConflicts(cfgs)
	hasDup := false
	for _, it := range issues {
		if strings.Contains(it.Message, "重复") {
			hasDup = true
		}
	}
	if !hasDup {
		t.Fatalf("应当检出重复端口: %+v", issues)
	}
}

func TestCrossFileConsistency_OnlineModeAndProxyForwarding(t *testing.T) {
	cfgs := []ParsedConfig{
		{Path: "server.properties", Fields: []*workerpb.ConfigField{{Key: "online-mode", Value: "true"}}},
		{Path: "spigot.yml", Fields: []*workerpb.ConfigField{{Key: "settings.bungeecord", Value: "true"}}},
	}
	issues := CheckProxyConsistency(cfgs)
	if len(issues) == 0 {
		t.Fatalf("online-mode=true 与 settings.bungeecord=true 应触发警告")
	}
}

func TestCrossFileConsistency_ForwardingSecretMatch(t *testing.T) {
	cfgs := []ParsedConfig{
		{Path: "velocity.toml", Fields: []*workerpb.ConfigField{{Key: "forwarding-secret", Value: "abc"}, {Key: "player-info-forwarding-mode", Value: "modern"}}},
		{Path: "paper-global.yml", Fields: []*workerpb.ConfigField{{Key: "proxies.velocity.enabled", Value: "true"}, {Key: "proxies.velocity.secret", Value: "xyz"}}},
	}
	issues := CheckForwardingSecret(cfgs)
	if len(issues) == 0 {
		t.Fatalf("secret 不一致应触发警告")
	}
}
