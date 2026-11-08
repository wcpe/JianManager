package logtail_test

// 本文件验证字符集解码已接进新采集器。
//
// # 为何必须钉住
//
// `log_ingest.charset` 这个配置项从旧链路存在至今。新采集器若不接它，
// 就会出现「配了 gbk 却不生效」——那类静默失效比报错难查得多，
// 因为它看起来“已经配好了”。
//
// # 现实验证据（2026-11-06）
//
// 生产 19 个实例的日志全部是合法 UTF-8，所以这不是紧急问题；
// 但配置面存在就必须真实生效，否则它会在下一次“中文变鬼”时变成嫌疑犯。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
)

// gbkLine 是一行 GBK 编码的 MC 日志（“中文测试”四个字的 GBK 字节）。
//
// 用字面字节而不是运行时转码：测试要验的正是「给定的这些字节被正确解释」，
// 若用 transform 先生成再断言，就把待测逻辑的一部分搬到测试里了。
var gbkLine = []byte("[18:00:01] [Server thread/INFO]: \xd6\xd0\xce\xc4\xb2\xe2\xca\xd4")

// TestSinkDecodesGBKWhenConfigured 验「配了 gbk 就会真的转码」。
func TestSinkDecodesGBKWhenConfigured(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "gbk-inst")
	logPath := filepath.Join(workDir, "logs", "latest.log")
	writeBytes(t, logPath, append(append([]byte{}, gbkLine...), '\n'))

	fake := newFakeVL(t)
	runner, err := logtail.Start(context.Background(), logtail.Config{
		Enabled:          true,
		ReadFromHead:     true,
		RegistryPath:     filepath.Join(root, "registry.jsonl"),
		ScanInterval:     30 * time.Millisecond,
		ProgressInterval: 30 * time.Millisecond,
		Bindings:         newBindings(t, workDir),
		Charset:          "gbk",
	}, fake.client(t), nil)
	if err != nil {
		t.Fatalf("启动: %v", err)
	}
	defer runner.Stop()

	if !waitFor(t, 8*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("未采到行，实际 %d", fake.count())
	}
	row := fake.snapshot()[0]
	if !contains(row["_msg"], "中文测试") {
		t.Fatalf("GBK 正文未被正确转码，实际 %q（应包含“中文测试”）", row["_msg"])
	}
	if row["source_charset"] != "gbk" {
		t.Errorf("发生转码时应当留 source_charset 标记，实际 %q", row["source_charset"])
	}
}

// TestSinkKeepsUTF8AndOmitsCharsetField 验 UTF-8 源不被误转码、也不写多余字段。
//
// # 为何要验「不写字段」
//
// 每行都写 source_charset=utf-8 会让这个字段变成纯噪音，
// 而它真正的价值是「用一条查询把被转码过的源捞出来」。
func TestSinkKeepsUTF8AndOmitsCharsetField(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "utf8-inst")
	logPath := filepath.Join(workDir, "logs", "latest.log")
	writeLines(t, logPath, `[18:00:01] [Server thread/INFO]: 中文测试`)

	fake := newFakeVL(t)
	runner, err := logtail.Start(context.Background(), logtail.Config{
		Enabled:          true,
		ReadFromHead:     true,
		RegistryPath:     filepath.Join(root, "registry.jsonl"),
		ScanInterval:     30 * time.Millisecond,
		ProgressInterval: 30 * time.Millisecond,
		Bindings:         newBindings(t, workDir),
		Charset:          "auto",
	}, fake.client(t), nil)
	if err != nil {
		t.Fatalf("启动: %v", err)
	}
	defer runner.Stop()

	if !waitFor(t, 8*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("未采到行，实际 %d", fake.count())
	}
	row := fake.snapshot()[0]
	if !contains(row["_msg"], "中文测试") {
		t.Fatalf("UTF-8 正文被破坏，实际 %q", row["_msg"])
	}
	if _, exists := row["source_charset"]; exists {
		t.Errorf("未发生转码时不应写 source_charset（否则该字段成为噪音），实际 %q", row["source_charset"])
	}
}

// writeBytes 写原始字节（用 append 一段换行）。
func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
