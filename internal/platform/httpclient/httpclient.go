// Package httpclient 提供 CP 与 Worker 共用的「每进程出站 HTTP 客户端工厂」（FR-174，见 ADR-037）。
//
// 所有出站下载（自更新 feed/二进制、JDK 归档、服务端 jar、CFR、未来 GitHub API）经本工厂
// 构造的 *http.Client 出站，统一受进程级代理配置约束：
//   - 空配 → 回退 http.ProxyFromEnvironment（等价改造前行为，仍尊重 HTTP_PROXY/NO_PROXY 环境变量）；
//   - http(s) 代理 → Transport.Proxy 固定返回该代理，但遵守 no_proxy；
//   - socks5 代理 → 经 golang.org/x/net/proxy 构造 SOCKS5 dialer，no_proxy 命中走直连；
//   - 非法代理 URL → 返回 error，供启动时 fail-fast。
//
// 工厂只负责 proxy/transport，不写死请求超时（大文件下载的长超时由各调用方控制）。
package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
	xproxy "golang.org/x/net/proxy"
)

// Config 是单个进程的出站代理配置（mapstructure 直接映射 yaml 的 proxy 段）。
type Config struct {
	// URL 代理地址，scheme 决定类型：http:// / https:// / socks5://。
	// 留空表示不显式配置，回退 ProxyFromEnvironment（尊重环境变量代理）。
	// 含凭据时经 ${ENV_VAR} 注入、不硬编码（config-files 规范）。
	URL string `mapstructure:"url"`
	// NoProxy 逗号分隔的免代理主机/域/CIDR（语义同 NO_PROXY 环境变量）。
	NoProxy string `mapstructure:"no_proxy"`
}

// defaultTransport 返回一份带合理连接级超时的基础 Transport（不设整体请求超时）。
// 复用标准库默认值，仅作为工厂注入 Proxy/DialContext 的载体。
func defaultTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// New 按进程代理配置构造出站 *http.Client。
//
// cfg.URL 留空时回退 ProxyFromEnvironment（行为同改造前 http.DefaultClient）。
// 非法/不支持 scheme 的 URL 返回 error（调用方应在启动时 fail-fast，不静默直连）。
// 返回的 client 不设 Timeout，由调用方按下载场景自行设置（避免掐断大文件下载）。
func New(cfg Config) (*http.Client, error) {
	tr := defaultTransport()
	raw := strings.TrimSpace(cfg.URL)

	if raw == "" {
		// 空配：等价改造前行为——尊重 HTTP_PROXY/HTTPS_PROXY/NO_PROXY 环境变量。
		// 但若显式配了 no_proxy，则叠加到环境变量代理判定上。
		if strings.TrimSpace(cfg.NoProxy) != "" {
			tr.Proxy = envProxyFuncWithNoProxy(cfg.NoProxy)
		} else {
			tr.Proxy = http.ProxyFromEnvironment
		}
		return &http.Client{Transport: tr}, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("解析代理地址失败: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("非法代理地址（缺少主机）: %s", Sanitize(raw))
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		tr.Proxy = fixedProxyFunc(u, cfg.NoProxy)
		return &http.Client{Transport: tr}, nil
	case "socks5", "socks5h":
		dialer, err := socks5DialContext(u, cfg.NoProxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = nil // SOCKS5 经 DialContext 拨号，不走 Transport.Proxy。
		tr.DialContext = dialer
		return &http.Client{Transport: tr}, nil
	default:
		return nil, fmt.Errorf("不支持的代理 scheme %q（仅支持 http/https/socks5）: %s", u.Scheme, Sanitize(raw))
	}
}

// fixedProxyFunc 返回「固定用 proxyURL，但遵守 no_proxy」的 Proxy 函数。
//
// 与「空配回退环境变量」不同：用户显式配了代理即表示「我的出站走这个代理」，
// 因此除 no_proxy 命中外一律走代理（不施加 httpproxy 对 localhost 的隐式直连特例，
// 避免「明明配了代理却对某些主机静默直连」的意外）。
func fixedProxyFunc(proxyURL *url.URL, noProxy string) func(*http.Request) (*url.URL, error) {
	match := newNoProxyMatcher(noProxy)
	return func(req *http.Request) (*url.URL, error) {
		if match(req.URL.Hostname(), req.URL.Port()) {
			return nil, nil // 命中 no_proxy → 直连。
		}
		return proxyURL, nil
	}
}

// envProxyFuncWithNoProxy 在「空 url」时使用：代理地址取自环境变量，但叠加显式 no_proxy。
// 即在 HTTP_PROXY/HTTPS_PROXY 基础上，把配置的 no_proxy 与环境 NO_PROXY 合并判定。
func envProxyFuncWithNoProxy(noProxy string) func(*http.Request) (*url.URL, error) {
	env := httpproxy.FromEnvironment() // 读取当前 HTTP_PROXY/HTTPS_PROXY/NO_PROXY
	if strings.TrimSpace(env.NoProxy) == "" {
		env.NoProxy = noProxy
	} else if strings.TrimSpace(noProxy) != "" {
		env.NoProxy = env.NoProxy + "," + noProxy
	}
	pf := env.ProxyFunc()
	return func(req *http.Request) (*url.URL, error) {
		return pf(req.URL)
	}
}

// socks5DialContext 构造 SOCKS5 拨号的 DialContext：命中 no_proxy 的主机走直连 dialer，
// 其余经 SOCKS5 代理。凭据（若 URL 带 user:pass）透传给 SOCKS5 握手。
func socks5DialContext(proxyURL *url.URL, noProxy string) (func(context.Context, string, string) (net.Conn, error), error) {
	var auth *xproxy.Auth
	if proxyURL.User != nil {
		pw, _ := proxyURL.User.Password()
		auth = &xproxy.Auth{User: proxyURL.User.Username(), Password: pw}
	}

	direct := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	sd, err := xproxy.SOCKS5("tcp", proxyURL.Host, auth, direct)
	if err != nil {
		return nil, fmt.Errorf("构造 SOCKS5 代理失败: %w", err)
	}
	contextDialer, ok := sd.(xproxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS5 dialer 不支持 context 拨号")
	}

	match := newNoProxyMatcher(noProxy)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			host, port = addr, ""
		}
		if match(host, port) {
			return direct.DialContext(ctx, network, addr) // 命中 no_proxy → 直连。
		}
		return contextDialer.DialContext(ctx, network, addr)
	}, nil
}

// noProxyEntry 是单条 no_proxy 规则（域名 / IP / CIDR，可带端口）。
type noProxyEntry struct {
	matchAll bool       // "*"：匹配所有主机
	host     string     // 规范化小写域名（含前导 . 表示仅子域）
	onlySub  bool       // host 带前导 . —— 仅匹配子域，不匹配域名本身
	ipNet    *net.IPNet // CIDR 规则（与 host 互斥）
	ip       net.IP     // 单 IP 规则（与 host 互斥）
	port     string     // 可选端口约束（空=任意端口）
}

// newNoProxyMatcher 把逗号分隔的 no_proxy 串编译为「(host,port)→是否直连」判定函数。
// 语义对齐 golang.org/x/net/http/httpproxy 文档（域名匹配本身+子域、前导 . 仅子域、
// IP 前缀、CIDR、可带端口、单独 * 表示全部直连），但不施加 localhost 隐式直连特例。
func newNoProxyMatcher(noProxy string) func(host, port string) bool {
	var entries []noProxyEntry
	for _, raw := range strings.Split(noProxy, ",") {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if s == "*" {
			return func(string, string) bool { return true }
		}

		var e noProxyEntry
		// 拆出可选端口（仅当冒号后是纯数字端口时；避免误伤 IPv6/带 scheme）。
		if h, p, err := net.SplitHostPort(s); err == nil {
			if _, perr := strconv.Atoi(p); perr == nil {
				s, e.port = h, p
			}
		}
		s = strings.ToLower(strings.TrimSpace(s))

		if _, ipnet, err := net.ParseCIDR(s); err == nil {
			e.ipNet = ipnet
		} else if ip := net.ParseIP(s); ip != nil {
			e.ip = ip
		} else {
			if strings.HasPrefix(s, ".") {
				e.onlySub = true
				e.host = s[1:]
			} else {
				e.host = s
			}
		}
		entries = append(entries, e)
	}

	if len(entries) == 0 {
		return func(string, string) bool { return false }
	}

	return func(host, port string) bool {
		host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
		var hostIP net.IP
		if host != "" {
			hostIP = net.ParseIP(host)
		}
		for _, e := range entries {
			if e.matchAll {
				return true
			}
			if e.port != "" && port != "" && e.port != port {
				continue
			}
			switch {
			case e.ipNet != nil:
				if hostIP != nil && e.ipNet.Contains(hostIP) {
					return true
				}
			case e.ip != nil:
				if hostIP != nil && e.ip.Equal(hostIP) {
					return true
				}
			case e.host != "":
				if matchHostSuffix(host, e.host, e.onlySub) {
					return true
				}
			}
		}
		return false
	}
}

// matchHostSuffix 报告 host 是否匹配规则域 rule：
// onlySub=false → 匹配 rule 本身与其子域（rule="foo.com" 命中 foo.com 与 bar.foo.com）；
// onlySub=true  → 仅匹配子域（rule="foo.com" 命中 bar.foo.com，不命中 foo.com）。
func matchHostSuffix(host, rule string, onlySub bool) bool {
	if host == "" || rule == "" {
		return false
	}
	if !onlySub && host == rule {
		return true
	}
	return strings.HasSuffix(host, "."+rule)
}

// Sanitize 脱敏代理 URL 中的 user:pass，仅保留 scheme://host:port（用于日志/错误信息）。
// 解析失败时返回原串去除明显凭据片段，避免泄露密码。
func Sanitize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// 退化处理：若形如 scheme://user:pass@host，截掉 @ 之前的凭据。
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			if i := strings.Index(raw, "://"); i >= 0 && i+3 <= at {
				return raw[:i+3] + raw[at+1:]
			}
		}
		return raw
	}
	if u.User != nil {
		u.User = nil
	}
	return u.String()
}
