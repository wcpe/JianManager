package tailer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 统计 Flush 真正写盘的次数（用文件 mtime/inode 变化判断 rename 是否发生）。
func countFlushes(t *testing.T, path string, during func()) int {
	t.Helper()
	last := fileMtimeOrZero(path)
	n := 0
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				if cur := fileMtimeOrZero(path); cur != last && cur != 0 {
					last = cur
					n++
				}
			}
		}
	}()
	during()
	close(done)
	return n
}

func fileMtimeOrZero(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}

// TestNoWriteWhenNothingChanged 钉住「没有新日志就不写盘」。
//
// 背景：原来 collector 每轮对每个文件无条件 Put（含一个每轮都变的时间戳），
// 于是 registry 每个 progress_interval 就被整份重写一次——与有没有新日志无关。
// 生产实测 19 个文件里大部分分钟级无写入，却每 2 秒被完整重写。
func TestNoWriteWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "idle.log")
	os.WriteFile(logPath, []byte("only-line\n"), 0o644)

	regPath := filepath.Join(dir, "registry.jsonl")
	reg, err := OpenRegistry(regPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := CollectorConfig{
		Paths: []string{filepath.Join(dir, "*.log")}, ReadFromHead: true,
		ScanInterval: 100 * time.Millisecond, ProgressInterval: 100 * time.Millisecond,
	}
	c := NewCollector(cfg, reg, SinkFunc(func(context.Context, []Line) error { return nil }))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// 先让首轮把位点写出来
	time.Sleep(400 * time.Millisecond)

	// 文件不再变化：这段时间内不应有新的写盘
	writes := countFlushes(t, regPath, func() {
		time.Sleep(1200 * time.Millisecond) // 12 个 progress 周期
	})
	t.Logf("空闲 1.2s（约 12 个 progress 周期）内写盘次数 = %d", writes)
	if writes > 1 {
		t.Fatalf("空闲时不应反复写盘，实际 %d 次", writes)
	}
}

// TestWriteOnlyWhenOffsetAdvances 反向约束：有新内容时必须写盘（否则重启会重读）。
func TestWriteOnlyWhenOffsetAdvances(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "active.log")
	os.WriteFile(logPath, []byte("a\n"), 0o644)

	regPath := filepath.Join(dir, "registry.jsonl")
	reg, _ := OpenRegistry(regPath)
	cfg := CollectorConfig{
		Paths: []string{filepath.Join(dir, "*.log")}, ReadFromHead: true,
		ScanInterval: 100 * time.Millisecond, ProgressInterval: 100 * time.Millisecond,
	}
	c := NewCollector(cfg, reg, SinkFunc(func(context.Context, []Line) error { return nil }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	time.Sleep(400 * time.Millisecond)

	before := fileMtimeOrZero(regPath)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("b\nc\n")
	f.Close()

	deadline := time.Now().Add(2 * time.Second)
	changed := false
	for time.Now().Before(deadline) {
		if fileMtimeOrZero(regPath) != before {
			changed = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !changed {
		t.Fatal("有新内容时 registry 必须更新，否则重启会重读这段")
	}
	t.Logf("有新内容时 registry 已更新 ✓")
}
