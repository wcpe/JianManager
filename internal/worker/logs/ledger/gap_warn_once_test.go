package ledger

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// 本文件钉住「缺口越界告警**每源只打一次**」。
//
// 背景（2026-10-27 真机事故）：原实现用
//
//	if len(indexes) == DefaultMaxUnresolvedGapsPerSource+1 { slog.Warn(...) }
//
// 表达「首次越界」。这个判据有两个毛病：
//
//  1. **不完整**：条数从 64 直接跳到 66（跳号）就永远命中不了 65，告警静默丢失；
//  2. **拦不住重复**：现场实测该条 WARN **44,640 条/小时**（10 秒 124 条），
//     6 个源同时刷，把日志推到 299 KB/s、单文件 36GB——而它本想消灭的
//     正是「日志量爆炸」这个问题本身。
//
// 现改为在 Entry 上记一次性标记。本文件把那两条语义分别钉住。

// captureWarnCount 捕获执行 fn 期间所有 WARN/INFO 日志的行数。
func captureWarnCount(t *testing.T, fn func()) int {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	h := slog.New(slog.NewTextHandler(syncWriter{&buf, &mu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(h)
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	mu.Lock()
	defer mu.Unlock()
	return strings.Count(buf.String(), "单源未解决缺口超过上限")
}

// syncWriter 让并发写入不破坏缓冲。
type syncWriter struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (w syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// buildOverflowEntry 造一个「同因未解决缺口数 = count」的账本条目。
func buildOverflowEntry(key SourceKey, count int) *Entry {
	e := &Entry{Key: key}
	for i := 0; i < count; i++ {
		start := uint64(i * 100)
		e.Gaps = append(e.Gaps, Gap{StartPos: start, EndPos: start + 9, Reason: GapReasonDeliverError})
	}
	return e
}

// TestFoldWarnHappensOncePerSource 是多轮折叠下只告一次的核心回归。
//
// 构造「反复越界」的真实形态：每次折叠后条数回到 64，再添几条又越界。
// 旧实现会在每一轮都打一条 WARN；新实现只在第一轮打。
func TestFoldWarnHappensOncePerSource(t *testing.T) {
	led := New()
	key := keyOf("fold-warn-once", "g1")
	e := buildOverflowEntry(key, DefaultMaxUnresolvedGapsPerSource+2)

	warns := captureWarnCount(t, func() {
		// 第一轮：越界 ⇒ 应告一次
		led.foldUnresolvedGapsLocked(e)
		// 后续 20 轮：每轮再添 2 条又越界 ⇒ 旧实现会再告 20 次
		for round := 0; round < 20; round++ {
			base := uint64(100000 + round*1000)
			e.Gaps = append(e.Gaps,
				Gap{StartPos: base, EndPos: base + 9, Reason: GapReasonDeliverError},
				Gap{StartPos: base + 20, EndPos: base + 29, Reason: GapReasonDeliverError})
			led.foldUnresolvedGapsLocked(e)
		}
	})

	require.Equal(t, 1, warns,
		"每源只能告一次（旧实现会在 21 轮里告 21 次，现场实测 44,640 条/小时）；实际 %d 次", warns)
	require.True(t, e.gapFoldWarned, "标记必须置位")
}

// TestFoldWarnedFlagSurvivesMultipleReasons 钉住「多个原因分组」不会各自告一次。
//
// 旧实现在每个 reason 桶里都可能命中 `== limit+1`，于是同一源按原因数重复告警。
// 新实现的一次性标记是 **entry 级**而不是 bucket 级。
func TestFoldWarnedFlagSurvivesMultipleReasons(t *testing.T) {
	led := New()
	key := keyOf("fold-warn-reasons", "g1")
	e := &Entry{Key: key}
	for i := 0; i < DefaultMaxUnresolvedGapsPerSource+2; i++ {
		start := uint64(i * 100)
		e.Gaps = append(e.Gaps,
			Gap{StartPos: start, EndPos: start + 9, Reason: GapReasonDeliverError},
			Gap{StartPos: start + 20, EndPos: start + 29, Reason: GapReasonStdioRawWriteFailed})
	}

	warns := captureWarnCount(t, func() { led.foldUnresolvedGapsLocked(e) })
	require.Equal(t, 1, warns, "两个原因桶越界，但也只能告一次；实际 %d 次", warns)
}

// TestFoldWarnedNotSetWhenNoOverflow 钉住「没越界就不置位」：
// 否则日后真的越界时反而不会告警（把提示吞掉）。
func TestFoldWarnedNotSetWhenNoOverflow(t *testing.T) {
	led := New()
	key := keyOf("fold-warn-nooverflow", "g1")
	e := buildOverflowEntry(key, DefaultMaxUnresolvedGapsPerSource-1)

	warns := captureWarnCount(t, func() { led.foldUnresolvedGapsLocked(e) })
	require.Zero(t, warns, "未越界不得告警")
	require.False(t, e.gapFoldWarned, "未越界不得置位标记")
}
