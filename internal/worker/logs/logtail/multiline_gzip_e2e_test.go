package logtail_test

// 本文件验证多行与 gz 经**真实 Collector + Runner** 的行为。
//
// # 为何要端到端再验一次
//
// 多行聚合器单测已验过语义，但“能不能真的接上采集链”是另一件事：
// offset 推进、批次切分、超时冲刷、gzip 去重——这些只在组装后才暴露。

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
	"github.com/wcpe/JianManager/internal/worker/logs/tailer"
)

// TestMultilineStacktraceMergedEndToEnd 验 Java 堆栈被合成**一条**事件。
func TestMultilineStacktraceMergedEndToEnd(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "ml")
	logPath := filepath.Join(workDir, "logs", "latest.log")
	writeLines(t, logPath,
		`[18:00:01] [Server thread/ERROR]: Could not pass event`,
		"\tat com.example.Book.getTitle(Book.java:16)",
		"\tat com.example.Bootstrap.main(Bootstrap.java:14)",
		`[18:00:02] [Server thread/INFO]: recovered`,
	)

	rule, err := tailer.NewMultiline(tailer.MultilineConfig{Pattern: `^\[`, Negate: true})
	if err != nil {
		t.Fatalf("编译多行: %v", err)
	}
	fake := newFakeVL(t)
	runner, err := logtail.Start(context.Background(), logtail.Config{
		Enabled:          true,
		ReadFromHead:     true,
		RegistryPath:     filepath.Join(root, "registry.jsonl"),
		ScanInterval:     30 * time.Millisecond,
		ProgressInterval: 30 * time.Millisecond,
		Bindings:         newBindings(t, workDir),
		Multiline:        rule,
		MultilineTimeout: 50 * time.Millisecond,
	}, fake.client(t), nil)
	if err != nil {
		t.Fatalf("启动: %v", err)
	}
	defer runner.Stop()

	// 4 行原文 ⇒ 2 条事件（1 条堆栈 + 1 条 INFO）。
	if !waitFor(t, 8*time.Second, func() bool { return fake.count() >= 2 }) {
		t.Fatalf("期望 2 条事件（堆栈已合并），实际 %d：%v", fake.count(), fake.snapshot())
	}
	time.Sleep(200 * time.Millisecond)
	rows := fake.snapshot()
	if len(rows) != 2 {
		t.Fatalf("事件数 = %d，期望 2（多行未生效或超时冲刷出错）：%v", len(rows), rows)
	}
	var stack string
	for _, r := range rows {
		if strings.Contains(r["_msg"], "Could not pass event") {
			stack = r["_msg"]
		}
	}
	if stack == "" {
		t.Fatalf("未找到堆栈事件：%v", rows)
	}
	if !strings.Contains(stack, "Book.java:16") || !strings.Contains(stack, "Bootstrap.java:14") {
		t.Errorf("堆栈行未并入事件：%q", stack)
	}
	if n := strings.Count(stack, "\n"); n != 2 {
		t.Errorf("堆栈事件应有 3 行（2 个换行），实际 %d：%q", n, stack)
	}
}

// TestGzipArchiveImportedEndToEnd 验历史 gz 归档真的被导入。
func TestGzipArchiveImportedEndToEnd(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "gz")
	logsDir := filepath.Join(workDir, "logs")
	writeLines(t, filepath.Join(logsDir, "latest.log"), `[18:00:01] [Server thread/INFO]: live`)

	// 造 2 份归档。
	for i, name := range []string{"2026-09-20-10.log.gz", "2026-09-20-11.log.gz"} {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write([]byte("[09:00:0" + string(rune('1'+i)) + "] [Server thread/INFO]: archived-" + name + "\n"))
		_ = zw.Close()
		if err := os.WriteFile(filepath.Join(logsDir, name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fake := newFakeVL(t)
	runner, err := logtail.Start(context.Background(), logtail.Config{
		Enabled:          true,
		ReadFromHead:     true,
		RegistryPath:     filepath.Join(root, "registry.jsonl"),
		ScanInterval:     30 * time.Millisecond,
		ProgressInterval: 30 * time.Millisecond,
		Bindings:         newBindings(t, workDir),
		IncludeGzip:      true,
	}, fake.client(t), nil)
	if err != nil {
		t.Fatalf("启动: %v", err)
	}
	defer runner.Stop()

	// 1 条 live + 2 条归档 = 3 条
	if !waitFor(t, 10*time.Second, func() bool { return fake.count() >= 3 }) {
		t.Fatalf("期望 3 条（live + 2 归档），实际 %d：%v", fake.count(), fake.snapshot())
	}
	time.Sleep(300 * time.Millisecond)
	rows := fake.snapshot()
	var archived int
	for _, r := range rows {
		if strings.Contains(r["_msg"], "archived-") {
			archived++
			if r["log_source_id"] != "inst:1/file" {
				t.Errorf("归档行的源标签错：%q", r["log_source_id"])
			}
		}
	}
	if archived != 2 {
		t.Fatalf("归档行数 = %d，期望 2（不重复）", archived)
	}
}

// TestGzipNotReadWhenDisabled 验默认不读 gz（用户明确要求实时不碰归档）。
func TestGzipNotReadWhenDisabled(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "gzoff")
	logsDir := filepath.Join(workDir, "logs")
	writeLines(t, filepath.Join(logsDir, "latest.log"), `[18:00:01] [Server thread/INFO]: live`)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte("[09:00:01] [Server thread/INFO]: should-not-appear\n"))
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(logsDir, "a.log.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := newFakeVL(t)
	runner, err := logtail.Start(context.Background(), logtail.Config{
		Enabled:          true,
		ReadFromHead:     true,
		RegistryPath:     filepath.Join(root, "registry.jsonl"),
		ScanInterval:     30 * time.Millisecond,
		ProgressInterval: 30 * time.Millisecond,
		Bindings:         newBindings(t, workDir),
		IncludeGzip:      false,
	}, fake.client(t), nil)
	if err != nil {
		t.Fatalf("启动: %v", err)
	}
	defer runner.Stop()

	if !waitFor(t, 8*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("live 行未采到")
	}
	time.Sleep(300 * time.Millisecond)
	for _, r := range fake.snapshot() {
		if strings.Contains(r["_msg"], "should-not-appear") {
			t.Fatalf("IncludeGzip=false 时不该读归档：%v", r)
		}
	}
}
