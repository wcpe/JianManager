package sampling

import (
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// 级别严重度序：数值越大越严重。级别别名的唯一真源仍是 normalize.CanonicalLevel
// （WARNING→WARN、SEVERE/FATAL→ERROR），本包不另立一套解析。
const (
	rankUnknown = -1
	rankTrace   = 10
	rankDebug   = 20
	rankInfo    = 30
	rankWarn    = 40
	rankError   = 50
)

// 规范级别名（降级到 error-only 时写进观测面）。
const (
	LevelNameError = "ERROR"
	LevelNameWarn  = "WARN"
)

// Rank 返回规范级别对应的严重度序。
//
// 无法判定的级别（空串 / 未识别 token）返回 rankUnknown。调用方必须把它当作
// 「不知道有多严重」处理，**一律不抑制**：本包的目标是压掉噪声，不是压掉看不清的东西。
// 把未知级别当低级别是危险默认值——真实日志里最常见的形态恰恰是「解析不出级别但其实是
// 异常堆栈头」。
func Rank(level string) int {
	switch normalize.CanonicalLevel(level) {
	case "TRACE":
		return rankTrace
	case "DEBUG":
		return rankDebug
	case "INFO":
		return rankInfo
	case "WARN":
		return rankWarn
	case "ERROR":
		return rankError
	default:
		return rankUnknown
	}
}

// LevelBelow 报告 level 是否严格低于 minLevel，即「按等级过滤应当被抑制」。
//
// 两个保守边界：
//   - 任一侧是 rankUnknown 就返回 false：minLevel 未识别时等于不启用等级过滤；
//     level 未识别时一律放行（理由见 Rank）。
//   - 相等返回 false（minLevel 语义是「保留该级别及以上」）。
func LevelBelow(level, minLevel string) bool {
	min := Rank(minLevel)
	if min == rankUnknown {
		return false
	}
	got := Rank(level)
	if got == rankUnknown {
		return false
	}
	return got < min
}

// NormalizeLevel 返回级别名的大写规范形态（写进汇总事件用）；未知返回空串。
func NormalizeLevel(level string) string {
	return strings.ToUpper(strings.TrimSpace(normalize.CanonicalLevel(level)))
}
