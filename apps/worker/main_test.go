package main

import (
	"net"
	"strconv"
	"testing"
)

func TestLocalWSAddrOnlyUsesLoopback(t *testing.T) {
	listener, err := net.Listen("tcp", localWSAddr(0))
	if err != nil {
		t.Fatalf("监听本机 WebSocket 地址失败: %v", err)
	}
	defer listener.Close()

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("解析监听地址失败: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("Worker WebSocket 必须仅监听 IPv4 回环地址，实际为 %q", host)
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("监听端口无效: %q", port)
	}
}
