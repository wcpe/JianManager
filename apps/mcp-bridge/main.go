// Command mcp-bridge 是 JianManager 的 MCP 协议适配器（FR-386，见 ADR-077）。
//
// 默认以 stdio 提供 MCP server，持 Agent Token 经 HTTPS/HTTP 调用 Control Plane
// Agent 运维 API（FR-384）。仅作 protocol adapter：不二次实现 scope/写白名单，
// 不链 gRPC/DB/Worker，不监听独立网络端口。
//
// 配置：--token / --cp-url 或环境变量 JM_AGENT_TOKEN / JM_AGENT_CP。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/wcpe/JianManager/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			usage()
			return 0
		case "--version", "-version", "version":
			fmt.Println(version.Version)
			return 0
		}
	}

	cfg, err := ParseConfig(args)
	if err != nil {
		if errors.Is(err, errShowVersion) {
			fmt.Println(version.Version)
			return 0
		}
		if isFlagHelp(err) {
			usage()
			return 0
		}
		fmt.Fprintf(os.Stderr, "错误: %v\n\n", err)
		usage()
		return 2
	}

	client := NewAgentClient(cfg.CPURL, cfg.Token, nil)
	srv := NewServer(client, os.Stdin, os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 1
	}
	return 0
}

// isFlagHelp 判断是否为 flag 包的 -h 帮助请求。
func isFlagHelp(err error) bool {
	return err != nil && err.Error() == "flag: help requested"
}
