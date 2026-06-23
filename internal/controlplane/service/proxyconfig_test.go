package service

import (
	"context"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBuildVelocityToml(t *testing.T) {
	entries := []proxyServerEntry{
		{Alias: "lobby", Address: "127.0.0.1:25566"},
		{Alias: "smp", Address: "10.0.0.2:25567", ForcedHost: "smp.example.com", Restricted: true},
	}
	out := buildVelocityToml(25565, "Hi \"there\"", true, entries)

	var parsed map[string]interface{}
	require.NoError(t, toml.Unmarshal([]byte(out), &parsed), "生成的 velocity.toml 必须可解析")
	require.Equal(t, "modern", parsed["player-info-forwarding-mode"])
	require.Equal(t, true, parsed["online-mode"])
	// 离线模式透传
	off := map[string]interface{}{}
	require.NoError(t, toml.Unmarshal([]byte(buildVelocityToml(25565, "x", false, entries)), &off))
	require.Equal(t, false, off["online-mode"])
	require.Equal(t, "forwarding.secret", parsed["forwarding-secret-file"])
	require.Equal(t, "0.0.0.0:25565", parsed["bind"])

	servers := parsed["servers"].(map[string]interface{})
	require.Equal(t, "127.0.0.1:25566", servers["lobby"])
	require.Equal(t, "10.0.0.2:25567", servers["smp"])
	try := servers["try"].([]interface{})
	require.Len(t, try, 2)
	require.Equal(t, "lobby", try[0])

	fh := parsed["forced-hosts"].(map[string]interface{})
	require.Contains(t, fh, "smp.example.com")
}

func TestBuildVelocityToml_Empty(t *testing.T) {
	out := buildVelocityToml(25565, "Hi", true, nil)
	var parsed map[string]interface{}
	require.NoError(t, toml.Unmarshal([]byte(out), &parsed))
	servers := parsed["servers"].(map[string]interface{})
	require.Len(t, servers["try"].([]interface{}), 0)
	// 即使无后端也必须显式输出空 [forced-hosts]，覆盖 Velocity 内置示例默认。
	require.Contains(t, out, "[forced-hosts]")
}

// TestBuildVelocityToml_AlwaysEmitsForcedHosts 回归：无论是否存在 forced-host 条目，
// 生成的 velocity.toml 都必须显式输出 [forced-hosts] 段。否则 Velocity 3.x 启动时会把内置
// 默认配置合并进来（含示例 factions.example.com=["factions"]、minigames.example.com=["minigames"]），
// 引用不存在的 server 导致 "configuration is invalid"，代理反复崩溃无法启动。
func TestBuildVelocityToml_AlwaysEmitsForcedHosts(t *testing.T) {
	cases := map[string][]proxyServerEntry{
		"无后端":               nil,
		"有后端但均无 forced-host": {{Alias: "lobby", Address: "127.0.0.1:25566"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			out := buildVelocityToml(25565, "Hi", true, entries)
			require.Contains(t, out, "[forced-hosts]", "必须显式输出 [forced-hosts] 段以覆盖 Velocity 内置示例默认")
			var parsed map[string]interface{}
			require.NoError(t, toml.Unmarshal([]byte(out), &parsed), "生成的 velocity.toml 必须可解析")
		})
	}
}

func TestBuildBungeeConfig(t *testing.T) {
	entries := []proxyServerEntry{{Alias: "lobby", Address: "127.0.0.1:25566", ForcedHost: "play.example.com"}}
	out, err := buildBungeeConfig(25565, "Hi", false, entries)
	require.NoError(t, err)
	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed), "生成的 config.yml 必须可解析")
	require.Equal(t, true, parsed["ip_forward"])
	require.Equal(t, false, parsed["online_mode"]) // 离线模式透传
	servers := parsed["servers"].(map[string]interface{})
	require.Contains(t, servers, "lobby")
	listeners := parsed["listeners"].([]interface{})
	require.Len(t, listeners, 1)
	l0 := listeners[0].(map[string]interface{})
	require.Equal(t, "0.0.0.0:25565", l0["host"])
	require.Contains(t, l0["forced_hosts"].(map[string]interface{}), "play.example.com")
}

func TestMergeVelocitySecretIntoPaperGlobal(t *testing.T) {
	// 从空生成最小档
	out, err := mergeVelocitySecretIntoPaperGlobal("", "abc123")
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(out), &m))
	vel := m["proxies"].(map[string]interface{})["velocity"].(map[string]interface{})
	require.Equal(t, true, vel["enabled"])
	require.Equal(t, true, vel["online-mode"])
	require.Equal(t, "abc123", vel["secret"])

	// 保留既有键
	existing := "some-key: 1\nproxies:\n  bungee-cord:\n    online-mode: true\n"
	out2, err := mergeVelocitySecretIntoPaperGlobal(existing, "xyz")
	require.NoError(t, err)
	var m2 map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(out2), &m2))
	require.Equal(t, 1, m2["some-key"])
	proxies := m2["proxies"].(map[string]interface{})
	require.Contains(t, proxies, "bungee-cord")
	require.Equal(t, "xyz", proxies["velocity"].(map[string]interface{})["secret"])
}

func TestMergeBungeeIntoSpigotAndPaperGlobal(t *testing.T) {
	out, err := mergeBungeeIntoSpigot("settings:\n  restart-on-crash: true\n")
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(out), &m))
	settings := m["settings"].(map[string]interface{})
	require.Equal(t, true, settings["bungeecord"])
	require.Equal(t, true, settings["restart-on-crash"]) // 保留既有

	pg, err := mergeBungeeIntoPaperGlobal("")
	require.NoError(t, err)
	var pm map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(pg), &pm))
	require.Equal(t, false, pm["proxies"].(map[string]interface{})["bungee-cord"].(map[string]interface{})["online-mode"])
}

func TestCoreService_ProxyClassifiers(t *testing.T) {
	require.True(t, IsProxyCore("velocity"))
	require.True(t, IsProxyCore("Waterfall"))
	require.True(t, IsProxyCore("bungeecord"))
	require.False(t, IsProxyCore("paper"))
	require.True(t, IsVelocityCore("velocity"))
	require.False(t, IsVelocityCore("bungeecord"))
}

func TestCoreService_BungeeResolve(t *testing.T) {
	s := NewCoreService()
	info, err := s.ResolveBuild(context.Background(), "bungeecord", "", 0)
	require.NoError(t, err)
	require.Equal(t, "BungeeCord.jar", info.Filename)
	require.Contains(t, info.DownloadURL, "ci.md-5.net")
	require.Equal(t, "", info.SHA256)

	versions, err := s.ListVersions(context.Background(), "bungeecord")
	require.NoError(t, err)
	require.Equal(t, []string{"latest"}, versions)
}

// 结构化启动：代理 OmitNogui 不追加 nogui，MC 默认仍追加。
func TestDeriveStartCommand_OmitNogui(t *testing.T) {
	proxyCmd, err := deriveStartCommand(&LaunchSpec{MemoryMb: 1024, CoreJar: "server.jar", OmitNogui: true})
	require.NoError(t, err)
	require.NotContains(t, proxyCmd, "nogui")
	require.Contains(t, proxyCmd, "-jar server.jar")

	mcCmd, err := deriveStartCommand(&LaunchSpec{MemoryMb: 2048, CoreJar: "server.jar"})
	require.NoError(t, err)
	require.Contains(t, mcCmd, "nogui")
}
