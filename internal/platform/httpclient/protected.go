// Package httpclient 的受保护出站子模块（见 ADR-105）。
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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ErrProxyUnsupported 出站目标需要经代理但该模式无法固定目标 IP。
//
// 这是有意的取舍而非缺陷：无法保证拨号目标等于已校验 IP 时，宁可让管理员为
// 该域名补 NO_PROXY / 改直连，也不静默绕过代理或绕过准入校验。
var ErrProxyUnsupported = errors.New("受保护出站不支持经代理访问（无法固定已校验的目标 IP），请为该域名配置 NO_PROXY 或改用直连")

// ErrBlockedTarget 目标地址落在禁止范围（私网 / 回环 / 链路本地 / 云元数据 / 非 HTTP-S）。
var ErrBlockedTarget = errors.New("目标地址被出站准入拒绝")

// GuardOptions 受保护出站的准入策略。
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

	// AllowedOrigins 精确 origin + CIDR 的绑定放行（§3 前置任务①）。
	//
	// 为什么不能只用 AllowPrivate：`AllowPrivate` 是「整段私网都放行」，
	// 而外部 MCP 连接要的是「只有这一个 origin（scheme+host+port）可以走，
	// 且它的 IP 必须落在这几条 CIDR 里」。前者一旦放行 10.0.0.0/8，
	// 同网段的其它服务（数据库、内部面板）就一并可达了。
	AllowedOrigins []AllowedOrigin

	// ── 协议预算（§3 前置任务②）──
	//
	// 都是 0 表示不额外限制（沿用 Timeout 的整体超时）。外部 MCP 是**不可信上游**，
	// 一个不换行的响应或永不结束的 SSE 流会把连接占死，故这些预算必须可配。

	// ResponseHeaderTimeout 等待响应头的上限。
	ResponseHeaderTimeout time.Duration
	// StreamIdleTimeout 流式响应**两帧之间**的空闲上限。
	// 与整体 Timeout 分开：长连接场景下整体超时要么太短（误杀正常长任务），
	// 要么太长（坏上游占住连接）。
	StreamIdleTimeout time.Duration
	// MaxResponseBytes 单次响应体上限；超出即失败（不截断后当成功）。
	MaxResponseBytes int64
	// MaxEventLineBytes 单行事件上限（防「一个不换行的巨大 data 行」吃满内存）。
	MaxEventLineBytes int

	// ── TLS 基线（§5.2）──
	//
	// **刻意不提供 InsecureSkipVerify 开关**：受保护出站的目标是「把业务数据与
	// 供应商凭据送出公网」，关掉证书校验等于把中间人攻击变成默认可用配置。
	// 需要自签证书的自托管端点应通过 TLSRootCAs 显式注入信任锚，而不是放弃校验。

	// TLSMinVersion 最低 TLS 版本；0 表示用 TLS1.2（不给「降到 TLS1.0」留口子）。
	// 显式而不是沿用 Go 默认：默认值会随 Go 版本演进，安全基线不该由编译器版本决定。
	TLSMinVersion uint16
	// TLSRootCAs 额外的信任锚（自签/私有 CA 场景）。为 nil 时只用系统根证书。
	TLSRootCAs *x509.CertPool
}

// AllowedOrigin 一条精确 origin 放行规则。
//
// 语义：「scheme://host:port 这个目标可以访问，但它解析出的 IP 必须命中 CIDRs 之一」。
// 两条同时满足才放行——只绑 origin 不校验 IP 挡不住 DNS rebinding，
// 只校验 IP 不绑 origin 则等于又退回「整段私网放行」。
type AllowedOrigin struct {
	// Scheme 小写 scheme（http / https）。
	Scheme string
	// Host 小写主机名或 IP 字面量（不含端口）。
	Host string
	// Port 端口（默认按 scheme 取 80/443）。
	Port int
	// CIDRs 该 origin 允许解析到的地址段。空表示不允许（宁可拒绝也不默认放行）。
	CIDRs []netip.Prefix
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

// tlsMinVersion 把 GuardOptions.TLSMinVersion 归一成实际生效的最低版本。
//
// 0 视为 TLS1.2；显式传更低的值（TLS1.0/1.1）会被**抬到 TLS1.2**而不是照办——
// 那两版已被 RFC 8996 弃用，允许配置它们等于给降级攻击留一个「管理员自己开的」口子。
func tlsMinVersion(configured uint16) uint16 {
	if configured < tls.VersionTLS12 {
		return tls.VersionTLS12
	}
	return configured
}

// Client 返回受保护出站 *http.Client。
//
// 该 client 的 Transport 不做通用代理转发：目标域若命中进程代理且该代理模式无法固定
// 目标 IP，请求会在发起前被拒绝（ErrProxyUnsupported），而不是悄悄直连。
func (g *Guard) Client() *http.Client {
	return g.clientWithProxy(nil)
}

// TLSConnectionState 报告一次请求实际协商出的 TLS 参数（§5.2 验证面）。
//
// 为什么需要它：`MinVersion` 只约束「协商的下限」，证明「这条链路确实走了
// TLS1.2+ 且确实协商到 HTTP/2」需要**观察到实际结果**，而不是读配置自证。
// 返回零值表示该连接未使用 TLS（明文 http 或连接复用自非 TLS 池）。
//
// 判据取 `resp.TLS`：它是本次响应对应连接的真实握手结果，不是配置回显。
func (g *Guard) TLSConnectionState(resp *http.Response) TLSState {
	if resp == nil || resp.TLS == nil {
		return TLSState{}
	}
	return TLSState{
		Enabled:     true,
		Version:     resp.TLS.Version,
		VersionName: tlsVersionName(resp.TLS.Version),
		CipherSuite: tls.CipherSuiteName(resp.TLS.CipherSuite),
		ServerName:  resp.TLS.ServerName,
		// HTTP/2 在 Go 里由 Transport 的 ALPN 协商决定；响应 Proto 是它的观测面。
		Proto:              resp.Proto,
		HTTP2:              resp.ProtoMajor == 2,
		NegotiatedProtocol: resp.TLS.NegotiatedProtocol,
	}
}

// TLSState 一次连接实际协商出的 TLS 参数。
type TLSState struct {
	// Enabled 该连接是否使用 TLS。
	Enabled bool `json:"enabled"`
	// Version 数值型 TLS 版本（如 0x0303）。
	Version uint16 `json:"version,omitempty"`
	// VersionName 可读版本名（TLS 1.2 / TLS 1.3）。
	VersionName string `json:"versionName,omitempty"`
	// CipherSuite 协商的密码套件名。
	CipherSuite string `json:"cipherSuite,omitempty"`
	// ServerName SNI。
	ServerName string `json:"serverName,omitempty"`
	// Proto 响应协议（HTTP/1.1 或 HTTP/2.0）。
	Proto string `json:"proto,omitempty"`
	// HTTP2 是否协商到 HTTP/2。
	HTTP2 bool `json:"http2"`
	// NegotiatedProtocol ALPN 协商结果（h2 / http/1.1）。
	NegotiatedProtocol string `json:"negotiatedProtocol,omitempty"`
}

// tlsVersionName 把数值版本转成可读名（未知返回空串，不猜）。
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return ""
	}
}

// ClientWithProxyEnv 返回「跟随进程环境代理」的受保护客户端（的 `env` 模式）。
//
// 与 Client() 的唯一差异是 Transport.Proxy = http.ProxyFromEnvironment。
// **准入判定完全不变**：仍然只允许 https、仍然拦截未放行的私网目标——
// 「走代理」不能变成绕过准入的旁路。
//
// 注意一个无法回避的限制：经代理出站时，我们无法在本地解析并固定目标 IP，
// 因此 DialContext 里对目标地址的预校验只能覆盖「代理自身地址」；
// 目标判定改由「配置阶段校验 + 只允许 https」承担。这也是为什么该模式
// **默认不是直连**，而必须由管理员显式选择——把风险摆到明面上。
func (g *Guard) ClientWithProxyEnv() *http.Client {
	return g.clientWithProxy(http.ProxyFromEnvironment)
}

// ClientWithProxy 返回经固定代理出站的受保护客户端（的 `custom` 模式）。
//
// 代理只决定「连到哪里」，不放松准入；代理地址本身同样受 https/私网策略约束
// （在配置阶段由 ValidateProxyURL 先挡一道）。
func (g *Guard) ClientWithProxy(proxyURL *url.URL) *http.Client {
	if proxyURL == nil {
		return g.Client()
	}
	fixed := proxyURL
	return g.clientWithProxy(func(*http.Request) (*url.URL, error) { return fixed, nil })
}

// clientWithProxy 是三个入口的唯一实现。
//
// proxyFn 为 nil 时显式禁用代理（不读环境变量）——这是刻意的：
// 受保护出站的目标准入不能被环境变量代理绕过。
func (g *Guard) clientWithProxy(proxyFn func(*http.Request) (*url.URL, error)) *http.Client {
	tr := &http.Transport{
		Proxy:               proxyFn,
		DialContext:         g.dialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		// TLS 基线显式固定（§5.2）：不显式给 MinVersion 就等于
		// 把「最低可接受版本」交给 Go 的默认值，而默认值会随版本演进。
		// 受保护出站送的是供应商凭据与业务数据，这条基线不该由编译器版本决定。
		// 注意：这里**不提供** InsecureSkipVerify 入口（见 GuardOptions 说明）。
		TLSClientConfig: &tls.Config{
			MinVersion: tlsMinVersion(g.opts.TLSMinVersion),
			RootCAs:    g.opts.TLSRootCAs,
		},
		// §5.2：等响应头的独立上限。慢速上游可以让连接一直挂在「等头部」，
		// 整体 Timeout 很长时这会把连接池耗尽。
		ResponseHeaderTimeout: g.opts.ResponseHeaderTimeout,
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

// ValidateBaseURL 在配置阶段先挡一道非法 URL（spec §4）。
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
	ips, err := g.resolveAllowedForOrigin(ctx, host, port)
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

// resolveAllowedForOrigin 解析主机并校验，优先按 **origin 绑定**规则判定。
//
// 有匹配的 AllowedOrigin 时，只认它自己的 CIDR——即使 AllowPrivate 放行了更宽的网段，
// 也不会因此让这个 origin 走到规则的地址上。这是「精确 origin + CIDR」与
// 「整段私网开关」的关键区别（§3 前置任务①）。
func (g *Guard) resolveAllowedForOrigin(ctx context.Context, host, port string) ([]netip.Addr, error) {
	origin, ok := g.matchAllowedOrigin(host, port)
	if !ok {
		return g.resolveAllowed(ctx, host)
	}
	var addrs []netip.Addr
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
	for _, ip := range addrs {
		// 元数据/特殊地址即使在该 origin 的 CIDR 内也永远拒绝：
		// 那是「拿到主机身份凭据」的代价，不该因为放行规则写宽了就漏出去。
		for _, p := range metadataPrefixes {
			if p.Contains(ip) {
				return nil, fmt.Errorf("%w: 拒绝访问云元数据地址 %s", ErrBlockedTarget, ip)
			}
		}
		if ip.IsUnspecified() || ip.IsMulticast() {
			return nil, fmt.Errorf("%w: 拒绝访问特殊地址 %s", ErrBlockedTarget, ip)
		}
		allowed := false
		for _, p := range origin.CIDRs {
			if p.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			// 任一地址不在该 origin 的 CIDR 内即整体拒绝（不挑一个能用的）：
			// 否则让域名多解析一条内网记录就绕过了 binding。
			return nil, fmt.Errorf("%w: %s 不在 origin %s 的放行地址段内", ErrBlockedTarget, ip, host)
		}
	}
	return addrs, nil
}

// matchAllowedOrigin 找匹配该 host:port 的 origin 规则。
func (g *Guard) matchAllowedOrigin(host, port string) (AllowedOrigin, bool) {
	host = strings.ToLower(host)
	for _, o := range g.opts.AllowedOrigins {
		if !strings.EqualFold(o.Host, host) {
			continue
		}
		wantPort := o.Port
		if wantPort == 0 {
			switch strings.ToLower(o.Scheme) {
			case "http":
				wantPort = 80
			case "https":
				wantPort = 443
			}
		}
		if wantPort != 0 && wantPort != atoiSafe(port) {
			continue
		}
		return o, true
	}
	return AllowedOrigin{}, false
}

// atoiSafe 宽松解析端口（失败返回 0，交由后续比较判不等）。
func atoiSafe(s string) int {
	n := 0
	if s == "" {
		return 0
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 65535 {
			return 0
		}
	}
	return n
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
