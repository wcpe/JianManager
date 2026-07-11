package jdk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestIsNetworkError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"TLS 握手超时", errors.New(`Get "https://x": net/http: TLS handshake timeout`), true},
		{"i/o timeout", fmt.Errorf("dial: %w", errors.New("read tcp: i/o timeout")), true},
		{"连接被拒", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), true},
		{"DNS 失败", &net.DNSError{Err: "no such host", Name: "x"}, true},
		{"context 超时", context.DeadlineExceeded, true},
		{"强制停止取消", context.Canceled, true},
		{"归档损坏(本地类)", errors.New("已下载但未找到 bin/java，JDK 可能不完整"), false},
		{"目标已存在(本地类)", errors.New("目标目录已存在: /x"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isNetworkError(c.err); got != c.want {
				t.Fatalf("isNetworkError(%v)=%v want %v", c.err, got, c.want)
			}
		})
	}
}

func TestAnnotateDownloadError(t *testing.T) {
	// 网络类：追加引导，且保留底层英文标记供前端识别。
	net := annotateDownloadError(errors.New(`下载失败: Get "https://github.com/...": net/http: TLS handshake timeout`))
	if !strings.Contains(net.Error(), "出站代理") || !strings.Contains(net.Error(), "运行时资产") {
		t.Fatalf("网络类错误应含代理/镜像引导: %v", net)
	}
	if !strings.Contains(strings.ToLower(net.Error()), "tls handshake timeout") {
		t.Fatalf("应保留底层英文标记供前端识别: %v", net)
	}
	// 本地类：原样不加引导。
	local := errors.New("已下载但未找到 bin/java")
	if got := annotateDownloadError(local); got.Error() != local.Error() {
		t.Fatalf("本地类错误不应被改写: %v", got)
	}
	if annotateDownloadError(nil) != nil {
		t.Fatal("nil 应返回 nil")
	}
}
