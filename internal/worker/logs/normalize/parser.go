package normalize

import (
	"regexp"
	"strings"
	"time"
)

// 行分类与级别归一：Java/MC 前缀 + Go slog level=，对齐 console-log-view 与 CP log_ingest 同源规则。
// 解析失败绝不发明 level。

var (
	// [HH:MM:SS] [thread/LEVEL]: body  — Paper/MC 原生
	javaThreadLevelRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})\]\s+\[([^\]/]*)/([A-Za-z]+)\]:\s*(.*)$`)
	// [HH:MM:SS LEVEL]: body
	javaTimeLevelRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})\s+([A-Za-z]+)\]:\s*(.*)$`)
	// [HH:MM:SS] body（无级别）
	javaTimeOnlyRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})\]\s+(.*)$`)

	// Go slog / logrus 结构化 level=（Beacon 等进程）；独立 token，可带引号。
	slogLevelRe = regexp.MustCompile(`(?i)(?:^|[\s])level=["']?(INFO|WARNING|WARN|ERROR|SEVERE|FATAL|DEBUG|TRACE)["']?(?:[\s]|$)`)
	slogTimeRe  = regexp.MustCompile(`(?:^|[\s])time=["']?([^"\s]+)["']?`)

	stackFrameRe   = regexp.MustCompile(`^\s+at\s+\S+|^\s+\.\.\.\s+\d+\s+more\s*$`)
	causedByRe     = regexp.MustCompile(`^\s*Caused by:`)
	suppressedRe   = regexp.MustCompile(`^\s*Suppressed:`)
	goPanicStackRe = regexp.MustCompile(`^(goroutine\s+\d+|panic:|\[signal )`)
	// Java 异常 FQCN 行（常无缩进，紧跟在日志头之后）。
	javaExcRe = regexp.MustCompile(`^[a-zA-Z_$][\w$]*(?:\.[\w$]+)*(?:Exception|Error|Throwable)\b`)
)

// CanonicalLevel 归一化显式解析到的级别别名；空输入返回空串，绝不发明。
// WARNING→WARN；SEVERE/FATAL→ERROR。其余非空 token 大写透传。
func CanonicalLevel(raw string) string {
	u := strings.ToUpper(strings.TrimSpace(raw))
	switch u {
	case "":
		return ""
	case "WARNING":
		return "WARN"
	case "SEVERE", "FATAL":
		return "ERROR"
	case "INFO", "WARN", "ERROR", "DEBUG", "TRACE":
		return u
	default:
		// 显式出现在行内的未知 token 保留（大写），仍不是“发明”。
		if len(u) <= 16 && isLevelToken(u) {
			return u
		}
		return ""
	}
}

func isLevelToken(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return len(s) >= 3
}

// lineKind 行分类结果。
type lineKind int

const (
	kindUnknown lineKind = iota
	kindEventStart
	kindContinuation
)

type parsedLine struct {
	kind      lineKind
	timeOfDay [3]int // H,M,S；hasClock=false 时无效
	hasClock  bool
	level     string // 已 CanonicalLevel
	thread    string
	body      string
	eventTime time.Time
	hasTime   bool
	slogRaw   bool
}

func parseLine(raw string, loc *time.Location, base time.Time) parsedLine {
	if loc == nil {
		loc = time.UTC
	}
	p := parsedLine{kind: kindUnknown, body: raw}

	// Continuation 优先：堆栈 / Caused by / Suppressed，即使内容里偶然出现 level= 也不开新事件。
	if looksLikeContinuation(raw) {
		p.kind = kindContinuation
		return p
	}

	if m := javaThreadLevelRe.FindStringSubmatch(raw); m != nil {
		p.kind = kindEventStart
		p.timeOfDay = [3]int{atoi2(m[1]), atoi2(m[2]), atoi2(m[3])}
		p.hasClock = true
		p.thread = m[4]
		p.level = CanonicalLevel(m[5])
		p.body = m[6]
		p.applyClock(loc, base)
		return p
	}
	if m := javaTimeLevelRe.FindStringSubmatch(raw); m != nil {
		p.kind = kindEventStart
		p.timeOfDay = [3]int{atoi2(m[1]), atoi2(m[2]), atoi2(m[3])}
		p.hasClock = true
		p.level = CanonicalLevel(m[4])
		p.body = m[5]
		p.applyClock(loc, base)
		return p
	}

	if m := slogLevelRe.FindStringSubmatch(raw); m != nil {
		p.kind = kindEventStart
		p.level = CanonicalLevel(m[1])
		p.slogRaw = true
		p.body = raw
		if tm := slogTimeRe.FindStringSubmatch(raw); tm != nil {
			if t, err := time.Parse(time.RFC3339Nano, tm[1]); err == nil {
				p.eventTime = t.UTC()
				p.hasTime = true
			} else if t, err := time.Parse(time.RFC3339, tm[1]); err == nil {
				p.eventTime = t.UTC()
				p.hasTime = true
			}
		}
		return p
	}

	if m := javaTimeOnlyRe.FindStringSubmatch(raw); m != nil {
		// 有时间戳但无显式级别：仍是新事件起点；level 留空，不发明。
		p.kind = kindEventStart
		p.timeOfDay = [3]int{atoi2(m[1]), atoi2(m[2]), atoi2(m[3])}
		p.hasClock = true
		p.body = m[4]
		p.applyClock(loc, base)
		return p
	}

	return p
}

func (p *parsedLine) applyClock(loc *time.Location, base time.Time) {
	if !p.hasClock {
		return
	}
	if base.IsZero() {
		base = time.Now()
	}
	b := base.In(loc)
	t := time.Date(b.Year(), b.Month(), b.Day(),
		p.timeOfDay[0], p.timeOfDay[1], p.timeOfDay[2], 0, loc)
	// 跨午夜：时刻相对 base 过远则回拨/前拨一天，不把时间“猜”到未来。
	if t.Sub(b) > 12*time.Hour {
		t = t.AddDate(0, 0, -1)
	} else if b.Sub(t) > 12*time.Hour {
		t = t.AddDate(0, 0, 1)
	}
	p.eventTime = t.UTC()
	p.hasTime = true
}

func looksLikeContinuation(raw string) bool {
	if raw == "" {
		return true
	}
	if stackFrameRe.MatchString(raw) || causedByRe.MatchString(raw) || suppressedRe.MatchString(raw) {
		return true
	}
	if goPanicStackRe.MatchString(raw) {
		return true
	}
	if javaExcRe.MatchString(raw) {
		return true
	}
	// 纯缩进续行（Java 异常消息折行等）
	if raw[0] == '\t' || strings.HasPrefix(raw, "    ") {
		return true
	}
	return false
}

func isEventStart(raw string) bool {
	return parseLine(raw, time.UTC, time.Time{}).kind == kindEventStart
}

func atoi2(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// extractSlogLevel 单独导出给测试：只做级别别名，不发明。
func extractSlogLevel(line string) string {
	m := slogLevelRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return CanonicalLevel(m[1])
}
