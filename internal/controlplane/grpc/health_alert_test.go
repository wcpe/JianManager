package grpc

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakeHealthNotifier struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeHealthNotifier) NotifyInstanceCircuitBroken(nodeUUID, instanceUUID, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, instanceUUID+":"+reason)
}

// FR-459：熔断上报 → 站内信告警，按实例去重（原因变化才重发，恢复后清除记录）。
func TestMaybeNotifyCircuitBroken_Dedup(t *testing.T) {
	h := &ControlPlaneHandler{healthAlerted: map[string]string{}}
	n := &fakeHealthNotifier{}
	h.SetHealthAlertNotifier(n)

	broken := &workerpb.InstanceState{InstanceUuid: "u1", Health: "circuit_broken", StatusReason: "r1"}
	h.maybeNotifyCircuitBroken("node", broken)
	h.maybeNotifyCircuitBroken("node", broken)
	assert.Len(t, n.calls, 1, "同原因不重发")

	changed := &workerpb.InstanceState{InstanceUuid: "u1", Health: "circuit_broken", StatusReason: "r2"}
	h.maybeNotifyCircuitBroken("node", changed)
	assert.Len(t, n.calls, 2, "原因变化重发")

	healthy := &workerpb.InstanceState{InstanceUuid: "u1", Health: "healthy"}
	h.maybeNotifyCircuitBroken("node", healthy)
	h.maybeNotifyCircuitBroken("node", broken)
	assert.Len(t, n.calls, 3, "恢复后再次熔断应重发")
}

// 非熔断健康态不触发告警。
func TestMaybeNotifyCircuitBroken_IgnoresNonBroken(t *testing.T) {
	h := &ControlPlaneHandler{healthAlerted: map[string]string{}}
	n := &fakeHealthNotifier{}
	h.SetHealthAlertNotifier(n)

	h.maybeNotifyCircuitBroken("node", &workerpb.InstanceState{InstanceUuid: "u1", Health: "dead", StatusReason: "假死"})
	h.maybeNotifyCircuitBroken("node", &workerpb.InstanceState{InstanceUuid: "u1"})
	assert.Empty(t, n.calls)
}
