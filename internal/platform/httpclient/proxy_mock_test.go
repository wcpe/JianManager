package httpclient

// FR-539 出站代理真机验收：验证 direct / env / custom 三种模式的实际行为。
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

// TestProxy_CustomMode_RejectsBecauseIPCannotBePinned 验证 FR-539 的核心取舍。
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
func TestProxy_EnvMode_UsesEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", proxyMockURL)

	g := loopbackGuard(t, GuardOptions{})
	client := g.ClientWithProxyEnv()

	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, tr.Proxy, "env 模式必须挂上代理函数")

	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	pu, perr := tr.Proxy(req)
	require.NoError(t, perr)
	if assert.NotNil(t, pu, "env 模式下 HTTPS_PROXY 应被采用") {
		assert.Equal(t, proxyMockAddr, pu.Host)
	}
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
