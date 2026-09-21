package service

import (
	"fmt"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newPortsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 内存库（共享缓存，按测试名隔离），避免 Windows 上 sqlite 文件在清理时被占用。
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}))
	return db
}

func mkInstance(name string, nodeID uint, ports AllocatedPorts) *model.Instance {
	return &model.Instance{
		Name: name, NodeID: nodeID, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x",
		ServerPort: ports.ServerPort, QueryPort: ports.QueryPort, ProbePort: ports.ProbePort,
	}
}

func TestAllocPortsForNode(t *testing.T) {
	db := newPortsTestDB(t)

	// 空节点：取各范围起点
	p1, err := allocPortsForNode(db, 1, true)
	require.NoError(t, err)
	require.Equal(t, 25565, p1.ServerPort)
	require.Equal(t, p1.ServerPort, p1.QueryPort) // query 约定等于 server
	require.Equal(t, 29940, p1.ProbePort)         // 探针端口取起点

	// 落库后再分配应跳过已占用端口
	require.NoError(t, db.Create(mkInstance("a", 1, p1)).Error)
	p2, err := allocPortsForNode(db, 1, true)
	require.NoError(t, err)
	require.Equal(t, 25566, p2.ServerPort)
	require.Equal(t, 29941, p2.ProbePort) // p1 占了 29940，跳到 29941

	// 不同节点独立计数
	p3, err := allocPortsForNode(db, 2, true)
	require.NoError(t, err)
	require.Equal(t, 25565, p3.ServerPort)

	// 软删除的实例释放其端口
	inst := mkInstance("b", 1, p2)
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Delete(inst).Error)
	p4, err := allocPortsForNode(db, 1, true)
	require.NoError(t, err)
	require.Equal(t, 25566, p4.ServerPort) // p2 的端口被回收
}

// TestAllocPortsForNode_ProbeInapplicable FR-454：探针不适用（代理/通用二进制/Beacon）时不分配
// 探针端口（ProbePort 恒 0），但仍分配 server/query；不占用的探针端口可被后续 MC 实例取用。
func TestAllocPortsForNode_ProbeInapplicable(t *testing.T) {
	db := newPortsTestDB(t)

	p, err := allocPortsForNode(db, 1, false)
	require.NoError(t, err)
	require.Equal(t, 25565, p.ServerPort, "server 端口照常分配")
	require.Equal(t, p.ServerPort, p.QueryPort, "query 约定等于 server")
	require.Zero(t, p.ProbePort, "探针不适用时不分配探针端口")

	// 不适用实例不写 probe_port，探针端口仍空置：后续 MC 实例取到探针起点。
	require.NoError(t, db.Create(&model.Instance{
		Name: "bin", NodeID: 1, Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "x",
		ServerPort: p.ServerPort, QueryPort: p.QueryPort,
	}).Error)
	probe, err := allocProbePortForNode(db, 1)
	require.NoError(t, err)
	require.Equal(t, 29940, probe, "探针端口未被不适用实例占用，仍从起点分配")
}

func TestAllocProbePortForNode(t *testing.T) {
	db := newPortsTestDB(t)

	// 空节点：探针端口取起点
	port, err := allocProbePortForNode(db, 1)
	require.NoError(t, err)
	require.Equal(t, 29940, port)

	// 既有实例占用的探针端口被跳过（含历史实例残留）
	require.NoError(t, db.Create(mkInstance("a", 1, AllocatedPorts{ServerPort: 25565, QueryPort: 25565, ProbePort: 29940})).Error)
	port2, err := allocProbePortForNode(db, 1)
	require.NoError(t, err)
	require.Equal(t, 29941, port2)

	// 不同节点独立计数
	port3, err := allocProbePortForNode(db, 2)
	require.NoError(t, err)
	require.Equal(t, 29940, port3)

	// 软删除实例释放其探针端口
	inst := mkInstance("b", 1, AllocatedPorts{ServerPort: 25566, QueryPort: 25566, ProbePort: 29941})
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Delete(inst).Error)
	port4, err := allocProbePortForNode(db, 1)
	require.NoError(t, err)
	require.Equal(t, 29941, port4)
}

func TestNodePortUsage(t *testing.T) {
	db := newPortsTestDB(t)
	require.NoError(t, db.Create(mkInstance("b", 1, AllocatedPorts{ServerPort: 25566, QueryPort: 25566})).Error)
	require.NoError(t, db.Create(mkInstance("a", 1, AllocatedPorts{ServerPort: 25565, QueryPort: 25565})).Error)
	require.NoError(t, db.Create(mkInstance("other-node", 2, AllocatedPorts{ServerPort: 25565, QueryPort: 25565})).Error)
	// 无端口的实例不计入
	noPort := &model.Instance{Name: "noport", NodeID: 1, Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x"}
	require.NoError(t, db.Create(noPort).Error)

	usage, err := NodePortUsage(db, 1)
	require.NoError(t, err)
	require.Len(t, usage, 2)
	// 按 server_port 升序
	require.Equal(t, 25565, usage[0].ServerPort)
	require.Equal(t, 25566, usage[1].ServerPort)

	ranges := DefaultPortRanges()
	require.Equal(t, 25565, ranges.ServerPortBase)
	require.Equal(t, 2000, ranges.RangeSize)
}

// TestEnsureProbePortConcurrentDistinct 验证节点级端口分配互斥（nodePortAllocMu）：
// 同节点多实例并发补口（EnsureProbePort）时端口互异。occupiedPortsForNode 是无事务
// 快照，修复前并发分配可能选中同一端口（-race 下复现概率更高）。
func TestEnsureProbePortConcurrentDistinct(t *testing.T) {
	db := newPortsTestDB(t)
	svc := NewInstanceService(db, nil, cpgrpc.NewClientPool())

	const n = 16
	instances := make([]*model.Instance, n)
	for i := range instances {
		inst := mkInstance(fmt.Sprintf("probe-%d", i), 1, AllocatedPorts{})
		require.NoError(t, db.Create(inst).Error)
		instances[i] = inst
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, inst := range instances {
		wg.Add(1)
		go func(i int, inst *model.Instance) {
			defer wg.Done()
			_, errs[i] = svc.EnsureProbePort(inst)
		}(i, inst)
	}
	wg.Wait()

	assigned := make(map[int]bool, n)
	for i, inst := range instances {
		require.NoError(t, errs[i], "实例 %d 补口失败", i)
		require.Greater(t, inst.ProbePort, 0, "实例 %d 未分到端口", i)
		require.False(t, assigned[inst.ProbePort], "并发补口选中重复探针端口 %d", inst.ProbePort)
		assigned[inst.ProbePort] = true
	}

	// 库内一致性：同节点所有实例的 probe_port 互异（含 0 之外的全部分配结果）。
	var rows []model.Instance
	require.NoError(t, db.Where("node_id = ?", 1).Find(&rows).Error)
	require.Len(t, rows, n)
	dbPorts := make(map[int]bool, n)
	for _, r := range rows {
		require.Greater(t, r.ProbePort, 0)
		require.False(t, dbPorts[r.ProbePort], "库内出现重复探针端口 %d", r.ProbePort)
		dbPorts[r.ProbePort] = true
	}
}
