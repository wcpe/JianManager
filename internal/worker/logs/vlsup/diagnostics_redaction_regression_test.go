package vlsup

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// 合法短密码可重复进入秘密表，替换不能再次扫描已插入的掩码并指数膨胀。
func TestRuntimeRegressionDiagnosticRedactionNeverRescansMask(t *testing.T) {
	got := redact(strings.Repeat("E", DiagnosticMaxLineBytes), []string{"E", "E", "E", "E", "E"})
	if len(got) > DiagnosticMaxLineBytes {
		t.Fatalf("单行脱敏发生无界扩张: input=4096 output=%d", len(got))
	}
}

// 故意放置不可用的脱敏器，证明速率门拒绝的行不会触达文本处理。
func TestRuntimeRegressionDiagnosticRateGateRunsBeforeRedaction(t *testing.T) {
	d := newProcessDiagnostics("hot", &RecordingSink{}, []string{"E", "E", "E", "E", "E"})
	d.now = func() time.Time { return time.Unix(42, 0) }
	d.second, d.rate, d.redactor = 42, DiagnosticLinesPerSecond, nil
	d.record("stderr", strings.Repeat("E", DiagnosticMaxLineBytes), false)
	d.close()
	<-d.reported
	lines, dropped, truncated := d.snapshot()
	if len(lines) != 0 || dropped != 1 || truncated != 0 {
		t.Fatalf("已拒绝行不应再脱敏或产生脱敏截断: lines=%d dropped=%d truncated=%d", len(lines), dropped, truncated)
	}
}

func TestRuntimeRegressionDiagnosticRedactionShortOverlapUTF8AndEncoding(t *testing.T) {
	for _, secret := range []string{"E", "D"} {
		if got := redact(secret, []string{secret, secret}); got != "[REDACTED]" {
			t.Errorf("短秘密不能忽略或重扫掩码: secret=%q length=%d", secret, len(got))
		}
	}
	if got := redact("abc bc abc", []string{"bc", "abc", "abc"}); got != "[REDACTED] [REDACTED] [REDACTED]" {
		t.Errorf("重叠秘密必须按最长匹配一次替换: %q", got)
	}
	for _, fragment := range []string{"first", "second"} {
		if got := redact(fragment, []string{"first\nsecond"}); got != "[REDACTED]" {
			t.Errorf("多行秘密分量漏脱敏: %q", got)
		}
	}
	secrets := credentialSecrets("jm", "E/你+")
	text := strings.Join(secrets, " ")
	got := redact(text, secrets)
	for _, secret := range secrets {
		if strings.Contains(got, secret) {
			t.Errorf("特殊编码凭据未去除: length=%d", len(secret))
		}
	}
	for _, input := range []string{strings.Repeat("你", DiagnosticMaxLineBytes), string([]byte{0xff, 0xfe}) + "secret", strings.Repeat("x", DiagnosticMaxLineBytes)} {
		got := redact(input, []string{"secret"})
		if !utf8.ValidString(got) || len(got) > DiagnosticMaxLineBytes {
			t.Errorf("脱敏必须有界且有效UTF8: output=%d valid=%v", len(got), utf8.ValidString(got))
		}
	}
}
