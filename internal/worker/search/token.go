package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenize 把一段文本切成小写词元集合，用于倒排索引。
//
// 词元定义：连续的字母/数字/下划线（按 Unicode 字母与数字，兼容含中日韩等的标识符），
// 全部小写化。标点、空白作为分隔。返回去重后的词元集合（倒排只需「该 token 是否出现」）。
//
// 注意：倒排只用于把搜索缩小到候选文件，真正的行号/片段由候选文件内的精确扫描得出，
// 故这里不保留位置信息、不做词干，保持索引小而快。
func tokenize(text string) map[string]struct{} {
	set := make(map[string]struct{})
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			set[b.String()] = struct{}{}
			b.Reset()
		}
	}
	for _, r := range text {
		if isTokenRune(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			flush()
		}
	}
	flush()
	return set
}

// queryTokens 把查询字符串切成词元切片（保序，供「候选必须包含全部 token」的交集判定）。
func queryTokens(q string) []string {
	seen := make(map[string]struct{})
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			tok := b.String()
			if _, ok := seen[tok]; !ok {
				seen[tok] = struct{}{}
				out = append(out, tok)
			}
			b.Reset()
		}
	}
	for _, r := range q {
		if isTokenRune(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			flush()
		}
	}
	flush()
	return out
}

// isTokenRune 判定一个 rune 是否属于词元字符（字母、数字、下划线）。
func isTokenRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// looksBinary 探测一段字节是否像二进制（含 NUL，或非法 UTF-8 占比过高）。
// 用于在扩展名规则之外兜底拦截二进制文件，避免污染倒排索引。
func looksBinary(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	// UTF-8 合法性：非法字节占比超过阈值视为二进制。
	invalid := 0
	total := 0
	s := sample
	for len(s) > 0 {
		r, size := utf8.DecodeRune(s)
		if r == utf8.RuneError && size == 1 {
			invalid++
		}
		total++
		s = s[size:]
	}
	return total > 0 && invalid*100/total > 30
}
