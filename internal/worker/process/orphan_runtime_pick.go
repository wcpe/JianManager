package process

import (
	"path"
	"strings"
)

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

// runtimeExecutableMarkers 是「**可执行体本身**即游戏服运行时」的标记（按可执行文件基名小写匹配）。
//
// 与 runtimeCmdlineMarkers 的差别就是「看哪里」：这里只看命令行首 token 的可执行文件名，
// 不认命令行任意位置出现的核心名与文件名。`.jar` / `paper` / `spigot` 这些标记其实是
// **被读取的文件名**，出现在 `cp`、`tar`、`grep`、`vim` 的参数里完全不表示进程在跑游戏服。
//
// 启动预检若按它们判占用，就会把「往工作目录拷贝 server.jar 的那条命令」判成活进程
// （issue #101 真机误报：`bash -c 'WD=…; cp /tmp/jmchain/paper.jar $WD/paper.jar'` 同时命中
// 路径子串与 `paper`/`.jar`，启动被 PREFLIGHT_FAILED 拒掉）。故预检改用本表。
var runtimeExecutableMarkers = []string{
	"java", // java / javaw（含 .../jdk-21/bin/java 这类带目录形式）
	"bedrock_server",
	"bedrock-server",
}

// looksLikeRuntimeExecutable 判断进程的**可执行体**是否像游戏服运行时（只看命令行首 token）。
//
// 供启动预检使用：比 looksLikeRuntimeCmdline 严格——命令行参数里出现 `.jar`/`paper` 不算，
// 只有真正以 java（或基岩版服务端）为可执行体的进程才算。孤儿扫描仍用宽口径的
// looksLikeRuntimeCmdline（它需要召回「wrapper 死了但 Java 还活着」的残留，见其注释）。
func looksLikeRuntimeExecutable(cmdline string) bool {
	exe := cmdlineExecutableBase(cmdline)
	if exe == "" {
		return false
	}
	for _, marker := range runtimeExecutableMarkers {
		if strings.Contains(exe, marker) {
			return true
		}
	}
	return false
}

// cmdlineExecutableBase 取命令行首个 token 的可执行文件基名（小写、去引号、去目录与 .exe 后缀）。
//
// 命令行以引号开头时按**成对引号**取整段：Windows 上 `"C:\Program Files\…\java.exe" -jar …`
// 含空格，按空白切分会把可执行体截成 `C:\Program` 而认不出来。取不到（空命令行）返回空串。
func cmdlineExecutableBase(cmdline string) string {
	s := strings.TrimSpace(cmdline)
	if s == "" {
		return ""
	}
	var exe string
	if s[0] == '"' || s[0] == '\'' {
		quote := s[0]
		if end := strings.IndexByte(s[1:], quote); end >= 0 {
			exe = s[1 : 1+end]
		} else {
			exe = s[1:]
		}
	} else if fields := strings.Fields(s); len(fields) > 0 {
		exe = fields[0]
	}
	exe = strings.ToLower(strings.Trim(exe, `"'`))
	// Windows 命令行用反斜杠分隔目录：先统一为 '/'，再取基名（path.Base 只认 '/'）。
	exe = strings.ReplaceAll(exe, `\`, "/")
	if exe == "" {
		return ""
	}
	exe = path.Base(exe)
	if exe == "." || exe == "/" {
		return ""
	}
	return strings.TrimSuffix(exe, ".exe")
}
