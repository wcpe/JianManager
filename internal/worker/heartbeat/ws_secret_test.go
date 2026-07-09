package heartbeat

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestWSSecretApplier_AppliesOnChange 值变化才应用；同值/空值去重（FR-275，见 ADR-061）。
func TestWSSecretApplier_AppliesOnChange(t *testing.T) {
	var applied []string
	a := newWSSecretApplier("boot-secret", func(s string) error {
		applied = append(applied, s)
		return nil
	})

	// 空值（旧 CP/未注入）→ 不动作。
	a.applyReply(&workerpb.HeartbeatResponse{})
	assert.Empty(t, applied)

	// 与启动初值相同 → 去重不应用。
	a.applyReply(&workerpb.HeartbeatResponse{WsTokenSecret: "boot-secret"})
	assert.Empty(t, applied)

	// 新值 → 应用一次。
	a.applyReply(&workerpb.HeartbeatResponse{WsTokenSecret: "rotated-1"})
	require.Equal(t, []string{"rotated-1"}, applied)

	// 同值重复下发（每拍携带）→ 不重复应用。
	a.applyReply(&workerpb.HeartbeatResponse{WsTokenSecret: "rotated-1"})
	require.Equal(t, []string{"rotated-1"}, applied)

	// 再轮换 → 再应用。
	a.applyReply(&workerpb.HeartbeatResponse{WsTokenSecret: "rotated-2"})
	require.Equal(t, []string{"rotated-1", "rotated-2"}, applied)
}

// TestWSSecretApplier_ApplyErrorDoesNotRetrySameValue 应用失败（如身份文件写盘失败）仅告警：
// 内存密钥已推进（校验即时正确优先），同值不重试——文件补写靠下次注册下发兜底。
func TestWSSecretApplier_ApplyErrorDoesNotRetrySameValue(t *testing.T) {
	calls := 0
	a := newWSSecretApplier("boot", func(s string) error {
		calls++
		return errors.New("persist failed")
	})

	a.applyReply(&workerpb.HeartbeatResponse{WsTokenSecret: "rotated"})
	a.applyReply(&workerpb.HeartbeatResponse{WsTokenSecret: "rotated"})
	assert.Equal(t, 1, calls, "同值失败不重试（去重先于应用）")
}

// TestWSSecretApplier_NilSafe 未注入 applier（旧装配）时对任意响应安全无操作。
func TestWSSecretApplier_NilSafe(t *testing.T) {
	var a *wsSecretApplier
	require.NotPanics(t, func() {
		a.applyReply(&workerpb.HeartbeatResponse{WsTokenSecret: "x"})
	})

	h := &Heartbeat{}
	require.NotPanics(t, func() {
		h.wsSecretApplier.applyReply(&workerpb.HeartbeatResponse{WsTokenSecret: "x"})
	})
}
