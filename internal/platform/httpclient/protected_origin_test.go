package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"testing"
)

/**
 * FR-517 §3 前置任务：origin 绑定与协议预算的准入语义。
 *
 * 守的是三件「看起来像安全、实际留了口子」的事：
 *  1. 精确 origin 放行**不等于**「整段私网放行」——放行 10.0.0.5/32 之后，
 *     同网段的 10.0.0.6 仍必须被拒；
 *  2. origin 规则只认**它自己**的 CIDR：即使 AllowPrivate 放了更宽的网段，
 *     匹配到的 origin 也不能借道走别的地址；
 *  3. 元数据地址在任何放行规则下都永远拒绝（放行写宽了也不能漏出去）。
 */

// originGuard 构造一个只解析到固定地址的 Guard（不碰真实 DNS）。
func originGuard(t *testing.T, addrs []string, opts GuardOptions) *Guard {
	t.Helper()
	parsed := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			t.Fatalf("测试地址非法 %q: %v", a, err)
		}
		parsed = append(parsed, ip)
	}
	g := NewGuard(opts)
	g.lookup = func(context.Context, string) ([]netip.Addr, error) { return parsed, nil }
	return g
}

func TestAllowedOrigin_ExactOriginOnly(t *testing.T) {
	// 放行 10.0.0.5/32（自托管 MCP）后：同名 origin 可过，别的私网地址仍被拒。
	g := originGuard(t, []string{"10.0.0.5"}, GuardOptions{
		AllowedOrigins: []AllowedOrigin{{
			Scheme: "http", Host: "mcp.internal", Port: 8080,
			CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")},
		}},
	})
	if _, err := g.resolveAllowedForOrigin(context.Background(), "mcp.internal", "8080"); err != nil {
		t.Fatalf("被放行的 origin 应可解析: %v", err)
	}

	// 同一 Guard 解析另一个私网地址（不同 host）→ 必须拒绝。
	g2 := originGuard(t, []string{"10.0.0.6"}, GuardOptions{
		AllowedOrigins: []AllowedOrigin{{
			Scheme: "http", Host: "mcp.internal", Port: 8080,
			CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")},
		}},
	})
	if _, err := g2.resolveAllowedForOrigin(context.Background(), "other.internal", "8080"); !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("未放行的私网地址必须被拒，得到 %v", err)
	}
}

func TestAllowedOrigin_CannotBorrowWiderAllowPrivate(t *testing.T) {
	// 关键区别：AllowPrivate 放了 10.0.0.0/8，但 origin 规则只允许 10.0.0.5/32。
	// 解析到 10.0.0.9 时**必须按 origin 的 CIDR 拒**，不能借 AllowPrivate 通过。
	g := originGuard(t, []string{"10.0.0.9"}, GuardOptions{
		AllowPrivate: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		AllowedOrigins: []AllowedOrigin{{
			Scheme: "http", Host: "mcp.internal", Port: 8080,
			CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")},
		}},
	})
	if _, err := g.resolveAllowedForOrigin(context.Background(), "mcp.internal", "8080"); !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("origin 规则不得借用更宽的 AllowPrivate，得到 %v", err)
	}
}

func TestAllowedOrigin_MetadataAlwaysBlocked(t *testing.T) {
	// 元数据地址即使在 origin 的 CIDR 内也永远拒绝。
	g := originGuard(t, []string{"169.254.169.254"}, GuardOptions{
		AllowedOrigins: []AllowedOrigin{{
			Scheme: "http", Host: "mcp.internal", Port: 8080,
			CIDRs: []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16")},
		}},
	})
	if _, err := g.resolveAllowedForOrigin(context.Background(), "mcp.internal", "8080"); !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("云元数据地址必须永远拒绝，得到 %v", err)
	}
}

func TestAllowedOrigin_MultiAddrAnyOutsideRejects(t *testing.T) {
	// 一个域名同时解析到允许与不允许的地址 → 整体拒绝。
	// 「挑一个能用的」等于让攻击者多挂一条 A 记录就绕过 binding。
	g := originGuard(t, []string{"10.0.0.5", "203.0.113.9"}, GuardOptions{
		AllowedOrigins: []AllowedOrigin{{
			Scheme: "http", Host: "mcp.internal", Port: 8080,
			CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")},
		}},
	})
	if _, err := g.resolveAllowedForOrigin(context.Background(), "mcp.internal", "8080"); !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("任一地址越界即应整体拒绝，得到 %v", err)
	}
}

func TestAllowedOrigin_PortMustMatch(t *testing.T) {
	// 端口不同的同名 host 不命中 origin 规则 → 回落到默认准入（私网被拒）。
	g := originGuard(t, []string{"10.0.0.5"}, GuardOptions{
		AllowedOrigins: []AllowedOrigin{{
			Scheme: "http", Host: "mcp.internal", Port: 8080,
			CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")},
		}},
	})
	if _, err := g.resolveAllowedForOrigin(context.Background(), "mcp.internal", "9999"); !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("端口不匹配的 origin 不该被放行，得到 %v", err)
	}
}

func TestAllowedOrigin_DefaultPortFromScheme(t *testing.T) {
	// Port=0 时按 scheme 取默认端口（https→443）。
	g := originGuard(t, []string{"10.0.0.5"}, GuardOptions{
		AllowInsecure: true,
		AllowedOrigins: []AllowedOrigin{{
			Scheme: "https", Host: "mcp.internal",
			CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")},
		}},
	})
	if _, err := g.resolveAllowedForOrigin(context.Background(), "mcp.internal", "443"); err != nil {
		t.Fatalf("https 默认端口应命中: %v", err)
	}
}

func TestAllowedOrigin_LoopbackNeedsExplicitOrigin(t *testing.T) {
	// 回环地址在没有 origin 规则时必须被拒（默认从严）。
	g := originGuard(t, []string{"127.0.0.1"}, GuardOptions{})
	if _, err := g.resolveAllowedForOrigin(context.Background(), "localhost", "8080"); !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("未放行的回环地址必须被拒，得到 %v", err)
	}
}

func TestProtocolBudgets_AppliedToTransport(t *testing.T) {
	// 协议预算必须真的进 Transport：只加字段不装配等于没有。
	g := NewGuard(GuardOptions{ResponseHeaderTimeout: 1234})
	tr, ok := g.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport 类型不符")
	}
	if tr.ResponseHeaderTimeout != 1234 {
		t.Fatalf("ResponseHeaderTimeout 未装配，得到 %v", tr.ResponseHeaderTimeout)
	}
	// 预算字段本身可读（供 MCP 客户端构造自己的 reader 限制）。
	if g.opts.MaxResponseBytes != 0 {
		t.Fatal("默认不应设响应体上限（由 MCP 客户端显式给）")
	}
}
