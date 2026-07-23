// Command jmagent 是 JianManager 的 Agent 管理 CLI（FR-385，见 ADR-076）。
//
// 与 jmctl（ADR-041 本机紧急直连 daemon）严格区分：本工具仅经 Control Plane HTTPS +
// Agent Token（Bearer jmat_*）调用 FR-384 暴露的 Agent 运维 API，不链 gRPC / 数据库 /
// Worker / daemon socket。Token 仅来自 --token 或环境变量 JM_AGENT_TOKEN，永不落盘。
//
// 策略（写白名单、scope、硬拒绝）全部由 CP 判定；CLI 只做薄 HTTP 客户端与输出格式化。
package main

import (
	"fmt"
	"os"
)

// usage 打印命令总览到 stderr。
func usage() {
	fmt.Fprint(os.Stderr, `jmagent — JianManager Agent 管理 CLI（经 CP HTTPS + Agent Token，见 FR-385 / ADR-076）

用法:
  jmagent [全局选项] whoami
  jmagent [全局选项] list nodes
  jmagent [全局选项] list instances [--node <id>]
  jmagent [全局选项] instance status  <id>
  jmagent [全局选项] instance metrics <id>
  jmagent [全局选项] instance start|stop|restart <id>
  jmagent [全局选项] node maintenance enter|leave <id>

全局选项:
  --token string     Agent Token（也可用环境变量 JM_AGENT_TOKEN）
  --cp-url string    Control Plane 基址（默认 $JM_AGENT_CP 或 http://127.0.0.1:8080）
  --output string    输出格式：text（默认）| json

说明:
  - 403/硬拒绝：中文原因写 stderr，进程以非零退出码结束
  - Token 不写日志、不落盘；仅内存持有用于 Authorization 头
  - 不实现 Token 颁发（请用面板或管理员 API）
`)
}

func main() {
	cfg, args, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n\n", err)
		usage()
		os.Exit(2)
	}
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	sub := args[0]
	rest := args[1:]

	// 帮助
	if sub == "-h" || sub == "--help" || sub == "help" {
		usage()
		return
	}

	// 需要 Token 的命令在 run* 内再校验；先构造客户端
	client := newClient(cfg)

	var runErr error
	switch sub {
	case "whoami":
		runErr = runWhoami(client, cfg, rest)
	case "list":
		runErr = runList(client, cfg, rest)
	case "instance":
		runErr = runInstance(client, cfg, rest)
	case "node":
		runErr = runNode(client, cfg, rest)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", sub)
		usage()
		os.Exit(2)
	}

	if runErr != nil {
		// APIError（含 403）已自带中文 message；其余包装为统一前缀
		if ae, ok := asAPIError(runErr); ok {
			fmt.Fprintf(os.Stderr, "错误: %s\n", ae.Message)
			// 403 与其它 HTTP 失败均非零；状态码映射到退出码便于脚本区分
			if ae.Status == 403 {
				os.Exit(1)
			}
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "错误: %v\n", runErr)
		os.Exit(1)
	}
}
