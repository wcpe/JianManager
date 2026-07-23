package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// 配置环境变量与 CLI 标志（与 jm-agent 对齐）。
const (
	envAgentToken = "JM_AGENT_TOKEN"
	envAgentCP    = "JM_AGENT_CP"
)

// Config mcp-bridge 运行配置。
type Config struct {
	// Token Agent Token 明文（Bearer jmat_*）。
	Token string
	// CPURL Control Plane 根地址，如 http://127.0.0.1:8080。
	CPURL string
}

// ParseConfig 从 flag + 环境变量解析配置。
// 优先级：命令行 flag > 环境变量。
func ParseConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("mcp-bridge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	token := fs.String("token", "", "Agent Token（也可用环境变量 "+envAgentToken+"）")
	cpURL := fs.String("cp-url", "", "Control Plane URL（也可用环境变量 "+envAgentCP+"）")
	showVersion := fs.Bool("version", false, "打印版本并退出")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if *showVersion {
		return Config{}, errShowVersion
	}

	cfg := Config{
		Token: strings.TrimSpace(firstNonEmpty(*token, os.Getenv(envAgentToken))),
		CPURL: strings.TrimSpace(firstNonEmpty(*cpURL, os.Getenv(envAgentCP))),
	}
	if cfg.Token == "" {
		return Config{}, errors.New("缺少 Agent Token：请设置 --token 或环境变量 " + envAgentToken)
	}
	if cfg.CPURL == "" {
		return Config{}, errors.New("缺少 Control Plane URL：请设置 --cp-url 或环境变量 " + envAgentCP)
	}
	// 去掉尾部斜杠，避免双斜杠。
	cfg.CPURL = strings.TrimRight(cfg.CPURL, "/")
	return cfg, nil
}

// errShowVersion 表示用户请求 --version，由 main 处理。
var errShowVersion = errors.New("show version")

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// usage 打印用法说明到 stderr。
func usage() {
	fmt.Fprint(os.Stderr, `mcp-bridge — JianManager MCP 协议适配器（FR-386 / ADR-077）

默认以 stdio 提供 MCP server，持 Agent Token 调用 Control Plane Agent 运维 API。
不监听网络端口；不二次实现 scope/写白名单（策略真源在 CP，ADR-076）。

用法:
  mcp-bridge [--token TOKEN] [--cp-url URL]

配置（flag 优先于 env）:
  --token / `+envAgentToken+`   Agent Token（Bearer jmat_*）
  --cp-url / `+envAgentCP+`     Control Plane 根 URL，如 http://127.0.0.1:8080

示例（Cursor / Claude Code MCP 配置）:
  {
    "command": "mcp-bridge",
    "env": {
      "JM_AGENT_TOKEN": "jmat_...",
      "JM_AGENT_CP": "http://127.0.0.1:8080"
    }
  }
`)
}
