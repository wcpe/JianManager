package tailer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDroppedLinesVisible 钉住「max_lines 截断必须可观测」。
//
// 背景：max_lines（默认 500）超限时会丢行，这是必要的保护（一条事件无限增长
// 会吃光内存）。但原来 dropped 计数只存在于聚合器里，没有任何观测面读它——
// 运维在日志页看到断掉的堆栈，无法判断是"应用只打了这么多"还是"采集器丢了一半"。
// 用户的要求是「丢得明白」，所以这个计数必须能从 Collector 读出来。
func TestDroppedLinesVisible(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stack.log")

	// 构造：1 个事件头 + 600 行续行（超过默认 max_lines=500）
	var b []byte
	b = append(b, []byte("12:00:00 [INFO] header\n")...)
	for i := 0; i < 600; i++ {
		b = append(b, []byte(fmt.Sprintf("\tat frame%d()\n", i))...)
	}
	// 再来一个事件头收尾，确保上面的缓冲被 flush
	b = append(b, []byte("12:00:01 [INFO] next\n")...)
	os.WriteFile(p, b, 0o644)

	cfg := CollectorConfig{
		Paths: []string{filepath.Join(dir, "*.log")}, ReadFromHead: true,
		Multiline: func() *Multiline {
			m, err := NewMultiline(MultilineConfig{
				Pattern: `^\[?[0-9]{2}:[0-9]{2}:[0-9]{2}`, Negate: true, Match: MatchAfter,
			})
			if err != nil {
				t.Fatal(err)
			}
			return m
		}(),
		ScanInterval: 100 * time.Millisecond, ProgressInterval: 100 * time.Millisecond,
	}
	c := NewCollector(cfg, nil, SinkFunc(func(context.Context, []Line) error { return nil }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	time.Sleep(800 * time.Millisecond)

	got := c.DroppedLines()
	t.Logf("DroppedLines() = %d（600 行续行，max_lines=500 ⇒ 应约 100）", got)
	if got == 0 {
		t.Fatal("max_lines 超限丢弃必须能从 Collector 读到，否则丢失完全静默")
	}

	// 没启用多行时必须是 0（不能凭空造数）
	c2 := NewCollector(CollectorConfig{
		Paths: []string{filepath.Join(dir, "*.log")}, ReadFromHead: true,
		ScanInterval: time.Hour, ProgressInterval: time.Hour,
	}, nil, SinkFunc(func(context.Context, []Line) error { return nil }))
	if d := c2.DroppedLines(); d != 0 {
		t.Fatalf("未启用多行时 DroppedLines 应为 0，实际 %d", d)
	}
	t.Logf("未启用多行时 = 0 ✓")
}
