package heartbeat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestProxyApplier_RebuildsOnGenerationChange generation 变化才触发重建，相同则不重建（避免每拍重建）。
func TestProxyApplier_RebuildsOnGenerationChange(t *testing.T) {
	var got []httpclient.Config
	pa := newProxyApplier(func(c httpclient.Config) error {
		got = append(got, c)
		return nil
	})

	gen := httpclient.ProxyGeneration(httpclient.Config{URL: "http://p:7890"})
	resp := &workerpb.HeartbeatResponse{ProxyUrl: "http://p:7890", ProxyGeneration: gen}

	pa.apply(resp) // 首次 → 重建
	pa.apply(resp) // 同 generation → 不重建
	require.Len(t, got, 1)
	require.Equal(t, "http://p:7890", got[0].URL)

	// generation 变化 → 再次重建。
	resp2 := &workerpb.HeartbeatResponse{
		ProxyUrl:        "socks5://q:1080",
		ProxyGeneration: httpclient.ProxyGeneration(httpclient.Config{URL: "socks5://q:1080"}),
	}
	pa.apply(resp2)
	require.Len(t, got, 2)
	require.Equal(t, "socks5://q:1080", got[1].URL)
}

// TestProxyApplier_EmptyGenerationClearsToDirect 空 generation（CP 期望直连/未下发）→ 重建为空配（回退本地）。
func TestProxyApplier_EmptyGenerationClearsToDirect(t *testing.T) {
	var got []httpclient.Config
	pa := newProxyApplier(func(c httpclient.Config) error {
		got = append(got, c)
		return nil
	})

	// 先应用一个代理。
	gen := httpclient.ProxyGeneration(httpclient.Config{URL: "http://p:7890"})
	pa.apply(&workerpb.HeartbeatResponse{ProxyUrl: "http://p:7890", ProxyGeneration: gen})
	require.Len(t, got, 1)

	// CP 改为期望直连（空 url + 空 generation）→ 应重建为空配（回退本地 yaml/env）。
	pa.apply(&workerpb.HeartbeatResponse{ProxyUrl: "", ProxyGeneration: ""})
	require.Len(t, got, 2)
	require.Empty(t, got[1].URL)

	// 再来一拍空 generation → 已是空配、不重复重建。
	pa.apply(&workerpb.HeartbeatResponse{ProxyUrl: "", ProxyGeneration: ""})
	require.Len(t, got, 2)
}

// TestProxyApplier_NilResponseNoop nil 响应不触发重建（健壮性）。
func TestProxyApplier_NilResponseNoop(t *testing.T) {
	called := 0
	pa := newProxyApplier(func(httpclient.Config) error { called++; return nil })
	pa.apply(nil)
	require.Equal(t, 0, called)
}

// TestProxyApplier_NilApplierNoop 未注入重建回调时 apply 安全空操作（向后兼容：CP 未下发代理）。
func TestProxyApplier_NilApplierNoop(t *testing.T) {
	var pa *proxyApplier // nil
	require.NotPanics(t, func() {
		pa.apply(&workerpb.HeartbeatResponse{ProxyUrl: "http://p:7890", ProxyGeneration: "abc"})
	})
}

// TestProxyApplier_RebuildErrorDoesNotAdvanceGeneration 重建失败时不推进 generation（下拍仍重试）。
func TestProxyApplier_RebuildErrorDoesNotAdvanceGeneration(t *testing.T) {
	calls := 0
	pa := newProxyApplier(func(httpclient.Config) error {
		calls++
		return errBoom
	})
	resp := &workerpb.HeartbeatResponse{ProxyUrl: "http://p:7890", ProxyGeneration: "g1"}
	pa.apply(resp)
	pa.apply(resp) // 上次失败 → 仍重试
	require.Equal(t, 2, calls)
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }
