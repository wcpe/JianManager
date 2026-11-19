// Command jm 是 JianManager 的命令行客户端（FR-564，重构自 FR-385 的 jmagent）。
//
// 定位对齐 GitHub CLI：
//   - `jm api` 一条命令覆盖 CP 全部 HTTP 路由，不做子命令逐个映射
//   - 少量高频命令作为糖（instance / node / list / whoami）
//   - `jm auth` 走设备授权（RFC 8628），令牌存系统钥匙串
//   - `jm config` 管理多个 CP 主机
//
// 与 jmctl 严格区分：jmctl 是 CP/Worker 全挂时的本机应急控制台，零网络面、仅连 daemon socket；
// jm 是日常运维面，经 CP HTTPS + 用户令牌（jmut_）。
//
// 依赖约束：只用标准库 + internal/platform/*，不链 gRPC/DB/service（见 spec 第 3.6 节）。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/internal/version"
)

// progName 返回展示用程序名：按 argv[0] 识别 jm / jmagent。
// jmagent 作为兼容别名保留一个版本周期，便于既有脚本平滑迁移。
func progName() string {
	base := filepath.Base(os.Args[0])
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "jm", "jmagent":
		return base
	default:
		return "jm"
	}
}

// usage 打印命令总览到 stderr。
func usage() {
	n := progName()
	fmt.Fprintf(os.Stderr, `%s — JianManager 命令行客户端（经 CP HTTPS + 用户令牌，见 FR-564 / ADR-112）

用法:
  %s api <METHOD> <PATH> [--data @file|-] [--query k=v]...   直接调用任意 CP 接口
  %s auth login [--host <url>]                                设备授权登录
  %s auth status                                              查看当前登录状态
  %s auth logout                                              登出（本地清除 + 远端吊销）
  %s config set-url|get-url|list|use|remove                   管理多个 CP 主机
  %s skill install [--host claude|codex|cursor]...             安装 Agent 运维知识到宿主
                    [--dir <path>] [--from <目录>] [--dry-run]

高频命令（等价于 jm api 的封装）:
  %s whoami
  %s list nodes
  %s list instances [--node <id>]
  %s instance status|metrics <id>
  %s instance start|stop|restart <id>
  %s node maintenance enter|leave <id>

全局选项:
  --version         显示版本号
  --url string      临时指定 CP 基址（优先于配置与环境变量）
  --token string    临时指定令牌（优先于钥匙串与环境变量）
  --output string   输出格式：text（默认）| json

环境变量:
  JM_URL            CP 基址（优先级低于 --url，高于配置文件）
  JM_TOKEN          令牌（优先级低于 --token，高于钥匙串）

说明:
  - 令牌存系统钥匙串；当前环境无钥匙串时报错并提示改用 JM_TOKEN，不落盘明文
  - 401：令牌无效/已吊销/已过期；403：权限或 scope 不足
  - 详见 docs/specs/jm-agent-cli/spec.md
`, n, n, n, n, n, n, n, n, n, n, n, n, n)
}

func main() {
	// 版本先于一切解析：`--version` 不带其它参数也要能出结果，
	// 否则装了哪个版本只能靠文件名猜（发行包正是靠它做烟测）。
	if version.Requested(os.Args[1:]) {
		fmt.Println(version.Version)
		return
	}

	cfg, args, err := parseGlobalArgs(os.Args[1:])
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

	if sub == "-h" || sub == "--help" || sub == "help" {
		usage()
		return
	}

	var runErr error
	switch sub {
	case "api":
		runErr = runAPI(cfg, rest)
	case "auth":
		runErr = runAuth(cfg, rest)
	case "config":
		runErr = runConfig(cfg, rest)
	case "skill":
		runErr = runSkill(cfg, rest)
	case "whoami":
		runErr = runWhoami(newClient(cfg), cfg, rest)
	case "list":
		runErr = runList(newClient(cfg), cfg, rest)
	case "instance":
		runErr = runInstance(newClient(cfg), cfg, rest)
	case "node":
		runErr = runNode(newClient(cfg), cfg, rest)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", sub)
		usage()
		os.Exit(2)
	}

	if runErr != nil {
		if ae, ok := asAPIError(runErr); ok {
			fmt.Fprintf(os.Stderr, "错误: %s\n", ae.Message)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "错误: %v\n", runErr)
		os.Exit(1)
	}
}
