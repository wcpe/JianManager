// Package schema 提供配置文件 schema 元数据 + 跨文件一致性校验。
package schema

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// FieldSchema 单字段元数据。
type FieldSchema struct {
	Key         string `json:"key"`
	Type        string `json:"type"` // string/int/bool/list
	Default     string `json:"default"`
	Description string `json:"description"`
	Group       string `json:"group,omitempty"`
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
			"server-port":      {Key: "server-port", Type: "int", Default: "25565", Description: "服务端监听端口", Group: "网络与身份"},
			"server-ip":        {Key: "server-ip", Type: "string", Default: "", Description: "绑定 IP，留空表示全部接口", Group: "网络与身份"},
			"motd":             {Key: "motd", Type: "string", Default: "A Minecraft Server", Description: "每日消息", Group: "展示与容量"},
			"max-players":      {Key: "max-players", Type: "int", Default: "20", Description: "最大在线人数", Group: "展示与容量"},
			"online-mode":      {Key: "online-mode", Type: "bool", Default: "true", Description: "正版验证，开启时玩家需登录 Mojang 账号", Group: "网络与身份"},
			"view-distance":    {Key: "view-distance", Type: "int", Default: "10", Description: "视距（区块）", Group: "展示与容量"},
			"difficulty":       {Key: "difficulty", Type: "string", Default: "easy", Description: "难度", Group: "展示与容量", Choices: []string{"peaceful", "easy", "normal", "hard"}},
			"gamemode":         {Key: "gamemode", Type: "string", Default: "survival", Description: "默认游戏模式", Group: "展示与容量", Choices: []string{"survival", "creative", "adventure", "spectator"}},
			"white-list":       {Key: "white-list", Type: "bool", Default: "false", Description: "是否启用白名单", Group: "展示与容量"},
			"enable-rcon":      {Key: "enable-rcon", Type: "bool", Default: "false", Description: "启用 RCON 远程控制", Group: "远程控制"},
			"rcon.port":        {Key: "rcon.port", Type: "int", Default: "25575", Description: "RCON 端口", Group: "远程控制"},
			"rcon.password":    {Key: "rcon.password", Type: "string", Default: "", Description: "RCON 密码（启用 RCON 时必填）", Group: "远程控制"},
			"enable-query":     {Key: "enable-query", Type: "bool", Default: "false", Description: "启用 GameSpy4 查询协议", Group: "远程控制"},
			"query.port":       {Key: "query.port", Type: "int", Default: "25565", Description: "查询协议端口", Group: "远程控制"},
			"spawn-protection": {Key: "spawn-protection", Type: "int", Default: "16", Description: "出生点保护半径（0 关闭）", Group: "展示与容量"},
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

// VelocityTomlModel Velocity 代理 velocity.toml 的关键项。
//
// ## 键名必须与 `parseFlatTOML` 的产物一致，否则表单会渲染「幽灵字段」
//
// 与 BungeeCord 那条同理：schema 里出现的每个键都必须能在真实文件里解析出来。
// 曾声明过的 `enabled` 与 `forwarding-secret` 就是反例——
//
//   - `enabled`：Velocity 的 toml 里从来没有这个键（是否启用由实例状态决定，不在配置里）。
//   - `forwarding-secret`：JM 走的是 `forwarding-secret-file = "forwarding.secret"`，
//     密钥落在**独立文件**而不是内联字段（见 proxyconfig.go 的 buildVelocityToml 与
//     proxy.go 的 secret 下发）；真实 Velocity 3.x 默认配置同样用 `-file` 形态。
//
// 这两条都会让 form 模式显示一个文件里并不存在的输入框；而 `patchTOML` 对缺失键是
// **追加到文件末尾**，用户一保存就会往 velocity.toml 里写进 `enabled = true` 与
// `forwarding-secret = ""`，污染代理配置。故一并移除。
//
// 跨代理转发密钥的一致性校验（crosscheck.go 的 CheckForwardingSecret）读取的是
// 「内联 forwarding-secret」场景；JM 自建的拓扑由平台统一生成并持久化同一份 secret
// 下发两侧（重建时复用），一致性由构造保证，不依赖该内联字段。
func VelocityTomlModel() ModelSchema {
	return ModelSchema{
		Name:        "velocity.toml",
		Description: "Velocity 代理配置",
		Format:      "toml",
		Fields: map[string]FieldSchema{
			"bind":                        {Key: "bind", Type: "string", Default: "0.0.0.0:25577", Description: "监听地址"},
			"motd":                        {Key: "motd", Type: "string", Default: "Velocity", Description: "MOTD"},
			"player-info-forwarding-mode": {Key: "player-info-forwarding-mode", Type: "string", Default: "none", Description: "玩家信息转发模式", Choices: []string{"none", "legacy", "modern"}},
		},
	}
}

// BungeeCordYAMLModel BungeeCord config.yml 的关键项（§2.2.4）。
//
// ## 键名必须与 `parseFlatYAML` 的产物一致
//
// `parseFlatYAML`（parsers.go:31）把嵌套 YAML 拍平成**点号路径**：`listeners:` 下缩进的
// `host:` 产出的是 `listeners.host`，不是 JSONPath 风格的 `listeners[].host`。
// 早先本 schema 写的是后者，两边对不上，form 模式的每个字段都绑不到真实值——
// 唯一能用的就只有文本模式。这里统一改成点号路径。
//
// ## 为什么 servers / permissions 不在本 schema 内
//
// 这两类**无法用「一个键 → 一个标量」的 schema 表单表达**，硬塞进来只会产生伪字段：
//
//   - `servers` 是「子服名 → {address, motd, restricted}」的映射，子服名由运维自定义、
//     无法穷举；拍平后是 `servers.<名字>.<属性>`。早期这里声明过一个 `servers` 标量字段，
//     结果是 form 模式渲染出一个真实配置里并不存在的输入框，而补丁会把整张子服表覆盖成
//     一个字符串（两台后端连同 address/motd 全部丢失）。子服拓扑请走拓扑页或文本模式。
//   - `permissions.default` / `permissions.admin` 是**列表**（`- 权限节点`），而
//     `parseFlatYAML` 跳过无冒号的列表项，因此这两个键在解析产物里根本不存在。
//     权限条目同样请在文本模式下编辑。
//
// 保留的全是「确实是标量」的关键项，保证 form 里出现的每个字段都能真正读出来、写回去。
func BungeeCordYAMLModel() ModelSchema {
	return ModelSchema{
		Name:        "config.yml",
		Description: "BungeeCord 代理配置（关键项）",
		Format:      "yaml",
		Fields: map[string]FieldSchema{
			// ---- 监听器 ----
			// listeners 在 YAML 里是列表；补丁写入时仅当列表恰好一项才会进入该项改写，
			// 多项时返回明确错误而不是替你猜改哪一个（见 config_patch.go 的 yamlDescend）。
			"listeners.host":        {Key: "listeners.host", Type: "string", Default: "0.0.0.0:25577", Description: "监听地址与端口（端口即代理对外端口）"},
			"listeners.max_players": {Key: "listeners.max_players", Type: "int", Default: "1", Description: "代理槽位上限"},
			"listeners.motd":        {Key: "listeners.motd", Type: "string", Default: "&1BungeeCord", Description: "服务器列表中显示的名称"},
			"listeners.query_port":  {Key: "listeners.query_port", Type: "int", Default: "-1", Description: "Query 协议端口，-1 表示关闭"},
			// ---- 转发与在线模式 ----
			"ip_forward": {
				Key:         "ip_forward",
				Type:        "bool",
				Default:     "false",
				Description: "启用真实 IP 转发（需后端 spigot.yml 的 bungeecord=true 或 paper 的 proxies.bungeecord.enabled=true）",
			},
			"online_mode": {
				Key:         "online_mode",
				Type:        "bool",
				Default:     "true",
				Description: "正版验证；关闭后玩家名可被冒用，仅限内网或已由上游鉴权的场景",
			},
			"connection_throttle":       {Key: "connection_throttle", Type: "int", Default: "4000", Description: "同一 IP 的连接间隔（毫秒），0 表示不限制"},
			"connection_throttle_limit": {Key: "connection_throttle_limit", Type: "int", Default: "3", Description: "超过间隔后允许的并发连接数"},
		},
	}
}
