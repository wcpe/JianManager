// Package httpclient 的受保护出站子模块（FR-514，见 ADR-105）。
//
// 为什么需要单独一套而不复用 New/NewProvider：那些工厂解决的是「代理怎么配」，
// 默认信任目标地址（下载 JDK、拉 GitHub Releases 都是可信来源）。而模型供应商与
// 外部 MCP 连接的地址是**管理员填写的任意 URL**，若直接把 `base_url` 交给普通
// http.Client，就等于给了一个可访问 127.0.0.1、云元数据 169.254.169.254 与其他内网
// 服务的 SSRF 原语。
//
// 【代理不是防护】本项目既有代理配置（internal/platform/httpclient/httpclient.go）
// 只决定「经由谁的出口」，不校验目标是谁——把安全寄托在代理上在无代理部署时直接失效。
//
// 【本文件的核心权衡】目的地址准入必须在**每次实际拨号**时校验并固定 IP，而不是
// 「先 Resolve 检查一遍、再交给默认 client 重新解析」——后者存在 DNS rebinding 与
// 检查-使用时间差（TOCTOU）。代价是：一旦启用 HTTP/SOCKS 代理，拨号目标是代理而非
// 业务域名，IP 无法固定，此时**显式拒绝**该组合（ErrProxyUnsupported）而不是静默直连
// ——静默直连会让「配了代理」的部署出现意外的绕过路径，比报错难排查得多。
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ErrProxyUnsupported 出站目标需要经代理但该模式无法固定目标 IP（FR-514）。
//
// 这是有意的取舍而非缺陷：无法保证拨号目标等于已校验 IP 时，宁可让管理员为
// 该域名补 NO_PROXY / 改直连，也不静默绕过代理或绕过准入校验。
var ErrProxyUnsupported = errors.New("受保护出站不支持经代理访问（无法固定已校验的目标 IP），请为该域名配置 NO_PROXY 或改用直连")

// ErrBlockedTarget 目标地址落在禁止范围（私网 / 回环 / 链路本地 / 云元数据 / 非 HTTP-S）。
var ErrBlockedTarget = errors.New("目标地址被出站准入拒绝")

// GuardOptions 受保护出站的准入策略（FR-514）。
//
// 默认值刻意从严：不填 AllowPrivate 就一个内网地址都访问不了。管理员要接自托管的
// 内网模型网关或 MCP 服务时，需显式给出 CIDR——放行粒度是网络段而不是「允许内网」
// 这种粗开关，避免一次放行把整片私网（含同机的其它服务）都暴露出去。
type GuardOptions struct {
	// AllowPrivate 允许访问的私网/回环地址段（精确 CIDR，如 10.0.0.0/8 或 127.0.0.1/32）。
	// 为空表示禁止一切非公网目标。
	AllowPrivate []netip.Prefix
	// AllowInsecure 允许 http:// 明文 BaseURL。默认只允许 https://，
	// 因为模型请求要带供应商凭据、响应含业务数据。
	AllowInsecure bool
	// Timeout 单次请求的整体超时；0 用默认 60 秒。
	Timeout time.Duration
	// AllowSameOriginRedirect 允许同源重定向跟随（默认 false＝任何重定向都拒绝）。
	AllowSameOriginRedirect bool
	// Proxy 进程出站代理配置。只用于判定「该目标是否会被代理接管」——
	// 一旦会，受保护出站直接拒绝（无法固定已校验 IP），不静默直连也不静默走代理。
	Proxy Config
	// NoProxy 兜底：Proxy.URL 为空但显式配了 no_proxy 时，用它判断哪些目标仍会
	// 经环境变量代理出站。
	NoProxy string
}

// 受保护出站默认值。数值与 spec §6 冻结的预算一致。
const (
	protectedDefaultTimeout = 60 * time.Second
	// protectedMaxRedirects 允许的同源重定向次数上限。
	protectedMaxRedirects = 1
)

// metadataPrefixes 云元数据服务地址段：无论 AllowPrivate 怎么配都**永远**拒绝。
//
// 单独列出来是因为它们常常落在「管理员为了自托管而放行的私网段」里面
// （169.254.169.254 属链路本地，部分云厂商的元数据服务也在 100.64/10 或私网段内），
// 而元数据端点泄漏的是主机身份凭据，代价远高于其它内网服务。
var metadataPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.169.254/32"), // AWS / GCP / Azure / OpenStack 通用
	netip.MustParsePrefix("169.254.170.2/32"),   // AWS ECS task metadata
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS IMDSv6
	netip.MustParsePrefix("100.100.100.200/32"), // 阿里云
}

// Guard 受保护出站客户端工厂。
type Guard struct {
	opts GuardOptions
	// dialer 基础拨号器（保留连接级超时与 keepalive）。
	dialer *net.Dialer
	// lookup 解析域名；默认 net.DefaultResolver.LookupNetIP，测试可替换。
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// noProxy 编译后的 no_proxy 判定（与既有代理实现同一套语义）。
	noProxy func(host, port string) bool
	// proxied 目标是否需要经代理出站。实现见 proxyAppliesTo。
	proxied bool
}

// NewGuard 构造受保护出站工厂。opts 零值即「只允许 https 公网、默认禁重定向」。
func NewGuard(opts GuardOptions) *Guard {
	if opts.Timeout <= 0 {
		opts.Timeout = protectedDefaultTimeout
	}
	noProxyRaw := strings.TrimSpace(opts.Proxy.NoProxy)
	if noProxyRaw == "" {
		noProxyRaw = strings.TrimSpace(opts.NoProxy)
	}
	// 显式配了代理 URL 才算「经代理出站」；只配 no_proxy 时仍可能被环境变量代理接管，
	// 因此只要代理地址为空就不把目标判为「必须经代理」——否则直连部署会被无谓拒绝。
	proxied := strings.TrimSpace(opts.Proxy.URL) != ""
	return &Guard{
		opts:   opts,
		dialer: &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second},
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		noProxy: newNoProxyMatcher(noProxyRaw),
		proxied: proxied,
	}
}

// Client 返回受保护出站 *http.Client。
//
// 该 client 的 Transport 不做通用代理转发：目标域若命中进程代理且该代理模式无法固定
// 目标 IP，请求会在发起前被拒绝（ErrProxyUnsupported），而不是悄悄直连。
func (g *Guard) Client() *http.Client {
	tr := &http.Transport{
		// 显式不用 ProxyFromEnvironment：受保护出站的目标准入不能被环境变量代理绕过。
		Proxy:               nil,
		DialContext:         g.dialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: g.opts.Timeout}
	// 默认不跟随重定向：一次 302 就能把已校验的公网目标换成内网地址，
	// 而 Go 的重定向会复用同一 client（不重新过我们的拨号校验之外的目标判断）。
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !g.opts.AllowSameOriginRedirect {
			return fmt.Errorf("%w: 受保护出站默认不跟随重定向", ErrBlockedTarget)
		}
		if len(via) > protectedMaxRedirects {
			return fmt.Errorf("%w: 重定向次数超过上限 %d", ErrBlockedTarget, protectedMaxRedirects)
		}
		// 同源 + 协议不降级：跨源重定向可能把凭据带去别的 host。
		prev := via[len(via)-1].URL
		if !sameOrigin(prev, req.URL) {
			return fmt.Errorf("%w: 拒绝跨源重定向 %s -> %s", ErrBlockedTarget, prev.Host, req.URL.Host)
		}
		if prev.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("%w: 拒绝 https 降级重定向", ErrBlockedTarget)
		}
		return nil
	}
	return client
}

// ValidateBaseURL 在配置阶段先挡一道非法 URL（FR-514 spec §4）。
//
// 这里只做**静态**校验（协议、userinfo、query、fragment）；域名解析与 IP 准入在
// 实际拨号时做——配置时能解析不代表运行时可解析（DNS 会变），反之亦然。
func (g *Guard) ValidateBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: 地址为空", ErrBlockedTarget)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: 地址无法解析", ErrBlockedTarget)
	}
	if u.Scheme != "https" && !(g.opts.AllowInsecure && u.Scheme == "http") {
		return nil, fmt.Errorf("%w: 仅支持 https（明文 http 需显式放行）", ErrBlockedTarget)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: 地址不得内嵌凭据", ErrBlockedTarget)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: 地址不得带 query 或 fragment", ErrBlockedTarget)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: 地址缺少主机", ErrBlockedTarget)
	}
	return u, nil
}

// dialContext 是准入的**唯一强制点**：任何请求（含重定向后的）都必须经此拨号。
func (g *Guard) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: 目标地址格式非法", ErrBlockedTarget)
	}
	// 代理模式下拨号目标是代理而非业务域名，我们无法证明最终目标 = 已校验 IP，
	// 因此直接拒绝并要求管理员为该域名配置 NO_PROXY。
	if g.proxyAppliesTo(host) {
		return nil, ErrProxyUnsupported
	}
	ips, err := g.resolveAllowed(ctx, host)
	if err != nil {
		return nil, err
	}
	// 逐个已校验 IP 尝试拨号：**只拨校验通过的地址**，不再交给系统重新解析。
	var lastErr error
	for _, ip := range ips {
		conn, derr := g.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无可用地址")
	}
	return nil, fmt.Errorf("连接目标失败: %w", lastErr)
}

// proxyAppliesTo 判断该主机的出站是否会被进程代理接管。
//
// 复用既有 no_proxy 语义（newNoProxyMatcher，与 New/NewProvider 同一套规则），
// 但**只用于判定「是否必须拒绝」**，不用于构造转发——受保护出站要么直连已校验 IP，
// 要么明确报错，绝不静默走代理或静默绕过代理。
func (g *Guard) proxyAppliesTo(host string) bool {
	if !g.proxied {
		return false
	}
	// no_proxy 命中则直连，代理不接管。
	return !g.noProxy(host, "")
}

// resolveAllowed 解析主机并逐地址做准入校验。
//
// 任一地址落在禁止范围即整体拒绝（不是「挑一个能用的」）：一个域名同时解析到
// 公网与内网时，攻击者只要让我们的连接落到内网那条就达成了目标。
func (g *Guard) resolveAllowed(ctx context.Context, host string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	// 字面 IP 直接校验，不走 DNS。
	if ip, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{ip}
	} else {
		resolved, rerr := g.lookup(ctx, host)
		if rerr != nil {
			return nil, fmt.Errorf("解析目标主机失败: %w", rerr)
		}
		if len(resolved) == 0 {
			return nil, fmt.Errorf("%w: 目标主机无解析结果", ErrBlockedTarget)
		}
		addrs = resolved
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, ip := range addrs {
		if err := g.checkAddr(ip); err != nil {
			return nil, err
		}
		out = append(out, ip)
	}
	return out, nil
}

// checkAddr 单个地址的准入判定（元数据 > 显式放行 > 公网）。
func (g *Guard) checkAddr(ip netip.Addr) error {
	ip = ip.Unmap()
	// 元数据地址永远拒绝，先于一切放行规则。
	for _, p := range metadataPrefixes {
		if p.Contains(ip) {
			return fmt.Errorf("%w: 拒绝访问云元数据地址 %s", ErrBlockedTarget, ip)
		}
	}
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return fmt.Errorf("%w: 拒绝访问特殊地址 %s", ErrBlockedTarget, ip)
	}
	// 公网可达：不需要放行规则。
	if !ip.IsPrivate() && !ip.IsLoopback() {
		return nil
	}
	// 私网/回环：必须在显式放行的 CIDR 内。
	for _, p := range g.opts.AllowPrivate {
		if p.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s 属私网/回环且未被放行", ErrBlockedTarget, ip)
}

// sameOrigin 判断两个 URL 是否同源（scheme + host + 有效端口）。
func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Hostname() == b.Hostname() && effectivePort(a) == effectivePort(b)
}

// effectivePort 取 URL 的有效端口（缺省按 scheme 补）。
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
