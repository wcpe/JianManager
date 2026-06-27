package httpclient

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// transportOf 取出 client 的 *http.Transport（工厂必产出该类型，否则测试失败）。
func transportOf(t *testing.T, c *http.Client) *http.Transport {
	t.Helper()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("期望 *http.Transport，实得 %T", c.Transport)
	}
	return tr
}

// proxyForURL 调用 transport.Proxy 解析给定目标 URL 应使用的代理（nil=直连）。
func proxyForURL(t *testing.T, tr *http.Transport, rawURL string) *url.URL {
	t.Helper()
	if tr.Proxy == nil {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	u, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy 解析失败: %v", err)
	}
	return u
}

// TestNew_EmptyConfig_UsesProxyFromEnvironment 空配时应等价 ProxyFromEnvironment（尊重 env 代理）。
func TestNew_EmptyConfig_UsesProxyFromEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy.example:3128")
	t.Setenv("NO_PROXY", "")

	c, err := New(Config{})
	if err != nil {
		t.Fatalf("空配应成功构造 client: %v", err)
	}
	tr := transportOf(t, c)
	got := proxyForURL(t, tr, "http://target.example/file")
	if got == nil || got.Host != "env-proxy.example:3128" {
		t.Fatalf("空配应回退环境代理，实得 %v", got)
	}
}

// TestNew_HTTPProxy_Used http(s) 代理应被 Transport.Proxy 固定返回。
func TestNew_HTTPProxy_Used(t *testing.T) {
	c, err := New(Config{URL: "http://cfg-proxy.example:8080"})
	if err != nil {
		t.Fatalf("http 代理应成功构造: %v", err)
	}
	tr := transportOf(t, c)
	got := proxyForURL(t, tr, "https://target.example/file")
	if got == nil || got.Scheme != "http" || got.Host != "cfg-proxy.example:8080" {
		t.Fatalf("应使用配置的 http 代理，实得 %v", got)
	}
}

// TestNew_HTTPSProxy_Used https scheme 的代理同样被使用。
func TestNew_HTTPSProxy_Used(t *testing.T) {
	c, err := New(Config{URL: "https://secure-proxy.example:8443"})
	if err != nil {
		t.Fatalf("https 代理应成功构造: %v", err)
	}
	tr := transportOf(t, c)
	got := proxyForURL(t, tr, "https://target.example/file")
	if got == nil || got.Scheme != "https" || got.Host != "secure-proxy.example:8443" {
		t.Fatalf("应使用配置的 https 代理，实得 %v", got)
	}
}

// TestNew_NoProxy_HTTPDirect no_proxy 命中的主机应走直连（Proxy 返回 nil）。
func TestNew_NoProxy_HTTPDirect(t *testing.T) {
	c, err := New(Config{URL: "http://cfg-proxy.example:8080", NoProxy: "internal.example,10.0.0.0/8"})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	tr := transportOf(t, c)

	// 命中 no_proxy 域 → 直连。
	if got := proxyForURL(t, tr, "http://internal.example/file"); got != nil {
		t.Fatalf("no_proxy 命中应直连，实得代理 %v", got)
	}
	// 命中 no_proxy CIDR → 直连。
	if got := proxyForURL(t, tr, "http://10.1.2.3/file"); got != nil {
		t.Fatalf("no_proxy CIDR 命中应直连，实得代理 %v", got)
	}
	// 未命中 → 走代理。
	if got := proxyForURL(t, tr, "http://public.example/file"); got == nil || got.Host != "cfg-proxy.example:8080" {
		t.Fatalf("未命中 no_proxy 应走代理，实得 %v", got)
	}
}

// TestNew_InvalidURL_Errors 非法/不支持 scheme 的代理 URL 应报错（启动 fail-fast）。
func TestNew_InvalidURL_Errors(t *testing.T) {
	cases := []string{
		"://missing-scheme",
		"ftp://unsupported.example:21",
		"http://%zz", // 不可解析
	}
	for _, raw := range cases {
		if _, err := New(Config{URL: raw}); err == nil {
			t.Fatalf("非法代理 URL %q 应报错", raw)
		}
	}
}

// TestNew_SOCKS5_TransportShape socks5 代理：Transport 应挂 DialContext 且 Proxy 为 nil（拨号经 dialer）。
func TestNew_SOCKS5_TransportShape(t *testing.T) {
	c, err := New(Config{URL: "socks5://socks.example:1080"})
	if err != nil {
		t.Fatalf("socks5 代理应成功构造: %v", err)
	}
	tr := transportOf(t, c)
	if tr.Proxy != nil {
		t.Fatal("socks5 模式 Transport.Proxy 应为 nil（拨号经 SOCKS5 dialer）")
	}
	if tr.DialContext == nil {
		t.Fatal("socks5 模式应设置 Transport.DialContext")
	}
}

// TestNew_SOCKS5_EndToEnd 起一个最小 SOCKS5 CONNECT 服务端，验证经它能真实下载到目标。
func TestNew_SOCKS5_EndToEnd(t *testing.T) {
	// 目标 HTTP 服务。
	want := "hello-via-socks5"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, want)
	}))
	defer target.Close()

	var dialed int32
	socksAddr := startMiniSOCKS5(t, &dialed)

	c, err := New(Config{URL: "socks5://" + socksAddr})
	if err != nil {
		t.Fatalf("socks5 client 构造失败: %v", err)
	}
	c.Timeout = 5 * time.Second

	resp, err := c.Get(target.URL)
	if err != nil {
		t.Fatalf("经 SOCKS5 下载失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != want {
		t.Fatalf("下载内容不符: 期望 %q 实得 %q", want, body)
	}
	if atomic.LoadInt32(&dialed) == 0 {
		t.Fatal("请求应经过 SOCKS5 代理，但代理未被拨号")
	}
}

// TestNew_SOCKS5_NoProxyDirect socks5 模式下 no_proxy 命中的主机应绕过代理直连。
func TestNew_SOCKS5_NoProxyDirect(t *testing.T) {
	want := "direct-no-socks"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, want)
	}))
	defer target.Close()

	// SOCKS5 指向一个不可用端口；若被使用必失败，从而证明确实走了直连。
	var dialed int32
	socksAddr := startMiniSOCKS5(t, &dialed)

	// 目标主机是 127.0.0.1，将其加入 no_proxy。
	c, err := New(Config{URL: "socks5://" + socksAddr, NoProxy: "127.0.0.1"})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	c.Timeout = 5 * time.Second

	resp, err := c.Get(target.URL)
	if err != nil {
		t.Fatalf("no_proxy 直连下载失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != want {
		t.Fatalf("下载内容不符: 期望 %q 实得 %q", want, body)
	}
	if atomic.LoadInt32(&dialed) != 0 {
		t.Fatal("no_proxy 命中应绕过 SOCKS5 直连，但代理被拨号了")
	}
}

// startMiniSOCKS5 启动一个仅支持「无认证 + CONNECT」的最小 SOCKS5 服务端，返回监听地址。
// 每接受一次 CONNECT 即把 dialed +1，便于断言请求是否真经过代理。
func startMiniSOCKS5(t *testing.T, dialed *int32) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("SOCKS5 监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSOCKS5(conn, dialed)
		}
	}()
	return ln.Addr().String()
}

// serveSOCKS5 处理一条 SOCKS5 连接：握手（无认证）→ CONNECT → 双向转发。
func serveSOCKS5(conn net.Conn, dialed *int32) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	// 握手：VER=5, NMETHODS, METHODS...
	ver, _ := br.ReadByte()
	if ver != 0x05 {
		return
	}
	nm, _ := br.ReadByte()
	if _, err := io.CopyN(io.Discard, br, int64(nm)); err != nil {
		return
	}
	// 回应无认证。
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// 请求：VER, CMD, RSV, ATYP, ADDR, PORT
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return
	}
	if hdr[1] != 0x01 { // 仅支持 CONNECT
		return
	}
	var host string
	switch hdr[3] {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 0x03: // 域名
		l, _ := br.ReadByte()
		b := make([]byte, int(l))
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = string(b)
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	var port uint16
	if err := binary.Read(br, binary.BigEndian, &port); err != nil {
		return
	}

	dst, err := net.DialTimeout("tcp", net.JoinHostPort(host, itoa(int(port))), 5*time.Second)
	if err != nil {
		// 回 general failure。
		_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer dst.Close()
	atomic.AddInt32(dialed, 1)

	// 回 success（BND.ADDR/PORT 填 0，客户端不校验）。
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}

	// 双向转发；缓存在 br 里的多余字节也要带过去。
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(dst, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, dst); done <- struct{}{} }()
	<-done
}

// itoa 是不引入 strconv 的小整数转字符串（仅用于端口）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [6]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
