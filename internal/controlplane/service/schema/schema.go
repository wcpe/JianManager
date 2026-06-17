// Package schema 提供 FR-031 配置文件 schema 元数据 + 跨文件一致性校验。
package schema

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wxys233/JianManager/proto/workerpb"
)

// FieldSchema 单字段元数据。
type FieldSchema struct {
	Key         string `json:"key"`
	Type        string `json:"type"` // string/int/bool/list
	Default     string `json:"default"`
	Description string `json:"description"`
	// Choices 类型为 list 时可选，限定可选值。
	Choices []string `json:"choices,omitempty"`
}

// ModelSchema 单文件完整 schema。
type ModelSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Fields      map[string]FieldSchema `json:"fields"`
	Format      string                 `json:"format"`
}

// MatchPath 返回给定文件路径最匹配的 schema；不匹配返回 nil。
func MatchPath(path string) *ModelSchema {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "server.properties":
		m := ServerPropertiesModel()
		return &m
	case "spigot.yml":
		m := SpigotYAMLModel()
		return &m
	case "bukkit.yml":
		m := BukkitYAMLModel()
		return &m
	case "paper-global.yml", "paper.yml", "config\\_paper.yml":
		m := PaperGlobalYAMLModel()
		return &m
	case "velocity.toml":
		m := VelocityTomlModel()
		return &m
	case "config.yml":
		// BungeeCord 与 Velocity 复用 config.yml 关键字（BungeeCord config.yml, Velocity 通常 velocity.toml）。
		// 通过父目录启发区分：plugins/velocity 前缀按 Velocity 处理，否则 BungeeCord。
		if strings.Contains(filepath.ToSlash(strings.ToLower(path)), "velocity/") {
			m := VelocityTomlModel()
			return &m
		}
		m := BungeeCordYAMLModel()
		return &m
	}
	return nil
}

// BuildFields 把 properties / yaml / toml 等原始字段填充到 schema，得到带类型推断的 ConfigField。
func BuildFields(format, content string) []*workerpb.ConfigField {
	switch format {
	case "properties":
		return parseProperties(content)
	case "yaml":
		return parseFlatYAML(content)
	case "toml":
		return parseFlatTOML(content)
	}
	return nil
}

// ApplyTypes 用 schema 给定的类型重写 value；类型转换失败的字段会带上类型提示。
func ApplyTypes(fields []*workerpb.ConfigField, model *ModelSchema) []*workerpb.ConfigField {
	if model == nil || len(fields) == 0 {
		return fields
	}
	out := make([]*workerpb.ConfigField, 0, len(fields))
	for _, f := range fields {
		meta, ok := model.Fields[f.Key]
		typ := f.Type
		desc := f.Description
		if ok {
			typ = meta.Type
			desc = meta.Description
		}
		out = append(out, &workerpb.ConfigField{
			Key: f.Key, Value: coerce(f.Value, typ), Type: typ, Description: desc, Line: f.Line,
		})
	}
	return out
}

// coerce 按目标类型格式化原始值；type 未知或转换失败时保留原值。
func coerce(value, typ string) string {
	switch typ {
	case "bool":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "yes", "1", "on":
			return "true"
		case "false", "no", "0", "off":
			return "false"
		}
	case "int":
		var n int
		if _, err := fmt.Sscanf(value, "%d", &n); err == nil {
			return fmt.Sprintf("%d", n)
		}
	}
	return value
}

// --- 各模型的字段表 ---

// ServerPropertiesModel 经典 server.properties 字段表（节选）。
func ServerPropertiesModel() ModelSchema {
	return ModelSchema{
		Name:        "server.properties",
		Description: "Minecraft Java 版服务端核心配置",
		Format:      "properties",
		Fields: map[string]FieldSchema{
			"server-port":      {Key: "server-port", Type: "int", Default: "25565", Description: "服务端监听端口"},
			"server-ip":        {Key: "server-ip", Type: "string", Default: "", Description: "绑定 IP，留空表示全部接口"},
			"motd":             {Key: "motd", Type: "string", Default: "A Minecraft Server", Description: "每日消息"},
			"max-players":      {Key: "max-players", Type: "int", Default: "20", Description: "最大在线人数"},
			"online-mode":      {Key: "online-mode", Type: "bool", Default: "true", Description: "正版验证，开启时玩家需登录 Mojang 账号"},
			"view-distance":    {Key: "view-distance", Type: "int", Default: "10", Description: "视距（区块）"},
			"difficulty":       {Key: "difficulty", Type: "string", Default: "easy", Description: "难度", Choices: []string{"peaceful", "easy", "normal", "hard"}},
			"gamemode":         {Key: "gamemode", Type: "string", Default: "survival", Description: "默认游戏模式", Choices: []string{"survival", "creative", "adventure", "spectator"}},
			"white-list":       {Key: "white-list", Type: "bool", Default: "false", Description: "是否启用白名单"},
			"enable-rcon":      {Key: "enable-rcon", Type: "bool", Default: "false", Description: "启用 RCON 远程控制"},
			"rcon.port":        {Key: "rcon.port", Type: "int", Default: "25575", Description: "RCON 端口"},
			"rcon.password":    {Key: "rcon.password", Type: "string", Default: "", Description: "RCON 密码（启用 RCON 时必填）"},
			"enable-query":     {Key: "enable-query", Type: "bool", Default: "false", Description: "启用 GameSpy4 查询协议"},
			"query.port":       {Key: "query.port", Type: "int", Default: "25565", Description: "查询协议端口"},
			"spawn-protection": {Key: "spawn-protection", Type: "int", Default: "16", Description: "出生点保护半径（0 关闭）"},
		},
	}
}

// SpigotYAMLModel spigot.yml（节选）。
func SpigotYAMLModel() ModelSchema {
	return ModelSchema{
		Name:        "spigot.yml",
		Description: "Spigot 服务端配置",
		Format:      "yaml",
		Fields: map[string]FieldSchema{
			"settings.bungeecord":     {Key: "settings.bungeecord", Type: "bool", Default: "false", Description: "是否接入 BungeeCord/Velocity 代理"},
			"settings.spam-protector": {Key: "settings.spam-protector", Type: "int", Default: "1", Description: "反垃圾等级"},
			"messages.whitelist":      {Key: "messages.whitelist", Type: "string", Default: "You are not whitelisted on this server!", Description: "白名单提示"},
		},
	}
}

// BukkitYAMLModel bukkit.yml（节选）。
func BukkitYAMLModel() ModelSchema {
	return ModelSchema{
		Name:        "bukkit.yml",
		Description: "Bukkit 服务端配置",
		Format:      "yaml",
		Fields: map[string]FieldSchema{
			"settings.allow-end":    {Key: "settings.allow-end", Type: "bool", Default: "true", Description: "是否允许进入末地"},
			"settings.spawn-radius": {Key: "settings.spawn-radius", Type: "int", Default: "16", Description: "出生点周围保护半径"},
		},
	}
}

// PaperGlobalYAMLModel paper-global.yml（节选）。
func PaperGlobalYAMLModel() ModelSchema {
	return ModelSchema{
		Name:        "paper-global.yml",
		Description: "Paper 服务端全局配置",
		Format:      "yaml",
		Fields: map[string]FieldSchema{
			"proxies.velocity.enabled":                 {Key: "proxies.velocity.enabled", Type: "bool", Default: "false", Description: "启用 Velocity modern forwarding"},
			"proxies.velocity.secret":                  {Key: "proxies.velocity.secret", Type: "string", Default: "", Description: "与 Velocity forwarding-secret 一致"},
			"proxies.bungeecord.enabled":               {Key: "proxies.bungeecord.enabled", Type: "bool", Default: "false", Description: "启用 BungeeCord IP 转发"},
			"chunk-loading.global-max-chunk-load-rate": {Key: "chunk-loading.global-max-chunk-load-rate", Type: "int", Default: "300", Description: "每秒最大区块加载数"},
		},
	}
}

// VelocityTomlModel velocity.toml（节选）。
func VelocityTomlModel() ModelSchema {
	return ModelSchema{
		Name:        "velocity.toml",
		Description: "Velocity 代理配置",
		Format:      "toml",
		Fields: map[string]FieldSchema{
			"bind":                        {Key: "bind", Type: "string", Default: "0.0.0.0:25577", Description: "监听地址"},
			"motd":                        {Key: "motd", Type: "string", Default: "Velocity", Description: "MOTD"},
			"player-info-forwarding-mode": {Key: "player-info-forwarding-mode", Type: "string", Default: "none", Description: "玩家信息转发模式", Choices: []string{"none", "legacy", "modern"}},
			"forwarding-secret":           {Key: "forwarding-secret", Type: "string", Default: "", Description: "modern 转发密钥（与后端 Paper proxies.velocity.secret 一致）"},
			"enabled":                     {Key: "enabled", Type: "bool", Default: "true", Description: "代理启用"},
		},
	}
}

// BungeeCordYAMLModel BungeeCord config.yml（节选）。
func BungeeCordYAMLModel() ModelSchema {
	return ModelSchema{
		Name:        "config.yml",
		Description: "BungeeCord 代理配置",
		Format:      "yaml",
		Fields: map[string]FieldSchema{
			"listeners[].host":        {Key: "listeners[].host", Type: "string", Default: "0.0.0.0:25577", Description: "监听地址"},
			"listeners[].max_players": {Key: "listeners[].max_players", Type: "int", Default: "1", Description: "代理槽位"},
			"ip_forward":              {Key: "ip_forward", Type: "bool", Default: "false", Description: "启用真实 IP 转发（需后端 spigot bungeecord=true / paper proxies.bungeecord.enabled=true）"},
		},
	}
}
