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
	// [HH:MM:SS.mmm] [thread/LEVEL]: body — 上一形态的**带毫秒**版（logback/log4j2
	// 的 `%d{HH:mm:ss.SSS}` 与 `%d{HH:mm:ss,SSS}` 都会这么写）。
	javaThreadLevelMsRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})[,.](\d{1,3})\]\s+\[([^\]/]*)/([A-Za-z]+)\]:\s*(.*)$`)
	// [HH:MM:SS LEVEL]: body
	javaTimeLevelRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})\s+([A-Za-z]+)\]:\s*(.*)$`)
	// [HH:MM:SS.mmm LEVEL]: body — 带毫秒的同形态。
	javaTimeLevelMsRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})[,.](\d{1,3})\s+([A-Za-z]+)\]:\s*(.*)$`)
	// [HH:MM:SS] body（无级别）
	javaTimeOnlyRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})\]\s+(.*)$`)
	// [HH:MM:SS.mmm] body（无级别）—— 带毫秒的同形态。
	javaTimeOnlyMsRe = regexp.MustCompile(`^\[(\d{2}):(\d{2}):(\d{2})[,.](\d{1,3})\]\s+(.*)$`)
	// YYYY-MM-DD HH:MM:SS[,mmm] body — 裸日期形态（Log4j2 默认 `%d{yyyy-MM-dd HH:mm:ss}`、
	// 多数自研 Java/Go 文本日志）。日期是**显式**给出的，故不走 applyClock 的日期补全。
	//
	// 分隔符收 `空格` 与 `T`（Java LocalDateTime.toString() 会写 T），但**不收**时区后缀
	// （`+08:00` / `Z`）：带时区的 ISO 时间由 slogTimeRe 负责，本规则只处理"无时区、按源
	// 时区解释"的朴素日期时间。尾部 `(?:\\s+(.*))?` 允许只有时间戳的空消息记录。
	log4jDateTimeRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})(?:[,.](\d{1,3}))?(?:\s+(.*))?$`)
	// HH:MM:SS [LEVEL] body — BungeeCord 形态：时刻**不带方括号**，级别带。
	//
	// 2026-10-20 真机验收根因：上面三条 Java 形态都要求时刻被 `[]` 包住，而 BungeeCord 写的是
	// `18:12:59 [INFO] ...`。缺这条规则时该行匹配不到任何时间形态 ⇒ hasTime=false ⇒
	// EventTimeUTC 留空 ⇒ 落 normalizeCanonicalEvent 回退到 IngestTimeUTC（=采集时刻）。
	// 于是同一行每次重新导入都得到不同的 event_time_utc ⇒ CanonicalContentHash 不同；
	// 而 EventID 只由「来源+代次+区间」决定、不含内容 ⇒ 同一 event_id 多哈希、且「同区间
	// 等长」不可裁定 ⇒ 每次启动对账重新裁定并累计 IdentityConflicts ⇒ ConflictCount>0
	// ⇒ 查询降级 conflict + 前端禁用导出。
	// 生产实测：inst:318 的 eventstore 13.0 万条事件里只有 6.4 万个唯一 event_id，
	// 6.4 万个 ID 各有多份副本（时间分别是 10-15 的 06:19/06:42/08:05）。
	bungeeTimeLevelRe = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})\s+\[([A-Za-z]+)\]\s*(.*)$`)
	// HH:MM:SS,mmm [LEVEL] body — 上一层形态的**带毫秒**版（同样是时刻不带方括号）。
	//
	// 同一根因（见上）：毫秒形态此前一条都不匹配 ⇒ hasTime=false ⇒ 事件时间回退采集时刻
	// ⇒ 重导即得新哈希，而 EventID 与内容无关 ⇒ 同一 event_id 多哈希的身份冲突。
	// 之所以**新增独立规则**而不是把既有四条改写成「可选毫秒」：既有四条已被生产数据与
	// 回归测试钉死（尤其 javaThreadLevelRe 的 `[^\]/]*` 线程名字符类），改写成可选组会
	// 把贪婪/回溯优先级变成需要证明的等价性；而毫秒形态**不可能**匹配既有规则（既有规则
	// 都要求秒后紧跟 `]` 或空白，毫秒形态在秒后是 `,`/`.`），所以新增规则是严格加集：
	// 只把今天落空的行收进来，不改变任何既有格式的归属。
	bungeeTimeLevelMsRe = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})[,.](\d{1,3})\s+\[([A-Za-z]+)\]\s*(.*)$`)

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
	// subNano 秒内亚秒偏移（纳秒）。只有**毫秒形态**（`[,.]mmm`）会设置它；
	// 既有形态一律留 0 ⇒ applyClock 的取整结果与改动前逐位相同（零行为变化）。
	// 保留毫秒而不是截断到秒：事件时间要如实反映源时钟精度，否则同一秒内的多条
	// 记录会在时间轴上被压成同一时刻。
	subNano   int
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
	if m := javaThreadLevelMsRe.FindStringSubmatch(raw); m != nil {
		// 带毫秒的 `[HH:MM:SS.mmm] [thread/LEVEL]: body`：与上一条同构，只多一个
		// 小数秒捕获组（m[4]），故放在它后面、其余字段位置整体右移一位。
		p.kind = kindEventStart
		p.timeOfDay = [3]int{atoi2(m[1]), atoi2(m[2]), atoi2(m[3])}
		p.subNano = fracToNano(m[4])
		p.hasClock = true
		p.thread = m[5]
		p.level = CanonicalLevel(m[6])
		p.body = m[7]
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
	if m := javaTimeLevelMsRe.FindStringSubmatch(raw); m != nil {
		// 带毫秒的 `[HH:MM:SS.mmm LEVEL]: body`。
		p.kind = kindEventStart
		p.timeOfDay = [3]int{atoi2(m[1]), atoi2(m[2]), atoi2(m[3])}
		p.subNano = fracToNano(m[4])
		p.hasClock = true
		p.level = CanonicalLevel(m[5])
		p.body = m[6]
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
	if m := javaTimeOnlyMsRe.FindStringSubmatch(raw); m != nil {
		// 带毫秒的 `[HH:MM:SS.mmm] body`（无级别）。
		p.kind = kindEventStart
		p.timeOfDay = [3]int{atoi2(m[1]), atoi2(m[2]), atoi2(m[3])}
		p.subNano = fracToNano(m[4])
		p.hasClock = true
		p.body = m[5]
		p.applyClock(loc, base)
		return p
	}

	if m := bungeeTimeLevelRe.FindStringSubmatch(raw); m != nil {
		// BungeeCord：`18:12:59 [INFO] body`。与上面三条 Java 形态同级处理——
		// 拿到确定时刻（hasClock ⇒ applyClock 用 base 的日期补全并置 hasTime），
		// 从根上避免「回退到采集时刻」导致的哈希漂移（见 bungeeTimeLevelRe 的注释）。
		p.kind = kindEventStart
		p.level = CanonicalLevel(m[4])
		p.timeOfDay = [3]int{atoi2(m[1]), atoi2(m[2]), atoi2(m[3])}
		p.hasClock = true
		p.body = m[5]
		p.applyClock(loc, base)
		return p
	}
	if m := bungeeTimeLevelMsRe.FindStringSubmatch(raw); m != nil {
		// 带毫秒的 `18:12:59,123 [INFO] body`：同 bungeeTimeLevelRe，只多小数秒。
		p.kind = kindEventStart
		p.level = CanonicalLevel(m[5])
		p.timeOfDay = [3]int{atoi2(m[1]), atoi2(m[2]), atoi2(m[3])}
		p.subNano = fracToNano(m[4])
		p.hasClock = true
		p.body = m[6]
		p.applyClock(loc, base)
		return p
	}

	if m := log4jDateTimeRe.FindStringSubmatch(raw); m != nil {
		// 裸日期形态：日期是**显式**的，不能走 applyClock —— applyClock 会用 base 的
		// 年月日覆盖行内日期，并按「离 base 超过 12h 就 ±1 天」去猜午夜跨日；对显式
		// 日期那两步都是错的（会把 10-14 的行改写成 10-15）。故直接给出 eventTime。
		//
		// 该分支刻意排在 slog 之后：显式 `level=`（结构化 KV）仍优先归属 slog 分支，
		// 与 javaTimeOnlyRe 的既有优先级一致，不改变任何既有行的归属。
		if t, ok := explicitDateTime(m, loc); ok {
			p.kind = kindEventStart
			p.eventTime = t
			p.hasTime = true
			p.body = m[8]
			return p
		}
		// 形态像日期但分量越界（如 13 月）：不接管，保持本次改动前的 RAW 行为，
		// 绝不把 time.Date 归一化后的「另一个时刻」写进事件。
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
	// subNano 只有毫秒形态非零；其余形态为 0，日期构造与改动前完全一致。
	t := time.Date(b.Year(), b.Month(), b.Day(),
		p.timeOfDay[0], p.timeOfDay[1], p.timeOfDay[2], p.subNano, loc)
	// 跨午夜：时刻相对 base 过远则回拨/前拨一天，不把时间“猜”到未来。
	if t.Sub(b) > 12*time.Hour {
		t = t.AddDate(0, 0, -1)
	} else if b.Sub(t) > 12*time.Hour {
		t = t.AddDate(0, 0, 1)
	}
	p.eventTime = t.UTC()
	p.hasTime = true
}

// fracToNano 把 1~3 位小数秒（毫秒形态的捕获组）换算成纳秒。
//
// 按**小数秒**语义右补零：`.5` 是 500ms 而不是 5ms。log4j2 的 `%H:%M:%S,SSS` 恒为 3 位，
// 但部分实现/手工配置会裁掉尾部零（`.100` 写成 `.1`），右补零才能还原真实时刻。
func fracToNano(s string) int {
	v := atoi2(s)
	switch len(s) {
	case 1:
		v *= 100 // .5 → 500ms
	case 2:
		v *= 10 // .50 → 500ms
	}
	return v * int(time.Millisecond)
}

// explicitDateTime 由 log4jDateTimeRe 的捕获组构造「显式日期 + 时刻」的事件时间。
//
// 为什么直接 time.Date 而不用 applyClock：日期是行内给出的，补全与跨午夜回拨都必须是
// 恒等操作；一旦走 applyClock，行内 10-14 会被 base 的 10-15 覆盖。时间按源时区解释后
// 立即转 UTC，与 clock 形态的换算口径一致（都经 loc）。
//
// 越界分量（13 月、2 月 30 日、99 时）一律拒绝返回 false：time.Date 会把越界值静默
// 归一化成**另一个**合法时刻（2 月 30 日→3 月 2 日），那等于往事件里写一个错误时间，
// 比不解析更糟。拒绝后该行维持改动前的 RAW 行为。
func explicitDateTime(m []string, loc *time.Location) (time.Time, bool) {
	year, month, day := atoi2(m[1]), atoi2(m[2]), atoi2(m[3])
	hour, min, sec := atoi2(m[4]), atoi2(m[5]), atoi2(m[6])
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || min > 59 || sec > 59 {
		return time.Time{}, false
	}
	nanos := 0
	if m[7] != "" {
		nanos = fracToNano(m[7])
	}
	t := time.Date(year, time.Month(month), day, hour, min, sec, nanos, loc)
	// 日/月必须原样落位（拦下 2 月 30 日这类被归一化的日期）。
	if t.Day() != day || int(t.Month()) != month {
		return time.Time{}, false
	}
	return t.UTC(), true
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
