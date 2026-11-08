package tailer

// 本文件验证 gzip 读取的边界行为。
//
// # 为何要测这么多边界
//
// gz 归档是**一次性读完、读完就不再来**的输入：一旦读错，那段历史就永久缺了，
// 而因为没有 offset 可回退，也没办法“再试一次”。所以边界必须提前验。

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGz(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGzipReadAllLines 是最基本的一条：行被完整、按序读出。
func TestGzipReadAllLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs", "2026-09-20-10.log.gz")
	writeGz(t, p, "line1\nline2\nline3\n")

	g, err := OpenGzip(p)
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	defer g.Close()

	var got []string
	if err := g.ReadAll(func(line []byte) error {
		got = append(got, string(line))
		return nil
	}); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := []string{"line1", "line2", "line3"}
	if len(got) != len(want) {
		t.Fatalf("行数 = %d，期望 %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 行 = %q，期望 %q", i, got[i], want[i])
		}
	}
	if g.Lines() != 3 {
		t.Errorf("Lines() = %d，期望 3", g.Lines())
	}
}

// TestGzipLastLineWithoutNewline 验末尾无换行的最后一行不丢。
//
// # 为何这条重要
//
// 不少程序结尾不写换行。若只认「读到 \n 才算一行」，最后一行会被静默丢掉——
// 而它恰好常是“服务器已停止”这类关键行。
func TestGzipLastLineWithoutNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log.gz")
	writeGz(t, p, "first\nlast-no-newline")

	g, err := OpenGzip(p)
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	defer g.Close()

	var got []string
	if err := g.ReadAll(func(line []byte) error {
		got = append(got, string(line))
		return nil
	}); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 2 || got[1] != "last-no-newline" {
		t.Fatalf("末尾无换行的行被丢了：%v", got)
	}
}

// TestGzipStripsCRLF 验 Windows 换行里的 \r 被去掉。
//
// Java 经 log4j2 在 Windows 上写 CRLF；不去 \r 会让正文尾部永久带一个不可见字符。
func TestGzipStripsCRLF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "w.log.gz")
	writeGz(t, p, "win1\r\nwin2\r\n")

	g, err := OpenGzip(p)
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	defer g.Close()

	var got []string
	if err := g.ReadAll(func(line []byte) error {
		got = append(got, string(line))
		return nil
	}); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	for i, l := range got {
		if strings.HasSuffix(l, "\r") {
			t.Errorf("第 %d 行尾部仍有 \\r：%q", i, l)
		}
	}
}

// TestGzipRejectsNonGzipExtension 验「扩展名是 .gz 但内容不是」得到清晰错误。
//
// 运维手滑改名是真实场景。静默产出乱码比报错难查得多。
func TestGzipRejectsNonGzipExtension(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fake.log.gz")
	if err := os.WriteFile(p, []byte("this is plain text, not gzip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGzip(p); err == nil {
		t.Fatal("非 gzip 内容应当报错")
	} else if !strings.Contains(err.Error(), "魔数") {
		t.Errorf("错误信息应指出魔数不符，实际：%v", err)
	}
}

// TestGzipCorruptReturnsErrorButKeepsReadLines 验 checksum 损坏时的错误语义。
//
// # 为何这样定
//
// 官方：校验失败 ⇒ 记 error 但**当作已读完**。
// 我们不能因为一份损坏的归档无限重试（会卡住整个采集链），
// 但也不能把“已读出来的行”回退（那些字节确实是对的）。
func TestGzipCorruptReturnsErrorButKeepsReadLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "corrupt.log.gz")
	writeGz(t, p, strings.Repeat("good line\n", 200))

	// 把尾部（checksum 区）破坏掉，模拟传输截断/磁盘损坏。
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 12 {
		t.Fatalf("测试文件太小：%d 字节", len(raw))
	}
	raw[len(raw)-6] ^= 0xFF
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := OpenGzip(p)
	if err != nil {
		// 头部还完好，理应能打开；打不开也算可接受（同样报错不静默）。
		t.Logf("打开时就报错（可接受）: %v", err)
		return
	}
	defer g.Close()

	var lines int
	readErr := g.ReadAll(func(line []byte) error {
		lines++
		return nil
	})
	if readErr == nil {
		t.Fatal("损坏的 gzip 必须报错，不能静默当成读完")
	}
	t.Logf("损坏后读出错（符合预期）：%v；已读出 %d 行", readErr, lines)
}

// TestIsGzipPath 是扫描期筛选的判据。
func TestIsGzipPath(t *testing.T) {
	cases := map[string]bool{
		"/a/b/2026-09-20-10.log.gz": true,
		"/a/b/x.GZ":                 true,
		"/a/b/latest.log":           false,
		"/a/b/proxy.log.0":          false,
	}
	for p, want := range cases {
		if got := IsGzipPath(p); got != want {
			t.Errorf("IsGzipPath(%q) = %v，期望 %v", p, got, want)
		}
	}
}

// TestGzipCallbackErrorStops 验回调出错时立即停下（不把后续行白读一遍）。
func TestGzipCallbackErrorStops(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stop.log.gz")
	writeGz(t, p, "a\nb\nc\nd\n")

	g, err := OpenGzip(p)
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	defer g.Close()

	stop := os.ErrClosed
	var got int
	err = g.ReadAll(func(line []byte) error {
		got++
		if got == 2 {
			return stop
		}
		return nil
	})
	if err == nil {
		t.Fatal("回调出错时应返回该错误")
	}
	if got != 2 {
		t.Errorf("回调应在第 2 行停下，实际调用了 %d 次", got)
	}
}
