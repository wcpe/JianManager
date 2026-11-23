package vlsup

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// TestVLDiagnosticLevelClassifiesByOwnLevel 钉住「VL 诊断行的 slog 级别判定」。
//
// 回归背景（2026-10-31 生产缺陷）：VictoriaLogs 把所有级别的日志都写到 stderr，
// 而 supervisor 把这个流的每一行当失败上报。旧实现无条件用 `logger.Error`，
// 于是 VL 每次启动打的几十行 info（内存上限/版本/监听地址）全部变成 ERROR
// ⇒ 运维 `grep ERROR` 看不到真问题。
//
// **最关键的一条是「无级别字段必须当 Error」**：Go panic 栈与 segfault 信息
// 恰恰最需要 ERROR，不能因为解析不出来就降级。变异验证：把 default 改成
// slog.LevelInfo ⇒ 下面 panic/空行/纯文本三条会同时转红。
func TestVLDiagnosticLevelClassifiesByOwnLevel(t *testing.T) {
	cases := []struct {
		name string
		line string
		want slog.Level
	}{
		// 真实行形态：record() 会在行首加 "stderr: " / "stdout: " 前缀，
		// 级别字段在第 2 个 TAB 分隔段。带前缀/不带前缀都得认出来。
		{"真实 info 行（带 stream 前缀）", "stderr: 2026-10-31T07:53:00.041Z\tinfo\tVictoriaMetrics@x/memory.go:45\tlimiting caches", slog.LevelInfo},
		{"真实 info 行（无前缀）", "2026-10-31T07:53:00.041Z\tinfo\tVictoriaMetrics@x\tmsg", slog.LevelInfo},
		{"warn 行", "stderr: 2026-10-31T07:53:00Z\twarn\tx\tmsg", slog.LevelWarn},
		{"warning 同义", "stderr: 2026-10-31T07:53:00Z\twarning\tx\tmsg", slog.LevelWarn},
		{"error 行", "stderr: 2026-10-31T07:53:00Z\terror\tx\tmsg", slog.LevelError},
		{"fatal 行", "stderr: 2026-10-31T07:53:00Z\tfatal\tx\tmsg", slog.LevelError},
		{"panic 日志行", "stderr: 2026-10-31T07:53:00Z\tpanic\tx\tmsg", slog.LevelError},
		{"大写 INFO 也要认", "stderr: 2026-10-31T07:53:00Z\tINFO\tx\tmsg", slog.LevelInfo},
		// —— 以下四条是「解析不出级别 ⇒ 保持 Error」的保守方向 ——
		{"Go panic 栈（无级别）", "panic: runtime error: invalid memory address", slog.LevelError},
		{"纯文本无级别", "stderr: something went wrong", slog.LevelError},
		{"空行", "", slog.LevelError},
		{"段错误", "SIGSEGV: segmentation violation code=0x1", slog.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vlDiagnosticLevel(tc.line); got != tc.want {
				t.Fatalf("vlDiagnosticLevel(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestIndependentLoggerSinkUsesLineLevel 验证 sink 真的把判定结果用上了。
//
// 单测 vlDiagnosticLevel 只能证明映射对；这条证明**上报路径确实按它分级**。
// 变异：把 ReportFailure 里的 logger.Log(...) 改回 logger.Error(...) ⇒ 本条转红。
func TestIndependentLoggerSinkUsesLineLevel(t *testing.T) {
	var got slog.Level
	var saw bool
	h := &captureLevelHandler{onRecord: func(r slog.Record) { got, saw = r.Level, true }}
	sink := IndependentLoggerSink{Logger: slog.New(h)}

	sink.ReportFailure("hot", errString("stderr: 2026-10-31T07:53:00Z\tinfo\tx\tlimiting caches"))
	if !saw || got != slog.LevelInfo {
		t.Fatalf("VL 的 info 行必须以 Info 上报，实得 level=%v saw=%v", got, saw)
	}

	sink.ReportFailure("hot", errString("panic: boom"))
	if got != slog.LevelError {
		t.Fatalf("无级别行必须以 Error 上报，实得 %v", got)
	}
}

type captureLevelHandler struct{ onRecord func(slog.Record) }

func (h *captureLevelHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureLevelHandler) Handle(_ context.Context, r slog.Record) error {
	h.onRecord(r)
	return nil
}
func (h *captureLevelHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureLevelHandler) WithGroup(_ string) slog.Handler      { return h }

func errString(s string) error { return errors.New(s) }
