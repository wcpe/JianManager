package logtail

import (
	"regexp"

	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// 行首时间与级别的识别规则。
//
// # 为什么是四条而不是一条
//
// 生产上并存两种日志前缀形态，它们的括号位置不同，一条正则收不住：
//
//	[01:19:43] [ServerMain/INFO]: Environment: ...   ← Paper/MC 原生（时刻与线程/级别各一个方括号）
//	18:12:59 [INFO] Using mbed TLS ...               ← BungeeCord（时刻**没有**方括号，级别带）
//
// 2026-11-06 真机实测：`/home/user/servers/*/proxy.log.0` 共 88234 行全是后者，
// 而只按前者的形态解析时**一行都识别不出** —— level 全空、eventTime 全部退回采集时刻。
// 后者对按级别保留策略是硬伤（`level:="WARN"` 匹配不到任何 proxy 行 ⇒ 永不清理）。
//
// 归一化侧（`normalize.CanonicalLevel`）已有同型教训：BungeeCord 的 `[WARNING]`/`[SEVERE]`
// 必须折成 `WARN`/`ERROR`，否则与保留策略白名单对不上。
var (
	// [HH:MM:SS] [thread/LEVEL]: body — Paper/MC 原生。线程名可含空格，故用 [^\]/]*。
	mcThreadLevelRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})(?:[,.]\d{1,3})?\]\s+\[([^\]/]*)/([A-Za-z]+)\]`)
	// [HH:MM:SS LEVEL]: body — 时刻与级别同一个方括号。
	bracketTimeLevelRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})(?:[,.]\d{1,3})?\s+([A-Za-z]+)\]`)
	// HH:MM:SS [LEVEL] body — BungeeCord：时刻**不带**方括号，级别带。
	bungeeTimeLevelRe = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})(?:[,.]\d{1,3})?\s+\[([A-Za-z]+)\]`)
	// [HH:MM:SS] body — 只有时刻、没有级别（紧跟在有级别的行之后的情形，或日志头部）。
	bracketTimeOnlyRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})(?:[,.]\d{1,3})?\]`)
	// HH:MM:SS body — 时刻裸起头、无级别。BungeeCord 里也存在（如异常行）。
	bareTimeOnlyRe = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})(?:[,.]\d{1,3})?\s`)
	// Go slog / logrus 结构化 level=INFO（Beacon 等进程）；独立 token，可带引号。
	slogLevelRe = regexp.MustCompile(`(?i)(?:^|[\s])level=["']?(INFO|WARNING|WARN|ERROR|SEVERE|FATAL|DEBUG|TRACE)["']?(?:[\s]|$)`)
)

// parseLogLine 从一行日志里提取 `HH:MM:SS` 时刻与级别。
//
// # 为什么时间与级别要一起解析
//
// 它们来自同一个行首前缀，分两次正则会重复描述同一形态（本仓的「同一概念多处实现」
// 缺陷就是这么来的）。一次匹配同时给出两者，形态一改只需改一处。
//
// # 为什么级别要归一
//
// 写入 VL 的 `level` 字段是**按级别保留策略的过滤目标**（`level:="WARN"`）。
// BungeeCord 写 `[WARNING]`、部分 JVM 写 `[SEVERE]`，若不折成白名单里的 `WARN`/`ERROR`，
// 这些行永远不会被任何按级别的删除规则命中。归一逻辑复用 `normalize.CanonicalLevel`
// ——**唯一真源**，不在本包重写别名表。
//
// # 绝不发明
//
// 识别不到就返回空串，由调用方按「已知的降级行为」处理（见 eventTime）。
// 不从内容里猜级别，也不给堆栈行硬套上一行的级别。
func parseLogLine(line string) (timestamp, level string) {
	if m := mcThreadLevelRe.FindStringSubmatch(line); m != nil {
		return m[1] + ":" + m[2] + ":" + m[3], normalize.CanonicalLevel(m[5])
	}
	if m := bracketTimeLevelRe.FindStringSubmatch(line); m != nil {
		return m[1] + ":" + m[2] + ":" + m[3], normalize.CanonicalLevel(m[4])
	}
	if m := bungeeTimeLevelRe.FindStringSubmatch(line); m != nil {
		return m[1] + ":" + m[2] + ":" + m[3], normalize.CanonicalLevel(m[4])
	}
	if m := bracketTimeOnlyRe.FindStringSubmatch(line); m != nil {
		return m[1] + ":" + m[2] + ":" + m[3], ""
	}
	if m := bareTimeOnlyRe.FindStringSubmatch(line); m != nil {
		return m[1] + ":" + m[2] + ":" + m[3], ""
	}
	// 结构化日志：时刻形态千差万别，只取级别；时间由调用方的「上一条/采集时刻」兜底。
	if m := slogLevelRe.FindStringSubmatch(line); m != nil {
		return "", normalize.CanonicalLevel(m[1])
	}
	return "", ""
}
