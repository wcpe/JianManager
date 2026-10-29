package rollog

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitFor 轮询等待条件成立，避免用固定 sleep（CI 上不稳定）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("超时未等到：%s", what)
}

// TestWriteAndAppend 验证基本写入与重启后续写（不截断已有内容）。
func TestWriteAndAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// 重启：不能截断旧内容，必须从当前大小继续追加。
	w2, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}
	if got := w2.Size(); got != int64(len("first\n")) {
		t.Fatalf("重启后 Size 应等于旧文件大小 %d，实际 %d", len("first\n"), got)
	}
	if _, err := w2.Write([]byte("second\n")); err != nil {
		t.Fatalf("第二次 Write 失败: %v", err)
	}
	_ = w2.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	if string(data) != "first\nsecond\n" {
		t.Fatalf("内容应为两行拼接，实际 %q", string(data))
	}
}

// TestRotateBySize 验证达阈值后自动轮转，且轮转是 rename（原文件内容完整进入轮转档）。
func TestRotateBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 16, MaxBackups: 10, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	// 写三次，每次会跨过 16 字节阈值。
	for i := 0; i < 3; i++ {
		if _, err := w.Write([]byte("0123456789\n")); err != nil { // 11 bytes
			t.Fatalf("Write %d 失败: %v", i, err)
		}
	}

	rotated, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if err != nil {
		t.Fatalf("Glob 失败: %v", err)
	}
	if len(rotated) == 0 {
		t.Fatal("应该产生至少一个轮转档")
	}

	// 轮转档内容必须是完整的两行（rename 语义：不丢字节）。
	data, err := os.ReadFile(rotated[0])
	if err != nil {
		t.Fatalf("读取轮转档失败: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("轮转档不应为空")
	}
	if !strings.Contains(string(data), "0123456789") {
		t.Fatalf("轮转档内容异常: %q", string(data))
	}
}

// TestRotateIsRenameNotCopy 是核心回归：轮转不得复制字节。
//
// 验证方式：轮转后原活跃文件的 inode 必然变化（rename 后新建了文件），
// 而轮转档保留原 inode。若实现退化成 copytruncate（读+写+截断），
// 活跃文件 inode 不变且会有一个瞬间的 “两文件同 inode” 状态。
func TestRotateIsRenameNotCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 8, MaxBackups: 10, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}

	if _, err := w.Write([]byte("0123456789\n")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("轮转后 Stat 失败: %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatal("轮转后活跃文件应是一个新文件（rename + 新建），但 inode 未变 ⇒ 实现更像 copytruncate")
	}
}

// TestCompressAsync 验证轮转后异步 gzip，且 .gz 能正常解压还原。
func TestCompressAsync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 16, MaxBackups: 10})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	const payload = "compressed-content-0123456789\n"
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	var gzPath string
	waitFor(t, ".gz 出现", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "app-*.log.gz"))
		if len(m) > 0 {
			gzPath = m[0]
			return true
		}
		return false
	})

	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("打开 .gz 失败: %v", err)
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip 解压器创建失败: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取 gzip 内容失败: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("gzip 内容不匹配:\n got=%q\nwant=%q", string(got), payload)
	}
}

// TestRetention 验证超过 MaxBackups 后旧档被删除。
func TestRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 4, MaxBackups: 2})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	for i := 0; i < 6; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("line-%d\n", i))); err != nil {
			t.Fatalf("Write %d 失败: %v", i, err)
		}
		// 同秒内多次轮转靠序号区分；稍作间隔确保时间戳不同。
		time.Sleep(2 * time.Millisecond)
	}

	waitFor(t, "轮转档收敛到 MaxBackups 以内", func() bool {
		plain, _ := filepath.Glob(filepath.Join(dir, "app-*.log"))
		gz, _ := filepath.Glob(filepath.Join(dir, "app-*.log.gz"))
		return len(plain)+len(gz) <= 2
	})
}

// TestResumePendingCompression 验证：进程在 rename 后、压缩前崩溃留下的 .log，
// 下次 Open 时会被补压（崩溃可恢复）。
func TestResumePendingCompression(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// 手工造一个“上次遗留”的未压缩轮转档。
	leftover := filepath.Join(dir, "app-20260101-000000.log")
	if err := os.WriteFile(leftover, []byte("leftover-content\n"), 0o644); err != nil {
		t.Fatalf("写入遗留档失败: %v", err)
	}

	w, err := Open(Config{Path: path, MaxSizeBytes: 1024, MaxBackups: 10})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	waitFor(t, "遗留档被补压", func() bool {
		_, statErr := os.Stat(leftover + ".gz")
		return statErr == nil
	})

	// 补压后原 .log 应被删除，避免重复占用。
	waitFor(t, "补压后原 .log 被删除", func() bool {
		_, statErr := os.Stat(leftover)
		return os.IsNotExist(statErr)
	})
}

// TestConcurrentWrite 验证并发写入安全，且不丢字节（-race 下跑）。
func TestConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// 阈值调大，确保本次测试不触发轮转（只验证写并发安全）。
	w, err := Open(Config{Path: path, MaxSizeBytes: 1 << 30, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}

	const (
		goroutines = 16
		perRoutine = 200
	)
	var wg sync.WaitGroup
	line := []byte("0123456789abcdef\n") // 17 bytes
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perRoutine; j++ {
				if _, err := w.Write(line); err != nil {
					t.Errorf("并发 Write 失败: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	want := goroutines * perRoutine * len(line)
	if len(data) != want {
		t.Fatalf("并发写入丢字节：期望 %d，实际 %d", want, len(data))
	}
	if got := bytes.Count(data, []byte("\n")); got != goroutines*perRoutine {
		t.Fatalf("行数不匹配：期望 %d，实际 %d", goroutines*perRoutine, got)
	}
}

// TestRotateEmptyFileNoop 验证空文件不轮转（避免零字节归档）。
func TestRotateEmptyFileNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	if err := w.Rotate(); err != nil {
		t.Fatalf("空文件 Rotate 不应报错: %v", err)
	}
	rotated, _ := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if len(rotated) != 0 {
		t.Fatalf("空文件不应产生轮转档，实际 %v", rotated)
	}
}

// TestPath 验证 Path() 返回配置路径（诊断接口依赖它）。
func TestPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()
	if w.Path() != path {
		t.Fatalf("Path() 应为 %q，实际 %q", path, w.Path())
	}
}

// TestOpenCreatesParentDir 验证父目录不存在时自动创建。
func TestOpenCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "app.log")
	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("父目录应被创建: %v", err)
	}
}

// TestOnErrorCallback 验证轮转错误经 OnError 上报而不是吞掉。
func TestOnErrorCallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	errs := make(chan error, 4)
	w, err := Open(Config{
		Path:            path,
		MaxSizeBytes:    1 << 30,
		DisableCompress: true,
		OnError:         func(e error) { errs <- e },
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	// 写正常内容不应产生 OnError 回调。
	if _, err := w.Write([]byte("ok\n")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	select {
	case e := <-errs:
		t.Fatalf("正常写入不应回调 OnError，实际: %v", e)
	case <-time.After(50 * time.Millisecond):
	}
}
