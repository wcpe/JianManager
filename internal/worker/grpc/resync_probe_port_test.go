package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestCreateInstance_ReregisterRefreshesProbePort FR-411 补口回归：
// 导入/历史实例起初 probe_port=0，CP 部署探针前补分配端口后经幂等 CreateInstance 重注册。
// 「已存在」分支必须刷新 Worker 内存表的 ProbePort，否则心跳采集器仍按旧值 0 过滤，
// 形成「探针桥已连接但监控永无数据」的静默断链（修复后下一拍心跳即采集，无需重启）。
func TestCreateInstance_ReregisterRefreshesProbePort(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	const uuid = "fr411-probe-port"
	// 初次注册：导入实例形态，probe_port=0。
	require.NoError(t, mgr.Create(uuid, "导入实例", "java -jar app.jar", "", t.TempDir(), nil, false, process.ProcessTypeDirect, "", "", 0, 0))
	srv := NewServer(mgr, "node-fr411", nil, nil, nil)

	spec := &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "导入实例",
		ProcessType:  string(process.ProcessTypeDirect),
		StartCommand: "java -jar app.jar",
		WorkDir:      t.TempDir(),
	}
	// 先补口注册（WorkDir 变化模拟真实重注册；此时实例已存在 → 走「已存在」刷新分支）。
	created, err := srv.registerInstanceFromProto(spec)
	require.NoError(t, err)
	_ = created

	// CP 补分配端口后重注册：带 probe_port=29941 的同 UUID 规格。
	spec.ProbePort = 29941
	_, err = srv.registerInstanceFromProto(spec)
	require.NoError(t, err)

	var snap *process.InstanceSnapshot
	for _, s := range mgr.GetAllInstanceStates() {
		if s.UUID == uuid {
			snap = &s
			break
		}
	}
	require.NotNil(t, snap)
	assert.Equal(t, 29941, snap.ProbePort, "重注册必须刷新内存表探针端口，心跳下一拍即可采集")
}

// TestResyncInstances_OverwritesProbePortForExisting FR-454：ResyncInstances 命中已存在实例时
// 仍以 CP 为准刷新 probe_port。Worker 重启经 RecoverDaemonInstances 从 PID 文件恢复的 RUNNING
// 实例带历史脏 probe_port（早期误配），而 CP 侧已按适用性收敛为 0——不覆盖就会继续每拍空抓 /metrics。
func TestResyncInstances_OverwritesProbePortForExisting(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	const uuid = "fr454-stale-probe-port"
	// 模拟恢复出的 RUNNING 实例：内存表带着历史脏探针端口。
	require.NoError(t, mgr.Create(uuid, "beacon", "beacon -d", "", t.TempDir(), nil, true, process.ProcessTypeDirect, "", "", 29940, 0))
	srv := NewServer(mgr, "node-fr454", nil, nil, nil)

	resp, err := srv.ResyncInstances(context.Background(), &workerpb.ResyncInstancesRequest{
		Instances: []*workerpb.CreateInstanceRequest{{
			InstanceUuid: uuid,
			Name:         "beacon",
			ProcessType:  string(process.ProcessTypeDirect),
			StartCommand: "beacon -d",
			WorkDir:      t.TempDir(),
			ProbePort:    0, // CP 侧按 IsProbeApplicable 收敛后的值
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), resp.Skipped, "已存在实例按跳过计数（不重复登记、不启动）")

	var snap *process.InstanceSnapshot
	for _, s := range mgr.GetAllInstanceStates() {
		if s.UUID == uuid {
			snap = &s
			break
		}
	}
	require.NotNil(t, snap)
	assert.Equal(t, 0, snap.ProbePort, "重连重推必须以 CP 为准刷新 probe_port（历史脏值被覆盖）")
}
