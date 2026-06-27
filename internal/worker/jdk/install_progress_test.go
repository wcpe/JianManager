package jdk

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// progressWriter 按 Content-Length 计算百分比并节流上报；末尾必到 100。
func TestProgressWriter_PercentAndThrottle(t *testing.T) {
	var got []int
	w := &progressWriter{total: 100, report: func(p int, _ string) { got = append(got, p) }}
	// 每写 1 字节触发一次，节流应只在 +3% 边界与 100% 上报。
	for i := 0; i < 100; i++ {
		if _, err := w.Write([]byte{0}); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) == 0 {
		t.Fatal("无任何进度上报")
	}
	if got[len(got)-1] != 100 {
		t.Fatalf("末次上报应为 100，得 %d", got[len(got)-1])
	}
	// 节流：100 次写入不应产生 100 次上报。
	if len(got) >= 100 {
		t.Fatalf("节流失效，上报 %d 次", len(got))
	}
}

// 无 Content-Length 时百分比保持 0，靠累计字节日志补充（这里数据小于 4MB，不强求有日志）。
func TestProgressWriter_NoContentLength(t *testing.T) {
	var maxPercent int
	w := &progressWriter{total: -1, report: func(p int, _ string) {
		if p > maxPercent {
			maxPercent = p
		}
	}}
	if _, err := w.Write(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if maxPercent != 0 {
		t.Fatalf("无 Content-Length 时百分比应保持 0，得 %d", maxPercent)
	}
}

// downloadAndExtractWithProgress：端到端从 httptest 下载 zip 归档、解压，且上报进度到 100。
func TestDownloadAndExtractWithProgress_EndToEnd(t *testing.T) {
	// 构造一个含 bin/java 的最小 zip（仅验证下载/解压/进度链路，不执行 java）。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("jdk-test/bin/java")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte("x"), 8192)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="jdk.zip"`)
		w.Header().Set("Content-Type", "application/zip")
		http.ServeContent(w, &http.Request{Method: http.MethodGet}, "jdk.zip", time.Time{}, bytes.NewReader(archive))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	var lastPercent int
	err = downloadAndExtractWithProgress(srv.Client(), srv.URL, dest, func(p int, _ string) {
		if p > lastPercent {
			lastPercent = p
		}
	})
	if err != nil {
		t.Fatalf("下载解压失败: %v", err)
	}
	if lastPercent != 100 {
		t.Fatalf("最终进度应为 100，得 %d", lastPercent)
	}
	if _, err := os.Stat(filepath.Join(dest, "jdk-test", "bin", "java")); err != nil {
		t.Fatalf("解压产物缺失: %v", err)
	}
}
