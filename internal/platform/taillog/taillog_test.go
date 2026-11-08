package taillog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadBasic 验证基本尾部读取。
func TestReadBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Read(path, 0, 0)
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if res.Content != "line1\nline2\nline3\n" {
		t.Fatalf("内容不匹配: %q", res.Content)
	}
	if res.Truncated {
		t.Fatal("小文件不应标记截断")
	}
	if res.FileBytes != 18 {
		t.Fatalf("FileBytes 应为 18，实际 %d", res.FileBytes)
	}
	if res.Path != path {
		t.Fatalf("Path 应为 %q，实际 %q", path, res.Path)
	}
}

// TestReadMaxLines 验证行数限制与截断标记。
func TestReadMaxLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line-%03d\n", i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Read(path, 10, 0)
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if !res.Truncated {
		t.Fatal("限制 10 行时应标记截断")
	}
	lines := strings.Split(strings.TrimSuffix(res.Content, "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("应返回 10 行，实际 %d", len(lines))
	}
	// 必须是最新的 10 行（尾部），不是头部。
	if lines[len(lines)-1] != "line-099" {
		t.Fatalf("最后一行应为 line-099，实际 %q", lines[len(lines)-1])
	}
}

// TestReadMaxBytesIndependentOfFileSize 是核心回归：读取耗时与文件大小无关。
//
// 事故现场日志可达 35GB（worker.out 真实案例）。本测试造一个远大于 maxBytes 的文件，
// 断言返回内容长度受 maxBytes 约束、且 Truncated 为真——说明只读了尾部有界区间。
func TestReadMaxBytesIndependentOfFileSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// 写 4MB（远超 64KB 默认上限），最后留一行可识别的标记。
	chunk := []byte(strings.Repeat("x", 1023) + "\n")
	for i := 0; i < 4096; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.WriteString("TAIL-MARKER\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	res, err := Read(path, 0, 0)
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if !res.Truncated {
		t.Fatal("大文件必须标记截断")
	}
	if len(res.Content) > DefaultMaxBytes {
		t.Fatalf("返回内容 %d 字节超过上限 %d", len(res.Content), DefaultMaxBytes)
	}
	if !strings.Contains(res.Content, "TAIL-MARKER") {
		t.Fatal("必须包含文件末尾的标记行")
	}
}

// TestReadClampsOversizedRequest 验证超限的请求被夹到硬上限，不是报错。
func TestReadClampsOversizedRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 传一个荒谬的大值：不报错，只是被夹紧。
	if _, err := Read(path, 999999, 999999999); err != nil {
		t.Fatalf("超限请求应被夹紧而非报错: %v", err)
	}
}

// TestReadMissingFile 验证文件不存在的错误可识别。
func TestReadMissingFile(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "nope.log"), 0, 0)
	if err == nil {
		t.Fatal("不存在的文件应报错")
	}
	if !errors.Is(err, ErrFileMissing) {
		t.Fatalf("错误应可被 ErrFileMissing 识别，实际 %v", err)
	}
}

// TestReadEmptyFile 验证空文件返回空内容且不报错。
func TestReadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Read(path, 0, 0)
	if err != nil {
		t.Fatalf("空文件不应报错: %v", err)
	}
	if res.Content != "" {
		t.Fatalf("空文件内容应为空，实际 %q", res.Content)
	}
	if res.FileBytes != 0 {
		t.Fatalf("FileBytes 应为 0，实际 %d", res.FileBytes)
	}
}

// TestReadEmptyPath 验证空路径被拒。
func TestReadEmptyPath(t *testing.T) {
	if _, err := Read("", 0, 0); err == nil {
		t.Fatal("空路径应报错")
	}
}
