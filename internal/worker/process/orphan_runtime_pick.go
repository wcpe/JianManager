package process

import "strings"

// looksLikeRuntimeCmdline 粗判一个进程命令行是否像“游戏服运行时”。
//
// ## 为什么需要这个判据
// 实例目录下住着的不只游戏服：tmux/systemd 起的 shell（`-bash`）、启动脚本、`tee` 管道、
// 备份工具都可能在同一个目录里。孤儿扫描要为“这个目录里有东西在跑”选一个**代表进程**，
// 选错了不只是显示难看——接管路径会拿它去和实例的启动命令比对，对不上就拒绝收敛，
// 于是“服务器在跑、平台显示停止”永远不会自愈（真实机上卡的就是这一步）。
//
// ## 判据刻意宽泛
// 这是**排序提示**而不是安全边界：命中就优先选它；都不命中也不能断言“目录里没人”（
// 自定义启动方式合法存在）。
//
// 两条调用方对「都不命中」的处理**刻意不同**：
//   - 周期扫描（scanForeignInRegisteredWorkdirs）：**不认作漂移**，只落一条观测审计；
//     宁可面板安静，也不挂一个指向 shell 的假“外来运行时”去诱人接管。
//   - 手动收养（probeForeignRuntimeOnDemand）：仍返回 fallback PID，以便报出
//     「目录里有东西在跑、但不像游戏服运行时」这个具体理由，而不是笼统说没东西可收养。
//
// 命中任一即认为像运行时：
//   - 命令行含 `java`（MC 服绝大多数形态）；
//   - 含 `.jar`（哪怕是自定义 JRE）；
//   - 含 `bedrock_server` / `bedrock-server`（基岩版）；
//   - 含 `paper` / `velocity` / `bungeecord` / `spigot` / `forge` / `fabric` 等常见核心名。
func looksLikeRuntimeCmdline(cmdline string) bool {
	c := strings.ToLower(cmdline)
	if c == "" {
		return false
	}
	for _, marker := range runtimeCmdlineMarkers {
		if strings.Contains(c, marker) {
			return true
		}
	}
	return false
}

// runtimeCmdlineMarkers 是“像运行时”的命令行标记（小写匹配）。
var runtimeCmdlineMarkers = []string{
	"java",
	".jar",
	"bedrock_server",
	"bedrock-server",
	"paper",
	"velocity",
	"bungeecord",
	"spigot",
	"forge",
	"fabric",
}
