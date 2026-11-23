package vlsup

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// DiagnosticMaxLineBytes 防止单行故障输出导致内存无界；超长行整体隐藏。
	DiagnosticMaxLineBytes = 4096
	// DiagnosticMaxLines 限制每个进程诊断环形缓冲的保留条数。
	DiagnosticMaxLines = 64
	// DiagnosticLinesPerSecond 限制独立日志落点的每进程输出速率。
	DiagnosticLinesPerSecond = 16
)

// processDiagnostics 是独立有界环形落点，不通过VL写入/查询客户端。
// 两个流分别组行，确保跨Write秘密在输出前完整脱敏；超长行整体隐藏而非泄露截断前缀。
type processDiagnostics struct {
	mu       sync.Mutex
	redactor *secretRedactor
	// now仅用于确定性验证速率门先于脱敏，生产始终为time.Now。
	now                func() time.Time
	lines              []string
	dropped, truncated int
	second             int64
	rate               int
	namespace          string
	sink               Sink
	reports            chan string
	reported           chan struct{}
}

func newProcessDiagnostics(namespace string, sink Sink, secrets []string) *processDiagnostics {
	if sink == nil {
		sink = IndependentLoggerSink{}
	}
	d := &processDiagnostics{namespace: namespace, sink: sink, redactor: newSecretRedactor(secrets), now: time.Now, reports: make(chan string, DiagnosticMaxLines), reported: make(chan struct{})}
	go func() {
		defer close(d.reported)
		for text := range d.reports {
			reportViaSink(d.sink, false, d.namespace, fmt.Errorf("%s", text))
		}
	}()
	return d
}

func (d *processDiagnostics) record(stream, text string, truncated bool) {
	// 原始长度界与速率门先执行，已丢弃行不再构建或扫描脱敏输出。
	if len(text) > DiagnosticMaxLineBytes {
		truncated = true
	}
	// 锁序writer.mu→diagnostics.mu；常数计数，不在锁内扫描文本或调用sink。
	d.mu.Lock()
	if truncated {
		d.truncated++
	}
	second := d.now().Unix()
	if second != d.second {
		d.second = second
		d.rate = 0
	}
	if d.rate >= DiagnosticLinesPerSecond {
		d.dropped++
		d.mu.Unlock()
		return
	}
	d.rate++
	d.mu.Unlock()

	countedTruncation := truncated
	if truncated {
		text = diagnosticHiddenLine
	} else {
		text, truncated = d.redactor.apply(text)
	}
	// 脱敏后的有效UTF8只移除控制字符，终端转义或回车不能伪造诊断行。
	text = strings.Map(func(r rune) rune {
		if r < ' ' && r != '\t' {
			return ' '
		}
		return r
	}, text)
	if len(stream) > 16 {
		stream = "diagnostic"
	}
	text = stream + ": " + text
	if len(text) > DiagnosticMaxLineBytes {
		text = stream + ": " + diagnosticHiddenLine
		truncated = true
	}
	// 锁序writer.mu→diagnostics.mu；仅复制最多64条已有的有界文本，不执行外部IO。
	d.mu.Lock()
	if truncated && !countedTruncation {
		d.truncated++
	}
	if len(d.lines) == DiagnosticMaxLines {
		copy(d.lines, d.lines[1:])
		d.lines[len(d.lines)-1] = text
	} else {
		d.lines = append(d.lines, text)
	}
	select {
	case d.reports <- text:
	default:
		d.dropped++
	}
	d.mu.Unlock()
}

func (d *processDiagnostics) snapshot() ([]string, int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.lines...), d.dropped, d.truncated
}

func (d *processDiagnostics) close() { close(d.reports) }

// diagnosticWriter 的缓冲上界4096字节；持续排空的成本不随单行长度增长。
type diagnosticWriter struct {
	d         *processDiagnostics
	stream    string
	pending   []byte
	truncated bool
	mu        sync.Mutex
}

func (w *diagnosticWriter) Write(p []byte) (int, error) {
	// 锁序writer.mu→diagnostics.mu；每个写入块只做有限组行，不持锁执行外部IO。
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			w.flushLocked()
			continue
		}
		if !w.truncated {
			if len(w.pending) < DiagnosticMaxLineBytes {
				w.pending = append(w.pending, b)
			} else {
				w.pending = nil
				w.truncated = true
			}
		}
	}
	return len(p), nil
}
func (w *diagnosticWriter) flushLocked() {
	if len(w.pending) > 0 || w.truncated {
		w.d.record(w.stream, string(w.pending), w.truncated)
	}
	w.pending = w.pending[:0]
	w.truncated = false
}
func (w *diagnosticWriter) flush() { w.mu.Lock(); defer w.mu.Unlock(); w.flushLocked() }

// credentialSecrets 同时隐藏可逆的Basic编码和URL编码，避免以变形泄露本地管理凭据。
func credentialSecrets(user, password string) []string {
	if password == "" {
		return nil
	}
	return []string{password, url.QueryEscape(password), url.PathEscape(password), base64.StdEncoding.EncodeToString([]byte(user + ":" + password))}
}

func argumentSecrets(args, env []string) []string {
	var out []string
	var user, password string
	for i, a := range args {
		key, val, has := strings.Cut(a, "=")
		lower := strings.ToLower(key)
		value := val
		if !has && i+1 < len(args) {
			value = args[i+1]
		}
		if lower == "-httpauth.username" {
			user = value
		}
		if lower == "-httpauth.password" {
			password = value
		}
		if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "authkey") {
			if has {
				out = append(out, val)
			} else if i+1 < len(args) {
				out = append(out, args[i+1])
			}
		}
	}
	for _, a := range env {
		key, val, ok := strings.Cut(a, "=")
		key = strings.ToLower(key)
		if ok && (strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "token")) {
			out = append(out, val)
		}
	}
	return append(out, credentialSecrets(user, password)...)
}
