package main

import (
	"fmt"
	"os"
	"strings"
)

// 默认 CP 基址（开发环境常见本地端口）。
const defaultCPURL = "http://127.0.0.1:8080"

// 环境变量名。
const (
	envAgentToken = "JM_AGENT_TOKEN"
	envAgentCP    = "JM_AGENT_CP"
)

// Config 保存全局运行时配置。Token 仅驻内存，不落盘。
type Config struct {
	Token  string // Agent Token（jmat_*）
	CPURL  string // Control Plane 基址，无尾斜杠
	Output string // text | json
}

// parseArgs 从 argv 抽出全局选项，返回配置与剩余位置参数（子命令及其参数）。
// 优先级：命令行 flag > 环境变量 > 默认值。
// 支持全局选项出现在子命令前后（任意位置抽取 --token / --cp-url / --output）。
func parseArgs(argv []string) (Config, []string, error) {
	cfg := Config{
		Token:  strings.TrimSpace(os.Getenv(envAgentToken)),
		CPURL:  strings.TrimSpace(os.Getenv(envAgentCP)),
		Output: "text",
	}
	if cfg.CPURL == "" {
		cfg.CPURL = defaultCPURL
	}

	var rest []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--token" || a == "-token":
			if i+1 >= len(argv) {
				return cfg, nil, fmt.Errorf("--token 需要参数")
			}
			i++
			cfg.Token = strings.TrimSpace(argv[i])
		case strings.HasPrefix(a, "--token="):
			cfg.Token = strings.TrimSpace(strings.TrimPrefix(a, "--token="))
		case a == "--cp-url" || a == "-cp-url":
			if i+1 >= len(argv) {
				return cfg, nil, fmt.Errorf("--cp-url 需要参数")
			}
			i++
			cfg.CPURL = strings.TrimSpace(argv[i])
		case strings.HasPrefix(a, "--cp-url="):
			cfg.CPURL = strings.TrimSpace(strings.TrimPrefix(a, "--cp-url="))
		case a == "--output" || a == "-output" || a == "-o":
			if i+1 >= len(argv) {
				return cfg, nil, fmt.Errorf("--output 需要参数")
			}
			i++
			cfg.Output = strings.TrimSpace(argv[i])
		case strings.HasPrefix(a, "--output="):
			cfg.Output = strings.TrimSpace(strings.TrimPrefix(a, "--output="))
		case a == "-h" || a == "--help":
			// 保留给 main 处理
			rest = append(rest, a)
		case strings.HasPrefix(a, "-"):
			return cfg, nil, fmt.Errorf("未知选项: %s", a)
		default:
			rest = append(rest, a)
		}
	}

	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.CPURL = strings.TrimRight(strings.TrimSpace(cfg.CPURL), "/")
	cfg.Output = strings.ToLower(strings.TrimSpace(cfg.Output))
	if cfg.Output != "text" && cfg.Output != "json" {
		return cfg, nil, fmt.Errorf("--output 仅支持 text 或 json，收到 %q", cfg.Output)
	}
	return cfg, rest, nil
}

// requireToken 确保已配置 Agent Token。
func requireToken(cfg Config) error {
	if cfg.Token == "" {
		return fmt.Errorf("缺少 Agent Token：请设置 --token 或环境变量 %s", envAgentToken)
	}
	return nil
}
