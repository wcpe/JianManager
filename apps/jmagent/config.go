package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// 配置与环境变量（FR-564 / ADR-112）。
//
// 优先级（高 → 低）：--url / --token 命令行 → JM_URL / JM_TOKEN 环境变量
//                  → 当前默认 host（配置文件）→ 内置默认值。
const (
	envURL   = "JM_URL"
	envToken = "JM_TOKEN"

	// defaultURL 本地开发默认 CP 地址。
	defaultURL = "http://127.0.0.1:8080"

	// configFileName 配置文件相对 ~/.config 的路径。
	configFileName = "jm/hosts.yml"
)

// HostConfig 单个 CP 主机的配置。
type HostConfig struct {
	// URL CP 基址，无尾斜杠。
	URL string `yaml:"url"`
	// Username 登录用户名（仅用于展示，鉴权靠 token）。
	Username string `yaml:"username,omitempty"`
	// TokenID 远端令牌 ID，logout 时用于吊销。
	TokenID uint `yaml:"tokenId,omitempty"`
	// TokenPrefix 令牌前缀，展示用（如 jmut_ab12）。
	TokenPrefix string `yaml:"tokenPrefix,omitempty"`
	// TokenExpiresAt 过期时间（RFC3339），status 时展示。
	TokenExpiresAt string `yaml:"tokenExpiresAt,omitempty"`
}

// ConfigFile 配置文件整体结构。
type ConfigFile struct {
	// Default 默认主机的 URL；空表示取第一个。
	Default string `yaml:"default,omitempty"`
	// Hosts 以主机名为键（URL 的 host 部分）。
	Hosts map[string]HostConfig `yaml:"hosts,omitempty"`
}

// Config 运行期配置（解析完成后的结果）。
type Config struct {
	// BaseURL 本次请求使用的 CP 基址。
	BaseURL string
	// Token 本次请求使用的令牌明文；空表示未登录。
	Token string
	// TokenID / TokenPrefix 来自配置文件，供 status/logout 使用。
	TokenID     uint
	TokenPrefix string
	Username    string
	// Output text | json
	Output string
	// HostKey 当前 host 在配置文件中的键（URL 的 host 部分）。
	HostKey string
}

// configPath 返回配置文件绝对路径。
func configPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法确定用户主目录: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, configFileName), nil
}

// loadConfigFile 读取配置文件；不存在时返回空配置（不报错）。
func loadConfigFile() (*ConfigFile, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ConfigFile{Hosts: map[string]HostConfig{}}, nil
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var cf ConfigFile
	if err := yaml.Unmarshal(body, &cf); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if cf.Hosts == nil {
		cf.Hosts = map[string]HostConfig{}
	}
	return &cf, nil
}

// saveConfigFile 原子写回配置文件。
func saveConfigFile(cf *ConfigFile) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	body, err := yaml.Marshal(cf)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	tmp := path + ".tmp"
	// 0600：配置文件含主机信息，不含令牌明文（令牌在钥匙串里）。
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换配置失败: %w", err)
	}
	return nil
}

// hostKeyOf 从 URL 提取 host 键（含端口），用作配置与钥匙串的键。
func hostKeyOf(rawURL string) (string, error) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return "", fmt.Errorf("URL 不能为空")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	trimmed := strings.TrimSuffix(s, "/")
	idx := strings.Index(trimmed, "://")
	if idx < 0 {
		return "", fmt.Errorf("URL 格式无效: %s", rawURL)
	}
	rest := trimmed[idx+3:]
	if rest == "" || strings.Contains(rest, "/") && !strings.Contains(rest, ":") {
		// 允许带路径前缀的部署（如 https://cp.example.com/jm），取 host 部分即可。
		if slash := strings.Index(rest, "/"); slash > 0 {
			rest = rest[:slash]
		}
	} else if slash := strings.Index(rest, "/"); slash > 0 {
		rest = rest[:slash]
	}
	if rest == "" {
		return "", fmt.Errorf("URL 缺少主机名: %s", rawURL)
	}
	return rest, nil
}

// normalizeURL 归一化为无尾斜杠、带 scheme 的形式。
func normalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return strings.TrimSuffix(s, "/")
}

// tokenKey 返回该 host 在钥匙串中的键。
func tokenKey(hostKey string) string { return "token/" + hostKey }

// parseGlobalArgs 抽取全局选项，返回配置与剩余参数。
//
// 支持选项出现在子命令前后（任意位置抽取）。
func parseGlobalArgs(argv []string) (Config, []string, error) {
	cfg := Config{Output: "text"}
	cf, err := loadConfigFile()
	if err != nil {
		return cfg, nil, err
	}

	var rest []string
	var explicitURL, explicitToken string
	var wantOutput string

	for i := 0; i < len(argv); i++ {
		a := argv[i]
		takeNext := func() (string, bool) {
			if i+1 >= len(argv) {
				return "", false
			}
			i++
			return argv[i], true
		}
		switch {
		case a == "--url" || a == "-u":
			v, ok := takeNext()
			if !ok {
				return cfg, nil, fmt.Errorf("--url 需要参数")
			}
			explicitURL = v
		case strings.HasPrefix(a, "--url="):
			explicitURL = strings.TrimPrefix(a, "--url=")
		case a == "--token":
			v, ok := takeNext()
			if !ok {
				return cfg, nil, fmt.Errorf("--token 需要参数")
			}
			explicitToken = v
		case strings.HasPrefix(a, "--token="):
			explicitToken = strings.TrimPrefix(a, "--token=")
		case a == "--output" || a == "-o":
			v, ok := takeNext()
			if !ok {
				return cfg, nil, fmt.Errorf("--output 需要参数")
			}
			wantOutput = v
		case strings.HasPrefix(a, "--output="):
			wantOutput = strings.TrimPrefix(a, "--output=")
		case a == "-h" || a == "--help":
			rest = append(rest, a)
		case strings.HasPrefix(a, "-") && !isSubcommandFlag(a):
			return cfg, nil, fmt.Errorf("未知选项: %s", a)
		default:
			rest = append(rest, a)
		}
	}

	// 输出格式。
	if wantOutput != "" {
		cfg.Output = strings.ToLower(strings.TrimSpace(wantOutput))
	}
	if cfg.Output != "text" && cfg.Output != "json" {
		return cfg, nil, fmt.Errorf("--output 仅支持 text 或 json，收到 %q", cfg.Output)
	}

	// 基址解析：--url > JM_URL > 默认 host > 内置默认。
	chosen := normalizeURL(explicitURL)
	if chosen == "" {
		chosen = normalizeURL(os.Getenv(envURL))
	}
	if chosen == "" {
		if cf.Default != "" {
			if h, ok := cf.Hosts[cf.Default]; ok {
				chosen = normalizeURL(h.URL)
				cfg.HostKey = cf.Default
			}
		}
	}
	if chosen == "" && len(cf.Hosts) > 0 {
		// 没有默认标记时取第一个，保持可用而非报错。
		for k, h := range cf.Hosts {
			chosen = normalizeURL(h.URL)
			cfg.HostKey = k
			break
		}
	}
	if chosen == "" {
		chosen = defaultURL
	}
	cfg.BaseURL = chosen
	if cfg.HostKey == "" {
		if k, err := hostKeyOf(chosen); err == nil {
			cfg.HostKey = k
		}
	}

	// 主机元信息（仅当命中的是配置文件里的 host）。
	if h, ok := cf.Hosts[cfg.HostKey]; ok {
		cfg.Username = h.Username
		cfg.TokenID = h.TokenID
		cfg.TokenPrefix = h.TokenPrefix
	}

	// 令牌解析：--token > JM_TOKEN > 钥匙串。
	if explicitToken != "" {
		cfg.Token = strings.TrimSpace(explicitToken)
	} else if envTok := strings.TrimSpace(os.Getenv(envToken)); envTok != "" {
		cfg.Token = envTok
	} else if tok, ok := loadTokenFromKeyring(cfg.HostKey); ok {
		cfg.Token = tok
	}

	return cfg, rest, nil
}

// subcommandFlags 是子命令专属选项，全局解析阶段只透传。
var subcommandFlags = map[string]struct{}{
	"--data":    {},
	"--query":   {},
	"--host":    {},
	"--name":    {},
	"--dry-run": {},
	"--force":   {},
	"--approve": {},
	"--deny":    {},
}

// isSubcommandFlag 判定是否为已知子命令选项（含 --key=value 形式）。
func isSubcommandFlag(arg string) bool {
	name := arg
	if idx := strings.Index(arg, "="); idx > 0 {
		name = arg[:idx]
	}
	_, ok := subcommandFlags[name]
	return ok
}

// requireLogin 确保已有令牌。
func requireLogin(cfg Config) error {
	if cfg.Token == "" {
		return fmt.Errorf("尚未登录：先运行 `jm auth login --url <url>`（或设置 %s）", envToken)
	}
	return nil
}

// requireToken 是 requireLogin 的兼容别名，供既有命令调用。
func requireToken(cfg Config) error { return requireLogin(cfg) }

// requireURL 确保已有可用基址。
func requireURL(cfg Config) error {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return fmt.Errorf("缺少 CP 地址：运行 `jm config set-url <url>` 或设置 %s", envURL)
	}
	return nil
}
