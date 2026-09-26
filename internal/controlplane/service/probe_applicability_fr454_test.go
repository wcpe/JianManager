package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
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
	// buildCreateInstanceRequest 会解析实例所属节点的 UUID 作为日志 holder 身份
	// （log_source_generation 含 worker uuid）；生产路径上游必然已有该节点，故此处补建夹具。
	node := &model.Node{Name: "fr454-probe-node", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)

	cases := []struct {
		name      string
		inst      *model.Instance
		wantProbe int32
	}{
		{
			name: "MC 后端保留探针端口",
			inst: &model.Instance{
				NodeID: node.ID, Name: "backend", Type: model.InstanceTypeMinecraftJava,
				Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29940, Status: model.InstanceStatusStopped,
			},
			wantProbe: 29940,
		},
		{
			name: "通用二进制归零",
			inst: &model.Instance{
				NodeID: node.ID, Name: "bin", Type: model.InstanceTypeGeneric,
				Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29941, Status: model.InstanceStatusStopped,
			},
			wantProbe: 0,
		},
		{
			name: "Beacon 归零",
			inst: &model.Instance{
				NodeID: node.ID, Name: "beacon", Type: model.InstanceTypeGeneric,
				Role: model.InstanceRoleBeacon, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29942, Status: model.InstanceStatusStopped,
			},
			wantProbe: 0,
		},
		{
			name: "历史误配 Beacon（type=minecraft_java）仍归零",
			inst: &model.Instance{
				NodeID: node.ID, Name: "legacy-beacon", Type: model.InstanceTypeMinecraftJava,
				Role: model.InstanceRoleBeacon, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", ProbePort: 29943, Status: model.InstanceStatusStopped,
			},
			wantProbe: 0,
		},
		{
			name: "代理归零",
			inst: &model.Instance{
				NodeID: node.ID, Name: "gate", Type: model.InstanceTypeMinecraftJava,
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

// TestCreate_ProbePortZeroedForNonApplicable FR-454：手工 API 建实例不得把非 0 ProbePort 直落库；
// 且 role=beacon 的 type 与搭建路径同口径归一为 generic（否则手工 API 可持续制造 type 脏值）。
func TestCreate_ProbePortZeroedForNonApplicable(t *testing.T) {
	db := newFR032ServiceTestDB(t)
	node := &model.Node{Name: "fr454-node", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	svc := NewInstanceService(db, nil, cpgrpc.NewClientPool())

	cases := []struct {
		name      string
		typ       model.InstanceType
		role      model.InstanceRole
		wantType  model.InstanceType
		wantProbe int
	}{
		{"MC 后端保留探针端口", model.InstanceTypeMinecraftJava, model.InstanceRoleBackend, model.InstanceTypeMinecraftJava, 29940},
		{"通用二进制归零", model.InstanceTypeGeneric, model.InstanceRoleUniversal, model.InstanceTypeGeneric, 0},
		{"Beacon 归零", model.InstanceTypeGeneric, model.InstanceRoleBeacon, model.InstanceTypeGeneric, 0},
		{"历史误记 type 的 Beacon 归一并归零", model.InstanceTypeMinecraftJava, model.InstanceRoleBeacon, model.InstanceTypeGeneric, 0},
		{"代理归零", model.InstanceTypeMinecraftJava, model.InstanceRoleProxy, model.InstanceTypeMinecraftJava, 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := svc.Create(CreateInstanceRequest{
				NodeID:       node.ID,
				Name:         "fr454-" + tc.name,
				Type:         tc.typ,
				Role:         tc.role,
				ProcessType:  model.ProcessTypeDirect,
				StartCommand: "x",
				ProbePort:    29940,
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantType, inst.Type, "case %d type 口径", i)
			require.Equal(t, tc.wantProbe, inst.ProbePort, "case %d 探针端口口径", i)

			// 落库值同样收敛（防「内存收敛了、库里仍是脏值」）。
			var loaded model.Instance
			require.NoError(t, db.First(&loaded, inst.ID).Error)
			require.Equal(t, tc.wantType, loaded.Type)
			require.Equal(t, tc.wantProbe, loaded.ProbePort)
		})
	}
}

// TestEnsureProbePort_SkipsNonApplicable FR-454 防御性守卫：不适用探针的实例永不补分配端口，
// 否则会写回一个永不被采集的 probe_port，并让调用方误以为「探针端口已就绪」。
func TestEnsureProbePort_SkipsNonApplicable(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewInstanceService(db, nil, cpgrpc.NewClientPool())
	t.Cleanup(svc.Shutdown)

	cases := []*model.Instance{
		{Name: "beacon", NodeID: 1, Type: model.InstanceTypeGeneric, Role: model.InstanceRoleBeacon,
			ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusStopped},
		{Name: "bin", NodeID: 1, Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
			ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusStopped},
		{Name: "gate", NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleProxy,
			ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusStopped},
	}
	for _, inst := range cases {
		t.Run(inst.Name, func(t *testing.T) {
			require.NoError(t, db.Create(inst).Error)
			changed, err := svc.EnsureProbePort(inst)
			require.NoError(t, err)
			require.False(t, changed, "不适用实例不得补口")
			require.Zero(t, inst.ProbePort)

			var loaded model.Instance
			require.NoError(t, db.First(&loaded, inst.ID).Error)
			require.Zero(t, loaded.ProbePort, "不得写回探针端口")
		})
	}
}
