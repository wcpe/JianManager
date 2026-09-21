package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestBuildCreateInstanceRequest_ProbePortByApplicability FR-454：下发给 Worker 的 probe_port
// 按探针适用性收敛——只有 MC Java 服务端保留探针端口；代理/Beacon/通用二进制一律归零。
// 这样 Worker 侧心跳采集（按 ProbePort>0 过滤）不再对这类实例每拍抓取 /metrics 必失败；
// 并兜底历史脏数据（早期误配、仍留在库中的 probe_port）。
func TestBuildCreateInstanceRequest_ProbePortByApplicability(t *testing.T) {
	db := newResyncTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	t.Cleanup(svc.Shutdown)

	cases := []struct {
		name      string
		inst      *model.Instance
		wantProbe int32
	}{
		{
			name: "MC 后端保留探针端口",
			inst: &model.Instance{
				NodeID: 1, Name: "backend", Type: model.InstanceTypeMinecraftJava,
				Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29940, Status: model.InstanceStatusStopped,
			},
			wantProbe: 29940,
		},
		{
			name: "通用二进制归零",
			inst: &model.Instance{
				NodeID: 1, Name: "bin", Type: model.InstanceTypeGeneric,
				Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29941, Status: model.InstanceStatusStopped,
			},
			wantProbe: 0,
		},
		{
			name: "Beacon 归零",
			inst: &model.Instance{
				NodeID: 1, Name: "beacon", Type: model.InstanceTypeGeneric,
				Role: model.InstanceRoleBeacon, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29942, Status: model.InstanceStatusStopped,
			},
			wantProbe: 0,
		},
		{
			name: "历史误配 Beacon（type=minecraft_java）仍归零",
			inst: &model.Instance{
				NodeID: 1, Name: "legacy-beacon", Type: model.InstanceTypeMinecraftJava,
				Role: model.InstanceRoleBeacon, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29943, Status: model.InstanceStatusStopped,
			},
			wantProbe: 0,
		},
		{
			name: "代理归零",
			inst: &model.Instance{
				NodeID: 1, Name: "gate", Type: model.InstanceTypeMinecraftJava,
				Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29944, Status: model.InstanceStatusStopped,
			},
			wantProbe: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Create(tc.inst).Error)
			spec, err := svc.buildCreateInstanceRequest(tc.inst)
			require.NoError(t, err)
			require.Equal(t, tc.wantProbe, spec.ProbePort)
		})
	}
}
