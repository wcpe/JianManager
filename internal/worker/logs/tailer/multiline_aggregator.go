package tailer

// 本文件是多行聚合的**状态机本体**：把行流变成事件流。
//
// # 职责边界
//
// 输入：一行一行（已去掉换行符）。输出：已完结的事件正文。
// 不关心时间解析、不关心写哪里、不关心字符集。
//
// # 为什么不放在 Harvester 里
//
// Harvester 的职责是「从文件读到行」（对齐官方）。多行是**行到事件**的转换，
// 与“从哪里读”无关——stdio 桥接、gz 解压、将来可能有的其他输入都用同一套。
// 分开后两边都能单独测。

import "time"

// DefaultMultilineTimeout 是官方默认的 `multiline.timeout`。
//
// 为何必须有超时：多行事件只能靠「下一行不匹配」来闭合。源静默后若没有超时，
// 最后一条事件会一直挂在缓冲里不发出，而它的 offset 已经推进了——
// 那段时间的数据在查询侧就是“不存在”。
const DefaultMultilineTimeout = 5 * time.Second

// MultilineAggregator 把行流聚合成事件（对齐 Filebeat 的 multiline 处理）。
//
// # 为什么需要 nextPending（before 模式的缓冲）
//
// `match: before` 的语义是「匹配的行要前置到**后面那个不匹配的行**」。
// 也就是说当前行得先存着，等看到下一个“不归它”的行才能确认它完结。
// 所以 before 模式需要一个只有一行的待定缓冲，不能用普通的累积列表。
type MultilineAggregator struct {
	rule *Multiline

	// lines 是 `after` 模式的累积缓冲。
	lines []string
	// pending 是 `before` 模式的待定行（等下一行决定它属于谁）。
	pending    string
	hasPending bool

	// lastFlush 是最近一次收到行的时刻（用于超时冲刷）。
	lastAt time.Time
	// timeout 是 `multiline.timeout`。
	timeout time.Duration
	// dropped 是被 max_lines 丢弃的行数（供 /metrics 与排障）。
	dropped int64
}

// NewMultilineAggregator 创建聚合器。rule 为 nil 时退化为「每行都是独立事件」。
func NewMultilineAggregator(rule *Multiline, timeout time.Duration) *MultilineAggregator {
	if timeout <= 0 {
		timeout = DefaultMultilineTimeout
	}
	return &MultilineAggregator{rule: rule, timeout: timeout, lastAt: time.Now()}
}

// Dropped 返回因 max_lines 被丢弃的行数。
func (a *MultilineAggregator) Dropped() int64 {
	if a == nil {
		return 0
	}
	return a.dropped
}

// Add 推入一行，返回本次可能完结的事件正文（无完结时为 nil）。
//
// 返回值是 `[]string` 而不是单个：一次 Add 理论上最多完结一条。
// 用切片是为了将来支持 `flush_pattern` 在同一行上既结束上一条又开启下一条的场景。
func (a *MultilineAggregator) Add(line string) []string {
	if a == nil {
		return nil
	}
	a.lastAt = time.Now()
	if a.rule == nil {
		// 无多行规则：每行一条事件（与未启用多行时的行为一致）。
		return []string{line}
	}
	if a.rule.joinsForward() {
		return a.addForward(line)
	}
	return a.addBackward(line)
}

// addForward 处理 `match: after`：接续行并到前一行。
func (a *MultilineAggregator) addForward(line string) []string {
	// flush_pattern 优先：命中则本行**结束**当前事件（本行属于它，不是新事件的开头）。
	//
	// 官方描述：flush_pattern “specifies a regex, in which the current multiline will be
	// flushed from memory, ending the multiline-message”。
	// 换句话说它是**终止符**而不是分隔符——把当前行当作新事件的首行，就会把
	// `END` 这种收尾标记从事件里切出去，堆栈的上下文就断在最后一行了。
	if a.rule.flush != nil && a.rule.flush.MatchString(line) {
		if len(a.lines) > 0 {
			a.lines = append(a.lines, line)
			ev := joinMultiline(a.lines)
			a.lines = a.lines[:0]
			return []string{ev}
		}
		// 缓冲为空：本行自成一个事件。
		return []string{line}
	}
	if a.rule.ShouldFlushBefore(line) {
		// 本行开启新事件 ⇒ 先把上一个缓冲收尾。
		var out []string
		if len(a.lines) > 0 {
			out = append(out, joinMultiline(a.lines))
			a.lines = a.lines[:0]
		}
		a.lines = append(a.lines, line)
		return out
	}
	if a.rule.Truncated(len(a.lines)) {
		a.dropped++
		return nil
	}
	a.lines = append(a.lines, line)
	return nil
}

// addBackward 处理 `match: before`：接续行前置到后面那一行。
//
// 用一行待定缓冲实现：
//
//	接续行不断累积到 pending；遇到不接续的行 ⇒ pending 完结（并上那一行）
func (a *MultilineAggregator) addBackward(line string) []string {
	isCont := a.rule.isContinuation(line)
	if a.hasPending && !isCont {
		// 待定事件收尾：pending（所有接续行）+ 本行（不接续的那一行）。
		ev := a.pending
		if ev != "" {
			ev += "\n"
		}
		ev += line
		a.pending, a.hasPending = "", false
		return []string{ev}
	}
	if a.rule.flush != nil && a.rule.flush.MatchString(line) {
		var out []string
		if a.hasPending {
			out = append(out, a.pending)
		}
		out = append(out, line)
		a.pending, a.hasPending = "", false
		return out
	}
	if a.hasPending {
		if a.rule.Truncated(countLines(a.pending) + 1) {
			a.dropped++
			return nil
		}
		a.pending += "\n" + line
		return nil
	}
	a.pending, a.hasPending = line, true
	return nil
}

// Flush 把缓冲里剩下的行强行完结（超时、文件轮转、停止时调用）。
func (a *MultilineAggregator) Flush() []string {
	if a == nil {
		return nil
	}
	var out []string
	if len(a.lines) > 0 {
		out = append(out, joinMultiline(a.lines))
		a.lines = a.lines[:0]
	}
	if a.hasPending {
		out = append(out, a.pending)
		a.pending, a.hasPending = "", false
	}
	return out
}

// Due 报告距上次收到行是否已超过 multiline.timeout。
func (a *MultilineAggregator) Due(now time.Time) bool {
	if a == nil {
		return false
	}
	if !a.hasPending && len(a.lines) == 0 {
		return false
	}
	return now.Sub(a.lastAt) >= a.timeout
}

// countLines 数一个已拼接正文的行数（before 模式估算缓冲长度用）。
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}
