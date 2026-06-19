package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// proxyServerEntry 是写入代理配置的单条后端 server（由 ServerRegistration + 后端地址派生，FR-035）。
// 由 ProxyService 预先按 priority 升序排列、过滤掉 disabled，并解析好 Address（host:port）。
type proxyServerEntry struct {
	Alias      string
	Address    string // host:port（同节点用 127.0.0.1）
	ForcedHost string
	Restricted bool
}

// genForwardingSecret 生成 Velocity modern 转发的 forwarding secret（32 hex 字符）。
func genForwardingSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strings.ReplaceAll(fmt.Sprintf("jm-%x", b), " ", "")
	}
	return hex.EncodeToString(b)
}

// tomlEscapeString 转义 TOML 基本字符串中的反斜杠与双引号。
func tomlEscapeString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// buildVelocityToml 生成 velocity.toml：modern 转发 + 监听端口 + 由注册关系派生的 servers/try/forced-hosts。
// onlineMode 决定代理是否向 Mojang 校验正版（离线模式群组服传 false）。
// secret 不写入此文件，而是写入 forwarding-secret-file 指向的 forwarding.secret。
func buildVelocityToml(listenPort int, motd string, onlineMode bool, entries []proxyServerEntry) string {
	var b strings.Builder
	b.WriteString("# 由 JianManager 生成（FR-035）。servers/try/forced-hosts 由平台注册关系同步管理。\n")
	b.WriteString("config-version = \"2.7\"\n")
	fmt.Fprintf(&b, "bind = \"0.0.0.0:%d\"\n", listenPort)
	fmt.Fprintf(&b, "motd = \"%s\"\n", tomlEscapeString(motd))
	b.WriteString("show-max-players = 500\n")
	fmt.Fprintf(&b, "online-mode = %t\n", onlineMode)
	b.WriteString("player-info-forwarding-mode = \"modern\"\n")
	b.WriteString("forwarding-secret-file = \"forwarding.secret\"\n")
	b.WriteString("announce-forge = false\n")

	b.WriteString("\n[servers]\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "%s = \"%s\"\n", e.Alias, e.Address)
	}
	b.WriteString("try = [\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  \"%s\",\n", e.Alias)
	}
	b.WriteString("]\n")

	forced := make([]proxyServerEntry, 0)
	for _, e := range entries {
		if strings.TrimSpace(e.ForcedHost) != "" {
			forced = append(forced, e)
		}
	}
	if len(forced) > 0 {
		b.WriteString("\n[forced-hosts]\n")
		for _, e := range forced {
			fmt.Fprintf(&b, "\"%s\" = [\"%s\"]\n", tomlEscapeString(e.ForcedHost), e.Alias)
		}
	}
	return b.String()
}

// bungee config.yml 的最小可用结构（FR-035）。BungeeCord/Waterfall 启动时会补全其余默认键。
type bungeeServerCfg struct {
	Motd       string `yaml:"motd"`
	Address    string `yaml:"address"`
	Restricted bool   `yaml:"restricted"`
}

type bungeeListenerCfg struct {
	QueryPort          int               `yaml:"query_port"`
	Motd               string            `yaml:"motd"`
	Priorities         []string          `yaml:"priorities"`
	BindLocalAddress   bool              `yaml:"bind_local_address"`
	Host               string            `yaml:"host"`
	MaxPlayers         int               `yaml:"max_players"`
	TabList            string            `yaml:"tab_list"`
	ForceDefaultServer bool              `yaml:"force_default_server"`
	ForcedHosts        map[string]string `yaml:"forced_hosts"`
	TabSize            int               `yaml:"tab_size"`
	PingPassthrough    bool              `yaml:"ping_passthrough"`
	ProxyProtocol      bool              `yaml:"proxy_protocol"`
}

type bungeeConfigCfg struct {
	IPForward                   bool                       `yaml:"ip_forward"`
	OnlineMode                  bool                       `yaml:"online_mode"`
	NetworkCompressionThreshold int                        `yaml:"network_compression_threshold"`
	Listeners                   []bungeeListenerCfg        `yaml:"listeners"`
	Servers                     map[string]bungeeServerCfg `yaml:"servers"`
}

// buildBungeeConfig 生成 BungeeCord/Waterfall config.yml：ip_forward + 监听端口 + servers/priorities/forced-hosts。
// onlineMode 决定代理是否向 Mojang 校验正版（离线模式群组服传 false）。
func buildBungeeConfig(listenPort int, motd string, onlineMode bool, entries []proxyServerEntry) (string, error) {
	servers := make(map[string]bungeeServerCfg, len(entries))
	priorities := make([]string, 0, len(entries))
	forced := map[string]string{}
	for _, e := range entries {
		servers[e.Alias] = bungeeServerCfg{Motd: motd, Address: e.Address, Restricted: e.Restricted}
		priorities = append(priorities, e.Alias)
		if strings.TrimSpace(e.ForcedHost) != "" {
			forced[e.ForcedHost] = e.Alias
		}
	}
	cfg := bungeeConfigCfg{
		IPForward:                   true,
		OnlineMode:                  onlineMode,
		NetworkCompressionThreshold: 256,
		Listeners: []bungeeListenerCfg{{
			QueryPort:          listenPort,
			Motd:               motd,
			Priorities:         priorities,
			BindLocalAddress:   true,
			Host:               fmt.Sprintf("0.0.0.0:%d", listenPort),
			MaxPlayers:         500,
			TabList:            "GLOBAL_PING",
			ForceDefaultServer: false,
			ForcedHosts:        forced,
			TabSize:            60,
			PingPassthrough:    false,
			ProxyProtocol:      false,
		}},
		Servers: servers,
	}
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		return "", fmt.Errorf("生成 bungee config.yml 失败: %w", err)
	}
	return "# 由 JianManager 生成（FR-035）。servers/priorities/forced_hosts 由平台注册关系同步管理。\n" + string(out), nil
}

// setNestedYAML 在 YAML 文本中设置嵌套键（缺失路径自动创建），保留其它键。
// 以 map round-trip 实现：注释/键顺序不保留（对全新生成的 paper 配置无影响）。
func setNestedYAML(content string, value interface{}, path ...string) (string, error) {
	if len(path) == 0 {
		return content, fmt.Errorf("空路径")
	}
	root := map[string]interface{}{}
	if strings.TrimSpace(content) != "" {
		if err := yaml.Unmarshal([]byte(content), &root); err != nil {
			return "", fmt.Errorf("解析 YAML 失败: %w", err)
		}
		if root == nil {
			root = map[string]interface{}{}
		}
	}
	cur := root
	for _, k := range path[:len(path)-1] {
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			cur[k] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = value
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("序列化 YAML 失败: %w", err)
	}
	return string(out), nil
}

// mergeVelocitySecretIntoPaperGlobal 把 Velocity modern 转发的 secret 写入后端 paper-global.yml：
// proxies.velocity.{enabled=true, online-mode=true, secret=<secret>}。existing 为空则生成最小档。
func mergeVelocitySecretIntoPaperGlobal(existing, secret string) (string, error) {
	out, err := setNestedYAML(existing, true, "proxies", "velocity", "enabled")
	if err != nil {
		return "", err
	}
	out, err = setNestedYAML(out, true, "proxies", "velocity", "online-mode")
	if err != nil {
		return "", err
	}
	return setNestedYAML(out, secret, "proxies", "velocity", "secret")
}

// mergeBungeeIntoPaperGlobal 为 BungeeCord/Waterfall 转发设置后端 paper-global.yml：
// proxies.bungee-cord.online-mode=false（legacy ip_forward 下后端不做正版校验）。
func mergeBungeeIntoPaperGlobal(existing string) (string, error) {
	return setNestedYAML(existing, false, "proxies", "bungee-cord", "online-mode")
}

// mergeBungeeIntoSpigot 为 BungeeCord/Waterfall 转发设置后端 spigot.yml：settings.bungeecord=true。
func mergeBungeeIntoSpigot(existing string) (string, error) {
	return setNestedYAML(existing, true, "settings", "bungeecord")
}
