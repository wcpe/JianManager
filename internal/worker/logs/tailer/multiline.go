package tailer

// 本文件实现 Filebeat 的 multiline 语义（`parsers: - multiline:`）。
//
// # 为什么自己实现而不是复用旧 normalize
//
// 旧的 `normalize` 把「多行合并 + 时间解析 + 字符集 + 级别提取」锦在一起，
// 且与四水位账本 / 事件生命周期绑定。本层只需要**一个纯函数**：
// 给定一行，告诉调用方「它属于当前缓冲，还是开启新事件」。
// 把它与账本解耦后可以直接单测全部四种 match/negate 组合。
//
// # 官方语义（Filebeat multiline-examples，逐字照录）
//
// | negate | match  | 结果 |
// |--------|--------|------|
// | false  | after  | 连续匹配合的行**追加到前面不匹配的行**上 |
// | false  | before | 连续的匹配合行**前置到后面不匹配的行**上 |
// | true   | after  | **不匹配**的行追加到前面匹配的行上（Java 堆栈用这个）|
// | true   | before | 不匹配的行前置到后面匹配的行上 |
//
// 另外两个选项（同样照官方）：
//   - `flush_pattern`：命中则立即冲刷当前缓冲（结束这条多行事件）
//   - `max_lines`：一条事件最多合并多少行，默认 500，超出丢弃多出的行
//
// # 为何只实现 pattern 型
//
// 官方还有 `count` / `while_pattern` 两种。本项目的实际需求是 Java 堆栈（pattern 型），
// 而多实现一种就多一套需要验证的语义。留口子：需要时再加，不影响现有配置。

import (
	"fmt"
	"regexp"
	"strings"
)

// MultilineMatch 对应官方 `match` 字段。
type MultilineMatch string

const (
	// MatchAfter 对应 `match: after`（Logstash 里的 `previous`）。
	MatchAfter MultilineMatch = "after"
	// MatchBefore 对应 `match: before`（Logstash 里的 `next`）。
	MatchBefore MultilineMatch = "before"
)

// DefaultMaxLines 是官方默认的 `max_lines`。
//
// 为什么保留 500 这个数：一条多行事件的正常体量是几十行（堆栈），500 已经覆盖了
// 极端情况；再大只会让单个事件的正文大到影响查询（VL 单行有上限）。
const DefaultMaxLines = 500

// MultilineConfig 是一条多行规则的配置（对应官方 `parsers: - multiline:` 块）。
type MultilineConfig struct {
	// Pattern 是 `multiline.pattern`（RE2 语法，与官方一致：Go regexp）。
	Pattern string
	// Negate 对应 `multiline.negate`，默认 false。
	Negate bool
	// Match 对应 `multiline.match`，默认 after。
	Match MultilineMatch
	// FlushPattern 对应 `multiline.flush_pattern`（可选）。
	FlushPattern string
	// MaxLines 对应 `multiline.max_lines`，零值用 DefaultMaxLines。
	MaxLines int
}

// Multiline 是一条编译好的多行规则。
type Multiline struct {
	re       *regexp.Regexp
	flush    *regexp.Regexp
	negate   bool
	match    MultilineMatch
	maxLines int
}

// NewMultiline 编译多行规则。pattern 非法时返回错误（启动即拒）。
func NewMultiline(cfg MultilineConfig) (*Multiline, error) {
	m := &Multiline{negate: cfg.Negate, match: cfg.Match, maxLines: cfg.MaxLines}
	if m.match == "" {
		m.match = MatchAfter
	}
	if m.match != MatchAfter && m.match != MatchBefore {
		return nil, newConfigError("multiline.match 只能是 after 或 before（当前 %q）", string(m.match))
	}
	if m.maxLines <= 0 {
		m.maxLines = DefaultMaxLines
	}
	re, err := regexp.Compile(cfg.Pattern)
	if err != nil {
		return nil, newConfigError("multiline.pattern 非法: %v", err)
	}
	m.re = re
	if cfg.FlushPattern != "" {
		fre, ferr := regexp.Compile(cfg.FlushPattern)
		if ferr != nil {
			return nil, newConfigError("multiline.flush_pattern 非法: %v", ferr)
		}
		m.flush = fre
	}
	return m, nil
}

// joinsForward 报告方向：true = 后续行并入前面的行（after），false = 前置到后面的行（before）。
//
// 官方语义的一处容易搞错的地方：`negate` 只翻转**“哪些行是接续行”**，不翻转方向。
// 所以：
//
//	negate=false, match=after  ⇒ 匹配的行是接续行，并到前一行
//	negate=true,  match=after  ⇒ 不匹配的行是接续行，并到前一行
//
// 两者方向都是 after。这正是旧实现容易写错的地方。
func (m *Multiline) joinsForward() bool { return m.match == MatchAfter }

// isContinuation 报告本行是否应并入当前事件（已按 negate 翻转）。
func (m *Multiline) isContinuation(line string) bool {
	matched := m.re.MatchString(line)
	if m.negate {
		return !matched
	}
	return matched
}

// ShouldFlushBefore 报告本行是否应触发「先冲刷缓冲」。
//
// 只在 `match: after` 下有意义：新事件的**首行**到来时，前一个缓冲必须收尾。
// 对于 `match: before`，当前行还未确定归属（可能要等下一行），不能提前冲刷。
func (m *Multiline) ShouldFlushBefore(line string) bool {
	if m.flush != nil && m.flush.MatchString(line) {
		return true
	}
	if !m.joinsForward() {
		return false
	}
	return !m.isContinuation(line)
}

// MaxLines 返回生效的 max_lines。
// Pattern / Negate / Match 是只读访问器，**供启动日志如实打印生效规则**。
//
// 为什么需要：默认开启多行后，没配 pattern 的部署里原始配置是空串，
// 而实际跑的是默认规则。装配层若照原始配置打印，会出现
// 「已启用」与「pattern: (空)」并排的误导性日志（2026-11-07 部署时踩过）。
// 规则对象才是真源，故把它的取值暴露出来。
func (m *Multiline) Pattern() string {
	if m == nil || m.re == nil {
		return ""
	}
	return m.re.String()
}

// Negate 返回生效的 negate 设置。
func (m *Multiline) Negate() bool {
	if m == nil {
		return false
	}
	return m.negate
}

// Match 返回生效的 match 模式。
func (m *Multiline) Match() MultilineMatch {
	if m == nil {
		return ""
	}
	return m.match
}

func (m *Multiline) MaxLines() int { return m.maxLines }

// Truncated 在超过 maxLines 时被调用，返回是否应丢弃该行。
//
// 官方语义：`max_lines` 超出后**多出的行被丢弃**（不是截断当前事件）。
func (m *Multiline) Truncated(current int) bool { return current >= m.maxLines }

// newConfigError 返回配置错误（保持错误文案前缀可预测，便于启动日志比对）。
func newConfigError(format string, args ...any) error {
	return &configError{msg: fmt.Sprintf(format, args...)}
}

type configError struct{ msg string }

func (e *configError) Error() string { return "tailer: " + e.msg }

// joinMultiline 把多行正文拼成一条事件。
//
// 用 `\n` 分隔而不是空格：堆栈的可读性完全依赖于保留换行与缩进。
// （官方的 `skip_newline` 选项可以去掉分隔符，本项目没这个需求。）
func joinMultiline(lines []string) string {
	if len(lines) == 1 {
		return lines[0]
	}
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l)
	}
	return b.String()
}
