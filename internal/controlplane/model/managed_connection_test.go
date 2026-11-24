package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSynthesizeManagedConnection 验证对账矩阵的连接态合成口径。
//
// 规则：桥没连一律 disconnected；桥连着则采信自报的四态；自报缺失/未知按 connected 兜底。
//
// ## 回归的缺陷：两套词表，"在线"这一项拼写不同
// 探针上行用 `online`（BridgeConnectionState.ONLINE.wireName），而 CP 合成值用 `connected`。
// 早期实现只认 `connected`，`online` 落进 fallback 分支——在"在线"这一项上**侥幸正确**，
// 所以真机联调没暴露。但这意味着白名单是残缺的：任何新取值都会静默记成"在线"，
// 把"不确定"说成"可用"。本用例钉住显式归一，让将来新增状态必须先想清楚归属。
func TestSynthesizeManagedConnection(t *testing.T) {
	cases := []struct {
		name     string
		bridge   bool
		reported string
		want     string
	}{
		// 桥断连是最高优先级：此时自报值必然是断连前的陈旧值。
		{"桥断连时忽略一切自报", false, "online", ManagedConnectionDisconnected},
		{"桥断连且自报也断连", false, "disconnected", ManagedConnectionDisconnected},
		{"桥断连时自报空", false, "", ManagedConnectionDisconnected},

		// 探针上行的真实取值是 online —— 必须归一为 connected，而不是靠 fallback 碰巧对。
		{"探针自报 online 归一为 connected", true, ManagedBridgeConnectionOnline, ManagedConnectionConnected},
		{"自报 connected 原样采信", true, "connected", ManagedConnectionConnected},

		// 这两态必须透传：unauthorized 表示控制面拒了凭据，属需告警的异常，
		// 不能因为"桥还连着"就说它正常。
		{"自报 degraded 透传", true, "degraded", ManagedConnectionDegraded},
		{"自报 unauthorized 透传", true, "unauthorized", ManagedConnectionUnauthorized},

		// 兜底：桥本身就是最强的可用性证据。
		{"自报缺失按 connected 兜底", true, "", ManagedConnectionConnected},
		{"未知取值按 connected 兜底", true, "maintenance", ManagedConnectionConnected},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, SynthesizeManagedConnection(c.bridge, c.reported))
		})
	}
}

// TestSynthesizeManagedConnection_OnlineIsNotDisconnected 单钉一条最容易搞反的映射。
//
// 语义相邻的两个词（online / connected）分属探针与 CP 两套词表。
// 若有人把 wire 值当合成值直接比较，`online` 会掉进"未知"分支——
// 那在当下恰好等于 connected，于是错误不会显形，只在新增状态时才炸。
func TestSynthesizeManagedConnection_OnlineIsNotDisconnected(t *testing.T) {
	assert.Equal(t, ManagedConnectionConnected,
		SynthesizeManagedConnection(true, ManagedBridgeConnectionOnline),
		"探针的 online 必须被识别为在线，不能落到未知分支")
	assert.NotEqual(t, ManagedConnectionDisconnected,
		SynthesizeManagedConnection(true, ManagedBridgeConnectionOnline))
}
