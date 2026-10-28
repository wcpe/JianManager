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

// TestParseFlatYAML_DeepNestingPathOrder 守护 ≥3 层嵌套时的路径顺序。
//
// `parseFlatYAML` 用一个「外层在前」的缩进栈拼路径；若正向遍历栈去前缀，
// 前缀会整段反序：`servers: {lobby: {motd: x}}` 产出 `lobby.servers.motd`，
// 而正确结果是 `servers.lobby.motd`。
//
// 2 层时栈里只有一个元素，正向反向结果相同，所以这个 bug 长期无人发现；
// 而 BungeeCord 的 `servers`、Paper 的 `world-settings` 都是 3 层，真实配置一律错位：
// schema 绑不上（form 显示空值）、补丁写不进去（路径找不到父级）。
func TestParseFlatYAML_DeepNestingPathOrder(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "两层（回归：本来就正确）",
			src:  "settings:\n  bungeecord: true\n",
			want: []string{"settings.bungeecord"},
		},
		{
			name: "三层：BungeeCord servers 表",
			src:  "servers:\n  lobby:\n    address: localhost:25566\n",
			want: []string{"servers.lobby.address"},
		},
		{
			name: "三层：Paper world-settings",
			src:  "world-settings:\n  default:\n    view-distance: 10\n",
			want: []string{"world-settings.default.view-distance"},
		},
		{
			name: "四层",
			src:  "a:\n  b:\n    c:\n      d: v\n",
			want: []string{"a.b.c.d"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fields := parseFlatYAML(c.src)
			got := make([]string, 0, len(fields))
			for _, f := range fields {
				got = append(got, f.Key)
			}
			if len(got) != len(c.want) {
				t.Fatalf("字段数不符: got=%v want=%v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("路径顺序错误: got=%q want=%q", got[i], c.want[i])
				}
			}
		})
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
	if m.Fields["server-port"].Group != "网络与身份" || m.Fields["max-players"].Group != "展示与容量" {
		t.Fatalf("server.properties schema 缺少表单分组元数据: %+v", m.Fields)
	}
}

func TestCrossFileConsistency_PortUnique(t *testing.T) {
	cfgs := []ParsedConfig{
		{Path: "a/server.properties", Fields: []*workerpb.ConfigField{{Key: "server-port", Value: "25565"}}},
		{Path: "b/server.properties", Fields: []*workerpb.ConfigField{{Key: "server-port", Value: "25565"}}},
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

func TestCrossFileConsistency_QueryPortUnique(t *testing.T) {
	// FR-451：query.port 纳入端口唯一性校验（与 server-port 同口径）。
	cfgs := []ParsedConfig{
		{Path: "a/server.properties", Fields: []*workerpb.ConfigField{{Key: "query.port", Value: "25566"}}},
		{Path: "b/server.properties", Fields: []*workerpb.ConfigField{{Key: "query.port", Value: "25566"}}},
	}
	issues := CheckPortConflicts(cfgs)
	hasDup := false
	for _, it := range issues {
		if strings.Contains(it.Message, "重复") {
			hasDup = true
		}
	}
	if !hasDup {
		t.Fatalf("应当检出重复的 query.port: %+v", issues)
	}
}

func TestParsedConfig_SetValue(t *testing.T) {
	cfg := ParsedConfig{Fields: []*workerpb.ConfigField{{Key: "server-port", Value: "25565"}}}
	cfg.SetValue("server-port", "25600") // 覆盖已有键
	cfg.SetValue("query.port", "25601")  // 追加新键
	if got := cfg.fieldLookup("server-port"); got != "25600" {
		t.Fatalf("覆盖失败: %s", got)
	}
	if got := cfg.fieldLookup("query.port"); got != "25601" {
		t.Fatalf("追加失败: %s", got)
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

// TestBungeeCordYAMLModel_KeysMatchParsedDotPaths 守护 FR-449 §2.2.4 的键名契约。
//
// 背景：该 schema 早先用 `listeners[].host` 这类 JSONPath 风格键名，而 `parseFlatYAML`
// 产出的是点号路径 `listeners.host`。两边对不上时 form 模式的每个字段都绑不到真实值，
// 但**没有任何报错**——只是表单显示空值、保存后原样覆盖，属最难发现的一类缺陷。
//
// 这条测试用一份**贴近真实 BungeeCord config.yml 的片段**（servers 是三层嵌套、permissions
// 是列表）跑一遍解析，断言 schema 里**每一个**声明的键都精确出现在解析产物中。
//
// 这里刻意不再保留任何「前缀放行」豁免：`servers` 与 `permissions.*` 已从 schema 移除，
// 留下的全是真正的标量键，就不该有配不上的理由。此前正是因为豁免逻辑存在，才掩盖了两个
// 更深的问题——解析器在 ≥3 层嵌套时把路径前缀写反（`servers.lobby.motd` 变成
// `lobby.servers.motd`），以及 servers/permissions 这类非标量字段被当成标量渲染。
func TestBungeeCordYAMLModel_KeysMatchParsedDotPaths(t *testing.T) {
	src := `listeners:
- host: 0.0.0.0:25577
  max_players: 100
  motd: '&1BungeeCord'
  query_port: 25577
ip_forward: true
online_mode: true
connection_throttle: 4000
connection_throttle_limit: 3
servers:
  lobby:
    address: localhost:25566
    motd: 'lobby'
    restricted: false
permissions:
  default:
  - bungeecord.command.server
  admin:
  - bungeecord.command.alert
`
	parsed := parseFlatYAML(src)
	byKey := make(map[string]struct{}, len(parsed))
	for _, f := range parsed {
		byKey[f.Key] = struct{}{}
	}

	model := BungeeCordYAMLModel()

	// 精确绑定：schema 里出现的每个键都必须能在这份真实形态的配置里解析出来。
	// 不再有「前缀放行」或「列表条目豁免」——那些豁免正是此前掩盖问题的原因：
	// 只要允许「大致能对上」，解析器把路径写反、把映射当标量渲染都不会被发现，
	// 而 form 模式会显示一个真实配置里并不存在的输入框，保存即破坏。
	for key := range model.Fields {
		if _, ok := byKey[key]; ok {
			continue
		}
		t.Errorf("schema 键 %q 在 parseFlatYAML 产物中不存在（form 模式将无法绑定）", key)
	}

	// 反向守护：schema 不得再出现 JSONPath 风格键名。
	for key := range model.Fields {
		if strings.Contains(key, "[]") {
			t.Errorf("schema 键 %q 含 JSONPath 风格（[]），与 parseFlatYAML 的点号路径不一致", key)
		}
	}
}

// TestVelocityTomlModel_KeysMatchParsedPaths 是 BungeeCord 那条守护的对称补充。
//
// 背景：`VelocityTomlModel` 曾声明 `enabled` 与 `forwarding-secret` 两个真实文件里
// **根本不存在**的键——前者 Velocity 的 toml 从来没有；后者 JM 走的是
// `forwarding-secret-file = "forwarding.secret"`，密钥落在独立文件而非内联字段。
//
// 后果与 BC 那批相同且更隐蔽：form 模式会渲染出这两个输入框，而 `patchTOML` 对缺失键是
// **追加到文件末尾**，用户一保存就往 velocity.toml 里写进 `enabled = true` 与
// `forwarding-secret = ""`，污染代理配置。这条测试把「schema 键 ⊆ 解析产物」钉死。
//
// 样例取自 JM 自己生成的 velocity.toml（proxyconfig.go 的 buildVelocityToml），
// 即运行时真正会看到的形态。
func TestVelocityTomlModel_KeysMatchParsedPaths(t *testing.T) {
	src := `# 由 JianManager 生成（FR-035）。servers/try/forced-hosts 由平台注册关系同步管理。
config-version = "2.7"
bind = "0.0.0.0:25577"
motd = "Velocity"
show-max-players = 500
online-mode = true
player-info-forwarding-mode = "modern"
forwarding-secret-file = "forwarding.secret"
announce-forge = false

[servers]
lobby = "127.0.0.1:25566"
`
	parsed := parseFlatTOML(src)
	byKey := make(map[string]struct{}, len(parsed))
	for _, f := range parsed {
		byKey[f.Key] = struct{}{}
	}

	for key := range VelocityTomlModel().Fields {
		if _, ok := byKey[key]; ok {
			continue
		}
		t.Errorf("schema 键 %q 在 parseFlatTOML 产物中不存在（form 模式会渲染幽灵字段，保存即向文件追加污染行）", key)
	}
}
