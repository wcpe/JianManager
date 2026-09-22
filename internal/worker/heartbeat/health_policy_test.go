package heartbeat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-459 心跳响应策略下发应用：presence（optional）标记 + 字段映射 + 老 CP 不下发不改动。

func TestApplyHealthPolicy_AppliesWhenPresent(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	var got process.HealthPolicy
	var called bool
	hb.SetHealthPolicyApplier(func(p process.HealthPolicy) { got = p; called = true })

	enabled := true
	resp := &workerpb.HeartbeatResponse{
		HealthScanEnabled:             &enabled,
		HealthScanIntervalMs:          45000,
		HealthProbeKind:               "tcp",
		HealthSuspicionThreshold:      4,
		HealthAction:                  "restart",
		HealthCircuitBreakerThreshold: 7,
		HealthCircuitBreakerWindowMs:  600000,
	}
	hb.applyHealthPolicy(resp)

	require.True(t, called)
	assert.True(t, got.Enabled)
	assert.Equal(t, 45*time.Second, got.ScanInterval)
	assert.Equal(t, "tcp", got.ProbeKind)
	assert.Equal(t, 4, got.SuspicionThreshold)
	assert.Equal(t, "restart", got.Action)
	assert.Equal(t, 7, got.CircuitBreakerThreshold)
	assert.Equal(t, 10*time.Minute, got.CircuitBreakerWindow)
}

// 老 CP 不下发（optional 为 nil）时不改动本地策略。
func TestApplyHealthPolicy_IgnoresWhenAbsent(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	called := false
	hb.SetHealthPolicyApplier(func(process.HealthPolicy) { called = true })

	hb.applyHealthPolicy(&workerpb.HeartbeatResponse{HealthAction: "restart"})
	assert.False(t, called, "老 CP 未下发策略（nil）不应覆盖本地配置")
}

// 未注入 applier 时不 panic（向后兼容）。
func TestApplyHealthPolicy_NoApplier(t *testing.T) {
	hb := New("localhost:9100", "node", "secret", 30*time.Second, nil)
	enabled := false
	assert.NotPanics(t, func() {
		hb.applyHealthPolicy(&workerpb.HeartbeatResponse{HealthScanEnabled: &enabled})
	})
}
