package jdk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stallServer 返回一个「发部分字节后永久挂起」的下载服务器，模拟下载流中途被掐
// （真机复现：经 SOCKS5 代理下载 temurin 21 卡 48%）。block 关闭后 handler 退出。
func stallServer(t *testing.T, block chan struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-block
	}))
	return srv
}

// TestDownload_StallCancelClassifiedAsNetwork 停滞看门狗掐断的下载 → 错误须含
// 停滞说明并归网络类（追加 FR-279 引导），而非裸 "context canceled"（FR-290）。
func TestDownload_StallCancelClassifiedAsNetwork(t *testing.T) {
	block := make(chan struct{})
	srv := stallServer(t, block)
	defer srv.Close()
	defer close(block) // LIFO：先解锁挂起的 handler，再 srv.Close()，避免 teardown 死锁

	err := downloadAndExtractStallParams(context.Background(), srv.Client(), srv.URL, t.TempDir(), nil,
		120*time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatal("停滞下载应失败")
	}
	if !strings.Contains(err.Error(), "下载停滞") {
		t.Fatalf("停滞掐断的错误应含「下载停滞」说明，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "疑似网络受限") {
		t.Fatalf("停滞掐断应归网络类（追加 FR-279 引导），实际: %v", err)
	}
}

// TestDownload_UserCancelNotMisclassified 用户主动取消（FR-227 强停，外部 ctx cancel）
// → 不得误标为网络受限、不得说成下载停滞（FR-290）。
func TestDownload_UserCancelNotMisclassified(t *testing.T) {
	block := make(chan struct{})
	srv := stallServer(t, block)
	defer srv.Close()
	defer close(block) // LIFO：先解锁挂起的 handler，再 srv.Close()，避免 teardown 死锁

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	err := downloadAndExtractStallParams(ctx, srv.Client(), srv.URL, t.TempDir(), nil,
		10*time.Second, 20*time.Millisecond)
	if err == nil {
		t.Fatal("被取消的下载应失败")
	}
	if strings.Contains(err.Error(), "疑似网络受限") {
		t.Fatalf("用户主动取消不应误标网络受限，实际: %v", err)
	}
	if strings.Contains(err.Error(), "下载停滞") {
		t.Fatalf("用户主动取消不应说成下载停滞，实际: %v", err)
	}
}
