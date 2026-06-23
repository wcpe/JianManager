package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// sha256Hex 返回字节切片的十六进制 sha256（测试辅助）。
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestDownload_ChecksumMatch(t *testing.T) {
	payload := []byte("fake-binary-content-v2")
	want := sha256Hex(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "downloaded.bin")
	// httptest 是 http://，故须 allowInsecure=true。
	if err := Download(context.Background(), srv.URL, want, dest, true); err != nil {
		t.Fatalf("Download 期望成功，实得 %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("读取下载文件失败: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("下载内容不符: 期望 %q 实得 %q", payload, got)
	}
}

func TestDownload_ChecksumMismatch(t *testing.T) {
	payload := []byte("actual-content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "downloaded.bin")
	wrongSHA := sha256Hex([]byte("something-else"))
	err := Download(context.Background(), srv.URL, wrongSHA, dest, true)
	if err == nil {
		t.Fatal("校验不符时 Download 应返回错误")
	}
	// 校验失败必须删除已下载文件，绝不留下半截/未校验的二进制。
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("校验失败后下载文件应被删除，实际仍存在: %v", statErr)
	}
}

func TestDownload_RejectsInsecureURL(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "x.bin")
	err := Download(context.Background(), "http://example.com/bin", "", dest, false)
	if err == nil {
		t.Fatal("非 https 且未允许时应拒绝")
	}
}

func TestDownload_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "x.bin")
	if err := Download(context.Background(), srv.URL, "", dest, true); err == nil {
		t.Fatal("HTTP 404 应返回错误")
	}
}

func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	content := []byte("hello world")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != sha256Hex(content) {
		t.Fatalf("FileSHA256 不符: 期望 %s 实得 %s", sha256Hex(content), got)
	}
}

func TestReplaceExecutable(t *testing.T) {
	dir := t.TempDir()
	// 用同目录两个文件模拟「当前二进制」与「下载的新二进制」，验证替换后内容确为新版本。
	target := filepath.Join(dir, "app")
	newBin := filepath.Join(dir, "app.new")
	if err := os.WriteFile(target, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBin, []byte("v2-new"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceExecutable(target, newBin); err != nil {
		t.Fatalf("ReplaceExecutable 失败: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2-new" {
		t.Fatalf("替换后目标内容应为新版本，实得 %q", got)
	}
	// 新二进制源文件应已被 rename 走（不再存在于原位）。
	if _, statErr := os.Stat(newBin); !os.IsNotExist(statErr) {
		t.Fatalf("替换后新二进制源文件应已被移走，仍存在: %v", statErr)
	}
}
