package service

import (
	"regexp"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// structuredLevelRe 匹配结构化日志的 level 键（Go slog / logrus 等 key=value 形态）。
// 只认独立 `level=` token（可带单/双引号），值必须落在已知级别白名单内；
// 正文里偶然出现的 `状态=` / `level_token=` 不会被误认。
var structuredLevelRe = regexp.MustCompile(`(?i)(?:^|[\s])level=["']?(INFO|WARNING|WARN|ERROR|SEVERE|FATAL|DEBUG|TRACE)["']?(?:[\s]|$)`)

// inferStructuredLogLevel 从日志行内容提取结构化 level=；提取不到返回 false。
// 与前端 console-log-line 的 STRUCTURED_LEVEL 规则同源，避免控制台与日志中心显示不一致。
func inferStructuredLogLevel(line string) (model.LogLevel, bool) {
	m := structuredLevelRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	switch strings.ToUpper(m[1]) {
	case "DEBUG", "TRACE":
		// 模型无 TRACE 档，归入 debug（前端保留 TRACE 展示，入库侧收敛）。
		return model.LogLevelDebug, true
	case "WARN", "WARNING":
		return model.LogLevelWarn, true
	case "ERROR", "SEVERE", "FATAL":
		return model.LogLevelError, true
	case "INFO":
		return model.LogLevelInfo, true
	default:
		return "", false
	}
}

// instanceLineLogLevel 实例日志单行级别推断。
// 优先取内容里的结构化 level=；没有则按流兜底：stderr→error、stdout→info（既有约定）。
// 非 MC 进程（如 Beacon 的 slog 全量 stderr）据此不再被整库误标为错误。
func instanceLineLogLevel(stream, line string) model.LogLevel {
	if lv, ok := inferStructuredLogLevel(line); ok {
		return lv
	}
	if stream == "stderr" {
		return model.LogLevelError
	}
	return model.LogLevelInfo
}
