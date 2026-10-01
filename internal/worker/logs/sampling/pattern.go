package sampling

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
)

// patternPreviewChars 是模式预览的截断长度：预览进汇总事件正文，太长会把
// 一条汇总撑成和原文一样重，采样也就失去了意义（成本治理的初衷）。
const patternPreviewChars = 120

// volatile 掩码：把「同一条消息的每次出现都不同」的部分替换为占位符，否则
// 「同源同消息高频抑制」永远匹配不到——现实里的刷屏正是「同一模板 + 变化的数字」。
//
// 顺序敏感：UUID / IPv4 必须早于十六进制与数字掩码，否则会被拆成碎片。
var (
	uuidRe = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	ipv4Re = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	hexRe  = regexp.MustCompile(`(?i)\b(?:0x)?[0-9a-f]{8,}\b`)
	// traceId/spanId 等常见的「前缀 + 长串」形态。
	kvHexRe = regexp.MustCompile(`(?i)\b(trace|span|request|correlation)[_-]?id["'=:\s]+[0-9a-f]{6,}\b`)
	digitRe = regexp.MustCompile(`\d+`)
	wsRe    = regexp.MustCompile(`\s+`)
)

// Signature 是一条事件的「同源同消息」分组签名。
//
// 级别与流必须参与分组：跨级别的行不得混入同一汇总（否则汇总的级别字段必然撒谎，
// 而且按级别的保留期就无法对汇总事件成立）。
type Signature struct {
	// Level 规范级别名（未知为空串）。
	Level string
	// Stream stdout|stderr。
	Stream string
	// Pattern 掩码后的模式预览（已截断，供观测与正文使用）。
	Pattern string
	// Key 分组键：level + stream + 模式哈希。哈希而非全量模式，是为了让模式表
	// 的每条记录大小有界（模式本身可能很长）。
	Key string
}

// SignatureOf 由事件的级别、流、正文推出分组签名。
func SignatureOf(level, stream, message string) Signature {
	pat := MaskMessage(message)
	h := fnv.New64a()
	_, _ = h.Write([]byte(pat))
	return Signature{
		Level:   NormalizeLevel(level),
		Stream:  strings.TrimSpace(stream),
		Pattern: pat,
		Key:     NormalizeLevel(level) + "\x00" + strings.TrimSpace(stream) + "\x00" + strconv.FormatUint(h.Sum64(), 16),
	}
}

// MaskMessage 把消息里的易变部分掩码成稳定模式，并截断到预览长度。
//
// 纯函数、无状态：同一个消息必然得到同一个模式（确定性要求见 doc.go）。
func MaskMessage(message string) string {
	s := message
	if m := kvHexRe.FindStringSubmatch(s); m != nil {
		s = kvHexRe.ReplaceAllString(s, m[1]+"id=<id>")
	}
	s = uuidRe.ReplaceAllString(s, "<uuid>")
	s = ipv4Re.ReplaceAllString(s, "<ip>")
	s = hexRe.ReplaceAllString(s, "<hex>")
	s = digitRe.ReplaceAllString(s, "#")
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, patternPreviewChars)
}

// TruncateRunes 按**字符**（而非字节）截断，避免把多字节字符切成两半留下非法 UTF-8。
// 与 agent_call_log 的截断口径一致：按字节截断在 MySQL utf8mb4 严格模式下会整行拒收，
// 「截断」这个兜底自己就失效了。
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len([]byte(s)) <= max && len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// Truncated 报告按 max 字符截断是否会丢失内容（供汇总事件如实标注）。
func Truncated(s string, max int) bool {
	if max <= 0 {
		return s != ""
	}
	return len([]rune(s)) > max
}

// describeSignature 供观测与错误信息使用的可读描述。
func describeSignature(sig Signature) string {
	lvl := sig.Level
	if lvl == "" {
		lvl = "UNKNOWN"
	}
	return fmt.Sprintf("%s/%s/%s", lvl, sig.Stream, sig.Pattern)
}
