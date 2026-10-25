package vlsup

import (
	"errors"
	"sort"
	"strings"
)

const diagnosticHiddenLine = "[诊断行或脱敏输出超过4096字节，已整体隐藏]"

var errDiagnosticRedactionLimit = errors.New("诊断脱敏输出达到字节上界")

// secretRedactor 在原输入上执行一次最长匹配；插入的掩码绝不会再次参与秘密扫描。
// 构造时去重并保留多行秘密的各个非空分量，短秘密不能简单忽略。
type secretRedactor struct{ replacer *strings.Replacer }

func newSecretRedactor(secrets []string) *secretRedactor {
	unique := map[string]struct{}{}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		unique[secret] = struct{}{}
		for _, part := range strings.FieldsFunc(secret, func(r rune) bool { return r == '\n' || r == '\r' }) {
			if part != "" {
				unique[part] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(unique))
	for secret := range unique {
		keys = append(keys, secret)
	}
	// 同起点优先最长秘密，不能让较短后缀先替换后泄露原本完整的较长秘密前缀。
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	pairs := make([]string, 0, 2*len(keys))
	for _, secret := range keys {
		pairs = append(pairs, secret, "[REDACTED]")
	}
	return &secretRedactor{replacer: strings.NewReplacer(pairs...)}
}

// boundedDiagnosticText 在字符串真正写入前检查空间，不先分配膨胀结果再事后隐藏。
type boundedDiagnosticText struct{ text strings.Builder }

func (w *boundedDiagnosticText) Write(p []byte) (int, error) {
	if len(p) > DiagnosticMaxLineBytes-w.text.Len() {
		return 0, errDiagnosticRedactionLimit
	}
	return w.text.Write(p)
}
func (w *boundedDiagnosticText) WriteString(s string) (int, error) {
	if len(s) > DiagnosticMaxLineBytes-w.text.Len() {
		return 0, errDiagnosticRedactionLimit
	}
	return w.text.WriteString(s)
}

func (r *secretRedactor) apply(text string) (string, bool) {
	if len(text) > DiagnosticMaxLineBytes {
		return diagnosticHiddenLine, true
	}
	var out boundedDiagnosticText
	if _, err := r.replacer.WriteString(&out, text); err != nil {
		return diagnosticHiddenLine, true
	}
	// 不合法的子进程字节不能流入结构化日志；规范化最多处理已被限制为4096字节的输出。
	clean := strings.ToValidUTF8(out.text.String(), "�")
	if len(clean) > DiagnosticMaxLineBytes {
		return diagnosticHiddenLine, true
	}
	return clean, false
}

func redact(text string, secrets []string) string {
	if len(text) > DiagnosticMaxLineBytes {
		return diagnosticHiddenLine
	}
	out, _ := newSecretRedactor(secrets).apply(text)
	return out
}
