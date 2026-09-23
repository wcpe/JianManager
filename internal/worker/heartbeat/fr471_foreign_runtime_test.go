package heartbeat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
)

// fr471StateProvider 是只提供实例快照的最小桩。
type fr471StateProvider struct{ states []process.InstanceSnapshot }

func (p fr471StateProvider) GetAllInstanceStates() []process.InstanceSnapshot { return p.states }

// TestBuildHeartbeatRequest_CarriesForeignRuntime FR-471：孤儿扫描观测到的运行态漂移
// （实例目录下的外来活进程）必须经心跳上报的 foreign_pid/foreign_cmdline 传给 CP——
// 否则 CP 的 runtime_drift_* 永远是 0，面板无从提示接管。
func TestBuildHeartbeatRequest_CarriesForeignRuntime(t *testing.T) {
	h := New("127.0.0.1:1", "node-fr471", "secret", time.Minute, fr471StateProvider{
		states: []process.InstanceSnapshot{
			{UUID: "drift", State: "STOPPED", ForeignPID: 4321, ForeignCmdline: "java -jar server.jar"},
			{UUID: "clean", State: "RUNNING", PID: 111},
		},
	})

	req, _ := h.buildHeartbeatRequest()
	require.Len(t, req.Instances, 2)

	byUUID := map[string]int{}
	for i, s := range req.Instances {
		byUUID[s.InstanceUuid] = i
	}
	drift := req.Instances[byUUID["drift"]]
	assert.Equal(t, int32(4321), drift.ForeignPid)
	assert.Equal(t, "java -jar server.jar", drift.ForeignCmdline)

	// 无漂移实例保持零值（老 CP 与新 CP 都按 0=无漂移处理）。
	clean := req.Instances[byUUID["clean"]]
	assert.Zero(t, clean.ForeignPid)
	assert.Empty(t, clean.ForeignCmdline)
}
