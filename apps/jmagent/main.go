// Command jm 是 JianManager 的命令行客户端（由 jmagent 重构而来，兼容别名保留一个版本周期）。
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
	fmt.Fprintf(os.Stderr, `%s — JianManager 命令行客户端（经 CP HTTPS + 用户令牌）

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

// subcommandUsage 返回子命令专属的帮助正文；未知子命令返回空串。
//
// 为什么不复用顶层 usage：`jm api --help` 要看到的是 api 的参数说明（--data/--query 等），
// 把整页总览再打一遍等于没说。与 `gh <cmd> --help` 的行为对齐。
func subcommandUsage(sub string) string {
	switch sub {
	case "api":
		return `用法: jm api <METHOD> <PATH> [选项]

直接调用 CP 任意 HTTP 路由，不做子命令映射（对齐 gh api）。

参数:
  <METHOD>          GET / POST / PUT / PATCH / DELETE（省略则默认 GET）
  <PATH>            CP 路由路径，如 /api/v1/instances

选项:
  --data, -d <v>    请求体：@文件路径、- 表示 stdin、其它按字面量 JSON
  --query, -q k=v   查询参数，可重复

示例:
  jm api GET /api/v1/instances
  jm api POST /api/v1/instances -d @payload.json
  jm api GET /api/v1/instances -q page=2 -q size=20
  echo '{"name":"x"}' | jm api POST /api/v1/instances -d -
`
	case "auth":
		return `用法: jm auth <子命令>

  login [--host <url>]   设备授权登录（RFC 8628）：先在浏览器确认，再取回令牌
  status                 查看当前登录状态与主机
  logout                 登出：清除本地钥匙串条目，并吊销远端令牌

说明:
  - 令牌存系统钥匙串，不落盘明文；无钥匙串时报错并提示改用 JM_TOKEN
  - login 的 --host 等价于同时设置该次的 CP 地址
`
	case "config":
		return `用法: jm config <子命令>

  set-url <url>   添加或更新一个 CP 主机，并设为默认
  get-url         打印当前生效的 CP 地址
  list            列出已配置的全部主机
  use <host>      切换默认主机
  remove <host>   删除某个主机配置

说明:
  - 地址优先级：--url > JM_URL > 默认主机 > 内置默认
`
	case "skill":
		return `用法: jm skill <子命令>

  install [选项]   把 Agent 运维知识（SKILL.md + references）装进宿主 skills 目录
  list             列出内置知识文件清单

install 选项:
  --host <name>    目标宿主：claude / codex / cursor；可重复或逗号分隔
                   缺省按 ~/.claude、~/.codex、~/.cursor 是否存在自动探测
  --dir <path>     直接指定安装目录（覆盖 --host 的路径推导）
  --from <dir>     从本地目录取内容，而非经 CP 静态路由（离线/开发用）
  --dry-run        只打印将写入的位置，不实际落盘

说明:
  - 内容来源默认是 CP 的匿名路由 GET /api/v1/skill/<path>，无需登录
  - 每个宿主都装到 <skills>/jianmanager/ 子目录，避免与其它技能混淆
`
	case "whoami":
		return `用法: jm whoami

显示当前令牌对应的身份：用户名、角色、策略版本与已授予的能力。

等价于 jm api GET /api/v1/agent/whoami，但输出经整理更易读。
`
	case "list":
		return `用法: jm list <资源>

  nodes                   列出 Token scope 内的节点
  instances [--node <id>] 列出实例，可按节点过滤
`
	case "instance":
		return `用法: jm instance <子命令> <id>

  status <id>            查看实例状态
  metrics <id>           查看实例指标
  start|stop|restart <id> 控制实例生命周期
`
	case "node":
		return `用法: jm node <子命令> <id>

  maintenance enter|leave <id>   进入/退出维护模式
`
	}
	return ""
}

// wantsHelp 判定参数里是否请求了帮助（只看第一个非全局参数位置，避免把
// `--data --help` 这类字面量误判成求助）。
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

// printSubcommandUsage 打印子命令帮助；顶层总览在末尾附一行指引。
func printSubcommandUsage(sub string) {
	fmt.Fprintln(os.Stderr, subcommandUsage(sub))
	fmt.Fprintf(os.Stderr, "运行 `%s --help` 查看全部命令。\n", progName())
}

// isContainerCommand 判定子命令是否只是「分发容器」（自身无直接动作）。
//
// 只对这些命令把裸执行导向帮助：`jm auth`/`jm config` 这类没有默认行为，
// 而 `jm list`、`jm whoami` 裸执行有确定语义（列全部 / 查自己），改掉会破坏用法。
func isContainerCommand(sub string) bool {
	switch sub {
	case "auth", "config", "skill":
		return true
	}
	return false
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

	// 子命令帮助必须在分发前拦下：各 runX 会先做 requireLogin/requireURL，
	// 否则 `jm api --help` 会因「尚未登录」而看不到用法——这恰恰是最需要 help 的时刻。
	// 容器型子命令（auth/config/skill）裸执行同样落到帮助：只给一行用法、还冠以
	// 「错误:」前缀，对一个「想看看怎么用」的人来说是误导。
	if subcommandUsage(sub) != "" && (wantsHelp(rest) || (len(rest) == 0 && isContainerCommand(sub))) {
		printSubcommandUsage(sub)
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
