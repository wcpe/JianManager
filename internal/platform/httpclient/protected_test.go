package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

// 受保护出站（FR-514，ADR-105）的准入判定单测。
//
// 关注点不是「能不能连通」，而是「不允许的目标一个都不能放行」——这类代码错了
// 不会有任何显式失败，只会安静地给出一条访问内网的通路，因此每条禁止规则都要
// 有独立断言，不能只测一个代表性例子。

func TestGuardValidateBaseURL(t *testing.T) {
	httpsGuard := NewGuard(GuardOptions{})
	insecureGuard := NewGuard(GuardOptions{AllowInsecure: true})

	tests := []struct {
		name    string
		guard   *Guard
		raw     string
		wantErr bool
	}{
		{"https 正常", httpsGuard, "https://api.example.com/v1", false},
		{"http 默认拒绝", httpsGuard, "http://api.example.com", true},
		{"http 显式放行", insecureGuard, "http://api.example.com", false},
		{"空地址", httpsGuard, "  ", true},
		{"内嵌凭据", httpsGuard, "https://user:pass@api.example.com", true},
		{"带 query", httpsGuard, "https://api.example.com?k=v", true},
		{"带 fragment", httpsGuard, "https://api.example.com#x", true},
		{"缺主机", httpsGuard, "https:///v1", true},
		{"非 http 协议", httpsGuard, "ftp://api.example.com", true},
		{"无法解析", httpsGuard, "://bad", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.guard.ValidateBaseURL(tt.raw)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ValidateBaseURL(%q) err=%v, wantErr=%v", tt.raw, err, tt.wantErr)
			}
		})
	}
}

func TestGuardCheckAddr(t *testing.T) {
	// 只放行 10.0.0.0/8 的一段，用来验证「放行是不放行的补集」而非粗开关。
	guard := NewGuard(GuardOptions{
		AllowPrivate: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
	})
	tests := []struct {
		addr    string
		wantErr bool
		reason  string
	}{
		{"1.1.1.1", false, "公网放行"},
		{"8.8.8.8", false, "公网放行"},
		{"10.1.2.3", false, "显式放行的私网段"},
		{"10.2.0.1", true, "私网但未放行"},
		{"192.168.1.1", true, "私网未放行"},
		{"127.0.0.1", true, "回环未放行"},
		{"169.254.169.254", true, "云元数据永远拒绝"},
		{"169.254.170.2", true, "ECS 元数据永远拒绝"},
		{"100.100.100.200", true, "阿里云元数据永远拒绝"},
		{"fd00:ec2::254", true, "IMDSv6 元数据永远拒绝"},
		{"0.0.0.0", true, "未指定地址"},
		{"224.0.0.1", true, "组播地址"},
		{"::1", true, "IPv6 回环未放行"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := guard.checkAddr(netip.MustParseAddr(tt.addr))
			if tt.wantErr != (err != nil) {
				t.Fatalf("%s(%s) err=%v, wantErr=%v", tt.addr, tt.reason, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrBlockedTarget) {
				t.Fatalf("%s 的错误应可归为 ErrBlockedTarget，实际 %v", tt.addr, err)
			}
		})
	}
}

// 元数据地址即使在「放行全部私网」的配置下也必须拒绝——这是本文件最关键的一条：
// 管理员为了接自托管网关而放行 169.254.0.0/16 时，不能顺带把云元数据端点打开。
func TestGuardMetadataBlockedEvenWhenPrivateAllowed(t *testing.T) {
	guard := NewGuard(GuardOptions{
		AllowPrivate: []netip.Prefix{
			netip.MustParsePrefix("0.0.0.0/0"),
			netip.MustParsePrefix("::/0"),
		},
	})
	for _, addr := range []string{"169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254"} {
		if err := guard.checkAddr(netip.MustParseAddr(addr)); err == nil {
			t.Fatalf("%s 是云元数据地址，放行全部私网时也必须拒绝", addr)
		}
	}
	// 对照：链路本地（169.254/16）里非元数据名单的地址**仍应拒绝**——
	// 放行 0.0.0.0/0 不等于放行链路本地，否则同网段的服务发现端点会一起暴露。
	if err := guard.checkAddr(netip.MustParseAddr("169.254.1.1")); err == nil {
		t.Fatal("链路本地地址即使放行全部私网也必须拒绝")
	}
}

// 一个域名同时解析到公网与内网时，必须整体拒绝：挑「能用的那个」等于留后门。
func TestGuardResolveAllowedRejectsMixedResolution(t *testing.T) {
	guard := NewGuard(GuardOptions{})
	guard.lookup = stubLookup(map[string][]string{
		"mixed.example.com":  {"1.1.1.1", "127.0.0.1"},
		"public.example.com": {"1.1.1.1", "8.8.8.8"},
	})

	if _, err := guard.resolveAllowed(context.Background(), "mixed.example.com"); err == nil {
		t.Fatal("混合解析（公网 + 回环）必须整体拒绝")
	}
	ips, err := guard.resolveAllowed(context.Background(), "public.example.com")
	if err != nil {
		t.Fatalf("纯公网解析应放行，实际 %v", err)
	}
	if len(ips) != 2 {
		t.Fatalf("应返回全部已校验地址，实际 %d 个", len(ips))
	}
}

// 字面 IP 不走 DNS，直接按准入判定。
func TestGuardResolveAllowedLiteralIP(t *testing.T) {
	guard := NewGuard(GuardOptions{})
	if _, err := guard.resolveAllowed(context.Background(), "127.0.0.1"); err == nil {
		t.Fatal("字面回环地址必须拒绝")
	}
	ips, err := guard.resolveAllowed(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatalf("字面公网地址应放行，实际 %v", err)
	}
	if len(ips) != 1 || ips[0].String() != "1.1.1.1" {
		t.Fatalf("应返回该字面地址，实际 %v", ips)
	}
}

// 配了代理时，未命中 no_proxy 的目标必须显式拒绝（无法固定 IP），
// 命中 no_proxy 的目标仍可走受保护直连。
func TestGuardProxyAppliesTo(t *testing.T) {
	guarded := NewGuard(GuardOptions{
		Proxy: Config{URL: "http://proxy.internal:3128", NoProxy: "api.example.com"},
	})
	if !guarded.proxyAppliesTo("other.example.com") {
		t.Fatal("配了代理且未命中 no_proxy 时，目标应判为需经代理")
	}
	if guarded.proxyAppliesTo("api.example.com") {
		t.Fatal("命中 no_proxy 时不应判为需经代理")
	}

	direct := NewGuard(GuardOptions{})
	if direct.proxyAppliesTo("anything.example.com") {
		t.Fatal("未配代理时不应判为需经代理")
	}
}

// 代理拒绝必须是明确错误而非静默直连。
func TestGuardDialRejectsProxiedTarget(t *testing.T) {
	guard := NewGuard(GuardOptions{
		Proxy: Config{URL: "http://proxy.internal:3128"},
	})
	_, err := guard.dialContext(context.Background(), "tcp", "api.example.com:443")
	if !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("经代理的目标应返回 ErrProxyUnsupported，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "NO_PROXY") {
		t.Fatalf("错误信息应给出可操作的处置建议，实际 %q", err.Error())
	}
}

// 重定向默认一律拒绝；即使显式允许同源，跨源与 https 降级也必须拒绝。
func TestGuardRedirectPolicy(t *testing.T) {
	t.Run("默认拒绝任何重定向", func(t *testing.T) {
		g := NewGuard(GuardOptions{})
		client := g.Client()
		req := mustRequest(t, "https://api.example.com/next")
		if err := client.CheckRedirect(req, nil); err == nil {
			t.Fatal("默认策略必须拒绝重定向")
		}
	})

	t.Run("允许同源单跳", func(t *testing.T) {
		g := NewGuard(GuardOptions{AllowSameOriginRedirect: true})
		client := g.Client()
		prev := mustRequest(t, "https://api.example.com/a")
		next := mustRequest(t, "https://api.example.com/b")
		if err := client.CheckRedirect(next, []*http.Request{prev}); err != nil {
			t.Fatalf("同源重定向应允许，实际 %v", err)
		}
	})

	t.Run("拒绝跨源重定向", func(t *testing.T) {
		g := NewGuard(GuardOptions{AllowSameOriginRedirect: true})
		client := g.Client()
		prev := mustRequest(t, "https://api.example.com/a")
		next := mustRequest(t, "https://evil.example.net/b")
		if err := client.CheckRedirect(next, []*http.Request{prev}); err == nil {
			t.Fatal("跨源重定向必须拒绝")
		}
	})

	t.Run("拒绝 https 降级", func(t *testing.T) {
		g := NewGuard(GuardOptions{AllowSameOriginRedirect: true, AllowInsecure: true})
		client := g.Client()
		prev := mustRequest(t, "https://api.example.com/a")
		next := mustRequest(t, "http://api.example.com/b")
		if err := client.CheckRedirect(next, []*http.Request{prev}); err == nil {
			t.Fatal("https 降级重定向必须拒绝")
		}
	})
}

// stubLookup 返回一个按固定表解析的 lookup 替身，避免单测依赖真实 DNS。
func stubLookup(table map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		raw, ok := table[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		out := make([]netip.Addr, 0, len(raw))
		for _, s := range raw {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

// mustRequest 构造一个仅用于 CheckRedirect 判定的请求。
func mustRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatalf("构造请求失败 %s: %v", raw, err)
	}
	return req
}

// mustURL 解析 URL，失败即终止用例。
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析 URL 失败 %s: %v", raw, err)
	}
	return u
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://a.example.com/x", "https://a.example.com/y", true},
		{"https://a.example.com/x", "https://a.example.com:443/y", true},
		{"https://a.example.com/x", "https://b.example.com/y", false},
		{"https://a.example.com/x", "http://a.example.com/y", false},
		{"http://a.example.com/x", "http://a.example.com:80/y", true},
	}
	for _, c := range cases {
		if got := sameOrigin(mustURL(t, c.a), mustURL(t, c.b)); got != c.want {
			t.Fatalf("sameOrigin(%s, %s)=%v, want %v", c.a, c.b, got, c.want)
		}
	}
}
