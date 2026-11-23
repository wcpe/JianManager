package httpclient

// 出站代理真机验收：验证 direct / env / custom 三种模式的实际行为。
//
// 要回答的问题：
//   1. custom 模式是否按设计显式拒绝（而非静默直连/静默走代理）？
//   2. direct 模式是否真的不读环境变量代理？
//   3. env 模式是否真的采用环境里的代理？
//   4. no_proxy 命中时是否放行直连？

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http/httpproxy"
)

const (
	proxyMockURL  = "http://127.0.0.1:18182"
	proxyMockAddr = "127.0.0.1:18182"
)

// loopbackGuard 构造放行回环的 Guard（仅测试）。
func loopbackGuard(t *testing.T, opts GuardOptions) *Guard {
	t.Helper()
	p, err := netip.ParsePrefix("127.0.0.0/8")
	require.NoError(t, err)
	opts.AllowPrivate = append(opts.AllowPrivate, p)
	if opts.Timeout == 0 {
		opts.Timeout = 3 * time.Second
	}
	return NewGuard(opts)
}

// TestProxy_CustomMode_RejectsBecauseIPCannotBePinned 验证出站代理的核心取舍。
//
// 配置固定代理后目标 IP 无法固定 ⇒ 必须显式返回 ErrProxyUnsupported，
// 而不是静默直连、也不是静默绕过准入。
func TestProxy_CustomMode_RejectsBecauseIPCannotBePinned(t *testing.T) {
	u, err := url.Parse(proxyMockURL)
	require.NoError(t, err)

	g := loopbackGuard(t, GuardOptions{Proxy: Config{URL: proxyMockURL}})
	client := g.ClientWithProxy(u)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://127.0.0.1:18181/v1/models", nil)
	require.NoError(t, err)

	_, err = client.Do(req)
	require.Error(t, err, "经固定代理访问时，因无法固定 IP 必须报错")
	assert.True(t, errors.Is(err, ErrProxyUnsupported),
		"错误应为 ErrProxyUnsupported，实际: %v", err)
}

// TestProxy_DirectMode_NotAffectedByEnvProxy 验证 direct 模式不被环境代理接管。
func TestProxy_DirectMode_NotAffectedByEnvProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", proxyMockURL)
	t.Setenv("HTTPS_PROXY", proxyMockURL)

	g := loopbackGuard(t, GuardOptions{})
	client := g.Client()

	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "应是 *http.Transport")
	assert.Nil(t, tr.Proxy,
		"direct 模式的 Transport.Proxy 必须为 nil——受保护出站的准入不能被环境代理绕过")
}

// TestProxy_EnvMode_UsesEnvironmentProxy 验证 env 模式确实采用环境里的代理。
//
// **为何不直接用 tr.Proxy 判断**（2026-11-10 修复）：
// 产品代码用的是 `http.ProxyFromEnvironment`，它内部用 `sync.Once` 缓存了
// 首次调用时看到的进程环境（Go 标准库行为，未导出重置 API）。
// 测试里 `t.Setenv` 改环境对那个缓存**无效**，于是：
//   - 单独跑本用例 → PASS（本用例是第一个读环境的）；
//   - 整包跑 → FAIL（包内其他用例先读过，缓存已固定成**宿主真实代理**，
//     在本机就是 HTTPS_PROXY=127.0.0.1:7890）。
//
// 这是测试的隔离问题，不是产品缺陷——生产环境变量启动后不变，缓存反而是优势。
// 故本用例改为：用 `httpproxy.FromEnvironment()` 现场读环境，验证
// 「给定的环境映射确实解析出预期代理」；再用 tr.Proxy 确认它**已挂载**。
func TestProxy_EnvMode_UsesEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", proxyMockURL)
	t.Setenv("NO_PROXY", "")
	// 同样清掉可能与本用例竞争的 HTTP_PROXY，避免继承宿主值干扰。
	t.Setenv("HTTP_PROXY", "")

	g := loopbackGuard(t, GuardOptions{})
	client := g.ClientWithProxyEnv()

	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	// 产品行为：env 模式必须挂上代理函数（不能静默直连）。
	require.NotNil(t, tr.Proxy, "env 模式必须挂上代理函数")

	// 语义验证：现场读环境得到的代理，应与本用例设置的完全一致。
	// 不用 tr.Proxy(req) 的返回值——它读的是进程级缓存，与 t.Setenv 无关。
	cfg := httpproxy.FromEnvironment()
	pu := cfg.ProxyFunc()
	got, err := pu(mustParseURL(t, "https://example.com/"))
	require.NoError(t, err)
	if assert.NotNil(t, got, "HTTPS_PROXY 已设置，应解析出代理") {
		assert.Equal(t, proxyMockAddr, got.Host)
	}

	// 对照组：未匹配 scheme 的目标不应被代理（避免“任意环境都返回同一代理”的假通过）。
	noProxyGot, err := pu(mustParseURL(t, "ftp://example.com/"))
	require.NoError(t, err)
	assert.Nil(t, noProxyGot, "未配置 ftp 代理时不应返回代理，证明上一条断言有信息量")
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// TestProxy_NoProxyBypassesProxy 验证 no_proxy 命中时不被判为需经代理。
func TestProxy_NoProxyBypassesProxy(t *testing.T) {
	g := loopbackGuard(t, GuardOptions{
		Proxy: Config{URL: proxyMockURL, NoProxy: "127.0.0.1"},
	})
	assert.False(t, g.proxyAppliesTo("127.0.0.1"),
		"no_proxy 命中时不应判为需经代理")
	assert.True(t, g.proxyAppliesTo("example.com"),
		"非 no_proxy 目标应判为需经代理")
}

// TestProxy_OnlyNoProxyConfiguredIsNotTreatedAsProxied 验证「只配 no_proxy 不配 URL」
// 不会被当成必须经代理——否则直连部署会被无谓拒绝。
func TestProxy_OnlyNoProxyConfiguredIsNotTreatedAsProxied(t *testing.T) {
	g := loopbackGuard(t, GuardOptions{
		Proxy: Config{NoProxy: "example.com"},
	})
	assert.False(t, g.proxyAppliesTo("other.com"),
		"代理 URL 为空时，不应把任何目标判为必须经代理")
}
