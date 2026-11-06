package tailer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCollectorResumesFromRegistryAcrossRestart 锤死「进程重启后从位点续读」。
//
// # 为什么单独一条
//
// TestHarvesterResumesFromOffset 测的是**手工传 offset** 给 NewHarvester；
// TestCollectorEndToEnd 测的是**同一个 Collector 实例**的连续读取。
// 两者都没验证「重启后从 registry 恢复位点」这条真实路径。
//
// # 为什么初始内容要超过 1024 字节
//
// 指纹只哈希前 1024 字节（对齐 Filebeat 默认）。文件不足 1024 字节时追加内容
// 会让指纹变化 ⇒ registry 命中不到 ⇒ 重读。这是已知局限（见 fingerprintOf 注释），
// 而生产的 MC 日志都是 MB 级，所以本用例用超过阈值的文件模拟真实场景。
//
// 变异验证：把 startOffsetFor 的 `return st.Offset` 改成 `return 0`，
// 本条必须转红（而 TestCollectorEndToEnd 不会红——它没重建 Collector）。
func TestCollectorResumesFromRegistryAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.jsonl")
	logPath := filepath.Join(dir, "latest.log")
	// 初始内容超过指纹阈值，保证追加后指纹稳定。
	initial := strings.Repeat("padding line to exceed fingerprint threshold\n", 40)
	if err := os.WriteFile(logPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := CollectorConfig{
		Paths:        []string{filepath.Join(dir, "*.log")},
		ReadFromHead: true,
	}
	ctx := context.Background()

	// ── 第一次“进程”：读完全部内容后落盘位点 ──
	{
		reg, err := OpenRegistry(regPath)
		if err != nil {
			t.Fatal(err)
		}
		sink := &collectSink{}
		c := NewCollector(cfg, reg, sink)
		if err := c.scanOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err := c.readOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if got := sink.snapshot(); len(got) != 40 {
			t.Fatalf("首轮应读 40 行，实际 %d 行", len(got))
		}
		if err := reg.Flush(); err != nil {
			t.Fatal(err)
		}
		c.shutdown()
	}

	// ── 进程重启前，应用又写了几行 ──
	appendTo(t, logPath, "c\nd\n")

	// ── 第二次“进程”：从 registry 恢复，只能读到新增的 c/d ──
	{
		reg, err := OpenRegistry(regPath)
		if err != nil {
			t.Fatal(err)
		}
		sink := &collectSink{}
		c := NewCollector(cfg, reg, sink)
		if err := c.scanOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err := c.readOnce(ctx); err != nil {
			t.Fatal(err)
		}
		got := sink.snapshot()
		want := []string{"c", "d"}
		if len(got) != len(want) {
			t.Fatalf("重启后应只读新增的 %v，实际 %v（重读=重复投递，漏读=丢数据）", want, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("第 %d 行：期望 %q 实际 %q", i, want[i], got[i])
			}
		}
		c.shutdown()
	}
}
